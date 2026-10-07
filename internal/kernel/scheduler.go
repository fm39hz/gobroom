package kernel

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
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
	definitions := StrategyDefinitions()
	strategies := make(map[Strategy]StrategyPrimitive, len(definitions))
	for _, definition := range definitions {
		strategies[definition.Runtime] = definition.Primitive
	}
	return &Scheduler{cursors: map[string]int{}, sticky: map[string]stickyState{}, gate: gate,
		strategies: strategies, modelState: map[string]*StrategyState{}, stateLocks: map[string]*sync.Mutex{}}
}

func (s *Scheduler) RegisterStrategy(name Strategy, primitive StrategyPrimitive) {
	if name == "" || primitive == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.strategies[name] = primitive
}

// SetStrategyDefinitions replaces the scheduler's strategy implementations
// from the immutable shared extension catalog bound during daemon startup.
func (s *Scheduler) SetStrategyDefinitions(definitions []StrategyDefinition) {
	strategies := make(map[Strategy]StrategyPrimitive, len(definitions))
	for _, definition := range definitions {
		if definition.Runtime != "" && definition.Primitive != nil {
			strategies[definition.Runtime] = definition.Primitive
		}
	}
	s.mu.Lock()
	s.strategies = strategies
	s.mu.Unlock()
}

func (s *Scheduler) Plan(node ModelNode) []MemberRef {
	s.mu.Lock()
	primitive, state, stateLock := s.strategyState(node)
	s.mu.Unlock()
	stateLock.Lock()
	state.StickyLimit = node.StickyLimit
	state.Config = append(json.RawMessage(nil), node.StrategyConfig...)
	if state.StickyLimit < 1 {
		state.StickyLimit = 1
	}
	planned := primitive.Plan(node.ID, append([]MemberRef(nil), node.Members...), state)
	stateLock.Unlock()
	return planned
}

func (s *Scheduler) OnFailure(node ModelNode, failure StrategyFailure) FailureAction {
	s.mu.Lock()
	primitive, state, stateLock := s.strategyState(node)
	s.mu.Unlock()
	stateLock.Lock()
	state.Config = append(json.RawMessage(nil), node.StrategyConfig...)
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
	stateKey := strategyStateKey(node.ID, node.StrategyRef)
	state := s.modelState[stateKey]
	if state == nil {
		state = &StrategyState{}
		s.modelState[stateKey] = state
	}
	stateLock := s.stateLocks[stateKey]
	if stateLock == nil {
		stateLock = &sync.Mutex{}
		s.stateLocks[stateKey] = stateLock
	}
	return primitive, state, stateLock
}

func strategyStateKey(nodeID string, ref extensions.Ref) string {
	if ref.Validate() == nil && ref.Kind == StrategyExtensionKind {
		return nodeID + "\x00" + ref.Key()
	}
	return nodeID
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
	if !isBuiltinStrategy(model.Strategy) && s.hasStrategy(model.Strategy) {
		node := ModelNode{ID: model.PublicName, Kind: ModelPhysical, Strategy: model.Strategy, StrategyRef: model.StrategyRef, StrategyConfig: append(json.RawMessage(nil), model.StrategyConfig...), StickyLimit: model.StickyLimit}
		byID := make(map[string]Route, len(available))
		for _, route := range available {
			byID[route.ID] = route
			node.Members = append(node.Members, MemberRef{Kind: MemberRoute, ID: route.ID, Weight: route.Weight})
		}
		planned := s.Plan(node)
		ordered := make([]Route, 0, len(planned))
		for _, member := range planned {
			if route, ok := byID[member.ID]; ok {
				ordered = append(ordered, route)
			}
		}
		if len(ordered) > 0 {
			return ordered
		}
		return available
	}
	if len(available) == 0 || model.Strategy == StrategyFallback {
		return available
	}
	if model.Strategy == StrategyRoundRobin {
		if len(available) == 1 {
			return available
		}
		s.mu.Lock()
		key := strategyStateKey(model.PublicName, model.StrategyRef)
		index := s.cursors[key] % len(available)
		s.cursors[key] = (index + 1) % len(available)
		s.mu.Unlock()
		return []Route{available[index]}
	}
	if len(available) < 2 {
		return available
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	key := strategyStateKey(model.PublicName, model.StrategyRef)
	start := s.cursors[key] % len(available)
	if model.Strategy == StrategyRoundRobinFallback {
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

func (s *Scheduler) hasStrategy(name Strategy) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.strategies[name] != nil
}

func isBuiltinStrategy(strategy Strategy) bool {
	switch strategy {
	case StrategyFallback, StrategyRotatingFallback, StrategyRoundRobin, StrategyRoundRobinFallback, StrategyWeighted:
		return true
	default:
		return false
	}
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

func (s *Scheduler) RankRoutesForSession(routes []Route, now time.Time, requestClass, sessionID string) []Route {
	available := s.availableRoutes(routes, now)
	if ranker, ok := s.gate.(SessionRanker); ok {
		return ranker.RankRoutesForSession(available, now, requestClass, sessionID)
	}
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
