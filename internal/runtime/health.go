package runtime

import (
	"iter"
	"sort"
	"sync"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/quota"
)

type RouteHealth struct {
	Lane          HealthLane     `json:"lane,omitempty"`
	OperationRef  extensions.Ref `json:"operationRef,omitzero"`
	Failures      int
	Successes     int
	Invalid       bool
	Generation    string `json:"-"`
	CooldownUntil time.Time
	LastError     string
	LastCause     kernel.OutcomeCause
	LastScope     kernel.OutcomeScope
	LastEvidence  kernel.EvidenceSource
	Confidence    float64
	LastAttempt   time.Time
	LastSuccess   time.Time
}

type HealthLane string

const (
	HealthOperational    HealthLane = "operational"
	HealthAuthentication HealthLane = "authentication"
)

func (lane HealthLane) normalized() HealthLane {
	if lane == HealthAuthentication {
		return lane
	}
	return HealthOperational
}

type RouteHealthView struct {
	RouteHealth
	Operations     []RouteHealth `json:"operations,omitempty"`
	Authentication []RouteHealth `json:"authentication,omitempty"`
}
type HealthGate struct {
	mu                sync.RWMutex
	routes            map[string]RouteHealth
	scoped            map[breakerKey]RouteHealth
	trials            map[breakerKey]bool
	observe           func(kernel.Route, RouteHealth)
	generations       map[breakerKey]string
	quotaGenerations  map[quotaGenerationIdentity]string
	routeGenerations  map[string]string
	routeRefs         map[string]kernel.Route
	generationEnabled bool
}

func NewHealthGate() *HealthGate {
	return &HealthGate{routes: map[string]RouteHealth{}, scoped: map[breakerKey]RouteHealth{}, trials: map[breakerKey]bool{}, generations: map[breakerKey]string{}, quotaGenerations: map[quotaGenerationIdentity]string{}, routeGenerations: map[string]string{}, routeRefs: map[string]kernel.Route{}}
}
func (g *HealthGate) SetObserver(observer func(kernel.Route, RouteHealth)) { g.observe = observer }

func (g *HealthGate) SetGenerations(routes map[string]kernel.Route) []string {
	type entry struct {
		route kernel.Route
		scope kernel.OutcomeScope
		value string
	}
	next := make(map[breakerKey]string, len(routes)*4)
	nextQuotas := make(map[quotaGenerationIdentity]string, len(routes)*4)
	entries := make(map[breakerKey]entry, len(routes)*4)
	activeRoutes := make(map[string]struct{}, len(routes))
	nextRouteRefs := make(map[string]kernel.Route, len(routes))
	nextRouteGenerations := make(map[string]string, len(routes))
	for _, route := range routes {
		activeRoutes[route.ID] = struct{}{}
		nextRouteRefs[route.ID] = route
		nextRouteGenerations[route.ID] = route.HealthGeneration.For(kernel.ScopeRoute)
		for _, scope := range []kernel.OutcomeScope{kernel.ScopeRoute, kernel.ScopeRouteConnection, kernel.ScopeConnection, kernel.ScopeProvider} {
			key := outcomeScopeKey(route, scope)
			value := route.HealthGeneration.For(scope)
			next[key] = value
			entries[key] = entry{route: route, scope: scope, value: value}
			authKey := outcomeLaneKey(route, scope, HealthAuthentication)
			authValue := route.HealthGeneration.ForAuthentication(scope)
			next[authKey] = authValue
			entries[authKey] = entry{route: route, scope: scope, value: authValue}
		}
		for operation, binding := range route.OperationBindings {
			bound := route
			bound.SessionOperationRef = extensions.Ref{Kind: "operation", ID: string(operation), ContractVersion: binding.ContractVersion}
			key := outcomeScopeKey(bound, kernel.ScopeOperation)
			value := route.HealthGeneration.For(kernel.ScopeOperation)
			next[key] = value
			entries[key] = entry{route: bound, scope: kernel.ScopeOperation, value: value}
			authKey := outcomeLaneKey(bound, kernel.ScopeOperation, HealthAuthentication)
			authValue := route.HealthGeneration.ForAuthentication(kernel.ScopeOperation)
			next[authKey] = authValue
			entries[authKey] = entry{route: bound, scope: kernel.ScopeOperation, value: authValue}
		}
		for _, identity := range []struct{ connection, model string }{{"", ""}, {"", route.ExternalModel}, {route.CredentialID, ""}, {route.CredentialID, route.ExternalModel}} {
			key := quotaGenerationKey(route.NodeID, identity.connection, identity.model, extensions.Ref{})
			nextQuotas[key] = route.HealthGeneration.ForQuota(identity.connection, identity.model)
			for operation, binding := range route.OperationBindings {
				ref := extensions.Ref{Kind: "operation", ID: string(operation), ContractVersion: binding.ContractVersion}
				nextQuotas[quotaGenerationKey(route.NodeID, identity.connection, identity.model, ref)] = route.HealthGeneration.ForQuota(identity.connection, identity.model)
			}
		}
	}
	var observations []HealthObservation
	g.mu.Lock()
	previous := g.generations
	previousRoutes := g.routeGenerations
	g.generations, g.generationEnabled = next, true
	g.quotaGenerations = nextQuotas
	g.routeGenerations = nextRouteGenerations
	g.routeRefs = nextRouteRefs
	changedRoutes := make([]string, 0)
	for routeID, generation := range nextRouteGenerations {
		if previousGeneration, exists := previousRoutes[routeID]; !exists || previousGeneration != generation {
			changedRoutes = append(changedRoutes, routeID)
		}
	}
	for routeID := range previousRoutes {
		if _, exists := nextRouteGenerations[routeID]; !exists {
			changedRoutes = append(changedRoutes, routeID)
		}
	}
	for key, item := range entries {
		state, exists := g.scoped[key]
		if !exists || (previous[key] == item.value && state.Generation == item.value) {
			continue
		}
		state.Failures = 0
		state.Invalid = false
		state.Generation = item.value
		state.CooldownUntil = time.Time{}
		state.LastError = ""
		state.LastCause = kernel.CauseUnknown
		state.LastEvidence = ""
		state.Confidence = 0
		state.LastAttempt = time.Time{}
		state.LastSuccess = time.Time{}
		state.LastScope = item.scope
		state.Lane = key.lane.normalized()
		if item.scope == kernel.ScopeOperation {
			state.OperationRef = item.route.SessionOperationRef
		}
		g.scoped[key] = state
		delete(g.trials, key)
		observations = append(observations, HealthObservation{Route: item.route, State: state})
	}
	for _, route := range routes {
		if state, blocked := g.routeProjectionLocked(route); blocked {
			g.routes[route.ID] = state
		} else if state, exists := g.latestOperationalStateLocked(route); exists {
			g.routes[route.ID] = state
		} else {
			delete(g.routes, route.ID)
		}
	}
	for routeID := range g.routes {
		if _, exists := activeRoutes[routeID]; !exists {
			delete(g.routes, routeID)
		}
	}
	for key := range g.scoped {
		if _, exists := next[key]; !exists {
			delete(g.scoped, key)
			delete(g.trials, key)
		}
	}
	observer := g.observe
	g.mu.Unlock()
	if observer != nil {
		for _, observation := range observations {
			// Epoch invalidation is not new authentication evidence. Old rows
			// already carry the superseded credential generation; persisting a
			// neutral row here could race a real failure of the replacement token.
			if observation.State.Lane.normalized() == HealthAuthentication {
				continue
			}
			observer(observation.Route, observation.State)
		}
	}
	sort.Strings(changedRoutes)
	return changedRoutes
}

type quotaGenerationIdentity struct {
	provider, connection, model string
	operation                   extensions.Ref
}

func quotaGenerationKey(providerID, connectionID, modelRef string, operation extensions.Ref) quotaGenerationIdentity {
	return quotaGenerationIdentity{provider: providerID, connection: connectionID, model: modelRef, operation: operation}
}

func (g *HealthGate) AttachQuotaGeneration(snapshot *quota.Snapshot) bool {
	if snapshot == nil {
		return false
	}
	if snapshot.ValidateOperation() != nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.generationEnabled {
		return true
	}
	generation, exists := g.quotaGenerations[quotaGenerationKey(snapshot.ProviderNodeID, snapshot.ConnectionID, snapshot.ModelRef, snapshot.OperationRef)]
	if !exists {
		return false
	}
	snapshot.Generation = generation
	return true
}

func (g *HealthGate) QuotaGenerationCurrent(snapshot quota.Snapshot) bool {
	if snapshot.ValidateOperation() != nil {
		return false
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	if !g.generationEnabled {
		return true
	}
	generation, exists := g.quotaGenerations[quotaGenerationKey(snapshot.ProviderNodeID, snapshot.ConnectionID, snapshot.ModelRef, snapshot.OperationRef)]
	return exists && snapshot.Generation != "" && snapshot.Generation == generation
}

func (g *HealthGate) AcceptsOutcome(route kernel.Route, scope kernel.OutcomeScope) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.laneGenerationCurrentLocked(route, scope, HealthOperational)
}

func (g *HealthGate) AcceptsLaneOutcome(route kernel.Route, scope kernel.OutcomeScope, lane HealthLane) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.laneGenerationCurrentLocked(route, scope, lane)
}

func (g *HealthGate) generationCurrentLocked(route kernel.Route, scope kernel.OutcomeScope) bool {
	if scope == kernel.ScopeRequest {
		return false
	}
	if scope == kernel.ScopeOperation && (route.SessionOperationRef.Kind != "operation" || route.SessionOperationRef.Validate() != nil) {
		return false
	}
	if !g.generationEnabled {
		return true
	}
	key := outcomeScopeKey(route, scope)
	current, exists := g.generations[key]
	return exists && current == route.HealthGeneration.For(scope)
}

func (g *HealthGate) Usable(route kernel.Route, now time.Time) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if route.SessionOperationRef.ID != "" && !g.generationCurrentLocked(route, kernel.ScopeOperation) {
		return false
	}
	if g.generationEnabled {
		generation, ok := g.generations[outcomeScopeKey(route, kernel.ScopeRoute)]
		if !ok || generation != route.HealthGeneration.For(kernel.ScopeRoute) {
			return false
		}
	}
	for key := range healthKeys(route) {
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
	if route.SessionOperationRef.ID != "" && !g.generationCurrentLocked(route, kernel.ScopeOperation) {
		return false
	}
	if !g.generationCurrentLocked(route, kernel.ScopeRoute) {
		return false
	}
	acquired := make([]breakerKey, 0, 2)
	for key := range healthKeys(route) {
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
	if !g.generationCurrentLocked(route, kernel.ScopeRoute) {
		return
	}
	for key := range healthKeys(route) {
		delete(g.trials, key)
	}
}

// ObserveOutcome is the passive runtime boundary. It accepts evidence only
// from an actual request attempt; no probe or timer can make a route healthy.
func (g *HealthGate) ObserveOutcome(route kernel.Route, outcome kernel.ClassifiedOutcome) {
	now := time.Now()
	lane := HealthOperational
	if outcome.Cause == kernel.CauseAuth {
		lane = HealthAuthentication
	}
	g.mu.Lock()
	key := outcomeLaneKey(route, outcome.Scope, lane)
	if !g.laneGenerationCurrentLocked(route, outcome.Scope, lane) {
		g.mu.Unlock()
		return
	}
	state := g.scoped[key]
	if route.ID != "" {
		g.routeRefs[route.ID] = route
	}
	if outcome.Cause == kernel.CauseSuccess && state.CooldownUntil.After(now) && !state.Invalid && (state.LastCause == kernel.CauseRateLimited || state.LastCause == kernel.CauseQuotaExhausted) {
		state.Successes++
		state.LastAttempt = now
		state.LastSuccess = now
		g.scoped[key] = state
		delete(g.trials, key)
		observer := g.observe
		g.mu.Unlock()
		if observer != nil {
			observer(route, state)
		}
		return
	}
	state.LastAttempt = now
	state.LastCause = outcome.Cause
	state.LastScope = outcome.Scope
	if outcome.Scope == kernel.ScopeOperation {
		state.OperationRef = route.SessionOperationRef
	}
	state.Lane = lane
	state.Generation = laneGeneration(route, outcome.Scope, lane)
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
	if lane == HealthOperational {
		if projection, blocked := g.routeProjectionLocked(route); blocked {
			g.routes[route.ID] = projection
		} else if routeState := g.routes[route.ID]; outcome.Scope == kernel.ScopeRoute || routeState.LastAttempt.IsZero() || state.LastAttempt.After(routeState.LastAttempt) {
			g.routes[route.ID] = state
		}
	} else if current, blocked := g.routeProjectionLocked(route); blocked {
		g.routes[route.ID] = current
	} else if current := g.routes[route.ID]; current.LastAttempt.IsZero() || state.LastAttempt.After(current.LastAttempt) {
		g.routes[route.ID] = state
	}
	observer := g.observe
	var recovered []HealthObservation
	if outcome.Cause == kernel.CauseSuccess {
		for trial := range healthKeys(route) {
			if trial == key || trial.lane != lane || !g.trials[trial] {
				continue
			}
			prior := g.scoped[trial]
			if prior.Invalid || prior.CooldownUntil.After(now) {
				continue
			}
			prior.Failures, prior.Invalid, prior.CooldownUntil, prior.LastError = 0, false, time.Time{}, ""
			prior.Successes++
			prior.LastAttempt, prior.LastSuccess = now, now
			prior.LastCause, prior.LastScope = kernel.CauseSuccess, trial.scope
			prior.Lane = lane
			prior.LastEvidence, prior.Confidence = kernel.EvidenceSuccessBody, 1
			g.scoped[trial] = prior
			delete(g.trials, trial)
			recovered = append(recovered, HealthObservation{Route: route, State: prior})
		}
	}
	g.mu.Unlock()
	if observer != nil {
		observer(route, state)
		for _, observation := range recovered {
			observer(observation.Route, observation.State)
		}
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
	opChanged := g.resetScopeLane(route, scope, HealthOperational, false)
	authChanged := g.resetScopeLane(route, scope, HealthAuthentication, false)
	return opChanged || authChanged
}

// ClearInvalid reopens only a permanently invalid route after positive
// discovery evidence, preserving unrelated cooldowns and feedback.
func (g *HealthGate) ClearInvalid(route kernel.Route, scope kernel.OutcomeScope) bool {
	return g.resetScopeLane(route, scope, HealthOperational, true)
}

func (g *HealthGate) resetScopeLane(route kernel.Route, scope kernel.OutcomeScope, lane HealthLane, invalidOnly bool) bool {
	key := outcomeLaneKey(route, scope, lane)
	g.mu.Lock()
	if !g.laneGenerationCurrentLocked(route, scope, lane) {
		g.mu.Unlock()
		return false
	}
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
	if scope == kernel.ScopeOperation {
		state.OperationRef = route.SessionOperationRef
	}
	state.Lane = lane.normalized()
	state.Generation = laneGeneration(route, scope, lane)
	g.scoped[key] = state
	if route.ID != "" {
		g.routeRefs[route.ID] = route
	}
	if exists && route.ID != "" {
		if lane == HealthOperational {
			g.routes[route.ID] = state
		} else if projection, blocked := g.routeProjectionLocked(route); blocked {
			g.routes[route.ID] = projection
		} else if projection, found := g.latestOperationalStateLocked(route); found {
			g.routes[route.ID] = projection
		} else {
			delete(g.routes, route.ID)
		}
	}
	delete(g.trials, key)
	observer := g.observe
	g.mu.Unlock()
	if observer != nil {
		observer(route, state)
	}
	return true
}

func (g *HealthGate) routeProjectionLocked(route kernel.Route) (RouteHealth, bool) {
	var latest RouteHealth
	blocked := false
	for key := range healthKeys(route) {
		if key.lane != HealthOperational {
			continue
		}
		state, exists := g.scoped[key]
		if !exists {
			continue
		}
		active := state.Invalid || state.CooldownUntil.After(time.Now())
		if active && !blocked {
			latest, blocked = state, true
			continue
		}
		if active == blocked && (latest.LastAttempt.IsZero() || state.LastAttempt.After(latest.LastAttempt)) {
			latest = state
		}
	}
	return latest, blocked
}

func (g *HealthGate) latestOperationalStateLocked(route kernel.Route) (RouteHealth, bool) {
	var latest RouteHealth
	found := false
	for key := range healthKeys(route) {
		if key.lane != HealthOperational {
			continue
		}
		state, exists := g.scoped[key]
		if exists && (!found || state.LastAttempt.After(latest.LastAttempt)) {
			latest, found = state, true
		}
	}
	return latest, found
}
func (g *HealthGate) MarkFailure(route kernel.Route, class kernel.ErrorClass, err error) {
	g.MarkFailureAfter(route, class, err, 0)
}

func (g *HealthGate) MarkFailureAfter(route kernel.Route, class kernel.ErrorClass, err error, override time.Duration) {
	lane := HealthOperational
	if class == kernel.ErrorAuth {
		lane = HealthAuthentication
	}
	g.mu.Lock()
	if !g.laneGenerationCurrentLocked(route, kernel.ScopeRoute, lane) {
		g.mu.Unlock()
		return
	}
	key := outcomeLaneKey(route, kernel.ScopeRoute, lane)
	state := g.scoped[key]
	state.Lane = lane
	state.LastScope = kernel.ScopeRoute
	state.LastCause = errorCause(class)
	state.LastAttempt = time.Now()
	state.Generation = laneGeneration(route, kernel.ScopeRoute, lane)
	state.Failures++
	delay := failureDelay(state.Failures, errorCause(class))
	if override > delay {
		delay = override
	}
	state.CooldownUntil = time.Now().Add(delay)
	if err != nil {
		state.LastError = err.Error()
	}
	g.scoped[key] = state
	if route.ID != "" {
		g.routeRefs[route.ID] = route
	}
	if lane == HealthOperational {
		if projection, blocked := g.routeProjectionLocked(route); blocked {
			g.routes[route.ID] = projection
		} else {
			g.routes[route.ID] = state
		}
	} else if projection, blocked := g.routeProjectionLocked(route); blocked {
		g.routes[route.ID] = projection
	} else {
		g.routes[route.ID] = state
	}
	delete(g.trials, key)
	g.mu.Unlock()
	if g.observe != nil {
		g.observe(route, state)
	}
}
func (g *HealthGate) MarkSuccess(route kernel.Route) {
	g.mu.Lock()
	if !g.generationCurrentLocked(route, kernel.ScopeRoute) {
		g.mu.Unlock()
		return
	}
	key := outcomeScopeKey(route, kernel.ScopeRoute)
	state := g.scoped[key]
	now := time.Now()
	state.Lane = HealthOperational
	if state.CooldownUntil.After(now) && !state.Invalid {
		state.Successes++
		state.LastAttempt = now
		state.LastSuccess = now
		g.scoped[key] = state
		g.mu.Unlock()
		if g.observe != nil {
			g.observe(route, state)
		}
		return
	}
	state.Successes++
	state.Failures = 0
	state.CooldownUntil = time.Time{}
	state.LastError = ""
	state.LastCause = kernel.CauseSuccess
	state.LastScope = kernel.ScopeRoute
	state.Generation = route.HealthGeneration.For(kernel.ScopeRoute)
	state.LastAttempt = now
	state.LastSuccess = now
	g.scoped[key] = state
	g.routes[route.ID] = state
	for key := range healthKeys(route) {
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

func (g *HealthGate) Restore(route kernel.Route, state RouteHealth) bool {
	if state.LastScope == kernel.ScopeOperation && (state.OperationRef != route.SessionOperationRef || state.OperationRef.Validate() != nil) {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	lane := state.Lane.normalized()
	state.Lane = lane
	if g.generationEnabled && (!g.laneGenerationCurrentLocked(route, state.LastScope, lane) || state.Generation != laneGeneration(route, state.LastScope, lane)) {
		return false
	}
	key := outcomeLaneKey(route, state.LastScope, lane)
	if current, exists := g.scoped[key]; exists && current.Generation == state.Generation && !current.LastAttempt.IsZero() && !state.LastAttempt.After(current.LastAttempt) {
		return false
	}
	g.scoped[key] = state
	if route.ID != "" {
		g.routeRefs[route.ID] = route
	}
	if route.ID != "" {
		if projection, blocked := g.routeProjectionLocked(route); blocked {
			g.routes[route.ID] = projection
		} else if lane == HealthOperational {
			g.routes[route.ID] = state
		} else if projection, found := g.latestOperationalStateLocked(route); found {
			g.routes[route.ID] = projection
		} else if current := g.routes[route.ID]; current.LastAttempt.IsZero() || state.LastAttempt.After(current.LastAttempt) {
			g.routes[route.ID] = state
		}
	}
	return true
}
func (g *HealthGate) Snapshot() map[string]RouteHealthView {
	g.mu.RLock()
	defer g.mu.RUnlock()
	result := make(map[string]RouteHealthView, len(g.routes))
	for id, state := range g.routes {
		result[id] = RouteHealthView{RouteHealth: state}
	}
	for key, state := range g.scoped {
		if key.lane == HealthAuthentication {
			for routeID, route := range g.routeRefs {
				if !healthKeyAppliesToRoute(key, route) {
					continue
				}
				view := result[routeID]
				view.Authentication = append(view.Authentication, state)
				result[routeID] = view
			}
			continue
		}
		if key.scope != kernel.ScopeOperation {
			continue
		}
		view := result[key.route]
		view.Operations = append(view.Operations, state)
		result[key.route] = view
	}
	for id, view := range result {
		sort.Slice(view.Operations, func(i, j int) bool {
			return view.Operations[i].OperationRef.Key() < view.Operations[j].OperationRef.Key()
		})
		sort.Slice(view.Authentication, func(i, j int) bool {
			if view.Authentication[i].LastScope != view.Authentication[j].LastScope {
				return view.Authentication[i].LastScope < view.Authentication[j].LastScope
			}
			return view.Authentication[i].OperationRef.Key() < view.Authentication[j].OperationRef.Key()
		})
		result[id] = view
	}
	return result
}

func (g *HealthGate) StateFor(route kernel.Route) RouteHealth {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if route.SessionOperationRef.ID != "" {
		if state, exists := g.scoped[outcomeScopeKey(route, kernel.ScopeOperation)]; exists {
			return state
		}
	}
	state := g.routes[route.ID]
	if state.LastScope == kernel.ScopeOperation {
		return RouteHealth{}
	}
	return state
}

func (g *HealthGate) ScopedState(route kernel.Route, scope kernel.OutcomeScope) (RouteHealth, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	state, exists := g.scoped[outcomeScopeKey(route, scope)]
	return state, exists
}

func (g *HealthGate) ScopedAuthState(route kernel.Route, scope kernel.OutcomeScope) (RouteHealth, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	state, exists := g.scoped[outcomeLaneKey(route, scope, HealthAuthentication)]
	return state, exists
}

func (g *HealthGate) ScopedLaneState(route kernel.Route, scope kernel.OutcomeScope, lane HealthLane) (RouteHealth, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	state, exists := g.scoped[outcomeLaneKey(route, scope, lane)]
	return state, exists
}

type breakerKey struct {
	scope                       kernel.OutcomeScope
	lane                        HealthLane
	route, connection, provider string
	operation                   extensions.Ref
}

func scopeKey(route kernel.Route) breakerKey {
	return breakerKey{scope: kernel.ScopeRoute, lane: HealthOperational, route: route.ID}
}

func outcomeScopeKey(route kernel.Route, scope kernel.OutcomeScope) breakerKey {
	return outcomeLaneKey(route, scope, HealthOperational)
}

func outcomeLaneKey(route kernel.Route, scope kernel.OutcomeScope, lane HealthLane) breakerKey {
	switch scope {
	case kernel.ScopeOperation:
		return breakerKey{scope: scope, lane: lane.normalized(), route: route.ID, connection: route.CredentialID, operation: route.SessionOperationRef}
	case kernel.ScopeConnection:
		return breakerKey{scope: scope, lane: lane.normalized(), connection: route.CredentialID}
	case kernel.ScopeProvider:
		return breakerKey{scope: scope, lane: lane.normalized(), provider: route.NodeID}
	case kernel.ScopeRouteConnection:
		return breakerKey{scope: scope, lane: lane.normalized(), route: route.ID, connection: route.CredentialID}
	default:
		key := scopeKey(route)
		key.lane = lane.normalized()
		return key
	}
}

func laneGeneration(route kernel.Route, scope kernel.OutcomeScope, lane HealthLane) string {
	if lane.normalized() == HealthAuthentication {
		return route.HealthGeneration.ForAuthentication(scope)
	}
	return route.HealthGeneration.For(scope)
}

func (g *HealthGate) laneGenerationCurrentLocked(route kernel.Route, scope kernel.OutcomeScope, lane HealthLane) bool {
	if scope == kernel.ScopeRequest {
		return false
	}
	if scope == kernel.ScopeOperation && (route.SessionOperationRef.Kind != "operation" || route.SessionOperationRef.Validate() != nil) {
		return false
	}
	if !g.generationEnabled {
		return true
	}
	current, exists := g.generations[outcomeLaneKey(route, scope, lane)]
	return exists && current == laneGeneration(route, scope, lane)
}

func healthKeys(route kernel.Route) iter.Seq[breakerKey] {
	return func(yield func(breakerKey) bool) {
		if !yield(scopeKey(route)) || !yield(outcomeLaneKey(route, kernel.ScopeRoute, HealthAuthentication)) {
			return
		}
		if route.SessionOperationRef.ID != "" {
			if !yield(outcomeScopeKey(route, kernel.ScopeOperation)) || !yield(outcomeLaneKey(route, kernel.ScopeOperation, HealthAuthentication)) {
				return
			}
		}
		if route.CredentialID != "" {
			if !yield(outcomeScopeKey(route, kernel.ScopeRouteConnection)) || !yield(outcomeScopeKey(route, kernel.ScopeConnection)) || !yield(outcomeLaneKey(route, kernel.ScopeRouteConnection, HealthAuthentication)) || !yield(outcomeLaneKey(route, kernel.ScopeConnection, HealthAuthentication)) {
				return
			}
		}
		if route.NodeID != "" {
			if !yield(outcomeScopeKey(route, kernel.ScopeProvider)) || !yield(outcomeLaneKey(route, kernel.ScopeProvider, HealthAuthentication)) {
				return
			}
		}
	}
}

func healthKeyAppliesToRoute(key breakerKey, route kernel.Route) bool {
	switch key.scope {
	case kernel.ScopeProvider:
		return key.provider != "" && key.provider == route.NodeID
	case kernel.ScopeConnection:
		return key.connection != "" && key.connection == route.CredentialID
	case kernel.ScopeRouteConnection:
		return key.route == route.ID && key.connection == route.CredentialID
	case kernel.ScopeOperation:
		return key.route == route.ID
	default:
		return key.route == route.ID
	}
}
