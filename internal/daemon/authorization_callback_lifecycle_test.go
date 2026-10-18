package daemon

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/provider"
)

func callbackSetupFixture(t *testing.T, advance func(context.Context, provider.AuthorizationState, provider.AuthorizationEvent) (provider.AuthorizationTransition, error)) (*Daemon, string, AuthorizationSetupView, *url.URL) {
	t.Helper()
	port := callbackTestPort(t)
	target := fmt.Sprintf("http://127.0.0.1:%d/receive?tenant=local", port)
	token, err := provider.NewAuthorizationState(rand.Text() + rand.Text())
	if err != nil {
		t.Fatal(err)
	}
	flow := &genericSetupFlow{begin: genericSetupTransition(t, "private-callback-state", genericSetupAction()), advance: advance}
	flow.begin.Callbacks = []provider.AuthorizationCallback{{ActionID: "continue", URL: target, CorrelationParameter: "ticket", CorrelationValue: token, ResponseParameters: []string{"proof"}, Required: true}}
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
	view, err := d.startSetupAuthorization(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.closeAuthorizationCallbacks)
	parsed, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("ticket", token.Encoded())
	query.Set("proof", "approved")
	parsed.RawQuery = query.Encode()
	return d, id, view, parsed
}

func TestAuthorizationCallbackCancellationFencesAdmittedCredential(t *testing.T) {
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	d, id, view, callback := callbackSetupFixture(t, func(ctx context.Context, _ provider.AuthorizationState, _ provider.AuthorizationEvent) (provider.AuthorizationTransition, error) {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		<-release
		credential, err := provider.NewCredentialState("late-callback-credential")
		return provider.AuthorizationTransition{Credential: &credential}, err
	})
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	result := make(chan int, 1)
	go func() {
		client := &http.Client{Timeout: 5 * time.Second}
		response, err := client.Get(callback.String())
		if err != nil {
			result <- 0
			return
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		result <- response.StatusCode
	}()
	awaitAuthorizationSignal(t, entered)
	if !d.finishSetupAuthorization(view.SessionID, "cancelled", "authorization was cancelled") {
		t.Fatal("cancel did not own callback session")
	}
	awaitAuthorizationSignal(t, cancelled)
	reservation, err := net.Listen("tcp", callback.Host)
	if err != nil {
		t.Fatalf("cancel retained listener reservation: %v", err)
	}
	_ = reservation.Close()
	releaseOnce.Do(func() { close(release) })
	select {
	case status := <-result:
		if status == http.StatusOK {
			t.Fatal("late credential was acknowledged")
		}
	case <-time.After(6 * time.Second):
		t.Fatal("callback handler did not drain")
	}
	d.authWG.Wait()
	current, ok := d.store.ConnectionCredentialByID(id)
	if !ok || current.Secret != "original-secret" {
		t.Fatal("cancelled callback persisted its late credential")
	}
	status, err := d.setupAuthorizationStatus(view.SessionID)
	if err != nil || status.Status != "cancelled" || len(status.CallbackModes) != 0 || len(status.Actions) != 0 {
		t.Fatalf("cancelled status retained callback state: %+v %v", status, err)
	}
}

func TestAuthorizationCallbackExpiryReleasesGrantAndListener(t *testing.T) {
	d, _, view, callback := callbackSetupFixture(t, nil)
	if !d.finishSetupAuthorization(view.SessionID, "expired", "authorization expired") {
		t.Fatal("could not expire callback session")
	}
	if listeners, bindings := callbackRegistryCounts(d); listeners != 0 || bindings != 0 {
		t.Fatalf("expiry retained listeners=%d bindings=%d", listeners, bindings)
	}
	reservation, err := net.Listen("tcp", callback.Host)
	if err != nil {
		t.Fatalf("expiry retained port: %v", err)
	}
	_ = reservation.Close()
	status, err := d.setupAuthorizationStatus(view.SessionID)
	if err != nil || status.Status != "expired" || len(status.CallbackModes) != 0 {
		t.Fatalf("expiry status=%+v %v", status, err)
	}
}

func TestAuthorizationCallbackWaitsForPublishedStepWithoutPolling(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		flow := &genericSetupFlow{begin: genericSetupTransition(t, "private", genericSetupAction())}
		flow.advance = func(context.Context, provider.AuthorizationState, provider.AuthorizationEvent) (provider.AuthorizationTransition, error) {
			return provider.AuthorizationTransition{Failure: provider.AuthorizationDenied}, nil
		}
		d, id := genericSetupDaemon(t, flow)
		view, err := d.startSetupAuthorization(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		sessions := d.authorizationSessions()
		sessions.mu.Lock()
		session := sessions.byID[view.SessionID]
		session.setupCallDone = make(chan struct{})
		sessions.byID[view.SessionID] = session
		sessions.inFlight[view.SessionID] = true
		sessions.mu.Unlock()
		done := make(chan error, 1)
		go func() {
			done <- d.deliverSetupCallback(context.Background(), view.SessionID, view.Revision, provider.AuthorizationEvent{Kind: provider.AuthorizationSubmit, ActionID: "continue", Value: json.RawMessage(`{"code":"123"}`)})
		}()
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("callback did not wait for previous step: %v", err)
		default:
		}
		if flow.advanceCalls.Load() != 0 {
			t.Fatal("callback bypassed the admitted step")
		}
		sessions.mu.Lock()
		session = sessions.byID[view.SessionID]
		sessions.inFlight[view.SessionID] = false
		close(session.setupCallDone)
		session.setupCallDone = nil
		sessions.byID[view.SessionID] = session
		sessions.mu.Unlock()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		status, err := d.setupAuthorizationStatus(view.SessionID)
		if err != nil || status.Status != "denied" {
			t.Fatalf("callback was not delivered after step publication: %+v %v", status, err)
		}
	})
}

func TestGenericAuthorizationSetupRequiredBindFailuresDoNotFillRetention(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	correlation, err := provider.NewAuthorizationState(strings.Repeat("c", 32))
	if err != nil {
		t.Fatal(err)
	}
	flow := &genericSetupFlow{begin: genericSetupTransition(t, "private", genericSetupAction())}
	flow.begin.Callbacks = []provider.AuthorizationCallback{{ActionID: "continue", URL: "http://" + occupied.Addr().String() + "/callback", CorrelationParameter: "ticket", CorrelationValue: correlation, Required: true}}
	d, id := genericSetupDaemon(t, flow)
	t.Cleanup(d.closeAuthorizationCallbacks)
	for i := 0; i < maxAuthorizationSessions+1; i++ {
		if _, err := d.startSetupAuthorization(context.Background(), id); err == nil {
			t.Fatal("required listener unexpectedly bound occupied port")
		}
	}
	if flow.beginCalls.Load() != maxAuthorizationSessions+1 {
		t.Fatalf("failed starts filled retention: driverCalls=%d", flow.beginCalls.Load())
	}
	sessions := d.authorizationSessions()
	sessions.mu.Lock()
	retained, reserved := len(sessions.byID), len(sessions.byConnection)
	sessions.mu.Unlock()
	if retained != 0 || reserved != 0 {
		t.Fatalf("unreachable failed starts retained sessions=%d reservations=%d", retained, reserved)
	}
}
