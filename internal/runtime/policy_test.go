package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/quota"
)

func TestPolicyGateBlocksExhaustedQuota(t *testing.T) {
	gate := NewPolicyGate()
	route := kernel.Route{ID: "r", NodeID: "node", ExternalModel: "model", Enabled: true}
	remaining := float64(0)
	reset := time.Now().Add(time.Hour)
	gate.SetQuota(quota.Snapshot{ProviderNodeID: "node", ModelRef: "model", WindowName: "daily", Remaining: &remaining, ResetAt: &reset})
	if gate.Usable(route, time.Now()) {
		t.Fatal("exhausted quota should block route")
	}
}

func TestPolicyGateRetainsObservedBalanceWithoutHardBlocking(t *testing.T) {
	gate := NewPolicyGate()
	route := kernel.Route{ID: "route", NodeID: "gateway", CredentialID: "account", ExternalModel: "provider/model", Enabled: true}
	remaining := float64(0)
	gate.SetQuota(quota.Snapshot{ProviderNodeID: route.NodeID, ConnectionID: route.CredentialID, WindowName: "balance-usd", Remaining: &remaining, RoutingEffect: quota.RoutingEffectObserve, Source: "http-json-quota"})
	if !gate.Usable(route, time.Now()) {
		t.Fatal("an observed credit balance should remain visible without blocking pay-as-you-go routing")
	}
	if snapshot := gate.Quotas()[quotaKey(quota.Snapshot{ProviderNodeID: route.NodeID, ConnectionID: route.CredentialID, WindowName: "balance-usd"})]; snapshot.RoutingEffect != quota.RoutingEffectObserve {
		t.Fatalf("observed routing effect was not retained in policy state: %#v", snapshot)
	}
}

func TestPolicyGateClearsQuotaEvidenceAfterConfigurationRecovery(t *testing.T) {
	gate := NewPolicyGate()
	first := kernel.Route{ID: "route-a", NodeID: "provider", CredentialID: "connection-a", ExternalModel: "model"}
	second := kernel.Route{ID: "route-b", NodeID: "provider", CredentialID: "connection-b", ExternalModel: "model"}
	remaining := float64(0)
	reset := time.Now().Add(time.Hour)
	gate.SetQuota(quota.Snapshot{ProviderNodeID: "provider", ConnectionID: first.CredentialID, ModelRef: "model", WindowName: "daily", Remaining: &remaining, ResetAt: &reset})
	gate.SetQuota(quota.Snapshot{ProviderNodeID: "provider", ConnectionID: second.CredentialID, ModelRef: "model", WindowName: "daily", Remaining: &remaining, ResetAt: &reset})
	gate.ClearQuotasForConnection(first.CredentialID)
	if !gate.Usable(first, time.Now()) || gate.Usable(second, time.Now()) {
		t.Fatal("connection recovery must clear only that connection's cached quota evidence")
	}
	gate.ClearQuotasForProvider(first.NodeID)
	if !gate.Usable(second, time.Now()) {
		t.Fatal("provider configuration recovery must clear its remaining quota evidence")
	}
}

func TestPolicyGateFencesQuotaEvidenceByRouteGeneration(t *testing.T) {
	gate := NewPolicyGate()
	oldRoute := kernel.Route{ID: "route", NodeID: "provider", CredentialID: "connection", ExternalModel: "model", HealthGeneration: kernel.RouteHealthGeneration{Provider: "provider-v1", Connection: "credential-v1", Model: "model-v1"}}
	gate.SetGenerations(map[string]kernel.Route{oldRoute.ID: oldRoute})
	remaining := float64(0)
	reset := time.Now().Add(time.Hour)
	oldQuota := quota.Snapshot{ProviderNodeID: "provider", ConnectionID: "connection", ModelRef: "model", WindowName: "daily", Remaining: &remaining, ResetAt: &reset, Generation: oldRoute.HealthGeneration.ForQuota("connection", "model")}
	if !gate.TrySetQuota(oldQuota) || gate.Usable(oldRoute, time.Now()) {
		t.Fatal("matching-generation quota evidence was not applied")
	}

	newRoute := oldRoute
	newRoute.HealthGeneration.Connection = "credential-v2"
	gate.SetGenerations(map[string]kernel.Route{newRoute.ID: newRoute})
	gate.PruneStaleQuotas()
	if !gate.Usable(newRoute, time.Now()) {
		t.Fatal("old quota evidence survived a connection generation change")
	}
	if gate.TrySetQuota(oldQuota) {
		t.Fatal("late quota evidence from the previous credential generation was accepted")
	}
	newQuota := oldQuota
	newQuota.Generation = newRoute.HealthGeneration.ForQuota("connection", "model")
	if !gate.TrySetQuota(newQuota) || gate.Usable(newRoute, time.Now()) {
		t.Fatal("new-generation quota evidence was not accepted")
	}
}

func TestPolicyGateIgnoresLateOutcomeAndUsageFromOldRouteGeneration(t *testing.T) {
	gate := NewPolicyGate()
	oldRoute := kernel.Route{ID: "route", NodeID: "provider", CredentialID: "connection", ExternalModel: "model", HealthGeneration: kernel.RouteHealthGeneration{Provider: "provider-v1", Connection: "credential-v1", Model: "model-v1"}}
	gate.SetGenerations(map[string]kernel.Route{oldRoute.ID: oldRoute})
	gate.ObserveUsage(oldRoute, kernel.UsageEvent{At: time.Now(), Status: "ok", SessionID: "old-session"})
	newRoute := oldRoute
	newRoute.HealthGeneration.Connection = "credential-v2"
	gate.SetGenerations(map[string]kernel.Route{newRoute.ID: newRoute})
	enrichmentCalls, quotaObservations := 0, 0
	gate.SetQuotaEnricher(func(context.Context, kernel.Route) { enrichmentCalls++ })
	gate.SetOutcomeQuotaObserver(func(quota.Snapshot) { quotaObservations++ })
	gate.ObserveOutcome(oldRoute, kernel.ClassifiedOutcome{Cause: kernel.CauseRateLimited, Scope: kernel.ScopeConnection, Retry: kernel.RetryAfter, Limits: []kernel.LimitWindow{{Name: "daily", Source: kernel.EvidenceResponseHeader}}})
	gate.ObserveUsage(oldRoute, kernel.UsageEvent{At: time.Now(), Status: "ok", SessionID: "old-session"})
	if enrichmentCalls != 0 || quotaObservations != 0 || len(gate.Quotas()) != 0 || len(gate.Health.Snapshot()) != 0 || len(gate.Performance.Snapshot()) != 0 {
		t.Fatalf("late old-generation evidence leaked into runtime: enrich=%d quota=%d quotas=%#v health=%#v performance=%#v", enrichmentCalls, quotaObservations, gate.Quotas(), gate.Health.Snapshot(), gate.Performance.Snapshot())
	}
}

func TestProviderModelQuotaGenerationIsStableAcrossConnections(t *testing.T) {
	gate := NewPolicyGate()
	first := kernel.Route{ID: "route-a", NodeID: "provider", CredentialID: "connection-a", ExternalModel: "model", HealthGeneration: kernel.RouteHealthGeneration{Provider: "provider-v1", Connection: "credential-a", Model: "catalog-route-a"}}
	second := kernel.Route{ID: "route-b", NodeID: "provider", CredentialID: "connection-b", ExternalModel: "model", HealthGeneration: kernel.RouteHealthGeneration{Provider: "provider-v1", Connection: "credential-b", Model: "catalog-route-b"}}
	gate.Health.SetGenerations(map[string]kernel.Route{first.ID: first, second.ID: second})
	snapshot := quota.Snapshot{ProviderNodeID: "provider", ModelRef: "model", WindowName: "daily"}
	if !gate.Health.AttachQuotaGeneration(&snapshot) || !gate.Health.QuotaGenerationCurrent(snapshot) {
		t.Fatalf("provider/model quota did not resolve across account routes: %#v", snapshot)
	}
}

func TestConnectionScopedQuotaAppliesAcrossModelsOnlyWithinItsConnection(t *testing.T) {
	gate := NewPolicyGate()
	routeA := kernel.Route{ID: "route-a", NodeID: "provider", CredentialID: "connection-a", ExternalModel: "model-a", Enabled: true, HealthGeneration: kernel.RouteHealthGeneration{Provider: "provider-v1", Connection: "connection-v1", Model: "model-a-v1"}}
	routeB := kernel.Route{ID: "route-b", NodeID: "provider", CredentialID: "connection-a", ExternalModel: "model-b", Enabled: true, HealthGeneration: kernel.RouteHealthGeneration{Provider: "provider-v1", Connection: "connection-v1", Model: "model-b-v1"}}
	otherConnection := kernel.Route{ID: "route-other", NodeID: "provider", CredentialID: "connection-b", ExternalModel: "model-a", Enabled: true, HealthGeneration: kernel.RouteHealthGeneration{Provider: "provider-v1", Connection: "connection-b-v1", Model: "model-other-v1"}}
	gate.SetGenerations(map[string]kernel.Route{routeA.ID: routeA, routeB.ID: routeB, otherConnection.ID: otherConnection})
	remaining := float64(0)
	reset := time.Now().Add(time.Hour)
	snapshot := quota.Snapshot{ProviderNodeID: "provider", ConnectionID: "connection-a", WindowName: "account-rpm", Remaining: &remaining, ResetAt: &reset, Generation: routeA.HealthGeneration.ForQuota("connection-a", "")}
	if !gate.TrySetQuota(snapshot) {
		t.Fatal("matching connection-scoped quota was rejected")
	}
	if gate.Usable(routeA, time.Now()) || gate.Usable(routeB, time.Now()) || !gate.Usable(otherConnection, time.Now()) {
		t.Fatal("connection-scoped quota should gate all models on one connection, but not another connection")
	}
}

func TestPolicyGateResolvesQuotaSourcesByFreshnessAndAuthority(t *testing.T) {
	gate := NewPolicyGate()
	remainingZero := float64(0)
	remainingFull := float64(12)
	at := time.Now().UTC().Truncate(time.Second)
	header := quota.Snapshot{ProviderNodeID: "provider", ConnectionID: "connection", ModelRef: "model", WindowName: "daily", Remaining: &remainingZero, Source: "response_header", Authority: quota.AuthorityResponseHeader, ObservedAt: at}
	gate.SetQuota(header)
	olderAPI := quota.Snapshot{ProviderNodeID: "provider", ConnectionID: "connection", ModelRef: "model", WindowName: "daily", Remaining: &remainingFull, Source: "http-json-quota", Authority: quota.AuthorityQuotaAPI, ObservedAt: at.Add(-time.Second)}
	if !gate.TrySetQuota(olderAPI) {
		t.Fatal("quota snapshot with no generation was rejected before generation fencing is enabled")
	}
	got := gate.Quotas()[quotaKey(header)]
	if got.Remaining == nil || *got.Remaining != 0 || got.Authority != quota.AuthorityResponseHeader {
		t.Fatalf("older quota API observation overwrote newer header evidence: %#v", got)
	}
	equalTimeAPI := olderAPI
	equalTimeAPI.ObservedAt = at
	if !gate.TrySetQuota(equalTimeAPI) {
		t.Fatal("equal-time quota evidence was unexpectedly rejected")
	}
	got = gate.Quotas()[quotaKey(header)]
	if got.Remaining == nil || *got.Remaining != 0 || got.Authority != quota.AuthorityResponseHeader {
		t.Fatalf("lower-authority evidence overwrote equal-time header evidence: %#v", got)
	}
	newerAPI := olderAPI
	newerAPI.ObservedAt = at.Add(time.Second)
	if !gate.TrySetQuota(newerAPI) {
		t.Fatal("newer provider API evidence was unexpectedly rejected")
	}
	got = gate.Quotas()[quotaKey(header)]
	if got.Remaining == nil || *got.Remaining != 12 || got.Authority != quota.AuthorityQuotaAPI {
		t.Fatalf("newer quota API evidence did not refresh the window: %#v", got)
	}
}

func TestStructuredQuotaErrorEvidenceBlocksOnlyUntilProviderReset(t *testing.T) {
	gate := NewPolicyGate()
	route := kernel.Route{ID: "route", NodeID: "provider", CredentialID: "connection", ExternalModel: "model", Enabled: true}
	reset := time.Now().Add(2 * time.Minute).UTC().Truncate(time.Second)
	body := []byte(`{"error":{"type":"insufficient_quota","message":"quota exceeded","reset_at":"` + reset.Format(time.RFC3339) + `"}}`)
	outcome := (provider.HTTPJSONErrorClassifier{}).ClassifyOutcome(429, nil, body)
	gate.ObserveOutcome(route, outcome)
	if gate.Usable(route, time.Now()) {
		t.Fatal("structured exhausted quota should block the route before its reset")
	}
	state := gate.Health.Snapshot()[route.ID]
	if state.LastCause != kernel.CauseQuotaExhausted || !state.CooldownUntil.Equal(reset) {
		t.Fatalf("passive health evidence=%#v", state)
	}
	var recorded quota.Snapshot
	for _, snapshot := range gate.Quotas() {
		if snapshot.ProviderNodeID == route.NodeID && snapshot.ConnectionID == route.CredentialID && snapshot.ModelRef == route.ExternalModel {
			recorded = snapshot
		}
	}
	if recorded.Remaining == nil || *recorded.Remaining != 0 || recorded.ResetAt == nil || !recorded.ResetAt.Equal(reset) {
		t.Fatalf("quota evidence was not committed to routing policy: %#v", recorded)
	}
	if gate.Usable(route, reset.Add(time.Second)) != true {
		t.Fatal("route should become eligible after the provider reset without a background healthcheck")
	}
}
