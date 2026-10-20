package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/store"
)

func TestAuthorizationSetupResumeReturnsActiveViewWithoutBegin(t *testing.T) {
	flow := &genericSetupFlow{begin: genericSetupTransition(t, `{"private":"resume-secret"}`, genericSetupAction())}
	d, id := genericSetupDaemon(t, flow)
	started, err := d.startSetupAuthorization(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := d.openSetupAuthorization(context.Background(), id, AuthorizationSetupResume)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.SessionID != started.SessionID || resumed.Revision != started.Revision || resumed.Status != started.Status || len(resumed.Actions) != len(started.Actions) {
		t.Fatalf("resume changed the active view: started=%+v resumed=%+v", started, resumed)
	}
	if len(resumed.Actions) > 0 && (resumed.Actions[0].ID != started.Actions[0].ID || resumed.Actions[0].Kind != started.Actions[0].Kind || resumed.Actions[0].Label != started.Actions[0].Label) {
		t.Fatalf("resume changed offered actions: started=%+v resumed=%+v", started.Actions, resumed.Actions)
	}
	if flow.beginCalls.Load() != 1 {
		t.Fatalf("Begin calls = %d, want 1", flow.beginCalls.Load())
	}
	if _, err := d.openSetupAuthorization(context.Background(), id, AuthorizationSetupNew); err == nil {
		t.Fatal("new setup was admitted while the connection had an active session")
	}
	if flow.beginCalls.Load() != 1 {
		t.Fatalf("busy new setup called Begin; calls = %d", flow.beginCalls.Load())
	}
}

func TestAuthorizationSetupUnknownModeDoesNotBegin(t *testing.T) {
	flow := &genericSetupFlow{begin: genericSetupTransition(t, `{"private":"mode-secret"}`, genericSetupAction())}
	d, id := genericSetupDaemon(t, flow)
	view, err := d.openSetupAuthorization(context.Background(), id, AuthorizationSetupStartMode("resume-ish"))
	if err == nil || view.SessionID != "" {
		t.Fatalf("unknown mode was accepted: view=%+v err=%v", view, err)
	}
	if flow.beginCalls.Load() != 0 {
		t.Fatalf("unknown mode called Begin %d times", flow.beginCalls.Load())
	}
	if strings.Contains(err.Error(), "mode-secret") {
		t.Fatal("mode error exposed private state")
	}
}

func TestAuthorizationSetupResumeWaitsForInitialBeginAndCallerCancellation(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	flow := &genericSetupFlow{}
	flow.beginOverride = func(ctx context.Context, _ provider.AuthInput) (provider.AuthorizationTransition, error) {
		close(entered)
		select {
		case <-release:
			return genericSetupTransition(t, `{"private":"begin-secret"}`, genericSetupAction()), nil
		case <-ctx.Done():
			return provider.AuthorizationTransition{}, ctx.Err()
		}
	}
	d, id := genericSetupDaemon(t, flow)
	ownerResult := make(chan error, 1)
	go func() { _, err := d.startSetupAuthorization(context.Background(), id); ownerResult <- err }()
	awaitAuthorizationSignal(t, entered)

	ctx, cancel := context.WithCancel(context.Background())
	resumeResult := make(chan error, 1)
	go func() { _, err := d.openSetupAuthorization(ctx, id, AuthorizationSetupResume); resumeResult <- err }()
	cancel()
	select {
	case err := <-resumeResult:
		if err == nil || !strings.Contains(err.Error(), "cancel") {
			t.Fatalf("canceled resume returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled resume did not return")
	}
	releaseOnce.Do(func() { close(release) })
	if err := <-ownerResult; err != nil {
		t.Fatalf("owner Begin failed after resume cancellation: %v", err)
	}
	if flow.beginCalls.Load() != 1 {
		t.Fatalf("Begin calls = %d, want 1", flow.beginCalls.Load())
	}
	view, err := d.openSetupAuthorization(context.Background(), id, AuthorizationSetupResume)
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != "awaiting_user" || flow.beginCalls.Load() != 1 {
		t.Fatalf("resume did not return owner's completed view: %+v, Begin calls=%d", view, flow.beginCalls.Load())
	}
}

func TestAuthorizationSetupResumeWaitsForAdvanceAndReturnsLatestRevision(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		var releaseOnce sync.Once
		defer releaseOnce.Do(func() { close(release) })
		flow := &genericSetupFlow{begin: genericSetupTransition(t, `{"private":"first"}`, genericSetupAction())}
		flow.advance = func(_ context.Context, _ provider.AuthorizationState, _ provider.AuthorizationEvent) (provider.AuthorizationTransition, error) {
			close(entered)
			<-release
			next := genericSetupTransition(t, `{"private":"second"}`, genericSetupAction())
			next.ExpiresAt = flow.begin.ExpiresAt
			return next, nil
		}
		d, id := genericSetupDaemon(t, flow)
		initial, err := d.startSetupAuthorization(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		stepResult := make(chan error, 1)
		go func() {
			_, err := d.advanceSetupAuthorization(context.Background(), initial.SessionID, initial.Revision, provider.AuthorizationEvent{Kind: provider.AuthorizationSubmit, ActionID: "continue", Value: []byte(`{"code":"123456"}`)})
			stepResult <- err
		}()
		awaitAuthorizationSignal(t, entered)
		resumeResult := make(chan AuthorizationSetupView, 1)
		resumeErr := make(chan error, 1)
		go func() {
			view, err := d.openSetupAuthorization(context.Background(), id, AuthorizationSetupResume)
			resumeResult <- view
			resumeErr <- err
		}()
		synctest.Wait()
		select {
		case <-resumeResult:
			t.Fatal("resume returned while the step was still in flight")
		default:
		}
		releaseOnce.Do(func() { close(release) })
		if err := <-stepResult; err != nil {
			t.Fatal(err)
		}
		view := <-resumeResult
		if err := <-resumeErr; err != nil {
			t.Fatal(err)
		}
		if view.SessionID != initial.SessionID || view.Revision != initial.Revision+1 || view.Status != "awaiting_user" {
			t.Fatalf("resume returned stale view: %+v", view)
		}
		if flow.beginCalls.Load() != 1 || flow.advanceCalls.Load() != 1 {
			t.Fatalf("driver calls: Begin=%d Advance=%d", flow.beginCalls.Load(), flow.advanceCalls.Load())
		}
	})
}

func TestAuthorizationSetupResumeDoesNotReuseExpiredSession(t *testing.T) {
	flow := &genericSetupFlow{begin: genericSetupTransition(t, `{"private":"expired"}`, genericSetupAction())}
	d, id := genericSetupDaemon(t, flow)
	first, err := d.startSetupAuthorization(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !d.finishSetupAuthorization(first.SessionID, "expired", "authorization expired") {
		t.Fatal("could not expire setup session")
	}
	second, err := d.openSetupAuthorization(context.Background(), id, AuthorizationSetupResume)
	if err != nil {
		t.Fatal(err)
	}
	if second.SessionID == first.SessionID || flow.beginCalls.Load() != 2 {
		t.Fatalf("fresh setup was not started: first=%s second=%s Begin calls=%d", first.SessionID, second.SessionID, flow.beginCalls.Load())
	}
}

func TestAuthorizationSetupResumeRejectsReplacedCredentialOwner(t *testing.T) {
	flow := &genericSetupFlow{begin: genericSetupTransition(t, `{"private":"stale-session"}`, genericSetupAction())}
	d, id := genericSetupDaemon(t, flow)
	started, err := d.startSetupAuthorization(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	current, ok := d.store.ConnectionCredentialByID(id)
	if !ok {
		t.Fatal("connection disappeared")
	}
	if _, err := d.store.CompareAndSwapConnectionCredential(context.Background(), current, "replacement-secret", store.PrincipalReplacement); err != nil {
		t.Fatal(err)
	}
	view, err := d.openSetupAuthorization(context.Background(), id, AuthorizationSetupResume)
	if err == nil || view.SessionID != "" {
		t.Fatalf("resume reused a session after credential replacement: view=%+v err=%v", view, err)
	}
	if strings.Contains(err.Error(), "replacement-secret") || strings.Contains(err.Error(), "stale-session") {
		t.Fatal("resume error exposed credential or provider state")
	}
	retained, statusErr := d.setupAuthorizationStatus(started.SessionID)
	if statusErr != nil || (retained.Status != "failed" && retained.Status != "cancelled") {
		t.Fatalf("stale session was not finished: view=%+v err=%v", retained, statusErr)
	}
	if flow.beginCalls.Load() != 1 {
		t.Fatalf("stale resume started another provider flow; Begin calls=%d", flow.beginCalls.Load())
	}
}

func TestAuthorizationSetupResumeDoesNotWaitForDaemonAwait(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	flow := &genericSetupFlow{begin: genericSetupTransition(t, "private-await-resume", provider.AuthorizationAction{ID: "code", Kind: provider.AuthorizationDisplayCode, Label: "Approve", Code: "PUBLIC-CODE"})}
	flow.begin.Await = true
	flow.advance = func(ctx context.Context, _ provider.AuthorizationState, event provider.AuthorizationEvent) (provider.AuthorizationTransition, error) {
		if event.Kind != provider.AuthorizationAwait {
			t.Error("unexpected event")
		}
		close(entered)
		select {
		case <-ctx.Done():
			return provider.AuthorizationTransition{}, ctx.Err()
		case <-release:
			return provider.AuthorizationTransition{}, context.Canceled
		}
	}
	d, id := genericSetupDaemon(t, flow)
	t.Cleanup(func() { close(release); d.cancelAuthorizationSessions(); d.authWG.Wait() })
	first, err := d.startSetupAuthorization(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	awaitAuthorizationSignal(t, entered)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	resumed, err := d.openSetupAuthorization(ctx, id, AuthorizationSetupResume)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.SessionID != first.SessionID || len(resumed.Actions) != 1 || flow.beginCalls.Load() != 1 {
		t.Fatal("reattachment lost published device instructions")
	}
	encoded, err := json.Marshal(resumed)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-await-resume") {
		t.Fatal("reattachment exposed private driver state")
	}
}

func TestAuthorizationSetupResumeOwnerRebindRetiresResources(t *testing.T) {
	flow := preparingCallbackFlow(t)
	allocated := ""
	flow.advance = func(_ context.Context, _ provider.AuthorizationState, event provider.AuthorizationEvent) (provider.AuthorizationTransition, error) {
		allocated = event.Callbacks[0].URL
		next := flow.begin
		next.Callbacks = append([]provider.AuthorizationCallback(nil), flow.begin.Callbacks...)
		next.Callbacks[0].URL = allocated
		return next, nil
	}
	d, id := genericSetupDaemon(t, flow)
	t.Cleanup(d.closeAuthorizationCallbacks)
	first, err := d.startSetupAuthorization(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	rebindCredentialDefinition(t, d, id, "unavailable-definition")
	if _, err := d.openSetupAuthorization(context.Background(), id, AuthorizationSetupResume); err == nil {
		t.Fatal("resumed an old owner's grant")
	}
	view, err := d.setupAuthorizationStatus(first.SessionID)
	if err != nil || view.Status != "failed" {
		t.Fatalf("old owner was not retired: %+v %v", view, err)
	}
	requireCallbackPortReleased(t, allocated)
}
