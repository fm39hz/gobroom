package runtime

import (
	"sync"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
)

type RouteHealth struct {
	Failures      int
	Successes     int
	Invalid       bool
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
	trials  map[string]bool
	observe func(kernel.Route, RouteHealth)
}

func NewHealthGate() *HealthGate {
	return &HealthGate{routes: map[string]RouteHealth{}, scoped: map[string]RouteHealth{}, trials: map[string]bool{}}
}
func (g *HealthGate) SetObserver(observer func(kernel.Route, RouteHealth)) { g.observe = observer }
func (g *HealthGate) Usable(route kernel.Route, now time.Time) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, key := range healthKeys(route) {
		state := g.scoped[key]
		if state.Invalid || state.CooldownUntil.After(now) {
			return false
		}
	}
	return true
}

// Acquire admits one real request for a route. A route with an expired
// cooldown and prior failure is half-open: only one request may try recovery.
func (g *HealthGate) Acquire(route kernel.Route, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	acquired := make([]string, 0, 2)
	for _, key := range healthKeys(route) {
		state := g.scoped[key]
		if state.Invalid || state.CooldownUntil.After(now) {
			for _, acquiredKey := range acquired {
				delete(g.trials, acquiredKey)
			}
			return false
		}
		if state.Failures > 0 && !state.CooldownUntil.IsZero() {
			if g.trials[key] {
				for _, acquiredKey := range acquired {
					delete(g.trials, acquiredKey)
				}
				return false
			}
			g.trials[key] = true
			acquired = append(acquired, key)
		}
	}
	return true
}

func (g *HealthGate) Release(route kernel.Route) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, key := range healthKeys(route) {
		delete(g.trials, key)
	}
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
		state.Invalid = false
		state.CooldownUntil = time.Time{}
		state.LastSuccess = now
		state.LastError = ""
	} else {
		state.Failures++
		if permanentlyInvalidates(outcome) {
			state.Invalid = true
			state.CooldownUntil = time.Time{}
		}
		if outcome.Message != "" {
			state.LastError = outcome.Message
		}
		if state.Invalid {
			state.CooldownUntil = time.Time{}
		} else if outcome.RetryAt.After(now) {
			state.CooldownUntil = outcome.RetryAt
		} else if outcome.Retry != kernel.RetryNever {
			state.CooldownUntil = now.Add(failureDelay(state.Failures, outcome.Cause))
		}
	}
	g.scoped[key] = state
	delete(g.trials, key)
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

func permanentlyInvalidates(outcome kernel.ClassifiedOutcome) bool {
	if outcome.Scope == kernel.ScopeRequest || outcome.Effect != kernel.EffectRejected {
		return false
	}
	switch outcome.Cause {
	case kernel.CauseAuth, kernel.CausePermission, kernel.CauseModelNotFound:
		return true
	default:
		return false
	}
}

// ResetScope removes runtime suppression after an explicit configuration
// change. The resulting neutral observation is persisted asynchronously.
func (g *HealthGate) ResetScope(route kernel.Route, scope kernel.OutcomeScope) bool {
	return g.resetScope(route, scope, false)
}

// ClearInvalid reopens only a permanently invalid route after positive
// discovery evidence, preserving unrelated cooldowns and feedback.
func (g *HealthGate) ClearInvalid(route kernel.Route, scope kernel.OutcomeScope) bool {
	return g.resetScope(route, scope, true)
}

func (g *HealthGate) resetScope(route kernel.Route, scope kernel.OutcomeScope, invalidOnly bool) bool {
	key := outcomeScopeKey(route, scope)
	g.mu.Lock()
	state, exists := g.scoped[key]
	if invalidOnly && (!exists || !state.Invalid) {
		g.mu.Unlock()
		return false
	}
	state.Failures = 0
	state.Invalid = false
	state.CooldownUntil = time.Time{}
	state.LastError = ""
	state.LastCause = kernel.CauseUnknown
	state.LastEvidence = ""
	state.Confidence = 0
	state.LastAttempt = time.Time{}
	state.LastSuccess = time.Time{}
	state.LastScope = scope
	g.scoped[key] = state
	if exists && route.ID != "" {
		g.routes[route.ID] = state
	}
	delete(g.trials, key)
	observer := g.observe
	g.mu.Unlock()
	if observer != nil {
		observer(route, state)
	}
	return true
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
	delete(g.trials, scopeKey(route))
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
	for _, key := range healthKeys(route) {
		delete(g.trials, key)
	}
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
	key := outcomeScopeKey(route, state.LastScope)
	g.scoped[key] = state
	if route.ID != "" {
		if current := g.routes[route.ID]; current.LastAttempt.IsZero() || state.LastAttempt.After(current.LastAttempt) {
			g.routes[route.ID] = state
		}
	}
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
