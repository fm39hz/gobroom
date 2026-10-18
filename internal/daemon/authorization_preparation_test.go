package daemon

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/fm39hz/gobroom/internal/provider"
)

func preparingCallbackFlow(t *testing.T) *genericSetupFlow {
	t.Helper()
	flow := &genericSetupFlow{begin: genericSetupTransition(t, "private-preparation", genericSetupAction())}
	flow.begin.Callbacks = []provider.AuthorizationCallback{allocatedCallbackSpec(t, "continue", "n", true)}
	return flow
}

func requireCallbackPortReleased(t *testing.T, raw string) {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", parsed.Host)
	if err != nil {
		t.Fatalf("preparation retained socket: %v", err)
	}
	_ = listener.Close()
}

func TestGenericAuthorizationPreparationPublishesOnlyResolvedActions(t *testing.T) {
	flow := preparingCallbackFlow(t)
	allocated := ""
	flow.advance = func(_ context.Context, _ provider.AuthorizationState, event provider.AuthorizationEvent) (provider.AuthorizationTransition, error) {
		if event.Kind != provider.AuthorizationCallbacksReady || len(event.Callbacks) != 1 {
			t.Fatalf("unexpected preparation event: %s", event.Kind)
		}
		allocated = event.Callbacks[0].URL
		resolved, err := url.Parse(allocated)
		if err != nil || resolved.Port() == "0" || resolved.Port() == "" {
			t.Fatal("resource was not allocated before driver call")
		}
		next := flow.begin
		next.Callbacks = append([]provider.AuthorizationCallback(nil), flow.begin.Callbacks...)
		next.Callbacks[0].URL = allocated
		authorize, _ := url.Parse("https://identity.test/authorize")
		authorize.RawQuery = url.Values{"redirect_uri": {allocated}}.Encode()
		next.Actions = append(append([]provider.AuthorizationAction(nil), flow.begin.Actions...), provider.AuthorizationAction{ID: "consent", Kind: provider.AuthorizationOpenURL, Label: "Approve", URL: authorize.String()})
		return next, nil
	}
	d, id := genericSetupDaemon(t, flow)
	t.Cleanup(d.closeAuthorizationCallbacks)
	view, err := d.startSetupAuthorization(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if view.Revision != 1 || len(view.Actions) != 2 || len(view.CallbackModes) != 1 || flow.advanceCalls.Load() != 1 {
		t.Fatalf("intermediate preparation escaped: %+v", view)
	}
	consent, err := url.Parse(view.Actions[1].URL)
	if err != nil || consent.Query().Get("redirect_uri") != allocated {
		t.Fatal("consent did not use the held URI")
	}
	if _, reservations := callbackReservationSnapshot(d); reservations != 0 {
		t.Fatal("activation retained preparation reservations")
	}
	d.finishSetupAuthorization(view.SessionID, "cancelled", "cancelled")
	requireCallbackPortReleased(t, allocated)
}

func TestGenericAuthorizationPreparationFailuresReleaseReservations(t *testing.T) {
	for _, kind := range []string{"driver", "scope", "loop"} {
		t.Run(kind, func(t *testing.T) {
			flow := preparingCallbackFlow(t)
			var allocated []string
			flow.advance = func(_ context.Context, _ provider.AuthorizationState, event provider.AuthorizationEvent) (provider.AuthorizationTransition, error) {
				if event.Kind != provider.AuthorizationCallbacksReady || len(event.Callbacks) != 1 {
					t.Fatal("expected callback resource event")
				}
				allocated = append(allocated, event.Callbacks[0].URL)
				if kind == "driver" {
					return provider.AuthorizationTransition{}, fmt.Errorf("private-driver-error")
				}
				next := flow.begin
				if kind == "scope" {
					next.Callbacks = append([]provider.AuthorizationCallback(nil), flow.begin.Callbacks...)
					next.Callbacks[0].URL = event.Callbacks[0].URL + "&unrequested=value"
				}
				return next, nil
			}
			d, id := genericSetupDaemon(t, flow)
			t.Cleanup(d.closeAuthorizationCallbacks)
			if _, err := d.startSetupAuthorization(context.Background(), id); err == nil || strings.Contains(err.Error(), "private-driver-error") {
				t.Fatal("failed negotiation succeeded or echoed private data")
			}
			if listeners, reservations := callbackReservationSnapshot(d); listeners != 0 || reservations != 0 {
				t.Fatalf("failure retained listeners=%d reservations=%d", listeners, reservations)
			}
			for _, raw := range allocated {
				requireCallbackPortReleased(t, raw)
			}
			if kind == "loop" && flow.advanceCalls.Load() != maxAuthorizationResourceRounds {
				t.Fatalf("unbounded resource negotiation: calls=%d", flow.advanceCalls.Load())
			}
			current, _ := d.store.ConnectionCredentialByID(id)
			if current.Secret != "original-secret" {
				t.Fatal("preparation failure changed credential")
			}
		})
	}
}

func TestGenericAuthorizationPreparationCancellationFencesLateCredential(t *testing.T) {
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	flow := preparingCallbackFlow(t)
	allocated := ""
	flow.advance = func(ctx context.Context, _ provider.AuthorizationState, event provider.AuthorizationEvent) (provider.AuthorizationTransition, error) {
		allocated = event.Callbacks[0].URL
		close(entered)
		<-ctx.Done()
		close(cancelled)
		<-release
		credential, err := provider.NewCredentialState("late-preparation-credential")
		return provider.AuthorizationTransition{Credential: &credential}, err
	}
	d, id := genericSetupDaemon(t, flow)
	t.Cleanup(d.closeAuthorizationCallbacks)
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	result := make(chan error, 1)
	go func() { _, err := d.startSetupAuthorization(context.Background(), id); result <- err }()
	awaitAuthorizationSignal(t, entered)
	d.cancelAuthorizationSessions()
	awaitAuthorizationSignal(t, cancelled)
	requireCallbackPortReleased(t, allocated)
	once.Do(func() { close(release) })
	if err := <-result; err == nil {
		t.Fatal("late prepared credential committed after cancellation")
	}
	d.authWG.Wait()
	current, _ := d.store.ConnectionCredentialByID(id)
	if current.Secret != "original-secret" {
		t.Fatal("late prepared credential replaced current state")
	}
}
