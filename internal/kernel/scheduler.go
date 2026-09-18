package kernel

import (
	"sync"
	"time"
)

type Gate interface{ Usable(Route, time.Time) bool }

type AlwaysOpenGate struct{}

func (AlwaysOpenGate) Usable(Route, time.Time) bool { return true }

type Scheduler struct {
	mu      sync.Mutex
	cursors map[string]int
	sticky  map[string]stickyState
	gate    Gate
}

type stickyState struct {
	index int
	count int
}

func NewScheduler(gate Gate) *Scheduler {
	if gate == nil {
		gate = AlwaysOpenGate{}
	}
	return &Scheduler{cursors: map[string]int{}, sticky: map[string]stickyState{}, gate: gate}
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
