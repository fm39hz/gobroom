package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/store"
)

var genericSetupSchemaRef = extensions.Ref{Kind: extensions.SchemaKind, ID: "authorization-input", ContractVersion: 1}

type genericSetupFlow struct {
	callbackOAuthFixture
	beginCalls    atomic.Int32
	advanceCalls  atomic.Int32
	advance       func(context.Context, provider.AuthorizationState, provider.AuthorizationEvent) (provider.AuthorizationTransition, error)
	begin         provider.AuthorizationTransition
	beginOverride func(context.Context, provider.AuthInput) (provider.AuthorizationTransition, error)
}

func (f *genericSetupFlow) BeginAuthorization(ctx context.Context, input provider.AuthInput) (provider.AuthorizationTransition, error) {
	f.beginCalls.Add(1)
	if f.beginOverride != nil {
		return f.beginOverride(ctx, input)
	}
	return f.begin, nil
}

func (f *genericSetupFlow) AdvanceAuthorization(ctx context.Context, state provider.AuthorizationState, event provider.AuthorizationEvent) (provider.AuthorizationTransition, error) {
	f.advanceCalls.Add(1)
	if f.advance != nil {
		return f.advance(ctx, state, event)
	}
	return provider.AuthorizationTransition{}, context.Canceled
}

func genericSetupAction() provider.AuthorizationAction {
	ref := genericSetupSchemaRef
	return provider.AuthorizationAction{ID: "continue", Kind: provider.AuthorizationInput, Label: "Continue", InputSchemaRef: &ref}
}

func genericSetupTransition(t *testing.T, encoded string, actions ...provider.AuthorizationAction) provider.AuthorizationTransition {
	t.Helper()
	state, err := provider.NewAuthorizationState(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return provider.AuthorizationTransition{State: state, Actions: actions, ExpiresAt: time.Now().Add(time.Minute)}
}

func genericSetupCatalog(t *testing.T, d *Daemon) {
	t.Helper()
	catalog := extensions.NewCatalog()
	document, err := extensions.BindSchemaDocument(genericSetupSchemaRef, json.RawMessage(`{"type":"object","properties":{"code":{"type":"string","minLength":1}},"required":["code"],"additionalProperties":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.RegisterSchema(genericSetupSchemaRef, document); err != nil {
		t.Fatal(err)
	}
	d.setupSchemas, err = catalog.Freeze()
	if err != nil {
		t.Fatal(err)
	}
}

func genericSetupDaemon(t *testing.T, flow *genericSetupFlow) (*Daemon, string) {
	t.Helper()
	d, id := cancellationAuthDaemon(t, flow)
	genericSetupCatalog(t, d)
	return d, id
}

func TestGenericAuthorizationSetupReusesActionIDAtCurrentRevision(t *testing.T) {
	flow := &genericSetupFlow{begin: genericSetupTransition(t, `{"private":"begin-secret"}`, genericSetupAction())}
	flow.advance = func(_ context.Context, state provider.AuthorizationState, event provider.AuthorizationEvent) (provider.AuthorizationTransition, error) {
		want := `{"private":"begin-secret"}`
		if string(event.Value) != `{"code":"111111"}` {
			want = `{"private":"otp-secret"}`
		}
		if state.Encoded() != want {
			t.Errorf("advance got wrong private state: %s", state)
		}
		if string(event.Value) == `{"code":"111111"}` {
			next := genericSetupTransition(t, `{"private":"otp-secret"}`, genericSetupAction())
			next.ExpiresAt = flow.begin.ExpiresAt
			return next, nil
		}
		credential, err := oauthFixtureState(provider.OAuthTokenState{AccessToken: "replacement-access", RefreshToken: "replacement-refresh", ClientID: "client"})
		if err != nil {
			return provider.AuthorizationTransition{}, err
		}
		return provider.AuthorizationTransition{Credential: &credential}, nil
	}
	d, connectionID := genericSetupDaemon(t, flow)
	view, err := d.startSetupAuthorization(context.Background(), connectionID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != "awaiting_user" || view.Revision != 1 {
		t.Fatalf("unexpected initial view: %+v", view)
	}
	if view.Actions[0].ID != "continue" || view.Actions[0].InputSchemaRef == nil {
		t.Fatalf("input action missing: %+v", view.Actions)
	}
	first, err := d.advanceSetupAuthorization(context.Background(), view.SessionID, view.Revision, provider.AuthorizationEvent{Kind: provider.AuthorizationSubmit, ActionID: "continue", Value: json.RawMessage(`{"code":"111111"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "awaiting_user" || first.Revision != 2 || first.Actions[0].ID != "continue" {
		t.Fatalf("same-ID next step not published: %+v", first)
	}
	if _, err := d.advanceSetupAuthorization(context.Background(), first.SessionID, view.Revision, provider.AuthorizationEvent{Kind: provider.AuthorizationSubmit, ActionID: "continue", Value: json.RawMessage(`{"code":"222222"}`)}); err == nil {
		t.Fatal("stale revision was accepted")
	}
	final, err := d.advanceSetupAuthorization(context.Background(), first.SessionID, first.Revision, provider.AuthorizationEvent{Kind: provider.AuthorizationSubmit, ActionID: "continue", Value: json.RawMessage(`{"code":"222222"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != "completed" || final.Activation == nil || !final.Activation.CredentialCommitted {
		t.Fatalf("credential was not committed: %+v", final)
	}
	if flow.advanceCalls.Load() != 2 {
		t.Fatalf("driver calls = %d, want 2", flow.advanceCalls.Load())
	}
	for _, public := range []AuthorizationSetupView{view, first, final} {
		encoded, err := json.Marshal(public)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "begin-secret") || strings.Contains(string(encoded), "otp-secret") || strings.Contains(string(encoded), "replacement-access") {
			t.Fatal("private state leaked into view")
		}
	}
}

func TestGenericAuthorizationSetupRejectsInvalidSchemaWithoutEndingSession(t *testing.T) {
	flow := &genericSetupFlow{begin: genericSetupTransition(t, `{"private":"state"}`, genericSetupAction())}
	flow.advance = func(context.Context, provider.AuthorizationState, provider.AuthorizationEvent) (provider.AuthorizationTransition, error) {
		return genericSetupTransition(t, `{"private":"next"}`, genericSetupAction()), nil
	}
	d, connectionID := genericSetupDaemon(t, flow)
	view, err := d.startSetupAuthorization(context.Background(), connectionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.advanceSetupAuthorization(context.Background(), view.SessionID, view.Revision, provider.AuthorizationEvent{Kind: provider.AuthorizationSubmit, ActionID: "continue", Value: json.RawMessage(`{"password":"do-not-leak"}`)}); err == nil || strings.Contains(err.Error(), "do-not-leak") {
		t.Fatal("invalid schema input accepted or echoed")
	}
	if got := flow.advanceCalls.Load(); got != 0 {
		t.Fatalf("driver called %d times for invalid input", got)
	}
	status, err := d.setupAuthorizationStatus(view.SessionID)
	if err != nil || status.Status != "awaiting_user" {
		t.Fatalf("invalid input ended session: %+v, %v", status, err)
	}
}

func TestGenericAuthorizationSetupRejectsUnsafeURLAndUnknownSchema(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action provider.AuthorizationAction
	}{
		{name: "prohibited URL", action: provider.AuthorizationAction{ID: "continue", Kind: provider.AuthorizationOpenURL, Label: "Open", URL: "javascript:alert(1)"}},
		{name: "unknown schema", action: func() provider.AuthorizationAction {
			a := genericSetupAction()
			ref := extensions.Ref{Kind: extensions.SchemaKind, ID: "missing", ContractVersion: 1}
			a.InputSchemaRef = &ref
			return a
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flow := &genericSetupFlow{begin: genericSetupTransition(t, `{"private":"state"}`, tc.action)}
			d, connectionID := genericSetupDaemon(t, flow)
			if _, err := d.startSetupAuthorization(context.Background(), connectionID); err == nil {
				t.Fatal("invalid transition was accepted")
			}
			if got := flow.beginCalls.Load(); got != 1 {
				t.Fatalf("Begin calls = %d", got)
			}
		})
	}
}

func TestGenericAuthorizationSetupConcurrentInputIsSingleFlight(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	flow := &genericSetupFlow{begin: genericSetupTransition(t, `{"private":"state"}`, genericSetupAction())}
	flow.advance = func(context.Context, provider.AuthorizationState, provider.AuthorizationEvent) (provider.AuthorizationTransition, error) {
		close(entered)
		<-release
		next := genericSetupTransition(t, `{"private":"next"}`, genericSetupAction())
		next.ExpiresAt = flow.begin.ExpiresAt
		return next, nil
	}
	d, connectionID := genericSetupDaemon(t, flow)
	view, err := d.startSetupAuthorization(context.Background(), connectionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	result := make(chan error, 1)
	go func() {
		_, e := d.advanceSetupAuthorization(context.Background(), view.SessionID, view.Revision, provider.AuthorizationEvent{Kind: provider.AuthorizationSubmit, ActionID: "continue", Value: json.RawMessage(`{"code":"123"}`)})
		result <- e
	}()
	awaitAuthorizationSignal(t, entered)
	if _, err := d.advanceSetupAuthorization(context.Background(), view.SessionID, view.Revision, provider.AuthorizationEvent{Kind: provider.AuthorizationSubmit, ActionID: "continue", Value: json.RawMessage(`{"code":"456"}`)}); err == nil {
		t.Fatal("concurrent input was admitted")
	}
	releaseOnce.Do(func() { close(release) })
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if flow.advanceCalls.Load() != 1 {
		t.Fatalf("Advance calls = %d, want 1", flow.advanceCalls.Load())
	}
}

func TestGenericAuthorizationSetupExpiredSessionIsRetained(t *testing.T) {
	flow := &genericSetupFlow{begin: genericSetupTransition(t, `{"private":"state"}`, genericSetupAction())}
	d, connectionID := genericSetupDaemon(t, flow)
	view, err := d.startSetupAuthorization(context.Background(), connectionID)
	if err != nil {
		t.Fatal(err)
	}
	if !d.finishSetupAuthorization(view.SessionID, "expired", "authorization expired") {
		t.Fatal("could not finish session as expired")
	}
	retained, err := d.setupAuthorizationStatus(view.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if retained.Status != "expired" || len(retained.Actions) != 0 {
		t.Fatalf("expired session was not safely retained: %+v", retained)
	}
}

func TestGenericAuthorizationSetupRetainsTypedProviderDenial(t *testing.T) {
	flow := &genericSetupFlow{begin: genericSetupTransition(t, "private", genericSetupAction())}
	flow.advance = func(context.Context, provider.AuthorizationState, provider.AuthorizationEvent) (provider.AuthorizationTransition, error) {
		return provider.AuthorizationTransition{Failure: provider.AuthorizationDenied}, nil
	}
	d, id := genericSetupDaemon(t, flow)
	view, err := d.startSetupAuthorization(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	view, err = d.advanceSetupAuthorization(context.Background(), view.SessionID, view.Revision, provider.AuthorizationEvent{Kind: provider.AuthorizationSubmit, ActionID: "continue", Value: json.RawMessage(`{"code":"decline"}`)})
	if err != nil || view.Status != "denied" || len(view.Actions) != 0 || view.Activation != nil {
		t.Fatalf("typed denial was not retained safely: %+v %v", view, err)
	}
	current, _ := d.store.ConnectionCredentialByID(id)
	if current.Secret != "original-secret" {
		t.Fatal("denial changed credential")
	}
}

func TestGenericAuthorizationSetupTimerExpiresWithoutFrontend(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		flow := &genericSetupFlow{begin: genericSetupTransition(t, `{"private":"expiring-state"}`, genericSetupAction())}
		d, id := genericSetupDaemon(t, flow)
		view, err := d.startSetupAuthorization(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Minute + time.Second)
		synctest.Wait()
		retained, err := d.setupAuthorizationStatus(view.SessionID)
		if err != nil || retained.Status != "expired" || len(retained.Actions) != 0 {
			t.Fatalf("timer did not expire setup: %+v %v", retained, err)
		}
		sessions := d.authorizationSessions()
		sessions.mu.Lock()
		current := sessions.byID[view.SessionID]
		reserved := sessions.byConnection[id]
		sessions.mu.Unlock()
		if current.setupState.Encoded() != "" || current.expectedCredential.Secret != "" || current.setupPollTimer != nil || reserved != "" {
			t.Fatal("expiry retained private state or connection reservation")
		}
	})
}

func TestGenericAuthorizationSetupShutdownDrainsLateBegin(t *testing.T) {
	entered, observedCancel, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	flow := &genericSetupFlow{}
	flow.beginOverride = func(ctx context.Context, _ provider.AuthInput) (provider.AuthorizationTransition, error) {
		close(entered)
		<-ctx.Done()
		close(observedCancel)
		<-release
		credential, err := provider.NewCredentialState("late-begin-credential")
		return provider.AuthorizationTransition{Credential: &credential}, err
	}
	d, id := genericSetupDaemon(t, flow)
	root, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.runCtx = root
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	result := make(chan error, 1)
	go func() { _, err := d.startSetupAuthorization(context.Background(), id); result <- err }()
	awaitAuthorizationSignal(t, entered)
	if _, err := d.startSetupAuthorization(context.Background(), id); err == nil {
		t.Fatal("concurrent setup start bypassed reservation")
	}
	cancel()
	d.cancelAuthorizationSessions()
	awaitAuthorizationSignal(t, observedCancel)
	drained := make(chan struct{})
	go func() { d.authWG.Wait(); close(drained) }()
	select {
	case <-drained:
		t.Fatal("shutdown did not retain driver ownership")
	default:
	}
	releaseOnce.Do(func() { close(release) })
	if err := <-result; err == nil {
		t.Fatal("late begin completed after shutdown")
	}
	awaitAuthorizationSignal(t, drained)
	current, _ := d.store.ConnectionCredentialByID(id)
	if current.Secret != "original-secret" {
		t.Fatal("shutdown allowed late credential commit")
	}
}

func TestGenericAuthorizationSetupCanceledCallNeverInvokesDriver(t *testing.T) {
	for _, stage := range []string{"begin", "advance"} {
		t.Run(stage, func(t *testing.T) {
			flow := &genericSetupFlow{begin: genericSetupTransition(t, "private", genericSetupAction())}
			d, id := genericSetupDaemon(t, flow)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if stage == "begin" {
				if _, err := d.startSetupAuthorization(ctx, id); err == nil {
					t.Fatal("canceled begin succeeded")
				}
				if flow.beginCalls.Load() != 0 {
					t.Fatal("canceled begin invoked driver")
				}
				return
			}
			view, err := d.startSetupAuthorization(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := d.advanceSetupAuthorization(ctx, view.SessionID, view.Revision, provider.AuthorizationEvent{Kind: provider.AuthorizationSubmit, ActionID: "continue", Value: json.RawMessage(`{"code":"123"}`)}); err == nil {
				t.Fatal("canceled advance succeeded")
			}
			if flow.advanceCalls.Load() != 0 {
				t.Fatal("canceled advance invoked driver")
			}
		})
	}
}

func TestGenericAuthorizationSetupCancellationFencesLateCredential(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	flow := &genericSetupFlow{begin: genericSetupTransition(t, `{"private":"state"}`, genericSetupAction())}
	flow.advance = func(ctx context.Context, _ provider.AuthorizationState, _ provider.AuthorizationEvent) (provider.AuthorizationTransition, error) {
		close(entered)
		<-ctx.Done()
		<-release
		credential, err := oauthFixtureState(provider.OAuthTokenState{AccessToken: "late-access", RefreshToken: "late-refresh", ClientID: "client"})
		return provider.AuthorizationTransition{Credential: &credential}, err
	}
	d, connectionID := genericSetupDaemon(t, flow)
	view, err := d.startSetupAuthorization(context.Background(), connectionID)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, e := d.advanceSetupAuthorization(context.Background(), view.SessionID, view.Revision, provider.AuthorizationEvent{Kind: provider.AuthorizationSubmit, ActionID: "continue", Value: json.RawMessage(`{"code":"123"}`)})
		result <- e
	}()
	awaitAuthorizationSignal(t, entered)
	d.finishSetupAuthorization(view.SessionID, "cancelled", "authorization was cancelled")
	close(release)
	if err := <-result; err == nil {
		t.Fatal("late driver success completed cancelled session")
	}
	credential, ok := d.store.ConnectionCredentialByID(connectionID)
	if !ok || credential.Secret != "original-secret" {
		t.Fatalf("late credential was persisted: %+v", credential)
	}
}

func TestGenericAuthorizationSetupCredentialReplacementFencesStaleCommit(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	flow := &genericSetupFlow{begin: genericSetupTransition(t, `{"private":"state"}`, genericSetupAction())}
	flow.advance = func(context.Context, provider.AuthorizationState, provider.AuthorizationEvent) (provider.AuthorizationTransition, error) {
		close(entered)
		<-release
		credential, err := oauthFixtureState(provider.OAuthTokenState{AccessToken: "stale-access", RefreshToken: "stale-refresh", ClientID: "client"})
		return provider.AuthorizationTransition{Credential: &credential}, err
	}
	d, connectionID := genericSetupDaemon(t, flow)
	view, err := d.startSetupAuthorization(context.Background(), connectionID)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, e := d.advanceSetupAuthorization(context.Background(), view.SessionID, view.Revision, provider.AuthorizationEvent{Kind: provider.AuthorizationSubmit, ActionID: "continue", Value: json.RawMessage(`{"code":"123"}`)})
		result <- e
	}()
	awaitAuthorizationSignal(t, entered)
	current, ok := d.store.ConnectionCredentialByID(connectionID)
	if !ok {
		t.Fatal("connection disappeared")
	}
	if _, err := d.store.CompareAndSwapConnectionCredential(context.Background(), current, "replacement-by-another-writer", store.PrincipalReplacement); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-result; err == nil {
		t.Fatal("stale authorization credential committed")
	}
	updated, ok := d.store.ConnectionCredentialByID(connectionID)
	if !ok || updated.Secret != "replacement-by-another-writer" {
		t.Fatalf("unexpected credential after stale commit: %+v", updated)
	}
}
