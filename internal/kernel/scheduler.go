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
	gate    Gate
}

func NewScheduler(gate Gate) *Scheduler {
	if gate == nil {
		gate = AlwaysOpenGate{}
	}
	return &Scheduler{cursors: map[string]int{}, gate: gate}
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
	available := make([]Route, 0, len(model.Candidates))
	for _, candidate := range model.Candidates {
		if candidate.Enabled && s.gate.Usable(candidate, now) {
			available = append(available, candidate)
		}
	}
	if len(available) < 2 || model.Strategy == StrategyFallback {
		return available
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	key := model.PublicName
	start := s.cursors[key] % len(available)
	s.cursors[key] = (start + 1) % len(available)
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
