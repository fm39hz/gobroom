package runtime

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/quota"
)

type PolicyGate struct {
	Health      *HealthGate
	Performance *PerformanceBook
	mu          sync.RWMutex
	quotas      map[string]quota.Snapshot
	enrich      func(context.Context, kernel.Route)
	affinityMu  sync.Mutex
	affinity    map[string]sessionRoute
}

type sessionRoute struct {
	RouteID string
	SeenAt  time.Time
}

type RouteExplanation struct {
	Route       kernel.Route      `json:"route"`
	Rank        int               `json:"rank"`
	Usable      bool              `json:"usable"`
	Reason      string            `json:"reason"`
	Health      RouteHealth       `json:"health"`
	Performance *PerformanceStats `json:"performance,omitempty"`
}

func NewPolicyGate() *PolicyGate {
	return &PolicyGate{Health: NewHealthGate(), Performance: NewPerformanceBook(), quotas: map[string]quota.Snapshot{}, affinity: map[string]sessionRoute{}}
}
func (g *PolicyGate) Usable(route kernel.Route, now time.Time) bool {
	if !g.Health.Usable(route, now) {
		return false
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, snapshot := range g.quotas {
		if snapshot.ProviderNodeID == route.NodeID && (snapshot.ModelRef == "" || snapshot.ModelRef == route.ExternalModel) && (snapshot.ConnectionID == "" || snapshot.ConnectionID == route.CredentialID) && snapshot.Exhausted(now) {
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

func (g *PolicyGate) SetQuotaEnricher(enrich func(context.Context, kernel.Route)) { g.enrich = enrich }

func (g *PolicyGate) ObserveOutcome(route kernel.Route, outcome kernel.ClassifiedOutcome) {
	g.Health.ObserveOutcome(route, outcome)
	if (outcome.Cause == kernel.CauseQuotaExhausted || outcome.Cause == kernel.CauseRateLimited) && g.enrich != nil {
		g.enrich(context.Background(), route)
	}
	for _, limit := range outcome.Limits {
		if limit.Name == "" {
			continue
		}
		snapshot := quota.Snapshot{ProviderNodeID: route.NodeID, ConnectionID: route.CredentialID, ModelRef: route.ExternalModel, WindowName: limit.Name, Source: string(limit.Source)}
		snapshot.Limit, snapshot.Remaining, snapshot.ResetAt = limit.Limit, limit.Remaining, limit.ResetAt
		if limit.Used != nil {
			snapshot.Used = *limit.Used
		}
		g.SetQuota(snapshot)
	}
}

func (g *PolicyGate) ObserveUsage(route kernel.Route, event kernel.UsageEvent) {
	if g.Performance != nil {
		g.Performance.Observe(route, event)
	}
	if event.SessionID != "" {
		g.affinityMu.Lock()
		if len(g.affinity) >= 2048 {
			g.expireAffinity(time.Now(), true)
		}
		g.affinity[event.SessionID] = sessionRoute{RouteID: route.ID, SeenAt: time.Now()}
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
	states := g.Health.Snapshot()
	performance := map[string]PerformanceStats{}
	if g.Performance != nil {
		performance = g.Performance.Snapshot()
	}
	result := append([]kernel.Route(nil), routes...)
	score := func(route kernel.Route) float64 {
		state := states[route.ID]
		if !state.LastAttempt.IsZero() && now.Sub(state.LastAttempt) > 30*time.Minute {
			return 0
		}
		value := float64(state.Successes - state.Failures*3)
		if !state.LastSuccess.IsZero() {
			value += 0.25
		}
		if g.Performance != nil {
			perf, ok := performance[route.ID]
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
	sort.SliceStable(result, func(i, j int) bool { return score(result[i]) > score(result[j]) })
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
	affinity, ok := g.affinity[sessionID]
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
	health := g.Health.Snapshot()
	performance := map[string]PerformanceStats{}
	if g.Performance != nil {
		performance = g.Performance.Snapshot()
	}
	result := make([]RouteExplanation, 0, len(ordered))
	for index, route := range ordered {
		state := health[route.ID]
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
	g.mu.Lock()
	defer g.mu.Unlock()
	g.quotas[quotaKey(snapshot)] = snapshot
}
func (g *PolicyGate) Quotas() map[string]quota.Snapshot {
	g.mu.RLock()
	defer g.mu.RUnlock()
	result := make(map[string]quota.Snapshot, len(g.quotas))
	for key, value := range g.quotas {
		result[key] = value
	}
	return result
}
func quotaKey(snapshot quota.Snapshot) string {
	return snapshot.ProviderNodeID + "\x00" + snapshot.ConnectionID + "\x00" + snapshot.ModelRef + "\x00" + snapshot.WindowName
}
