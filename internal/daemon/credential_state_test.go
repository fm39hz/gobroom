package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
)

const credentialStateTestValue = `{"access_token":"private-state-canary"}`

type credentialStateFailureFlow struct {
	callbackOAuthFixture
	refreshState provider.CredentialState
	resolveMode  string
	resolveStart chan struct{}
}

func (f *credentialStateFailureFlow) Resolve(ctx context.Context, input provider.AuthInput) (kernel.Credential, error) {
	if input.Secret == "original-secret" {
		return kernel.Credential{ConnectionID: input.ConnectionID, Type: input.Type, Secret: input.Secret}, nil
	}
	switch f.resolveMode {
	case "reject-state":
		return kernel.Credential{}, fmt.Errorf("provider error exposed %s", input.Secret)
	case "late-after-cancel":
		close(f.resolveStart)
		<-ctx.Done()
		// Model a provider that returns success after cancellation.
		return kernel.Credential{Secret: "late-access"}, nil
	case "invalid-runtime-header":
		return kernel.Credential{Secret: "candidate-access", RequestHeaders: http.Header{"X-Private": {"bad\r\nvalue"}}}, nil
	case "invalid-auth-context":
		return kernel.Credential{Secret: "candidate-access", AuthContext: kernel.AuthContext{"provider.user": "bad\nvalue"}}, nil
	default:
		return kernel.Credential{}, fmt.Errorf("unexpected replacement resolve")
	}
}

func (f *credentialStateFailureFlow) Refresh(context.Context, provider.AuthInput, kernel.Credential) (provider.CredentialState, error) {
	return f.refreshState, nil
}

func assertCredentialStateUnchanged(t *testing.T, d *Daemon, connectionID, beforeRevision string) {
	t.Helper()
	stored, ok := d.store.ConnectionCredentialByID(connectionID)
	if !ok || stored.Secret != "original-secret" || stored.Revision != beforeRevision {
		t.Fatalf("failed refresh changed stored credential: %#v, exists=%v; want original secret and revision %s", stored, ok, beforeRevision)
	}
}

func TestRefreshCredentialRejectsInvalidProviderStateBeforeCAS(t *testing.T) {
	flow := &credentialStateFailureFlow{callbackOAuthFixture: callbackOAuthFixture{}, refreshState: provider.CredentialState{}}
	d, connectionID := cancellationAuthDaemon(t, flow)
	before, ok := d.store.ConnectionCredentialByID(connectionID)
	if !ok {
		t.Fatal("fixture connection credential is unavailable")
	}
	route := credentialRefreshRoute(t, d, connectionID)
	_, err := d.refreshCredential(context.Background(), route, kernel.Credential{ConnectionID: connectionID, Type: before.Type, Secret: before.Secret})
	if err == nil {
		t.Fatal("refresh accepted an empty provider credential state")
	}
	assertCredentialStateUnchanged(t, d, connectionID, before.Revision)
}

func TestRefreshCredentialRedactsProviderStateResolveErrorBeforeCAS(t *testing.T) {
	state, err := provider.NewCredentialState(credentialStateTestValue)
	if err != nil {
		t.Fatal(err)
	}
	flow := &credentialStateFailureFlow{callbackOAuthFixture: callbackOAuthFixture{}, refreshState: state, resolveMode: "reject-state"}
	d, connectionID := cancellationAuthDaemon(t, flow)
	before, ok := d.store.ConnectionCredentialByID(connectionID)
	if !ok {
		t.Fatal("fixture connection credential is unavailable")
	}
	route := credentialRefreshRoute(t, d, connectionID)
	_, err = d.refreshCredential(context.Background(), route, kernel.Credential{ConnectionID: connectionID, Type: before.Type, Secret: before.Secret})
	if err == nil || strings.Contains(err.Error(), state.Encoded()) || strings.Contains(err.Error(), "private-state-canary") {
		t.Fatalf("replacement resolve error = %v; want a safe failure without provider state", err)
	}
	assertCredentialStateUnchanged(t, d, connectionID, before.Revision)
}

func TestRefreshCredentialLateReplacementResolveCannotCASAfterCancellation(t *testing.T) {
	for _, cancelKind := range []string{"caller", "daemon"} {
		t.Run(cancelKind, func(t *testing.T) {
			state, err := provider.NewCredentialState(credentialStateTestValue)
			if err != nil {
				t.Fatal(err)
			}
			flow := &credentialStateFailureFlow{
				callbackOAuthFixture: callbackOAuthFixture{}, refreshState: state,
				resolveMode: "late-after-cancel", resolveStart: make(chan struct{}),
			}
			d, connectionID := cancellationAuthDaemon(t, flow)
			before, ok := d.store.ConnectionCredentialByID(connectionID)
			if !ok {
				t.Fatal("fixture connection credential is unavailable")
			}
			route := credentialRefreshRoute(t, d, connectionID)
			callerCtx := context.Background()
			cancelCaller := func() {}
			stopDaemon := func() {}
			if cancelKind == "caller" {
				callerCtx, cancelCaller = context.WithCancel(context.Background())
			} else {
				root, stop := context.WithCancel(context.Background())
				d.runCtx = root
				stopDaemon = stop
			}
			defer cancelCaller()
			defer stopDaemon()

			result := make(chan error, 1)
			go func() {
				_, refreshErr := d.refreshCredential(callerCtx, route, kernel.Credential{ConnectionID: connectionID, Type: before.Type, Secret: before.Secret})
				result <- refreshErr
			}()
			awaitAuthorizationSignal(t, flow.resolveStart)
			if cancelKind == "caller" {
				cancelCaller()
			} else {
				stopDaemon()
			}
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("late replacement resolve returned %v, want context cancellation", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("late replacement resolve did not return after cancellation")
			}
			assertCredentialStateUnchanged(t, d, connectionID, before.Revision)
		})
	}
}

func TestRefreshCredentialRejectsMalformedRuntimeContextBeforeCAS(t *testing.T) {
	for _, mode := range []string{"invalid-runtime-header", "invalid-auth-context"} {
		t.Run(mode, func(t *testing.T) {
			state, err := provider.NewCredentialState(credentialStateTestValue)
			if err != nil {
				t.Fatal(err)
			}
			flow := &credentialStateFailureFlow{callbackOAuthFixture: callbackOAuthFixture{}, refreshState: state, resolveMode: mode}
			d, connectionID := cancellationAuthDaemon(t, flow)
			before, ok := d.store.ConnectionCredentialByID(connectionID)
			if !ok {
				t.Fatal("fixture connection credential is unavailable")
			}
			route := credentialRefreshRoute(t, d, connectionID)
			_, err = d.refreshCredential(context.Background(), route, kernel.Credential{ConnectionID: connectionID, Type: before.Type, Secret: before.Secret})
			if err == nil {
				t.Fatal("refresh accepted malformed runtime context")
			}
			assertCredentialStateUnchanged(t, d, connectionID, before.Revision)
		})
	}
}
