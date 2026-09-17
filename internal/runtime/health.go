package runtime

import (
	"sync"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
)

type RouteHealth struct {
	Failures      int
	CooldownUntil time.Time
	LastError     string
	LastSuccess   time.Time
}
type HealthGate struct {
	mu     sync.RWMutex
	routes map[string]RouteHealth
}

func NewHealthGate() *HealthGate { return &HealthGate{routes: map[string]RouteHealth{}} }
func (g *HealthGate) Usable(route kernel.Route, now time.Time) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	state := g.routes[route.ID]
	return state.CooldownUntil.IsZero() || !state.CooldownUntil.After(now)
}
func (g *HealthGate) MarkFailure(route kernel.Route, class kernel.ErrorClass, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	state := g.routes[route.ID]
	state.Failures++
	delay := time.Duration(5*state.Failures) * time.Second
	if class == kernel.ErrorAuth {
		delay = 5 * time.Minute
	} else if class == kernel.ErrorCooldown {
		delay = time.Duration(30*state.Failures) * time.Second
	}
	if delay > 10*time.Minute {
		delay = 10 * time.Minute
	}
	state.CooldownUntil = time.Now().Add(delay)
	if err != nil {
		state.LastError = err.Error()
	}
	g.routes[route.ID] = state
}
func (g *HealthGate) MarkSuccess(route kernel.Route) {
	g.mu.Lock()
	defer g.mu.Unlock()
	state := g.routes[route.ID]
	state.Failures = 0
	state.CooldownUntil = time.Time{}
	state.LastError = ""
	state.LastSuccess = time.Now()
	g.routes[route.ID] = state
}
func (g *HealthGate) Snapshot() map[string]RouteHealth {
	g.mu.RLock()
	defer g.mu.RUnlock()
	result := make(map[string]RouteHealth, len(g.routes))
	for id, state := range g.routes {
		result[id] = state
	}
	return result
}
