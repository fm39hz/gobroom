package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/store"
)

// These tests deliberately cross the resource, callback, and credential commit
// boundaries. They use only the in-process generic authorization fixture.
func TestAuthorizationCompositionResourceCallbackManualReplacement(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	flow := preparingCallbackFlow(t)
	allocatedURL := ""
	flow.advance = func(_ context.Context, state provider.AuthorizationState, event provider.AuthorizationEvent) (provider.AuthorizationTransition, error) {
		if event.Kind == provider.AuthorizationCallbacksReady {
			if len(event.Callbacks) != 1 || event.Callbacks[0].URL == "" {
				t.Fatal("callback resource was not ready before driver continuation")
			}
			allocatedURL = event.Callbacks[0].URL
			next := flow.begin
			next.Callbacks = []provider.AuthorizationCallback{{ActionID: "continue", URL: event.Callbacks[0].URL, CorrelationParameter: "ticket", CorrelationValue: flow.begin.Callbacks[0].CorrelationValue, ResponseParameters: []string{"proof"}, Required: true}}
			return next, nil
		}
		if event.Kind != provider.AuthorizationSubmit || state.Encoded() != "private-preparation" {
			t.Fatalf("unexpected callback continuation: kind=%s", event.Kind)
		}
		close(entered)
		<-release // Return success after the stored credential has changed.
		late, err := provider.NewCredentialState("late-composed-credential")
		return provider.AuthorizationTransition{Credential: &late}, err
	}
	d, id := genericSetupDaemon(t, flow)
	catalog := extensions.NewCatalog()
	document, err := extensions.BindSchemaDocument(genericSetupSchemaRef, json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"}},"required":["url"],"additionalProperties":false}`))
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
	t.Cleanup(d.closeAuthorizationCallbacks)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	view, err := d.startSetupAuthorization(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if flow.advanceCalls.Load() != 1 || view.Revision != 1 || len(view.CallbackModes) != 1 {
		t.Fatalf("resource negotiation did not publish exactly one final revision: calls=%d view=%+v", flow.advanceCalls.Load(), view)
	}
	if view.CallbackModes[0].Mode != AuthorizationCallbackLoopback {
		t.Fatalf("unexpected callback mode: %+v", view.CallbackModes[0])
	}
	callbackURL, err := url.Parse(allocatedURL)
	if err != nil {
		t.Fatal(err)
	}
	query := callbackURL.Query()
	query.Set("ticket", flow.begin.Callbacks[0].CorrelationValue.Encoded())
	query.Set("proof", "approved")
	callbackURL.RawQuery = query.Encode()
	result := make(chan int, 1)
	go func() {
		response, err := (&http.Client{Timeout: 5 * time.Second}).Get(callbackURL.String())
		if err != nil {
			result <- 0
			return
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		result <- response.StatusCode
	}()
	awaitAuthorizationSignal(t, entered)
	if _, err := d.store.UpdateConnection(store.UpdateConnectionInput{ID: id, Secret: "manual-composed-credential"}); err != nil {
		t.Fatal(err)
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case status := <-result:
		if status == http.StatusOK {
			t.Fatal("callback acknowledged a credential rejected by the replacement fence")
		}
	case <-time.After(6 * time.Second):
		t.Fatal("callback request did not finish")
	}
	d.authWG.Wait()
	current, ok := d.store.ConnectionCredentialByID(id)
	if !ok || current.Secret != "manual-composed-credential" {
		t.Fatalf("late callback replaced manual credential (exists=%v)", ok)
	}
	status, err := d.setupAuthorizationStatus(view.SessionID)
	if err != nil || status.Status == "active" || len(status.CallbackModes) != 0 || len(status.Actions) != 0 {
		t.Fatalf("stale callback left a live authorization: status=%+v err=%v", status, err)
	}
	reservation, err := net.Listen("tcp", callbackURL.Host)
	if err != nil {
		t.Fatalf("callback listener was not released: %v", err)
	}
	_ = reservation.Close()
	if flow.advanceCalls.Load() != 2 {
		t.Fatalf("unexpected driver stage count: %d", flow.advanceCalls.Load())
	}
}

func TestAuthorizationCompositionResourceFailureLeavesNoPublicState(t *testing.T) {
	flow := preparingCallbackFlow(t)
	flow.advance = func(_ context.Context, _ provider.AuthorizationState, event provider.AuthorizationEvent) (provider.AuthorizationTransition, error) {
		if event.Kind != provider.AuthorizationCallbacksReady {
			t.Fatalf("driver called at unexpected stage %q", event.Kind)
		}
		return provider.AuthorizationTransition{}, context.Canceled
	}
	d, id := genericSetupDaemon(t, flow)
	t.Cleanup(d.closeAuthorizationCallbacks)
	if view, err := d.startSetupAuthorization(context.Background(), id); err == nil || view.SessionID != "" {
		t.Fatalf("cancelled resource continuation escaped publicly: view=%+v err=%v", view, err)
	}
	if flow.advanceCalls.Load() != 1 {
		t.Fatalf("resource driver stage count=%d, want one", flow.advanceCalls.Load())
	}
	if listeners, reservations := callbackReservationSnapshot(d); listeners != 0 || reservations != 0 {
		t.Fatalf("failed composition retained resources: listeners=%d reservations=%d", listeners, reservations)
	}
	credential, ok := d.store.ConnectionCredentialByID(id)
	if !ok || credential.Secret != "original-secret" {
		t.Fatal("resource failure altered private credential state")
	}
}
