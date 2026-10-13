package runtime

import (
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
