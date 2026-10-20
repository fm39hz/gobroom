package kernel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

type refreshContextAdapter struct {
	*issuerReplayAdapter
	replayed []Credential
}

func (*refreshContextAdapter) ClassifyOutcome(status int, _ http.Header, _ []byte) ClassifiedOutcome {
	return ClassifiedOutcome{StatusCode: status, Cause: CauseAuth, Recovery: CredentialRecoveryRefresh, Effect: EffectRejected, Scope: ScopeConnection, Retry: RetryNever}
}

func (a *refreshContextAdapter) Prepare(ctx context.Context, request NormalizedRequest, route Route, credential Credential) (UpstreamRequest, error) {
	upstream, err := a.issuerReplayAdapter.Prepare(ctx, request, route, credential)
	if err != nil || upstream.Replay == nil {
		return upstream, err
	}
	replay := upstream.Replay
	upstream.Replay = func(ctx context.Context, refreshed Credential) (UpstreamRequest, error) {
		a.replayed = append(a.replayed, refreshed)
		return replay(ctx, refreshed)
	}
	return upstream, nil
}

func TestUnauthorizedRefreshRetriesWhenRuntimeContextChanges(t *testing.T) {
	first := &issuerReplayAdapter{id: "issuer-a-adapter", failStatus: http.StatusUnauthorized, failures: 1}
	second := &issuerReplayAdapter{id: "issuer-b-adapter"}
	k := newReplayKeyKernel(t, first, second)
	observed := &refreshContextAdapter{issuerReplayAdapter: first}
	k.Adapters[first.id] = observed
	refreshCalls := 0
	setTestCredentialRefresher(k, func(_ context.Context, _ Route, selected Credential) (Credential, error) {
		refreshCalls++
		if selected.Secret != "same-access-token" {
			t.Fatalf("selected credential secret=%q", selected.Secret)
		}
		return Credential{
			ConnectionID: selected.ConnectionID,
			Type:         selected.Type,
			Secret:       selected.Secret,
			Generation:   "generation-2",
			RequestHeaders: http.Header{
				"X-Provider-Context": []string{"refreshed"},
			},
			AuthContext: AuthContext{"account": "refreshed"},
		}, nil
	})

	err := k.Execute(context.Background(), replayKeyRequest("refresh-context-key"), Credential{
		ConnectionID: "conn-a",
		Type:         "oauth2",
		Secret:       "same-access-token",
		Generation:   "generation-1",
	}, httptest.NewRecorder())
	if err != nil {
		t.Fatal(err)
	}
	if refreshCalls != 1 || first.calls != 2 || second.calls != 0 {
		t.Fatalf("refresh calls=%d issuer A=%d issuer B=%d", refreshCalls, first.calls, second.calls)
	}
	if len(observed.replayed) != 1 {
		t.Fatalf("replayed credentials=%#v", observed.replayed)
	}
	got := observed.replayed[0]
	if got.Secret != "same-access-token" || got.Generation != "generation-2" || got.RequestHeaders.Get("X-Provider-Context") != "refreshed" || got.AuthContext["account"] != "refreshed" {
		t.Fatalf("retry did not receive refreshed runtime context: %#v", got)
	}
}
