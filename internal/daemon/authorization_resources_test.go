package daemon

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/provider"
)

func allocatedCallbackSpec(t *testing.T, action, marker string, required bool) provider.AuthorizationCallback {
	t.Helper()
	state, err := provider.NewAuthorizationState(strings.Repeat(marker, 32))
	if err != nil {
		t.Fatal(err)
	}
	return provider.AuthorizationCallback{
		ActionID: action, URL: "http://127.0.0.1:0/callback?tenant=local",
		CorrelationParameter: "ticket", CorrelationValue: state,
		ResponseParameters: []string{"proof"}, Required: required,
	}
}

func callbackReservationSnapshot(d *Daemon) (int, int) {
	d.callbackMu.Lock()
	defer d.callbackMu.Unlock()
	return len(d.callbackListeners), len(d.callbackReservations)
}

func TestAuthorizationCallbackReservationAllocatesAndActivatesExactGrant(t *testing.T) {
	d := &Daemon{}
	t.Cleanup(d.closeAuthorizationCallbacks)
	spec := allocatedCallbackSpec(t, "continue", "a", true)
	resources, err := d.reserveAuthorizationCallbacks(context.Background(), "owner", []provider.AuthorizationCallback{spec})
	if err != nil || len(resources) != 1 {
		t.Fatalf("reserve callback: resources=%#v err=%v", resources, err)
	}
	if resources[0].ActionID != spec.ActionID || resources[0].Mode != provider.AuthorizationCallbackLoopback {
		t.Fatalf("unexpected reservation: %#v", resources[0])
	}
	if strings.Contains(resources[0].URL, ":0/") || !strings.HasPrefix(resources[0].URL, "http://127.0.0.1:") {
		t.Fatalf("reservation did not resolve port zero: %q", resources[0].URL)
	}
	probe, err := http.Get(resources[0].URL)
	if err != nil {
		t.Fatalf("probe reserved callback URL: %v", err)
	}
	_ = probe.Body.Close()
	if probe.StatusCode != http.StatusBadRequest {
		t.Fatalf("unactivated reservation responded %d, want %d", probe.StatusCode, http.StatusBadRequest)
	}

	resolved := spec
	resolved.URL = resources[0].URL
	var invoked string
	binding := authorizationCallbackBinding{spec: resolved, invoke: func(_ context.Context, raw string) error { invoked = raw; return nil }}
	if _, err := d.replaceAuthorizationCallbacks("owner", []authorizationCallbackBinding{binding}); err != nil {
		t.Fatalf("activate exact reserved callback: %v", err)
	}
	parsed, err := url.Parse(resources[0].URL)
	if err != nil || parsed.Port() == "0" || parsed.Port() == "" {
		t.Fatalf("resolved URL has no allocated TCP port: %q (%v)", resources[0].URL, err)
	}
	callbackURL := fmt.Sprintf("%s&ticket=%s&proof=ok", resources[0].URL, resolved.CorrelationValue.Encoded())
	response, err := http.Get(callbackURL)
	if err != nil {
		t.Fatalf("invoke activated callback: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || invoked == "" {
		t.Fatalf("activated callback status=%d invoked=%q", response.StatusCode, invoked)
	}
}

func TestAuthorizationCallbackReservationReuseAndOwnerIsolation(t *testing.T) {
	d := &Daemon{}
	t.Cleanup(d.closeAuthorizationCallbacks)
	spec := allocatedCallbackSpec(t, "continue", "b", true)
	first, err := d.reserveAuthorizationCallbacks(context.Background(), "owner-one", []provider.AuthorizationCallback{spec})
	if err != nil {
		t.Fatal(err)
	}
	reused, err := d.reserveAuthorizationCallbacks(context.Background(), "owner-one", []provider.AuthorizationCallback{spec})
	if err != nil || len(reused) != 1 || reused[0].URL != first[0].URL {
		t.Fatalf("identical same-owner reservation changed: first=%#v reused=%#v err=%v", first, reused, err)
	}
	other, err := d.reserveAuthorizationCallbacks(context.Background(), "owner-two", []provider.AuthorizationCallback{spec})
	if err != nil || len(other) != 1 || other[0].URL == first[0].URL {
		t.Fatalf("distinct owners did not receive distinct ports: first=%#v other=%#v err=%v", first, other, err)
	}
	for _, resource := range []provider.AuthorizationCallbackResource{first[0], other[0]} {
		parsed, err := url.Parse(resource.URL)
		if err != nil {
			t.Fatal(err)
		}
		conn, err := net.Dial("tcp", net.JoinHostPort(parsed.Hostname(), parsed.Port()))
		if err != nil {
			t.Fatalf("allocated socket is not held at %q: %v", resource.URL, err)
		}
		_ = conn.Close()
	}
}

func TestAuthorizationCallbackReservationRollbackCancellationAndRelease(t *testing.T) {
	d := &Daemon{}
	t.Cleanup(d.closeAuthorizationCallbacks)
	blocked, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blocked.Close()
	port := blocked.Addr().(*net.TCPAddr).Port
	fixed := allocatedCallbackSpec(t, "fixed", "c", true)
	fixed.URL = fmt.Sprintf("http://127.0.0.1:%d/callback", port)
	dynamic := allocatedCallbackSpec(t, "dynamic", "d", true)
	if _, err := d.reserveAuthorizationCallbacks(context.Background(), "rollback", []provider.AuthorizationCallback{dynamic, fixed}); err == nil {
		t.Fatal("required fixed-port failure unexpectedly succeeded")
	}
	if listeners, reservations := callbackReservationSnapshot(d); listeners != 0 || reservations != 0 {
		t.Fatalf("failed batch retained listeners=%d reservations=%d", listeners, reservations)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.reserveAuthorizationCallbacks(ctx, "cancelled", []provider.AuthorizationCallback{dynamic}); err == nil {
		t.Fatal("cancelled reservation unexpectedly succeeded")
	}
	if listeners, reservations := callbackReservationSnapshot(d); listeners != 0 || reservations != 0 {
		t.Fatalf("cancelled request retained listeners=%d reservations=%d", listeners, reservations)
	}
	resources, err := d.reserveAuthorizationCallbacks(context.Background(), "release", []provider.AuthorizationCallback{dynamic})
	if err != nil {
		t.Fatal(err)
	}
	d.releaseAuthorizationCallbacks("release")
	parsed, err := url.Parse(resources[0].URL)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := net.Listen("tcp", net.JoinHostPort(parsed.Hostname(), parsed.Port()))
	if err != nil {
		t.Fatalf("release did not close allocated socket: %v", err)
	}
	_ = probe.Close()
}

func TestAuthorizationCallbackActivationRejectsChangedReservation(t *testing.T) {
	for _, mutate := range []struct {
		name string
		edit func(*provider.AuthorizationCallback)
	}{
		{name: "action", edit: func(spec *provider.AuthorizationCallback) { spec.ActionID = "other-action" }},
		{name: "required policy", edit: func(spec *provider.AuthorizationCallback) { spec.Required = !spec.Required }},
		{name: "URL", edit: func(spec *provider.AuthorizationCallback) { spec.URL += "&changed=yes" }},
		{name: "nonce", edit: func(spec *provider.AuthorizationCallback) {
			state, err := provider.NewAuthorizationState(strings.Repeat("z", 32))
			if err != nil {
				t.Fatal(err)
			}
			spec.CorrelationValue = state
		}},
		{name: "response parameters", edit: func(spec *provider.AuthorizationCallback) { spec.ResponseParameters = []string{"other"} }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			d := &Daemon{}
			t.Cleanup(d.closeAuthorizationCallbacks)
			original := allocatedCallbackSpec(t, "continue", "e", true)
			resources, err := d.reserveAuthorizationCallbacks(context.Background(), "owner", []provider.AuthorizationCallback{original})
			if err != nil {
				t.Fatal(err)
			}
			changed := original
			changed.URL = resources[0].URL
			mutate.edit(&changed)
			_, err = d.replaceAuthorizationCallbacks("owner", []authorizationCallbackBinding{{spec: changed, invoke: func(context.Context, string) error { return nil }}})
			if err == nil {
				t.Fatal("activation accepted a callback different from its reservation")
			}
		})
	}
}

func TestAuthorizationCallbackActivationPrunesUnusedReservations(t *testing.T) {
	d := &Daemon{}
	t.Cleanup(d.closeAuthorizationCallbacks)
	spec := allocatedCallbackSpec(t, "unused", "f", true)
	resources, err := d.reserveAuthorizationCallbacks(context.Background(), "owner", []provider.AuthorizationCallback{spec})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.replaceAuthorizationCallbacks("owner", nil); err != nil {
		t.Fatal(err)
	}
	if listeners, reservations := callbackReservationSnapshot(d); listeners != 0 || reservations != 0 {
		t.Fatalf("empty activation retained listeners=%d reservations=%d", listeners, reservations)
	}
	parsed, err := url.Parse(resources[0].URL)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(parsed.Hostname(), parsed.Port()))
	if err != nil {
		t.Fatalf("pruned reservation still owns its port: %v", err)
	}
	_ = listener.Close()
}

func TestGenericAuthorizationSetupRejectsPublicCallbacksReadyEvent(t *testing.T) {
	flow := &genericSetupFlow{begin: genericSetupTransition(t, `{"private":"setup-state"}`, genericSetupAction())}
	d, connectionID := genericSetupDaemon(t, flow)
	t.Cleanup(d.closeAuthorizationCallbacks)
	view, err := d.startSetupAuthorization(context.Background(), connectionID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.advanceSetupAuthorization(context.Background(), view.SessionID, view.Revision, provider.AuthorizationEvent{
		Kind:      provider.AuthorizationCallbacksReady,
		Callbacks: []provider.AuthorizationCallbackResource{{ActionID: "continue", URL: "http://127.0.0.1:12345/callback", Mode: provider.AuthorizationCallbackLoopback}},
	})
	if err == nil {
		t.Fatal("public setup advance accepted a daemon-only callback-ready event")
	}
	if got := flow.advanceCalls.Load(); got != 0 {
		t.Fatalf("callback-ready event reached provider before authorization: calls=%d", got)
	}
}
