package daemon

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/store"
)

type activationAuthFixture struct {
	callbackOAuthFixture
	issued atomic.Int32
}

func (f *activationAuthFixture) ExchangeAuthorizationCode(context.Context, string, string) (provider.CredentialState, error) {
	f.issued.Add(1)
	return provider.NewCredentialState("replacement-state-canary")
}
func (f *activationAuthFixture) BeginDeviceAuthorization(context.Context) (provider.DeviceAuthorization, error) {
	return provider.DeviceAuthorization{DeviceCode: "private-code", UserCode: "USER", VerificationURL: "https://identity.test/device", ExpiresAt: time.Now().Add(time.Minute)}, nil
}
func (f *activationAuthFixture) WaitForDeviceAuthorization(context.Context, provider.DeviceAuthorization) (provider.CredentialState, error) {
	return f.ExchangeAuthorizationCode(context.Background(), "", "")
}

func TestAuthorizationCommitReportsActivationFailureWithoutRepeatingExchange(t *testing.T) {
	for _, kind := range []string{"code", "device"} {
		for _, failure := range []string{"routing_reset", "version_persist", "serve_before", "serve_after"} {
			t.Run(kind+"/"+failure, func(t *testing.T) {
				flow := &activationAuthFixture{callbackOAuthFixture: callbackOAuthFixture{redirectURL: "https://client.test/callback"}}
				d, id := cancellationAuthDaemon(t, flow)
				initialVersion := d.server.Control().Version()
				switch failure {
				case "routing_reset":
					if err := d.store.SaveConnectionRuntime(store.ConnectionRuntime{ConnectionID: id, Status: "invalid"}); err != nil {
						t.Fatal(err)
					}
					if _, err := d.store.DB.Exec(`CREATE TRIGGER fail_activation BEFORE UPDATE ON connection_runtime BEGIN SELECT RAISE(ABORT,'fixture reset failure'); END`); err != nil {
						t.Fatal(err)
					}
				case "version_persist":
					if _, err := d.store.DB.Exec(`CREATE TRIGGER fail_activation BEFORE INSERT ON settings WHEN NEW.key='snapshot_version' BEGIN SELECT RAISE(ABORT,'fixture version failure'); END`); err != nil {
						t.Fatal(err)
					}
				case "serve_before", "serve_after":
					d.server.SetReloadHook(func(next kernel.Snapshot) (kernel.Snapshot, error) {
						version := initialVersion
						if failure == "serve_after" {
							version = next.Version
						}
						return kernel.Snapshot{Version: version}, errors.New("fixture publish failure")
					})
				}
				var sessionID string
				if kind == "code" {
					challenge, err := d.startAuthorization(id)
					if err != nil {
						t.Fatal(err)
					}
					sessionID = challenge.SessionID
					result, err := d.completeAuthorization(context.Background(), sessionID, sessionID, "approved", "")
					if err != nil || !result.Activation.CredentialCommitted {
						t.Fatalf("postcommit authorization=%+v err=%v", result, err)
					}
				} else {
					challenge, err := d.startDeviceAuthorization(context.Background(), id)
					if err != nil {
						t.Fatal(err)
					}
					sessionID = challenge.SessionID
					drained := make(chan struct{})
					go func() { d.authWG.Wait(); close(drained) }()
					awaitAuthorizationSignal(t, drained)
				}
				get := func() *CredentialActivationView {
					t.Helper()
					if kind == "code" {
						v, err := d.authorizationStatus(sessionID)
						if err != nil || v.Status != "completed" {
							t.Fatalf("code status=%+v %v", v, err)
						}
						return v.Activation
					}
					v, err := d.deviceAuthorizationStatus(sessionID)
					if err != nil || v.Status != "completed" {
						t.Fatalf("device status=%+v %v", v, err)
					}
					return v.Activation
				}
				view := get()
				if view == nil || !view.CredentialCommitted || view.Status != store.ActivationPending || view.ErrorCode == "" {
					t.Fatalf("activation=%+v", view)
				}
				credential, ok := d.store.ConnectionCredentialByID(id)
				if !ok || credential.Secret != "replacement-state-canary" {
					t.Fatal("durable credential was lost after activation failure")
				}
				if failure == "version_persist" && (!view.Progress.ControlPublished || view.Progress.VersionPersisted || !view.Progress.ServingPublished) {
					t.Fatalf("postpublication receipt=%+v", view.Progress)
				}
				if failure == "serve_before" && view.Progress.ServingPublished {
					t.Fatal("failed hook falsely reports serving publication")
				}
				if failure == "serve_after" && !view.Progress.ServingPublished {
					t.Fatal("postpublication hook error hid actual serving publication")
				}
				if failure == "routing_reset" && view.Progress.RoutingReset {
					t.Fatal("failed reset was reported as applied")
				}
				if failure == "version_persist" || failure == "routing_reset" {
					if _, err := d.store.DB.Exec(`DROP TRIGGER fail_activation`); err != nil {
						t.Fatal(err)
					}
				} else {
					d.server.SetReloadHook(nil)
				}
				active, err := d.retryCredentialActivation(context.Background(), id)
				if err != nil || active.Status != store.ActivationActive {
					t.Fatalf("activation retry=%+v %v", active, err)
				}
				if current := get(); current == nil || current.Status != store.ActivationActive {
					t.Fatalf("retained session did not observe reconciliation: %+v", current)
				}
				if flow.issued.Load() != 1 {
					t.Fatalf("activation repeated token exchange %d times", flow.issued.Load())
				}
			})
		}
	}
}

func TestCredentialActivationExhaustsAndCannotClearNewCredentialState(t *testing.T) {
	d, id := cancellationAuthDaemon(t, callbackOAuthFixture{})
	expected, _ := d.store.ConnectionCredentialByID(id)
	key, err := d.store.CompareAndSwapConnectionCredential(context.Background(), expected, "committed-state", store.PrincipalReplacement)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.store.DB.Exec(`CREATE TRIGGER fail_activation BEFORE INSERT ON settings WHEN NEW.key='snapshot_version' BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	unlock, err := d.lockCredentialRefresh(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var view CredentialActivationView
	for range credentialActivationAttempts {
		view, err = d.activateCredentialLocked(context.Background(), key)
		if err != nil {
			t.Fatal(err)
		}
	}
	unlock()
	if view.Status != store.ActivationExhausted || view.Attempts != credentialActivationAttempts {
		t.Fatalf("unbounded activation retries: %+v", view)
	}
	if _, pending, err := d.store.NextCredentialActivation(context.Background()); err != nil || pending {
		t.Fatalf("exhausted activation still scheduled: %v %v", pending, err)
	}
	if _, err := d.store.UpdateConnection(store.UpdateConnectionInput{ID: id, Secret: "newer-state"}); err != nil {
		t.Fatal(err)
	}
	if err := d.store.SaveConnectionRuntime(store.ConnectionRuntime{ConnectionID: id, Status: "invalid"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.retryCredentialActivation(context.Background(), id); err == nil {
		t.Fatal("superseded activation was retried")
	}
	runtime, ok, err := d.store.ConnectionRuntime(id)
	if err != nil || !ok || runtime.Status != "invalid" {
		t.Fatalf("old activation cleared newer routing state: %+v %v", runtime, err)
	}
}

func TestCredentialActivationWorkerDrainsOnShutdown(t *testing.T) {
	d, id := cancellationAuthDaemon(t, callbackOAuthFixture{})
	expected, _ := d.store.ConnectionCredentialByID(id)
	key, err := d.store.CompareAndSwapConnectionCredential(context.Background(), expected, "worker-state", store.PrincipalReplacement)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.runCtx = ctx
	d.activationWake, d.activationDone = make(chan struct{}, 1), make(chan struct{})
	entered := make(chan struct{})
	version := d.server.Control().Version()
	d.server.SetReloadHook(func(kernel.Snapshot) (kernel.Snapshot, error) {
		close(entered)
		<-ctx.Done()
		return kernel.Snapshot{Version: version}, ctx.Err()
	})
	go d.runCredentialActivations(ctx)
	awaitAuthorizationSignal(t, entered)
	cancel()
	awaitAuthorizationSignal(t, d.activationDone)
	a, ok, err := d.store.CredentialActivation(context.Background(), id)
	if err != nil || !ok || a.Commit != key || a.Status != store.ActivationPending {
		t.Fatalf("interrupted activation lost durable work: %+v %v", a, err)
	}
}
