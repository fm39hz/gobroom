package kernel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fm39hz/gobroom/internal/operations"
)

type recoveryOutcomeAdapter struct {
	*issuerReplayAdapter
	outcome ClassifiedOutcome
}

func TestCredentialRecoveryDoesNotInferHintsFromHTTPStatus(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		first := &issuerReplayAdapter{id: "issuer-a-adapter", failStatus: status, failures: 1}
		second := &issuerReplayAdapter{id: "issuer-b-adapter"}
		k := newReplayKeyKernelWithRecovery(t, first, second, CredentialRecoveryRefresh)
		calls := 0
		k.RefreshCredential = func(context.Context, Route, Credential) (Credential, error) { calls++; return Credential{}, nil }
		_ = k.Execute(context.Background(), replayKeyRequest("no-hint"), Credential{Secret: "expired"}, httptest.NewRecorder())
		if calls != 0 {
			t.Fatalf("HTTP status %d manufactured a credential recovery hint", status)
		}
	}
}

func TestCredentialRecoveryAllowsAmbiguousEffectOnlyWithScopedReplayProof(t *testing.T) {
	first := &issuerReplayAdapter{id: "issuer-a-adapter", failStatus: 498, failures: 1}
	second := &issuerReplayAdapter{id: "issuer-b-adapter"}
	k := newReplayKernelWithRecoverySafety(t, first, second, CredentialRecoveryRefresh, operations.ReplayWithKey)
	k.Adapters[first.id] = &recoveryOutcomeAdapter{issuerReplayAdapter: first, outcome: ClassifiedOutcome{Cause: CauseAuth, Recovery: CredentialRecoveryRefresh, Effect: EffectUnknown, Scope: ScopeConnection, Retry: RetryNever}}
	calls := 0
	k.RefreshCredential = func(_ context.Context, _ Route, selected Credential) (Credential, error) {
		calls++
		selected.Secret = "replacement"
		return selected, nil
	}
	if err := k.Execute(context.Background(), replayKeyRequest("scoped-proof"), Credential{ConnectionID: "conn-a", Secret: "expired"}, httptest.NewRecorder()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || first.calls != 2 || second.calls != 0 {
		t.Fatalf("scoped recovery calls=%d first=%d other=%d", calls, first.calls, second.calls)
	}
}

func (a *recoveryOutcomeAdapter) ClassifyOutcome(int, http.Header, []byte) ClassifiedOutcome {
	return a.outcome
}

func TestCredentialRecoveryRequiresAuthClassificationAndRoutePolicy(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		cause    OutcomeCause
		scope    OutcomeScope
		recovery CredentialRecovery
		want     int
	}{
		{name: "permission denial", status: http.StatusForbidden, cause: CausePermission, recovery: CredentialRecoveryRefresh},
		{name: "quota denial", status: http.StatusForbidden, cause: CauseQuotaExhausted, recovery: CredentialRecoveryRefresh},
		{name: "request-scoped auth error", status: http.StatusUnauthorized, cause: CauseAuth, scope: ScopeRequest, recovery: CredentialRecoveryRefresh},
		{name: "explicit refresh signal on custom status", status: 498, cause: CauseAuth, recovery: CredentialRecoveryRefresh, want: 1},
		{name: "mode none vetoes refresh hint", status: http.StatusUnauthorized, cause: CauseAuth, recovery: CredentialRecoveryNone},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			first := &issuerReplayAdapter{id: "issuer-a-adapter", failStatus: test.status, failures: 1}
			second := &issuerReplayAdapter{id: "issuer-b-adapter"}
			k := newReplayKeyKernelWithRecovery(t, first, second, test.recovery)
			scope := test.scope
			if scope == "" {
				scope = ScopeConnection
			}
			classified := &recoveryOutcomeAdapter{issuerReplayAdapter: first, outcome: ClassifiedOutcome{
				Cause: test.cause, Recovery: CredentialRecoveryRefresh, Effect: EffectRejected,
				Scope: scope, Retry: RetryNever, Confidence: 1,
			}}
			k.Adapters[first.id] = classified
			refreshCalls := 0
			k.RefreshCredential = func(_ context.Context, _ Route, selected Credential) (Credential, error) {
				refreshCalls++
				return Credential{ConnectionID: selected.ConnectionID, Type: selected.Type, Secret: "refreshed"}, nil
			}
			_ = k.Execute(context.Background(), replayKeyRequest("credential-recovery-"+test.name), Credential{ConnectionID: "conn-a", Type: "oauth2", Secret: "expired"}, httptest.NewRecorder())
			if refreshCalls != test.want {
				t.Fatalf("refresh calls=%d, want %d", refreshCalls, test.want)
			}
			if test.want == 1 && first.calls != 2 {
				t.Fatalf("explicit custom-status recovery dispatched %d times, want 2", first.calls)
			}
		})
	}
}

func TestCredentialRecoveryUnknownHintAndUnknownRoutePolicy(t *testing.T) {
	t.Run("unknown hint fails closed", func(t *testing.T) {
		first := &issuerReplayAdapter{id: "issuer-a-adapter", failStatus: http.StatusUnauthorized, failures: 1}
		second := &issuerReplayAdapter{id: "issuer-b-adapter"}
		k := newReplayKeyKernelWithRecovery(t, first, second, CredentialRecoveryRefresh)
		k.Adapters[first.id] = &recoveryOutcomeAdapter{issuerReplayAdapter: first, outcome: ClassifiedOutcome{
			Cause: CauseAuth, Recovery: CredentialRecovery("future-mode"), Effect: EffectRejected,
			Scope: ScopeConnection, Retry: RetryNever, Confidence: 1,
		}}
		refreshCalls := 0
		k.RefreshCredential = func(_ context.Context, _ Route, credential Credential) (Credential, error) {
			refreshCalls++
			return credential, nil
		}
		_ = k.Execute(context.Background(), replayKeyRequest("unknown-recovery-hint"), Credential{ConnectionID: "conn-a", Type: "oauth2", Secret: "expired"}, httptest.NewRecorder())
		if refreshCalls != 0 {
			t.Fatalf("unknown recovery hint triggered %d refreshes", refreshCalls)
		}
	})

	t.Run("unknown route policy rejected at snapshot", func(t *testing.T) {
		first := &issuerReplayAdapter{id: "issuer-a-adapter"}
		second := &issuerReplayAdapter{id: "issuer-b-adapter"}
		k := newReplayKeyKernelWithRecovery(t, first, second, CredentialRecoveryRefresh)
		snapshot := k.Snapshots.Load()
		route := snapshot.Routes["route-a"]
		route.CredentialRecovery = CredentialRecovery("future-mode")
		snapshot.Routes[route.ID] = route
		if _, err := NewSnapshotStore(snapshot); err == nil {
			t.Fatal("snapshot accepted an unknown route credential recovery policy")
		}
	})
}

func TestCredentialRecoveryStillRequiresReplaySafeEffect(t *testing.T) {
	for _, test := range []struct {
		name   string
		effect UpstreamEffect
		safety operations.ReplaySafety
	}{
		{name: "accepted effect", effect: EffectAccepted, safety: operations.ReplayWithKey},
		{name: "ambiguous effect without client proof", effect: EffectUnknown, safety: operations.ReplayWithKey},
		{name: "replay never", effect: EffectRejected, safety: operations.ReplayNever},
	} {
		t.Run(test.name, func(t *testing.T) {
			first := &issuerReplayAdapter{id: "issuer-a-adapter", failStatus: 498, failures: 1}
			second := &issuerReplayAdapter{id: "issuer-b-adapter"}
			k := newReplayKernelWithRecoverySafety(t, first, second, CredentialRecoveryRefresh, test.safety)
			k.Adapters[first.id] = &recoveryOutcomeAdapter{issuerReplayAdapter: first, outcome: ClassifiedOutcome{
				Cause: CauseAuth, Recovery: CredentialRecoveryRefresh, Effect: test.effect,
				Scope: ScopeConnection, Retry: RetryNever, Confidence: 1,
			}}
			refreshCalls := 0
			k.RefreshCredential = func(_ context.Context, _ Route, selected Credential) (Credential, error) {
				refreshCalls++
				return Credential{ConnectionID: selected.ConnectionID, Type: selected.Type, Secret: "refreshed"}, nil
			}
			request := replayKeyRequest("effect-" + test.name)
			if test.effect == EffectUnknown {
				request = replayKeyRequest("")
			}
			_ = k.Execute(context.Background(), request, Credential{ConnectionID: "conn-a", Type: "oauth2", Secret: "expired"}, httptest.NewRecorder())
			if refreshCalls != 0 {
				t.Fatalf("unsafe effect triggered %d refreshes", refreshCalls)
			}
		})
	}
}
