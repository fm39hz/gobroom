package daemon

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/fm39hz/gobroom/internal/provider"
)

func TestGenericAuthorizationAwaitOutlivesStepTimeoutWithoutFrontend(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		type rootContextKey struct{}
		flow := &genericSetupFlow{begin: genericSetupTransition(t, "private-await")}
		flow.begin.Await = true
		flow.begin.ExpiresAt = time.Now().Add(2 * time.Minute)
		flow.advance = func(ctx context.Context, _ provider.AuthorizationState, event provider.AuthorizationEvent) (provider.AuthorizationTransition, error) {
			if ctx.Value(rootContextKey{}) != "daemon-value" {
				t.Error("await lost daemon-owned context values")
			}
			if event.Kind != provider.AuthorizationAwait {
				t.Error("wait driver received another event")
			}
			deadline, ok := ctx.Deadline()
			if !ok || deadline.Sub(time.Now()) <= authorizationStepTimeout {
				t.Error("await inherited the short RPC step timeout")
			}
			time.Sleep(40 * time.Second)
			if err := ctx.Err(); err != nil {
				return provider.AuthorizationTransition{}, err
			}
			credential, err := provider.NewCredentialState("awaited-credential")
			return provider.AuthorizationTransition{Credential: &credential}, err
		}
		d, id := genericSetupDaemon(t, flow)
		d.runCtx = context.WithValue(context.Background(), rootContextKey{}, "daemon-value")
		view, err := d.startSetupAuthorization(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		// No get or frontend poll causes the upstream await to run.
		time.Sleep(45 * time.Second)
		synctest.Wait()
		view, err = d.setupAuthorizationStatus(view.SessionID)
		if err != nil || view.Status != "completed" || view.Activation == nil || !view.Activation.CredentialCommitted {
			t.Fatalf("background await did not commit: %+v %v", view, err)
		}
		current, _ := d.store.ConnectionCredentialByID(id)
		if current.Secret != "awaited-credential" || flow.advanceCalls.Load() != 1 {
			t.Fatalf("await was repeated or lost: calls=%d", flow.advanceCalls.Load())
		}
	})
}

func TestGenericAuthorizationAwaitCancellationFencesLateResult(t *testing.T) {
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	flow := &genericSetupFlow{begin: genericSetupTransition(t, "private-await")}
	flow.begin.Await = true
	flow.advance = func(ctx context.Context, _ provider.AuthorizationState, _ provider.AuthorizationEvent) (provider.AuthorizationTransition, error) {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		<-release
		credential, err := provider.NewCredentialState("late-awaited-credential")
		return provider.AuthorizationTransition{Credential: &credential}, err
	}
	d, id := genericSetupDaemon(t, flow)
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	view, err := d.startSetupAuthorization(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	awaitAuthorizationSignal(t, entered)
	if !d.finishSetupAuthorization(view.SessionID, "cancelled", "authorization was cancelled") {
		t.Fatal("could not cancel await")
	}
	awaitAuthorizationSignal(t, cancelled)
	once.Do(func() { close(release) })
	d.authWG.Wait()
	current, _ := d.store.ConnectionCredentialByID(id)
	if current.Secret != "original-secret" {
		t.Fatal("late awaited credential crossed cancellation")
	}
	view, err = d.setupAuthorizationStatus(view.SessionID)
	if err != nil || view.Status != "cancelled" {
		t.Fatalf("cancellation status lost: %+v %v", view, err)
	}
}

func TestGenericAuthorizationRemovedDeviceRPCsAreNotAliased(t *testing.T) {
	d := &Daemon{}
	for _, method := range []string{"auth.device.start", "auth.device.get", "auth.device.cancel", "auth.authorization.start", "auth.authorization.get", "auth.authorization.complete", "auth.authorization.cancel"} {
		response := d.handleIPC(context.Background(), IPCRequest{Method: method})
		if response.OK || response.Error != "unknown method: "+method {
			t.Fatalf("removed device RPC still resolves: %+v", response)
		}
	}
}
