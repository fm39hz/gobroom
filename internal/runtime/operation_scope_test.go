package runtime

import (
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/quota"
)

func operationTestRoute() kernel.Route {
	return kernel.Route{
		ID: "route", NodeID: "provider", CredentialID: "credential", ExternalModel: "model",
		OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{
			normalize.OperationChatGenerate:    {ContractVersion: 1},
			normalize.Operation("chat.cancel"): {ContractVersion: 1},
		},
		HealthGeneration: kernel.RouteHealthGeneration{Provider: "provider-v1", Connection: "credential-v1", Model: "route-v1"},
	}
}

func operationTestRef(id string, version uint64) extensions.Ref {
	return extensions.Ref{Kind: "operation", ID: id, ContractVersion: version}
}

func operationTestBound(route kernel.Route, ref extensions.Ref) kernel.Route {
	route.SessionOperationRef = ref
	return route
}

func TestOperationHealthFailureAndStatusSuccessRemainIsolated(t *testing.T) {
	route := operationTestRoute()
	generate := operationTestBound(route, operationTestRef(string(normalize.OperationChatGenerate), 1))
	cancel := operationTestBound(route, operationTestRef("chat.cancel", 1))
	gate := NewHealthGate()
	gate.SetGenerations(map[string]kernel.Route{route.ID: route})

	gate.ObserveOutcome(generate, kernel.ClassifiedOutcome{Cause: kernel.CauseProtocol, Scope: kernel.ScopeOperation, Retry: kernel.RetryAfter, RetryAt: time.Now().Add(time.Hour), Message: "generate failed"})
	gate.ObserveOutcome(cancel, kernel.ClassifiedOutcome{Cause: kernel.CauseSuccess, Effect: kernel.EffectAccepted, Scope: kernel.ScopeOperation, Retry: kernel.RetryNow})
	if gate.Usable(generate, time.Now()) {
		t.Fatal("failed generate operation became usable after another operation succeeded")
	}
	if !gate.Usable(cancel, time.Now()) {
		t.Fatal("operation failure leaked into the unrelated cancel operation")
	}
	if got := gate.StateFor(generate); got.LastScope != kernel.ScopeOperation || got.OperationRef != generate.SessionOperationRef {
		t.Fatalf("generate state lost exact operation evidence: %#v", got)
	}
	if got := gate.StateFor(cancel); got.LastScope != kernel.ScopeOperation || got.OperationRef != cancel.SessionOperationRef || got.Successes != 1 {
		t.Fatalf("cancel success was not retained as its own operation evidence: %#v", got)
	}
	view := gate.Snapshot()[route.ID]
	if len(view.Operations) != 2 {
		t.Fatalf("operation snapshot should retain both operation states: %#v", view.Operations)
	}
	states := map[extensions.Ref]RouteHealth{}
	for _, state := range view.Operations {
		states[state.OperationRef] = state
	}
	if states[generate.SessionOperationRef].LastCause != kernel.CauseProtocol || states[cancel.SessionOperationRef].LastCause != kernel.CauseSuccess {
		t.Fatalf("snapshot mixed operation outcomes: %#v", view.Operations)
	}
}

func TestOperationHealthUsesExactRefAndCredentialGeneration(t *testing.T) {
	route := operationTestRoute()
	old := operationTestBound(route, operationTestRef(string(normalize.OperationChatGenerate), 1))
	gate := NewHealthGate()
	gate.SetGenerations(map[string]kernel.Route{route.ID: route})
	gate.ObserveOutcome(old, kernel.ClassifiedOutcome{Cause: kernel.CauseProtocol, Scope: kernel.ScopeOperation, Retry: kernel.RetryAfter, RetryAt: time.Now().Add(time.Hour)})

	credential := route
	credential.HealthGeneration.Connection = "credential-v2"
	credential.OperationBindings = map[normalize.Operation]kernel.RouteOperationBinding{
		normalize.OperationChatGenerate:    {ContractVersion: 2},
		normalize.Operation("chat.cancel"): {ContractVersion: 1},
	}
	gate.SetGenerations(map[string]kernel.Route{credential.ID: credential})
	newVersion := operationTestBound(credential, operationTestRef(string(normalize.OperationChatGenerate), 2))
	if !gate.Usable(newVersion, time.Now()) {
		t.Fatal("stale operation or credential generation blocked the new binding")
	}
	gate.ObserveOutcome(old, kernel.ClassifiedOutcome{Cause: kernel.CauseAuth, Effect: kernel.EffectRejected, Scope: kernel.ScopeOperation, Retry: kernel.RetryNever})
	if !gate.Usable(newVersion, time.Now()) {
		t.Fatal("late stale operation evidence blocked the current credential generation")
	}
	unknownVersion := operationTestBound(credential, operationTestRef(string(normalize.OperationChatGenerate), 3))
	if gate.AcceptsOutcome(unknownVersion, kernel.ScopeOperation) || gate.Usable(unknownVersion, time.Now()) {
		t.Fatal("unknown operation contract version was accepted or considered usable")
	}
}

func TestOperationHalfOpenTrialsAreSingleFlightAndRecoverOnlyTheirOperation(t *testing.T) {
	route := operationTestRoute()
	generate := operationTestBound(route, operationTestRef(string(normalize.OperationChatGenerate), 1))
	cancel := operationTestBound(route, operationTestRef("chat.cancel", 1))
	gate := NewHealthGate()
	gate.SetGenerations(map[string]kernel.Route{route.ID: route})
	gate.Restore(generate, RouteHealth{Failures: 1, CooldownUntil: time.Now().Add(-time.Second), LastScope: kernel.ScopeOperation, OperationRef: generate.SessionOperationRef, Generation: route.HealthGeneration.For(kernel.ScopeOperation)})

	now := time.Now()
	if !gate.Acquire(generate, now) {
		t.Fatal("first post-cooldown operation trial should be admitted")
	}
	if gate.Acquire(generate, now) {
		t.Fatal("same-operation concurrent trial should be rejected")
	}
	if !gate.Acquire(cancel, now) {
		t.Fatal("independent operation should not share another operation's half-open trial")
	}
	gate.ObserveOutcome(generate, kernel.ClassifiedOutcome{Cause: kernel.CauseSuccess, Effect: kernel.EffectAccepted, Scope: kernel.ScopeOperation, Retry: kernel.RetryNow})
	if !gate.Usable(generate, now) || gate.StateFor(generate).Failures != 0 {
		t.Fatal("successful operation trial did not recover its own operation")
	}
	if gate.StateFor(cancel).Successes != 0 {
		t.Fatal("operation success changed an unrelated operation state")
	}
}

func TestOperationTrialSuccessRecoversExpiredBroaderConnectionTrial(t *testing.T) {
	route := operationTestRoute()
	generate := operationTestBound(route, operationTestRef(string(normalize.OperationChatGenerate), 1))
	gate := NewHealthGate()
	gate.SetGenerations(map[string]kernel.Route{route.ID: route})
	gate.Restore(route, RouteHealth{Failures: 1, CooldownUntil: time.Now().Add(-time.Second), LastScope: kernel.ScopeConnection, Generation: route.HealthGeneration.For(kernel.ScopeConnection)})

	now := time.Now()
	if !gate.Acquire(generate, now) {
		t.Fatal("real request should acquire the expired connection trial")
	}
	gate.ObserveOutcome(generate, kernel.ClassifiedOutcome{Cause: kernel.CauseSuccess, Effect: kernel.EffectAccepted, Scope: kernel.ScopeOperation, Retry: kernel.RetryNow})
	state, ok := gate.ScopedState(route, kernel.ScopeConnection)
	if !ok || state.Failures != 0 || !state.CooldownUntil.IsZero() || state.LastCause != kernel.CauseSuccess {
		t.Fatalf("operation success did not recover the acquired expired connection trial: %#v, exists=%t", state, ok)
	}
}

func TestConnectionAuthFailureBlocksEveryOperationOnCredential(t *testing.T) {
	route := operationTestRoute()
	first := operationTestBound(route, operationTestRef(string(normalize.OperationChatGenerate), 1))
	second := operationTestBound(route, operationTestRef("chat.cancel", 1))
	gate := NewHealthGate()
	gate.SetGenerations(map[string]kernel.Route{route.ID: route})
	gate.ObserveOutcome(first, kernel.ClassifiedOutcome{Cause: kernel.CauseAuth, Effect: kernel.EffectRejected, Scope: kernel.ScopeConnection, Retry: kernel.RetryNever})
	if gate.Usable(first, time.Now()) || gate.Usable(second, time.Now()) {
		t.Fatal("connection-scoped auth rejection did not block every operation on its credential")
	}
}

func TestOperationQuotaIdentityAndExhaustionAreOperationScoped(t *testing.T) {
	gate := NewPolicyGate()
	route := operationTestRoute()
	generate := operationTestBound(route, operationTestRef(string(normalize.OperationChatGenerate), 1))
	cancel := operationTestBound(route, operationTestRef("chat.cancel", 1))
	remaining := float64(0)
	reset := time.Now().Add(time.Hour)
	quotaSnapshot := quota.Snapshot{ProviderNodeID: route.NodeID, ConnectionID: route.CredentialID, ModelRef: route.ExternalModel, WindowName: "daily", OperationRef: generate.SessionOperationRef, Remaining: &remaining, ResetAt: &reset}
	if quotaSnapshot.Identity().Operation != generate.SessionOperationRef {
		t.Fatalf("typed quota identity omitted exact operation: %#v", quotaSnapshot.Identity())
	}
	gate.SetQuota(quotaSnapshot)
	if gate.Usable(generate, time.Now()) {
		t.Fatal("exhausted generate quota did not block generate")
	}
	if !gate.Usable(cancel, time.Now()) {
		t.Fatal("operation scoped quota leaked into cancel")
	}
	broad := quotaSnapshot
	broad.OperationRef = extensions.Ref{}
	gate.SetQuota(broad)
	if gate.Usable(generate, time.Now()) || gate.Usable(cancel, time.Now()) {
		t.Fatal("operation independent exhausted quota did not gate all operations")
	}
}
