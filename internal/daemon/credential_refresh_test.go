package daemon

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/store"
)

type delayedCredentialFlow struct {
	callbackOAuthFixture
	resolveEntered  chan struct{}
	resolveRelease  chan struct{}
	refreshEntered  chan struct{}
	refreshRelease  chan struct{}
	observedContext chan context.Context
}

func releaseCredentialCall(channel chan struct{}) func() {
	var once sync.Once
	return func() { once.Do(func() { close(channel) }) }
}

func (f *delayedCredentialFlow) Resolve(ctx context.Context, input provider.AuthInput) (kernel.Credential, error) {
	if f.resolveEntered != nil {
		if f.observedContext != nil {
			f.observedContext <- ctx
		}
		close(f.resolveEntered)
		<-f.resolveRelease // Deliberately return success even if ctx was cancelled.
	}
	return kernel.Credential{ConnectionID: input.ConnectionID, Type: input.Type, Secret: input.Secret}, nil
}

func (f *delayedCredentialFlow) Refresh(ctx context.Context, _ provider.AuthInput, _ kernel.Credential) (provider.CredentialState, error) {
	if f.refreshEntered != nil {
		if f.observedContext != nil {
			f.observedContext <- ctx
		}
		close(f.refreshEntered)
		<-f.refreshRelease // Deliberately model a provider returning late success.
	}
	return provider.NewCredentialState("refreshed-secret")
}

func credentialRefreshRoute(t *testing.T, d *Daemon, connectionID string) kernel.Route {
	t.Helper()
	node, err := d.store.ProviderNodeByConnection(connectionID)
	if err != nil {
		t.Fatal(err)
	}
	return kernel.Route{
		AuthFlowID: "oauth:oauth2", CredentialID: connectionID, NodeID: node.ID,
		DefinitionRef: node.DefinitionRef(), CredentialType: "oauth2",
	}
}

func TestResolveCredentialReturnsCallerCancellationAfterLateSuccess(t *testing.T) {
	flow := &delayedCredentialFlow{resolveEntered: make(chan struct{}), resolveRelease: make(chan struct{})}
	release := releaseCredentialCall(flow.resolveRelease)
	t.Cleanup(release)
	d, connectionID := cancellationAuthDaemon(t, flow)
	route := credentialRefreshRoute(t, d, connectionID)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := d.resolveCredential(ctx, route)
		result <- err
	}()
	awaitAuthorizationSignal(t, flow.resolveEntered)
	cancel()
	release()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("late successful resolve returned error %v, want context cancellation", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("resolve did not return after its provider call was released")
	}
}

func TestRefreshCredentialDoesNotPersistLateSuccessAfterCancellation(t *testing.T) {
	flow := &delayedCredentialFlow{refreshEntered: make(chan struct{}), refreshRelease: make(chan struct{})}
	release := releaseCredentialCall(flow.refreshRelease)
	t.Cleanup(release)
	d, connectionID := cancellationAuthDaemon(t, flow)
	route := credentialRefreshRoute(t, d, connectionID)
	current, ok := d.store.ConnectionCredentialByID(connectionID)
	if !ok {
		t.Fatal("fixture connection credential is unavailable")
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := d.refreshCredential(ctx, route, kernel.Credential{ConnectionID: current.ID, Type: current.Type, Secret: current.Secret})
		result <- err
	}()
	awaitAuthorizationSignal(t, flow.refreshEntered)
	cancel()
	release()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("late successful refresh returned error %v, want context cancellation", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("refresh did not return after its provider call was released")
	}
	stored, ok := d.store.ConnectionCredentialByID(connectionID)
	if !ok || stored.Secret != "original-secret" {
		t.Fatalf("cancelled refresh persisted a credential: %#v, exists=%v", stored, ok)
	}
}

func TestRefreshCredentialRejectsResultAfterManualCredentialUpdate(t *testing.T) {
	flow := &delayedCredentialFlow{refreshEntered: make(chan struct{}), refreshRelease: make(chan struct{})}
	release := releaseCredentialCall(flow.refreshRelease)
	t.Cleanup(release)
	d, connectionID := cancellationAuthDaemon(t, flow)
	route := credentialRefreshRoute(t, d, connectionID)
	current, ok := d.store.ConnectionCredentialByID(connectionID)
	if !ok {
		t.Fatal("fixture connection credential is unavailable")
	}
	result := make(chan error, 1)
	go func() {
		_, err := d.refreshCredential(context.Background(), route, kernel.Credential{ConnectionID: current.ID, Type: current.Type, Secret: current.Secret})
		result <- err
	}()
	awaitAuthorizationSignal(t, flow.refreshEntered)
	if _, err := d.store.UpdateConnection(store.UpdateConnectionInput{ID: connectionID, Secret: "manual-credential"}); err != nil {
		t.Fatal(err)
	}
	release()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("refresh succeeded after the stored credential changed")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("refresh did not return after its provider call was released")
	}
	stored, ok := d.store.ConnectionCredentialByID(connectionID)
	if !ok || stored.Secret != "manual-credential" {
		t.Fatalf("stale refresh replaced the manual credential: %#v, exists=%v", stored, ok)
	}
}

func TestRefreshCredentialCancelledWaiterDoesNotWaitForOwnerRelease(t *testing.T) {
	flow := &delayedCredentialFlow{}
	d, connectionID := cancellationAuthDaemon(t, flow)
	route := credentialRefreshRoute(t, d, connectionID)
	current, ok := d.store.ConnectionCredentialByID(connectionID)
	if !ok {
		t.Fatal("fixture connection credential is unavailable")
	}
	ownerUnlock, err := d.lockCredentialRefresh(context.Background(), connectionID)
	if err != nil {
		t.Fatal(err)
	}
	defer ownerUnlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := make(chan error, 1)
	go func() {
		_, err := d.refreshCredential(ctx, route, kernel.Credential{ConnectionID: current.ID, Type: current.Type, Secret: current.Secret})
		result <- err
	}()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled refresh waiter returned error %v, want context cancellation", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled refresh waiter blocked until the owner released its lock")
	}
}

func TestCredentialCallsRejectDaemonCancellationBeforeLateSuccess(t *testing.T) {
	for _, stage := range []string{"resolve", "refresh", "reuse-newer"} {
		t.Run(stage, func(t *testing.T) {
			entered, gate := make(chan struct{}), make(chan struct{})
			release := releaseCredentialCall(gate)
			t.Cleanup(release)
			flow := &delayedCredentialFlow{observedContext: make(chan context.Context, 1)}
			if stage == "refresh" {
				flow.refreshEntered, flow.refreshRelease = entered, gate
			} else {
				flow.resolveEntered, flow.resolveRelease = entered, gate
			}
			d, connectionID := cancellationAuthDaemon(t, flow)
			root, stop := context.WithCancel(context.Background())
			defer stop()
			d.runCtx = root
			route := credentialRefreshRoute(t, d, connectionID)
			before, _ := d.store.ConnectionCredentialByID(connectionID)
			result := make(chan error, 1)
			go func() {
				var err error
				if stage == "resolve" {
					_, err = d.resolveCredential(context.Background(), route)
				} else {
					secret := before.Secret
					if stage == "reuse-newer" {
						secret = "superseded-secret"
					}
					_, err = d.refreshCredential(context.Background(), route, kernel.Credential{ConnectionID: connectionID, Type: before.Type, Secret: secret})
				}
				result <- err
			}()
			awaitAuthorizationSignal(t, entered)
			observed := <-flow.observedContext
			stop()
			select {
			case <-observed.Done():
			case <-time.After(3 * time.Second):
				t.Fatal("daemon shutdown did not reach auth driver context")
			}
			release()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("late %s error=%v", stage, err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("late auth call did not return")
			}
			after, _ := d.store.ConnectionCredentialByID(connectionID)
			if after.Revision != before.Revision || after.Secret != before.Secret {
				t.Fatal("late auth success changed credential")
			}
		})
	}
}
