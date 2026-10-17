package runtime

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/quota"
)

type PolicyGate struct {
	Health        *HealthGate
	Performance   *PerformanceBook
	generationMu  sync.RWMutex
	mu            sync.RWMutex
	quotas        map[quota.Identity]quota.Snapshot
	enrich        func(context.Context, kernel.Route)
	enrichCtx     context.Context
	quotaObserver func(quota.Snapshot)
	affinityMu    sync.Mutex
	affinity      map[affinityKey]sessionRoute
}

type affinityKey struct {
	session   string
	operation extensions.Ref
}

type sessionRoute struct {
	RouteID string
	SeenAt  time.Time
}

type RouteExplanation struct {
	Route         kernel.Route                           `json:"route"`
	Rank          int                                    `json:"rank"`
	Usable        bool                                   `json:"usable"`
	Reason        string                                 `json:"reason"`
	Health        RouteHealth                            `json:"health"`
	Performance   *PerformanceStats                      `json:"performance,omitempty"`
	Compatibility []kernel.RouteCompatibilityExplanation `json:"compatibility,omitempty"`
}

func NewPolicyGate() *PolicyGate {
	return &PolicyGate{Health: NewHealthGate(), Performance: NewPerformanceBook(), quotas: map[quota.Identity]quota.Snapshot{}, affinity: map[affinityKey]sessionRoute{}, enrichCtx: context.Background()}
}

func (g *PolicyGate) SetGenerations(routes map[string]kernel.Route) {
	g.generationMu.Lock()
	changed := g.Health.SetGenerations(routes)
	g.resetRouteGenerations(changed)
	g.pruneStaleQuotas()
	g.generationMu.Unlock()
}

func (g *PolicyGate) Usable(route kernel.Route, now time.Time) bool {
	g.generationMu.RLock()
	defer g.generationMu.RUnlock()
	if !g.Health.Usable(route, now) {
		return false
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, snapshot := range g.quotas {
		if snapshot.ProviderNodeID == route.NodeID && (snapshot.ModelRef == "" || snapshot.ModelRef == route.ExternalModel) && (snapshot.ConnectionID == "" || snapshot.ConnectionID == route.CredentialID) && (snapshot.OperationRef.ID == "" || snapshot.OperationRef == route.SessionOperationRef) && snapshot.Exhausted(now) {
			return false
		}
	}
	return true
}
func (g *PolicyGate) MarkFailure(route kernel.Route, class kernel.ErrorClass, err error) {
	g.Health.MarkFailure(route, class, err)
}
func (g *PolicyGate) MarkFailureAfter(route kernel.Route, class kernel.ErrorClass, err error, delay time.Duration) {
	g.Health.MarkFailureAfter(route, class, err, delay)
}
func (g *PolicyGate) MarkSuccess(route kernel.Route) { g.Health.MarkSuccess(route) }

func (g *PolicyGate) SetQuotaEnricher(enrich func(context.Context, kernel.Route)) {
	g.mu.Lock()
	g.enrich = enrich
	g.mu.Unlock()
}
func (g *PolicyGate) SetEnrichmentContext(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	g.mu.Lock()
	g.enrichCtx = ctx
	g.mu.Unlock()
}
func (g *PolicyGate) SetOutcomeQuotaObserver(observer func(quota.Snapshot)) {
	g.mu.Lock()
	g.quotaObserver = observer
	g.mu.Unlock()
}

func (g *PolicyGate) ObserveOutcome(route kernel.Route, outcome kernel.ClassifiedOutcome) {
	g.generationMu.RLock()
	defer g.generationMu.RUnlock()
	if !g.Health.AcceptsOutcome(route, outcome.Scope) {
		return
	}
	g.Health.ObserveOutcome(route, outcome)
	g.mu.RLock()
	enrich, enrichCtx := g.enrich, g.enrichCtx
	g.mu.RUnlock()
	if (outcome.Cause == kernel.CauseQuotaExhausted || outcome.Cause == kernel.CauseRateLimited) && enrich != nil {
		enrich(enrichCtx, route)
	}
	for _, limit := range outcome.Limits {
		if limit.Name == "" {
			continue
		}
		authority := quota.AuthorityFromSource(string(limit.Source))
		if authority == quota.AuthorityUnknown {
			authority = quota.AuthorityInferred
		}
		snapshot := quota.Snapshot{ProviderNodeID: route.NodeID, ConnectionID: route.CredentialID, ModelRef: route.ExternalModel, WindowName: limit.Name, Source: string(limit.Source), Authority: authority, ObservedAt: time.Now(), Generation: route.HealthGeneration.ForQuota(route.CredentialID, route.ExternalModel)}
		if outcome.Scope == kernel.ScopeOperation {
			snapshot.OperationRef = route.SessionOperationRef
		}
		snapshot.Limit, snapshot.Remaining, snapshot.ResetAt = limit.Limit, limit.Remaining, limit.ResetAt
		if limit.Used != nil {
			snapshot.Used = *limit.Used
		}
		resolved, accepted := g.upsertQuotaAtCurrentGeneration(snapshot)
		if !accepted {
			continue
		}
		g.mu.RLock()
		observer := g.quotaObserver
		g.mu.RUnlock()
		if observer != nil {
			observer(resolved)
		}
	}
}

func (g *PolicyGate) ObserveUsage(route kernel.Route, event kernel.UsageEvent) {
	g.generationMu.RLock()
	defer g.generationMu.RUnlock()
	if !g.Health.AcceptsOutcome(route, kernel.ScopeRoute) {
		return
	}
	if g.Performance != nil {
		g.Performance.Observe(route, event)
	}
	if event.SessionID != "" {
		g.affinityMu.Lock()
		if len(g.affinity) >= 2048 {
			g.expireAffinity(time.Now(), true)
		}
		g.affinity[affinityKey{session: event.SessionID, operation: route.SessionOperationRef}] = sessionRoute{RouteID: route.ID, SeenAt: time.Now()}
		g.affinityMu.Unlock()
	}
}

// RankRoutes performs bounded, explainable feedback ranking. It never changes
// durable user order: unknown routes retain their relative order and the
// adaptive score only orders routes within the same eligible set.
func (g *PolicyGate) RankRoutes(routes []kernel.Route, now time.Time) []kernel.Route {
	return g.RankRoutesFor(routes, now, "")
}

func (g *PolicyGate) RankRoutesFor(routes []kernel.Route, now time.Time, requestClass string) []kernel.Route {
	g.generationMu.RLock()
	defer g.generationMu.RUnlock()
	score := func(route kernel.Route) float64 {
		state := g.Health.StateFor(route)
		if !state.LastAttempt.IsZero() && now.Sub(state.LastAttempt) > 30*time.Minute {
			return 0
		}
		value := float64(state.Successes - state.Failures*3)
		if !state.LastSuccess.IsZero() {
			value += 0.25
		}
		if g.Performance != nil {
			var perf PerformanceStats
			var ok bool
			if route.SessionOperationRef.ID == "" {
				perf, ok = g.Performance.RouteSnapshot(route.ID)
			}
			if requestClass != "" {
				if classPerf, classOK := g.Performance.ClassSnapshot(route.ID, requestClass); classOK && classPerf.Samples >= 2 {
					perf, ok = classPerf, true
				}
			}
			if ok && perf.Samples > 0 {
				reliability := float64(perf.Successes) / float64(perf.Samples)
				value += reliability
				if perf.EWMLatency > 0 {
					value += 1 / (1 + perf.EWMLatency.Seconds())
				}
			}
		}
		return value
	}
	// Freeze each score once: live feedback must not change a sort comparator,
	// and the hot path need not re-lock health/performance for every comparison.
	ranked := make([]struct {
		index int
		score float64
	}, len(routes))
	for index, route := range routes {
		ranked[index].index, ranked[index].score = index, score(route)
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
	result := make([]kernel.Route, len(routes))
	for index, rankedRoute := range ranked {
		result[index] = routes[rankedRoute.index]
	}
	return result
}

func (g *PolicyGate) RankRoutesForSession(routes []kernel.Route, now time.Time, requestClass, sessionID string) []kernel.Route {
	result := g.RankRoutesFor(routes, now, requestClass)
	if sessionID == "" {
		return result
	}
	g.affinityMu.Lock()
	defer g.affinityMu.Unlock()
	g.expireAffinity(now, false)
	operation := extensions.Ref{}
	if len(routes) > 0 {
		operation = routes[0].SessionOperationRef
	}
	for _, route := range routes {
		if route.SessionOperationRef != operation {
			return result
		}
	}
	affinity, ok := g.affinity[affinityKey{session: sessionID, operation: operation}]
	if !ok || now.Sub(affinity.SeenAt) > 30*time.Minute {
		return result
	}
	for index, route := range result {
		if route.ID == affinity.RouteID && index > 0 {
			return append([]kernel.Route{route}, append(result[:index:index], result[index+1:]...)...)
		}
	}
	return result
}

func (g *PolicyGate) ExplainRoutes(routes []kernel.Route, now time.Time, requestClass, sessionID string) []RouteExplanation {
	ordered := g.RankRoutesForSession(routes, now, requestClass, sessionID)
	performance := map[string]PerformanceStats{}
	if g.Performance != nil {
		performance = g.Performance.Snapshot()
	}
	result := make([]RouteExplanation, 0, len(ordered))
	for index, route := range ordered {
		state := g.Health.StateFor(route)
		reason := "eligible; static order"
		if !g.Usable(route, now) {
			reason = "blocked by health/quota"
		} else if !state.LastSuccess.IsZero() {
			reason = "adaptive success preference"
		}
		var perf *PerformanceStats
		if item, ok := performance[route.ID]; ok {
			copy := item
			perf = &copy
		}
		result = append(result, RouteExplanation{Route: route, Rank: index + 1, Usable: g.Usable(route, now), Reason: reason, Health: state, Performance: perf})
	}
	return result
}

func (g *PolicyGate) expireAffinity(now time.Time, forceOne bool) {
	for id, item := range g.affinity {
		if now.Sub(item.SeenAt) > 30*time.Minute {
			delete(g.affinity, id)
		}
	}
	if forceOne && len(g.affinity) >= 2048 {
		for id := range g.affinity {
			delete(g.affinity, id)
			break
		}
	}
}
func (g *PolicyGate) SetQuota(snapshot quota.Snapshot) {
	if normalizeQuotaObservation(&snapshot, quota.AuthorityExplicit) != nil {
		return
	}
	g.setQuota(snapshot)
}

func (g *PolicyGate) setQuota(snapshot quota.Snapshot) quota.Snapshot {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := quotaKey(snapshot)
	if current, exists := g.quotas[key]; exists {
		snapshot = quota.MergeSnapshot(current, snapshot)
	}
	g.quotas[key] = snapshot
	return snapshot
}

func (g *PolicyGate) TrySetQuota(snapshot quota.Snapshot) bool {
	_, accepted := g.UpsertQuota(snapshot)
	return accepted
}

func (g *PolicyGate) UpsertQuota(snapshot quota.Snapshot) (quota.Snapshot, bool) {
	g.generationMu.RLock()
	defer g.generationMu.RUnlock()
	return g.upsertQuotaAtCurrentGeneration(snapshot)
}

// RecordQuota applies evidence and forwards the resolved merge while holding
// the route-generation read fence, so a reload cannot race the persistence
// enqueue and make old-generation quota evidence appear current.
func (g *PolicyGate) RecordQuota(snapshot quota.Snapshot, record func(quota.Snapshot)) bool {
	g.generationMu.RLock()
	defer g.generationMu.RUnlock()
	resolved, accepted := g.upsertQuotaAtCurrentGeneration(snapshot)
	if !accepted {
		return false
	}
	if record != nil {
		record(resolved)
	}
	return true
}

func (g *PolicyGate) ReplayHealthObservation(observation HealthObservation, enqueue func(HealthObservation) bool) (bool, bool) {
	g.generationMu.RLock()
	defer g.generationMu.RUnlock()
	scope := observation.State.LastScope
	lane := observation.State.Lane.normalized()
	if !g.Health.AcceptsLaneOutcome(observation.Route, scope, lane) || observation.State.Generation != laneGeneration(observation.Route, scope, lane) {
		return false, false
	}
	g.Health.Restore(observation.Route, observation.State)
	state, exists := g.Health.ScopedLaneState(observation.Route, scope, lane)
	if !exists || state.Generation != observation.State.Generation {
		return false, false
	}
	observation.State = state
	if enqueue == nil {
		return true, false
	}
	return true, enqueue(observation)
}

func (g *PolicyGate) upsertQuotaAtCurrentGeneration(snapshot quota.Snapshot) (quota.Snapshot, bool) {
	if !g.Health.QuotaGenerationCurrent(snapshot) {
		return quota.Snapshot{}, false
	}
	if err := normalizeQuotaObservation(&snapshot, quota.AuthorityQuotaAPI); err != nil {
		return quota.Snapshot{}, false
	}
	return g.setQuota(snapshot), true
}

func normalizeQuotaObservation(snapshot *quota.Snapshot, fallback quota.EvidenceAuthority) error {
	if snapshot == nil {
		return fmt.Errorf("quota snapshot is required")
	}
	if err := snapshot.ValidateOperation(); err != nil {
		return err
	}
	if snapshot.ObservedAt.IsZero() {
		snapshot.ObservedAt = time.Now()
	}
	if snapshot.Authority == quota.AuthorityUnknown {
		snapshot.Authority = quota.AuthorityFromSource(snapshot.Source)
		if snapshot.Authority == quota.AuthorityUnknown {
			snapshot.Authority = fallback
		}
	}
	if snapshot.Source == "" && snapshot.Authority != quota.AuthorityUnknown {
		snapshot.Source = snapshot.Authority.String()
	}
	return nil
}

func (g *PolicyGate) ClearQuotasForConnection(connectionID string) {
	if connectionID == "" {
		return
	}
	g.generationMu.Lock()
	defer g.generationMu.Unlock()
	g.mu.Lock()
	for key, snapshot := range g.quotas {
		if snapshot.ConnectionID == connectionID {
			delete(g.quotas, key)
		}
	}
	g.mu.Unlock()
}

func (g *PolicyGate) ClearQuotasForProvider(providerNodeID string) {
	if providerNodeID == "" {
		return
	}
	g.generationMu.Lock()
	defer g.generationMu.Unlock()
	g.mu.Lock()
	for key, snapshot := range g.quotas {
		if snapshot.ProviderNodeID == providerNodeID {
			delete(g.quotas, key)
		}
	}
	g.mu.Unlock()
}

func (g *PolicyGate) PruneStaleQuotas() {
	g.generationMu.Lock()
	defer g.generationMu.Unlock()
	g.pruneStaleQuotas()
}

func (g *PolicyGate) pruneStaleQuotas() {
	g.mu.Lock()
	for key, snapshot := range g.quotas {
		if !g.Health.QuotaGenerationCurrent(snapshot) {
			delete(g.quotas, key)
		}
	}
	g.mu.Unlock()
}

func (g *PolicyGate) resetRouteGenerations(routeIDs []string) {
	if len(routeIDs) == 0 {
		return
	}
	if g.Performance != nil {
		g.Performance.ResetRoutes(routeIDs)
	}
	changed := make(map[string]struct{}, len(routeIDs))
	for _, routeID := range routeIDs {
		changed[routeID] = struct{}{}
	}
	g.affinityMu.Lock()
	for sessionID, route := range g.affinity {
		if _, stale := changed[route.RouteID]; stale {
			delete(g.affinity, sessionID)
		}
	}
	g.affinityMu.Unlock()
}
func (g *PolicyGate) Quotas() map[quota.Identity]quota.Snapshot {
	g.mu.RLock()
	defer g.mu.RUnlock()
	result := make(map[quota.Identity]quota.Snapshot, len(g.quotas))
	for key, value := range g.quotas {
		result[key] = value
	}
	return result
}
func quotaKey(snapshot quota.Snapshot) quota.Identity {
	return snapshot.Identity()
}
