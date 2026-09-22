package runtime

import (
	"sync"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
)

type RouteHealth struct {
	Failures      int
	Successes     int
	CooldownUntil time.Time
	LastError     string
	LastCause     kernel.OutcomeCause
	LastScope     kernel.OutcomeScope
	LastEvidence  kernel.EvidenceSource
	Confidence    float64
	LastAttempt   time.Time
	LastSuccess   time.Time
}
type HealthGate struct {
	mu      sync.RWMutex
	routes  map[string]RouteHealth
	scoped  map[string]RouteHealth
	observe func(kernel.Route, RouteHealth)
}

func NewHealthGate() *HealthGate {
	return &HealthGate{routes: map[string]RouteHealth{}, scoped: map[string]RouteHealth{}}
}
func (g *HealthGate) SetObserver(observer func(kernel.Route, RouteHealth)) { g.observe = observer }
func (g *HealthGate) Usable(route kernel.Route, now time.Time) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, key := range healthKeys(route) {
		state := g.scoped[key]
		if state.CooldownUntil.After(now) {
			return false
		}
	}
	return true
}

// ObserveOutcome is the passive runtime boundary. It accepts evidence only
// from an actual request attempt; no probe or timer can make a route healthy.
func (g *HealthGate) ObserveOutcome(route kernel.Route, outcome kernel.ClassifiedOutcome) {
	now := time.Now()
	g.mu.Lock()
	key := outcomeScopeKey(route, outcome.Scope)
	state := g.scoped[key]
	state.LastAttempt = now
	state.LastCause = outcome.Cause
	state.LastScope = outcome.Scope
	state.Confidence = outcome.Confidence
	if len(outcome.Evidence) > 0 {
		state.LastEvidence = outcome.Evidence[0]
	}
	if outcome.Cause == kernel.CauseSuccess {
		state.Successes++
		state.Failures = 0
		state.CooldownUntil = time.Time{}
		state.LastSuccess = now
		state.LastError = ""
	} else {
		state.Failures++
		if outcome.Message != "" {
			state.LastError = outcome.Message
		}
		if outcome.RetryAt.After(now) {
			state.CooldownUntil = outcome.RetryAt
		} else if outcome.Retry != kernel.RetryNever {
			state.CooldownUntil = now.Add(failureDelay(state.Failures, outcome.Cause))
		}
	}
	g.scoped[key] = state
	// Keep a route projection for existing control-plane/TUI consumers. The
	// scoped map remains authoritative for eligibility.
	routeState := g.routes[route.ID]
	if outcome.Scope == kernel.ScopeRoute || routeState.LastAttempt.IsZero() || state.LastAttempt.After(routeState.LastAttempt) {
		routeState = state
	}
	g.routes[route.ID] = routeState
	observer := g.observe
	g.mu.Unlock()
	if observer != nil {
		observer(route, state)
	}
}
func (g *HealthGate) MarkFailure(route kernel.Route, class kernel.ErrorClass, err error) {
	g.MarkFailureAfter(route, class, err, 0)
}

func (g *HealthGate) MarkFailureAfter(route kernel.Route, class kernel.ErrorClass, err error, override time.Duration) {
	g.mu.Lock()
	state := g.scoped[scopeKey(route)]
	state.Failures++
	delay := failureDelay(state.Failures, errorCause(class))
	if override > delay {
		delay = override
	}
	state.CooldownUntil = time.Now().Add(delay)
	if err != nil {
		state.LastError = err.Error()
	}
	g.scoped[scopeKey(route)] = state
	g.routes[route.ID] = state
	g.mu.Unlock()
	if g.observe != nil {
		g.observe(route, state)
	}
}
func (g *HealthGate) MarkSuccess(route kernel.Route) {
	g.mu.Lock()
	state := g.scoped[scopeKey(route)]
	now := time.Now()
	state.Successes++
	state.Failures = 0
	state.CooldownUntil = time.Time{}
	state.LastError = ""
	state.LastCause = kernel.CauseSuccess
	state.LastScope = kernel.ScopeRoute
	state.LastAttempt = now
	state.LastSuccess = now
	g.scoped[scopeKey(route)] = state
	g.routes[route.ID] = state
	g.mu.Unlock()
	if g.observe != nil {
		g.observe(route, state)
	}
}

func errorCause(class kernel.ErrorClass) kernel.OutcomeCause {
	switch class {
	case kernel.ErrorAuth:
		return kernel.CauseAuth
	case kernel.ErrorCooldown:
		return kernel.CauseRateLimited
	default:
		return kernel.CauseUnknown
	}
}

func failureDelay(failures int, cause kernel.OutcomeCause) time.Duration {
	if failures < 1 {
		failures = 1
	}
	base := 5 * time.Second
	switch cause {
	case kernel.CauseAuth, kernel.CausePermission:
		base = 5 * time.Minute
	case kernel.CauseQuotaExhausted:
		base = 10 * time.Minute
	case kernel.CauseRateLimited, kernel.CauseCapacity, kernel.CauseOverloaded:
		base = 30 * time.Second
	}
	delay := time.Duration(failures) * base
	if delay > 10*time.Minute {
		delay = 10 * time.Minute
	}
	return delay
}

func (g *HealthGate) Restore(route kernel.Route, state RouteHealth) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.scoped[scopeKey(route)] = state
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

func scopeKey(route kernel.Route) string { return "route:" + route.ID }

func outcomeScopeKey(route kernel.Route, scope kernel.OutcomeScope) string {
	switch scope {
	case kernel.ScopeConnection:
		return "connection:" + route.CredentialID
	case kernel.ScopeProvider:
		return "provider:" + route.NodeID
	case kernel.ScopeRouteConnection:
		return "route_connection:" + route.ID + "\x00" + route.CredentialID
	default:
		return scopeKey(route)
	}
}

func healthKeys(route kernel.Route) []string {
	keys := []string{scopeKey(route)}
	if route.CredentialID != "" {
		keys = append(keys, "route_connection:"+route.ID+"\x00"+route.CredentialID, "connection:"+route.CredentialID)
	}
	if route.NodeID != "" {
		keys = append(keys, "provider:"+route.NodeID)
	}
	return keys
}
