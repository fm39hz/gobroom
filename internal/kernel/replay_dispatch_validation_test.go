package kernel

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"
)

func TestReplayWithKeyCancellationDuringFinalDispatchValidation(t *testing.T) {
	for _, cancelAt := range []int{1, 2} {
		t.Run(string(rune('0'+cancelAt)), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			first := &issuerReplayAdapter{id: "issuer-a-adapter", failStatus: http.StatusServiceUnavailable, failures: 1}
			second := &issuerReplayAdapter{id: "issuer-b-adapter"}
			k := newReplayKeyKernel(t, first, second)
			checks, releases := 0, 0
			k.AcquireCredential = func(_ context.Context, route Route) (*CredentialLease, error) {
				return NewCredentialLease(CredentialLeaseScopeForRoute(route), Credential{ConnectionID: route.CredentialID}, time.Now().Add(time.Hour), func(context.Context) error {
					checks++
					if checks == cancelAt {
						cancel()
					}
					return nil // Deliberately returns success after cancellation.
				}, func() { releases++ })
			}
			err := k.Execute(ctx, replayKeyRequest("cancel-final-fence"), Credential{}, httptest.NewRecorder())
			if !errors.Is(err, context.Canceled) || first.calls != cancelAt-1 || second.calls != 0 || releases != 1 {
				t.Fatalf("err=%v A=%d B=%d checks=%d releases=%d", err, first.calls, second.calls, checks, releases)
			}
		})
	}
}

func TestReplayWithKeyRetentionExpiresDuringFinalDispatchValidation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		first := &issuerReplayAdapter{id: "issuer-a-adapter", failStatus: http.StatusServiceUnavailable, failures: 1}
		second := &issuerReplayAdapter{id: "issuer-b-adapter"}
		k := newReplayKeyKernel(t, first, second)
		checks, releases := 0, 0
		k.AcquireCredential = func(_ context.Context, route Route) (*CredentialLease, error) {
			return NewCredentialLease(CredentialLeaseScopeForRoute(route), Credential{ConnectionID: route.CredentialID}, time.Now().Add(time.Hour), func(context.Context) error {
				checks++
				if checks == 2 {
					time.Sleep(time.Minute)
				}
				return nil
			}, func() { releases++ })
		}
		err := k.Execute(context.Background(), replayKeyRequest("expiry-final-fence"), Credential{}, httptest.NewRecorder())
		var suppressed *ReplaySuppressedError
		if !errors.As(err, &suppressed) || first.calls != 1 || second.calls != 0 || checks != 2 || releases != 1 {
			t.Fatalf("expired replay crossed final validation: err=%v A=%d B=%d checks=%d releases=%d", err, first.calls, second.calls, checks, releases)
		}
	})
}
