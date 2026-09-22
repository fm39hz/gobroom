package kernel

import (
	"sync"
	"time"
)

type Gate interface{ Usable(Route, time.Time) bool }

type AlwaysOpenGate struct{}

func (AlwaysOpenGate) Usable(Route, time.Time) bool { return true }

type Scheduler struct {
	mu         sync.Mutex
	cursors    map[string]int
	sticky     map[string]stickyState
	strategies map[Strategy]StrategyPrimitive
	modelState map[string]*StrategyState
	stateLocks map[string]*sync.Mutex
	gate       Gate
}

type stickyState struct {
	index int
	count int
}

func NewScheduler(gate Gate) *Scheduler {
	if gate == nil {
		gate = AlwaysOpenGate{}
	}
	return &Scheduler{cursors: map[string]int{}, sticky: map[string]stickyState{}, gate: gate,
		strategies: map[Strategy]StrategyPrimitive{StrategyFallback: OrderedFallback{}, StrategyRotatingFallback: RotatingFallback{}, StrategyRoundRobin: RotatingFallback{}, StrategyRoundRobinFallback: RotatingFallback{}, StrategyWeighted: WeightedFallback{}}, modelState: map[string]*StrategyState{}, stateLocks: map[string]*sync.Mutex{}}
}

func (s *Scheduler) RegisterStrategy(name Strategy, primitive StrategyPrimitive) {
	if name == "" || primitive == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.strategies[name] = primitive
}

func (s *Scheduler) Plan(node ModelNode) []MemberRef {
	s.mu.Lock()
	primitive, state, stateLock := s.strategyState(node)
	s.mu.Unlock()
	stateLock.Lock()
	planned := primitive.Plan(node.ID, append([]MemberRef(nil), node.Members...), state)
	stateLock.Unlock()
	return planned
}

func (s *Scheduler) OnFailure(node ModelNode, failure StrategyFailure) FailureAction {
	s.mu.Lock()
	primitive, state, stateLock := s.strategyState(node)
	s.mu.Unlock()
	stateLock.Lock()
	action := primitive.OnFailure(failure, state)
	stateLock.Unlock()
	return action
}

// strategyState must be called with s.mu held. Per-node locks let unrelated
// model policies plan concurrently while preserving each policy's state.
func (s *Scheduler) strategyState(node ModelNode) (StrategyPrimitive, *StrategyState, *sync.Mutex) {
	primitive := s.strategies[node.Strategy]
	if primitive == nil {
		primitive = s.strategies[StrategyFallback]
	}
	state := s.modelState[node.ID]
	if state == nil {
		state = &StrategyState{}
		s.modelState[node.ID] = state
	}
	stateLock := s.stateLocks[node.ID]
	if stateLock == nil {
		stateLock = &sync.Mutex{}
		s.stateLocks[node.ID] = stateLock
	}
	return primitive, state, stateLock
}

func (s *Scheduler) Select(model ResolvedModel, now time.Time) (Route, error) {
	available := s.Order(model, now)
	if len(available) == 0 {
		return Route{}, ErrNoRoute
	}
	return available[0], nil
}

// Order returns candidates in the exact order the kernel should attempt them.
// Keeping ordering here makes selection and fallback share one policy instead
// of having a separate scheduler that the execution path can accidentally skip.
func (s *Scheduler) Order(model ResolvedModel, now time.Time) []Route {
	return s.OrderWithPreferred(model, now, "")
}

func (s *Scheduler) OrderWithPreferred(model ResolvedModel, now time.Time, preferredConnectionID string) []Route {
	available := make([]Route, 0, len(model.Candidates))
	for _, candidate := range model.Candidates {
		if candidate.Enabled && s.gate.Usable(candidate, now) {
			available = append(available, candidate)
		}
	}
	if preferredConnectionID != "" {
		for index, candidate := range available {
			if candidate.CredentialID == preferredConnectionID {
				available = append([]Route{candidate}, append(available[:index:index], available[index+1:]...)...)
				return available
			}
		}
	}
	if ranker, ok := s.gate.(RouteRanker); ok {
		available = ranker.RankRoutes(available, now)
	}
	if len(available) < 2 || model.Strategy == StrategyFallback {
		return available
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	key := model.PublicName
	start := s.cursors[key] % len(available)
	if model.Strategy == StrategyRoundRobin || model.Strategy == StrategyRoundRobinFallback {
		limit := model.StickyLimit
		if limit < 1 {
			limit = 1
		}
		state := s.sticky[key]
		if state.index >= len(available) {
			state.index = start
		}
		start = state.index
		state.count++
		if state.count >= limit {
			state.index = (start + 1) % len(available)
			state.count = 0
		}
		s.sticky[key] = state
		s.cursors[key] = (start + 1) % len(available)
	} else {
		s.cursors[key] = (start + 1) % len(available)
	}
	ordered := make([]Route, 0, len(available))
	if model.Strategy == StrategyWeighted {
		// Weighted scheduling chooses a deterministic starting point on a
		// virtual ring, then appends every other candidate once for fallback.
		total := 0
		for _, candidate := range available {
			weight := candidate.Weight
			if weight < 1 {
				weight = 1
			}
			total += weight
		}
		point := start % total
		chosen := 0
		for i, candidate := range available {
			weight := candidate.Weight
			if weight < 1 {
				weight = 1
			}
			if point < weight {
				chosen = i
				break
			}
			point -= weight
		}
		for i := range available {
			ordered = append(ordered, available[(chosen+i)%len(available)])
		}
		return ordered
	}
	for i := range available {
		ordered = append(ordered, available[(start+i)%len(available)])
	}
	return ordered
}

// RankRoutes applies the same runtime gate/ranker to a nested route group.
// Nested model policies remain owned by Plan; this only orders physical route
// candidates after hard eligibility has been checked.
func (s *Scheduler) RankRoutes(routes []Route, now time.Time) []Route {
	return s.rankRoutes(routes, now, "")
}

func (s *Scheduler) RankRoutesFor(routes []Route, now time.Time, requestClass string) []Route {
	available := s.availableRoutes(routes, now)
	if ranker, ok := s.gate.(RequestClassRanker); ok {
		return ranker.RankRoutesFor(available, now, requestClass)
	}
	return s.rankRoutes(available, now, requestClass)
}

func (s *Scheduler) rankRoutes(routes []Route, now time.Time, _ string) []Route {
	available := make([]Route, 0, len(routes))
	for _, route := range routes {
		if route.Enabled && s.gate.Usable(route, now) {
			available = append(available, route)
		}
	}
	if ranker, ok := s.gate.(RouteRanker); ok {
		available = ranker.RankRoutes(available, now)
	}
	return available
}

func (s *Scheduler) availableRoutes(routes []Route, now time.Time) []Route {
	available := make([]Route, 0, len(routes))
	for _, route := range routes {
		if route.Enabled && s.gate.Usable(route, now) {
			available = append(available, route)
		}
	}
	return available
}

func (s *Scheduler) Acquire(route Route, now time.Time) bool {
	if admission, ok := s.gate.(RouteAdmission); ok {
		return admission.Acquire(route, now)
	}
	return true
}

func (s *Scheduler) Release(route Route) {
	if admission, ok := s.gate.(RouteAdmission); ok {
		admission.Release(route)
	}
}
