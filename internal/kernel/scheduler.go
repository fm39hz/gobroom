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
	available := make([]Route, 0, len(model.Candidates))
	for _, candidate := range model.Candidates {
		if candidate.Enabled && s.gate.Usable(candidate, now) {
			available = append(available, candidate)
		}
	}
	if len(available) == 0 {
		return Route{}, ErrNoRoute
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	key := model.PublicName
	index := s.cursors[key] % len(available)
	if model.Strategy == StrategyRoundRobin || model.Strategy == StrategyRoundRobinFallback {
		s.cursors[key] = (index + 1) % len(available)
	}
	return available[index], nil
}
