package daemon

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/api"
	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/runtime"
	"github.com/fm39hz/gobroom/internal/store"
)

type credentialCompletionFlow struct {
	refreshCalls atomic.Int32
	enteredOnce  sync.Once
	entered      chan struct{}
	release      chan struct{}
	refreshInput chan kernel.Credential
}

func (*credentialCompletionFlow) ID() string { return "credential-completion-fixture" }
func (*credentialCompletionFlow) SetupSchema() provider.SetupSchema {
	return provider.SetupSchema{}
}
func (*credentialCompletionFlow) Resolve(_ context.Context, input provider.AuthInput) (kernel.Credential, error) {
	return kernel.Credential{ConnectionID: input.ConnectionID, Type: input.Type, Secret: input.Secret}, nil
}
func (f *credentialCompletionFlow) Refresh(_ context.Context, _ provider.AuthInput, current kernel.Credential) (provider.CredentialState, error) {
	f.refreshCalls.Add(1)
	f.refreshInput <- current
	f.enteredOnce.Do(func() { close(f.entered) })
	<-f.release
	return provider.NewCredentialState("refreshed-secret")
}

func TestRefreshCredentialConcurrentCallersSharePersistedCredential(t *testing.T) {
	const callers = 8
	state, err := store.Open(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	node, err := state.CreateProviderNode(store.CreateProviderNodeInput{
		Name: "Credential fixture", Prefix: "credential-fixture", BaseURL: "https://provider.test/v1",
		Protocol: "openai_chat", DefinitionID: "credential-fixture", AuthMode: "api_key",
	})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := state.CreateConnection(store.CreateConnectionInput{
		ProviderNodeID: node.ID, Name: "account", CredentialType: "api_key", Secret: "original-secret",
	})
	if err != nil {
		t.Fatal(err)
	}

	flow := &credentialCompletionFlow{
		entered: make(chan struct{}), release: make(chan struct{}), refreshInput: make(chan kernel.Credential, callers),
	}
	var releaseOnce sync.Once
	releaseRefresh := func() { releaseOnce.Do(func() { close(flow.release) }) }
	t.Cleanup(releaseRefresh)
	d := &Daemon{
		store:     state,
		server:    api.NewServer(state),
		authFlows: map[string]provider.AuthFlow{"credential-fixture:api_key": flow},
		providerAuthFlowIDs: map[extensions.Ref]map[string]string{
			node.DefinitionRef(): {"api_key": "credential-fixture:api_key"},
		},
		policy: runtime.NewPolicyGate(),
	}
	initial := d.server.Control().Snapshot()
	d.policy.SetGenerations(initial.Routes)
	d.kernel, err = kernel.New(initial, d.policy, callers)
	if err != nil {
		t.Fatal(err)
	}
	d.server.SetReloadHook(func(next kernel.Snapshot) (kernel.Snapshot, error) {
		d.policy.SetGenerations(next.Routes)
		err := d.kernel.PublishSnapshot(next)
		return d.kernel.Snapshots.Load(), err
	})

	// A missing ConnectionID in the request snapshot distinguishes the
	// connection-scoped resolved credential from the stale caller snapshot.
	current := kernel.Credential{Type: "api_key", Secret: "original-secret"}
	configured, _ := state.ConnectionCredentialByID(connection.ID)
	route := kernel.Route{
		AuthFlowID: "credential-fixture:api_key", CredentialID: connection.ID,
		NodeID: node.ID, DefinitionRef: node.DefinitionRef(), CredentialType: "api_key",
		CredentialRecovery: kernel.CredentialRecoveryRefresh,
		HealthGeneration:   kernel.RouteHealthGeneration{Connection: configured.OperationalGeneration(), Credential: configured.Revision},
	}
	results := make(chan kernel.Credential, callers)
	errorsByCall := make(chan error, callers)
	var started sync.WaitGroup
	started.Add(callers)
	for range callers {
		go func() {
			started.Done()
			refreshed, refreshErr := d.refreshCredential(context.Background(), route, current)
			results <- refreshed
			errorsByCall <- refreshErr
		}()
	}
	started.Wait()
	select {
	case <-flow.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("no caller reached the provider refresh")
	}

	// Wait until every other caller has registered against this connection's
	// semaphore before allowing the first refresh to persist and release it.
	deadline := time.Now().Add(3 * time.Second)
	for {
		d.credentialRefreshMu.Lock()
		lock := d.credentialRefreshes[connection.ID]
		waiting := lock != nil && lock.users == callers
		d.credentialRefreshMu.Unlock()
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("all concurrent callers did not join the per-connection refresh semaphore")
		}
		time.Sleep(time.Millisecond)
	}
	releaseRefresh()

	for range callers {
		select {
		case refreshErr := <-errorsByCall:
			if refreshErr != nil {
				t.Fatalf("refresh returned an error: %v", refreshErr)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("a concurrent refresh caller did not complete")
		}
		select {
		case refreshed := <-results:
			if refreshed.ConnectionID != connection.ID || refreshed.Type != "api_key" || refreshed.Secret != "refreshed-secret" {
				t.Fatalf("refresh returned credential %#v, want the persisted replacement", refreshed)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("a concurrent refresh caller returned no credential")
		}
	}
	if calls := flow.refreshCalls.Load(); calls != 1 {
		t.Fatalf("provider refresh calls = %d, want exactly one", calls)
	}
	select {
	case refreshInput := <-flow.refreshInput:
		if refreshInput.ConnectionID != connection.ID || refreshInput.Secret != "original-secret" {
			t.Fatalf("provider refresh received %#v, want the latest credential resolved from storage", refreshInput)
		}
	default:
		t.Fatal("provider refresh input was not recorded")
	}
	stored, ok := state.ConnectionCredentialByID(connection.ID)
	if !ok || stored.Secret != "refreshed-secret" {
		t.Fatalf("stored credential = %#v, exists=%v; want persisted replacement", stored, ok)
	}
	// Request refresh only commits/wakes; local activation is a separate daemon
	// task. Drive that task explicitly in this unit fixture without a running daemon.
	if d.kernel.Snapshots.Load().Version != initial.Version {
		t.Fatal("request refresh synchronously reloaded the routing graph")
	}
	if view, err := d.retryCredentialActivation(context.Background(), connection.ID); err != nil || view.Status != store.ActivationActive {
		t.Fatalf("activate committed refresh: %+v %v", view, err)
	}
	if d.kernel.Snapshots.Load().Version == initial.Version {
		t.Fatalf("serving snapshot version remained %d after successful post-CAS reload", initial.Version)
	}
	d.credentialRefreshMu.Lock()
	lockRemained := d.credentialRefreshes[connection.ID] != nil
	d.credentialRefreshMu.Unlock()
	if lockRemained {
		t.Fatal("refresh lock entry remained after all callers completed")
	}
}

type ownerCompletionGateFlow struct {
	callbackOAuthFixture
	entered     chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
}

func (f *ownerCompletionGateFlow) releaseToken() {
	f.releaseOnce.Do(func() { close(f.release) })
}

func (f *ownerCompletionGateFlow) awaitToken() (provider.CredentialState, error) {
	close(f.entered)
	<-f.release
	return provider.NewCredentialState("new-token")
}

func (*ownerCompletionGateFlow) BeginDeviceAuthorization(context.Context) (provider.DeviceAuthorization, error) {
	return provider.DeviceAuthorization{
		DeviceCode: "private-device-code", UserCode: "USER-CODE",
		VerificationURL: "https://identity.test/device", ExpiresAt: time.Now().Add(time.Minute),
	}, nil
}

func (f *ownerCompletionGateFlow) WaitForDeviceAuthorization(context.Context, provider.DeviceAuthorization) (provider.CredentialState, error) {
	return f.awaitToken()
}

func TestAuthorizationCompletionRejectsProviderDefinitionRebind(t *testing.T) {
	for _, kind := range []string{"code", "device"} {
		t.Run(kind, func(t *testing.T) {
			flow := &ownerCompletionGateFlow{entered: make(chan struct{}), release: make(chan struct{})}
			authFlow := provider.AuthFlow(flow)
			if kind == "device" {
				authFlow = deviceAuthorizationDriver(t, flow,
					func(ctx context.Context, _ provider.AuthInput) (provider.DeviceAuthorization, error) {
						return flow.BeginDeviceAuthorization(ctx)
					},
					func(ctx context.Context, device provider.DeviceAuthorization) (provider.CredentialState, error) {
						return flow.WaitForDeviceAuthorization(ctx, device)
					},
				)
			} else {
				authFlow = authorizationCodeFixture(t, flow, "https://client.test/callback", false, func(context.Context, provider.AuthorizationCodeExchange) (provider.CredentialState, error) {
					return flow.awaitToken()
				})
			}
			d, connectionID := cancellationAuthDaemon(t, authFlow)
			t.Cleanup(flow.releaseToken)
			before, ok := d.store.ConnectionCredentialByID(connectionID)
			if !ok {
				t.Fatal("fixture connection credential is unavailable")
			}
			var sessionID string
			if kind == "code" {
				challenge, err := d.startSetupAuthorization(context.Background(), connectionID)
				if err != nil {
					t.Fatal(err)
				}
				sessionID = challenge.SessionID
				event := codeSubmitEvent(t, challenge, "approved")
				result := make(chan error, 1)
				go func() {
					_, completeErr := d.advanceSetupAuthorization(context.Background(), sessionID, challenge.Revision, event)
					result <- completeErr
				}()
				awaitAuthorizationSignal(t, flow.entered)
				rebindCredentialDefinition(t, d, connectionID, "different-definition")
				rebindCredentialDefinition(t, d, connectionID, "oauth")
				flow.releaseToken()
				if err := <-result; err == nil {
					t.Fatal("authorization code completion committed after provider definition changed away and back")
				}
				status, err := d.setupAuthorizationStatus(sessionID)
				if err != nil || status.Status != "failed" {
					t.Fatalf("authorization status=%+v err=%v; want failed", status, err)
				}
			} else {
				challenge, err := d.startSetupAuthorization(context.Background(), connectionID)
				if err != nil {
					t.Fatal(err)
				}
				sessionID = challenge.SessionID
				awaitAuthorizationSignal(t, flow.entered)
				rebindCredentialDefinition(t, d, connectionID, "different-definition")
				rebindCredentialDefinition(t, d, connectionID, "oauth")
				flow.releaseToken()
				drained := make(chan struct{})
				go func() { d.authWG.Wait(); close(drained) }()
				awaitAuthorizationSignal(t, drained)
				status, err := d.setupAuthorizationStatus(sessionID)
				if err != nil || status.Status != "failed" {
					t.Fatalf("device authorization status=%+v err=%v; want failed", status, err)
				}
			}
			stored, ok := d.store.ConnectionCredentialByID(connectionID)
			if !ok || stored.Secret != before.Secret || stored.Revision == before.Revision {
				t.Fatalf("definition rebind committed token or failed to rotate revision: %#v, exists=%v", stored, ok)
			}
		})
	}
}
