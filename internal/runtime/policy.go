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
}

func NewPolicyGate() *PolicyGate {
	return &PolicyGate{Health: NewHealthGate(), Performance: NewPerformanceBook(), quotas: map[string]quota.Snapshot{}}
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
