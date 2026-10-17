package daemon

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/store"
)

type ownerGateCredentialFlow struct {
	callbackOAuthFixture
	resolveCalls   atomic.Int32
	refreshCalls   atomic.Int32
	refreshEntered chan struct{}
	refreshRelease chan struct{}
}

func (f *ownerGateCredentialFlow) Resolve(_ context.Context, input provider.AuthInput) (kernel.Credential, error) {
	f.resolveCalls.Add(1)
	return kernel.Credential{ConnectionID: input.ConnectionID, Type: input.Type, Secret: input.Secret}, nil
}

func (f *ownerGateCredentialFlow) Refresh(_ context.Context, _ provider.AuthInput, _ kernel.Credential) (provider.CredentialState, error) {
	f.refreshCalls.Add(1)
	if f.refreshEntered != nil {
		close(f.refreshEntered)
		<-f.refreshRelease
	}
	return provider.NewCredentialState("refreshed-secret")
}

func rebindCredentialDefinition(t *testing.T, d *Daemon, connectionID, definitionID string) {
	t.Helper()
	node, err := d.store.ProviderNodeByConnection(connectionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.store.UpdateProviderNode(store.UpdateProviderNodeInput{
		ID: node.ID, DefinitionID: definitionID, DefinitionContractVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialOwnerRejectsPinnedRoutesAfterDefinitionRebind(t *testing.T) {
	for _, stage := range []string{"resolve", "refresh"} {
		t.Run(stage, func(t *testing.T) {
			flow := &ownerGateCredentialFlow{}
			d, connectionID := cancellationAuthDaemon(t, flow)
			route := credentialRefreshRoute(t, d, connectionID)
			current, ok := d.store.ConnectionCredentialByID(connectionID)
			if !ok {
				t.Fatal("fixture connection credential is unavailable")
			}
			rebindCredentialDefinition(t, d, connectionID, "different-definition")
			var err error
			if stage == "resolve" {
				_, err = d.resolveCredential(context.Background(), route)
			} else {
				_, err = d.refreshCredential(context.Background(), route, kernel.Credential{
					ConnectionID: current.ID, Type: current.Type, Secret: current.Secret,
				})
			}
			if err == nil {
				t.Fatalf("%s accepted a route pinned to the previous provider definition", stage)
			}
			if got := flow.resolveCalls.Load() + flow.refreshCalls.Load(); got != 0 {
				t.Fatalf("stale route reached authorization flow %d times", got)
			}
			stored, ok := d.store.ConnectionCredentialByID(connectionID)
			if !ok || stored.Secret != "original-secret" {
				t.Fatalf("stale route changed credential: %#v, exists=%v", stored, ok)
			}
		})
	}
}

func TestCredentialOwnerRejectsIncorrectAuthBinding(t *testing.T) {
	for name, alter := range map[string]func(*kernel.Route){
		"node":               func(r *kernel.Route) { r.NodeID = "other-node" },
		"definition version": func(r *kernel.Route) { r.DefinitionRef.ContractVersion++ },
		"auth primitive":     func(r *kernel.Route) { r.AuthFlowID = "other-auth-binding" },
		"mode":               func(r *kernel.Route) { r.CredentialType = "other-mode" },
	} {
		t.Run(name, func(t *testing.T) {
			flow := &ownerGateCredentialFlow{}
			d, id := cancellationAuthDaemon(t, flow)
			route := credentialRefreshRoute(t, d, id)
			alter(&route)
			if _, err := d.resolveCredential(context.Background(), route); err == nil {
				t.Fatal("incorrect auth binding was accepted")
			}
			if flow.resolveCalls.Load() != 0 {
				t.Fatal("incorrect binding reached auth driver")
			}
		})
	}
}

func TestRefreshCredentialOwnerRebindBackInvalidatesInflightCAS(t *testing.T) {
	flow := &ownerGateCredentialFlow{refreshEntered: make(chan struct{}), refreshRelease: make(chan struct{})}
	release := releaseCredentialCall(flow.refreshRelease)
	t.Cleanup(release)
	d, connectionID := cancellationAuthDaemon(t, flow)
	current, ok := d.store.ConnectionCredentialByID(connectionID)
	if !ok {
		t.Fatal("fixture connection credential is unavailable")
	}
	route := credentialRefreshRoute(t, d, connectionID)
	result := make(chan error, 1)
	go func() {
		_, err := d.refreshCredential(context.Background(), route, kernel.Credential{
			ConnectionID: current.ID, Type: current.Type, Secret: current.Secret,
		})
		result <- err
	}()
	awaitAuthorizationSignal(t, flow.refreshEntered)
	rebindCredentialDefinition(t, d, connectionID, "different-definition")
	rebindCredentialDefinition(t, d, connectionID, "oauth")
	release()
	if err := <-result; err == nil {
		t.Fatal("in-flight refresh committed after provider ownership changed away and back")
	}
	stored, ok := d.store.ConnectionCredentialByID(connectionID)
	if !ok || stored.Secret != "original-secret" || stored.Revision == current.Revision {
		t.Fatalf("owner rebind race produced credential %#v, exists=%v; want original secret with rotated revision", stored, ok)
	}
}
