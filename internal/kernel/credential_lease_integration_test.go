package kernel

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/fm39hz/gobroom/internal/normalize"
)

// setTestCredentialRefresher issues leases from test material factories.
func setTestCredentialRefresher(k *Kernel, refresh func(context.Context, Route, Credential) (Credential, error)) {
	k.RefreshCredentialLease = func(ctx context.Context, route Route, old *CredentialLease) (*CredentialLease, error) {
		scope := CredentialLeaseScopeForRoute(route)
		material, err := old.MaterialForRefresh(ctx, scope)
		if err != nil {
			return nil, err
		}
		material, err = refresh(ctx, route, material)
		if err != nil {
			return nil, err
		}
		return NewCredentialLease(scope, material, time.Now().Add(time.Minute), nil, nil)
	}
}

func leaseForRoute(t *testing.T, route Route, credential Credential, check func(context.Context) error, release func()) *CredentialLease {
	t.Helper()
	lease, err := NewCredentialLease(CredentialLeaseScopeForRoute(route), credential, time.Now().Add(time.Minute), check, release)
	if err != nil {
		t.Fatal(err)
	}
	return lease
}

type leaseFenceGate struct{ failures atomic.Int32 }

func (*leaseFenceGate) Usable(Route, time.Time) bool                               { return true }
func (g *leaseFenceGate) MarkFailure(Route, ErrorClass, error)                     { g.failures.Add(1) }
func (g *leaseFenceGate) MarkFailureAfter(Route, ErrorClass, error, time.Duration) { g.failures.Add(1) }
func (*leaseFenceGate) MarkSuccess(Route)                                          {}

func TestCredentialLeaseFenceAfterPrepareStopsDispatchAndFallback(t *testing.T) {
	first := &issuerReplayAdapter{id: "issuer-a-adapter"}
	second := &issuerReplayAdapter{id: "issuer-b-adapter"}
	k := newReplayKeyKernel(t, first, second)
	gate := &leaseFenceGate{}
	k.Scheduler.gate = gate
	var revoked atomic.Bool
	k.AcquireCredential = func(_ context.Context, route Route) (*CredentialLease, error) {
		return leaseForRoute(t, route, Credential{ConnectionID: route.CredentialID, Type: route.CredentialType, Secret: "lease"}, func(context.Context) error {
			if revoked.Load() {
				return errors.New("lease revoked")
			}
			return nil
		}, nil), nil
	}
	first.prepareHook = func(context.Context, int) { revoked.Store(true) }

	err := k.Execute(context.Background(), replayKeyRequest("lease-fence"), Credential{Secret: "caller material"}, httptest.NewRecorder())
	if err == nil || err.Error() != "lease revoked" {
		t.Fatalf("Execute error=%v, want local lease revocation", err)
	}
	if first.calls != 0 || second.calls != 0 {
		t.Fatalf("revoked lease dispatched or fell back: first=%d second=%d", first.calls, second.calls)
	}
	if got := gate.failures.Load(); got != 0 {
		t.Fatalf("local lease rejection recorded %d upstream health failures", got)
	}
}

func TestCredentialLeaseReleasedAfterSuccessfulResponseAndTransportError(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "transport error"}[fail], func(t *testing.T) {
			adapter := &issuerReplayAdapter{id: "issuer-a-adapter"}
			if fail {
				adapter.executeError = errors.New("upstream connection failed")
				adapter.failures = 1
				adapter.noReplay = true
			}
			k := newReplayKeyKernel(t, adapter, &issuerReplayAdapter{id: "issuer-b-adapter"})
			var released atomic.Int32
			k.AcquireCredential = func(_ context.Context, route Route) (*CredentialLease, error) {
				return leaseForRoute(t, route, Credential{ConnectionID: route.CredentialID, Type: route.CredentialType, Secret: "lease"}, nil, func() { released.Add(1) }), nil
			}
			_ = k.Execute(context.Background(), replayKeyRequest("lease-release"), Credential{}, httptest.NewRecorder())
			if got := released.Load(); got != 1 {
				t.Fatalf("lease released %d times, want once", got)
			}
		})
	}
}

func TestCredentialLeaseRefreshReleasesReplacedLease(t *testing.T) {
	first := &issuerReplayAdapter{id: "issuer-a-adapter", failStatus: 498, failures: 1}
	second := &issuerReplayAdapter{id: "issuer-b-adapter"}
	k := newReplayKeyKernel(t, first, second)
	k.Adapters[first.id] = &recoveryOutcomeAdapter{issuerReplayAdapter: first, outcome: ClassifiedOutcome{
		Cause: CauseAuth, Recovery: CredentialRecoveryRefresh, Effect: EffectRejected,
		Scope: ScopeConnection, Retry: RetryNever, Confidence: 1,
	}}
	var originalReleased, replacementReleased atomic.Int32
	k.AcquireCredential = func(_ context.Context, route Route) (*CredentialLease, error) {
		return leaseForRoute(t, route, Credential{ConnectionID: route.CredentialID, Type: route.CredentialType, Secret: "old"}, nil, func() { originalReleased.Add(1) }), nil
	}
	k.RefreshCredentialLease = func(_ context.Context, route Route, old *CredentialLease) (*CredentialLease, error) {
		if _, err := old.MaterialForRefresh(context.Background(), CredentialLeaseScopeForRoute(route)); err != nil {
			return nil, err
		}
		return leaseForRoute(t, route, Credential{ConnectionID: route.CredentialID, Type: route.CredentialType, Secret: "new", Generation: "credential-v2"}, nil, func() { replacementReleased.Add(1) }), nil
	}

	if err := k.Execute(context.Background(), replayKeyRequest("lease-refresh"), Credential{}, httptest.NewRecorder()); err != nil {
		t.Fatal(err)
	}
	if first.calls != 2 || originalReleased.Load() != 1 || replacementReleased.Load() != 1 {
		t.Fatalf("refresh lifecycle: dispatches=%d original releases=%d replacement releases=%d", first.calls, originalReleased.Load(), replacementReleased.Load())
	}
}

type leaseDelayedRenderer struct{ *issuerReplayAdapter }

func (a *leaseDelayedRenderer) RenderResponse(ctx context.Context, response UpstreamResponse, writer http.ResponseWriter, format normalize.Format, hooks StreamHooks) error {
	time.Sleep(2 * time.Second)
	return a.issuerReplayAdapter.RenderResponse(ctx, response, writer, format, hooks)
}

func TestCredentialLeaseExpiryDoesNotCancelAcceptedResponse(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		adapter := &issuerReplayAdapter{id: "issuer-a-adapter"}
		k := newReplayKeyKernel(t, adapter, &issuerReplayAdapter{id: "issuer-b-adapter"})
		k.Adapters[adapter.id] = &leaseDelayedRenderer{adapter}
		var released atomic.Int32
		k.AcquireCredential = func(_ context.Context, route Route) (*CredentialLease, error) {
			lease, err := NewCredentialLease(CredentialLeaseScopeForRoute(route), Credential{ConnectionID: route.CredentialID, Type: route.CredentialType, Secret: "short lease"}, time.Now().Add(time.Second), nil, func() { released.Add(1) })
			return lease, err
		}

		if err := k.Execute(context.Background(), replayKeyRequest("lease-expiry-after-dispatch"), Credential{}, httptest.NewRecorder()); err != nil {
			t.Fatalf("accepted response was canceled by lease expiry: %v", err)
		}
		if adapter.calls != 1 || released.Load() != 1 {
			t.Fatalf("dispatches=%d lease releases=%d, want one each", adapter.calls, released.Load())
		}
	})
}
