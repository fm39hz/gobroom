package daemon

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/fm39hz/gobroom/internal/provider"
)

// callbackTestPort briefly reserves an OS-selected port to give registry tests
// a stable, nonzero port. The registry itself owns the later bind.
func callbackTestPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func callbackTestBinding(t *testing.T, action, callbackURL, token string, required bool, invoke func(context.Context, string) error) authorizationCallbackBinding {
	t.Helper()
	state, err := provider.NewAuthorizationState(token)
	if err != nil {
		t.Fatal(err)
	}
	return authorizationCallbackBinding{
		spec: provider.AuthorizationCallback{
			ActionID: action, URL: callbackURL, CorrelationParameter: "state",
			CorrelationValue: state, ResponseParameters: []string{"code"}, Required: required,
		},
		invoke: invoke,
	}
}

func callbackRegistryCounts(d *Daemon) (listeners, bindings int) {
	d.callbackMu.Lock()
	defer d.callbackMu.Unlock()
	return len(d.callbackListeners), len(d.callbackBindings)
}

func TestAuthorizationCallbackRegistrySharesListenerAndReleasesByOwner(t *testing.T) {
	port := callbackTestPort(t)
	var firstCalls, secondCalls atomic.Int32
	d := &Daemon{}
	firstURL := fmt.Sprintf("http://127.0.0.1:%d/one", port)
	secondURL := fmt.Sprintf("http://localhost:%d/two", port)
	defer d.closeAuthorizationCallbacks()
	views, err := d.replaceAuthorizationCallbacks("owner-one", []authorizationCallbackBinding{
		callbackTestBinding(t, "action-one", firstURL, strings.Repeat("a", 32), true, func(context.Context, string) error { firstCalls.Add(1); return nil }),
	})
	if err != nil || len(views) != 1 || views[0].Mode != AuthorizationCallbackLoopback {
		t.Fatalf("register first grant: views=%#v err=%v", views, err)
	}
	views, err = d.replaceAuthorizationCallbacks("owner-two", []authorizationCallbackBinding{
		callbackTestBinding(t, "action-two", secondURL, strings.Repeat("b", 32), true, func(context.Context, string) error { secondCalls.Add(1); return nil }),
	})
	if err != nil || len(views) != 1 || views[0].Mode != AuthorizationCallbackLoopback {
		t.Fatalf("register second grant: views=%#v err=%v", views, err)
	}
	if got, _ := callbackRegistryCounts(d); got != 1 {
		t.Fatalf("shared bind address allocated %d listeners, want 1", got)
	}

	d.releaseAuthorizationCallbacks("owner-one")
	req := httptest.NewRequest(http.MethodGet, secondURL+"?code=ok&state="+strings.Repeat("b", 32), nil)
	req.RemoteAddr = "127.0.0.1:1234"
	resp := httptest.NewRecorder()
	d.handleAuthorizationCallback(resp, req)
	if resp.Code != http.StatusOK || secondCalls.Load() != 1 || firstCalls.Load() != 0 {
		t.Fatalf("remaining owner's grant not active: status=%d first=%d second=%d", resp.Code, firstCalls.Load(), secondCalls.Load())
	}
	if got, _ := callbackRegistryCounts(d); got != 1 {
		t.Fatal("releasing one owner closed a listener still used by another")
	}
	d.releaseAuthorizationCallbacks("owner-two")
	if got, _ := callbackRegistryCounts(d); got != 0 {
		t.Fatal("last release retained listener")
	}
	reservation, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("last release did not close the actual listener reservation: %v", err)
	}
	_ = reservation.Close()
}

func TestAuthorizationCallbackReplacementRollbackAndOptionalBindFailure(t *testing.T) {
	oldPort := callbackTestPort(t)
	blocked, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blocked.Close()
	blockedPort := blocked.Addr().(*net.TCPAddr).Port
	newPort := callbackTestPort(t)
	var oldCalls atomic.Int32
	d := &Daemon{}
	defer d.closeAuthorizationCallbacks()
	oldURL := fmt.Sprintf("http://127.0.0.1:%d/old", oldPort)
	if _, err := d.replaceAuthorizationCallbacks("owner", []authorizationCallbackBinding{
		callbackTestBinding(t, "old", oldURL, strings.Repeat("o", 32), true, func(context.Context, string) error { oldCalls.Add(1); return nil }),
	}); err != nil {
		t.Fatal(err)
	}
	_, err = d.replaceAuthorizationCallbacks("owner", []authorizationCallbackBinding{
		callbackTestBinding(t, "tentative", fmt.Sprintf("http://127.0.0.1:%d/new", newPort), strings.Repeat("n", 32), true, func(context.Context, string) error { return nil }),
		callbackTestBinding(t, "blocked", fmt.Sprintf("http://127.0.0.1:%d/fail", blockedPort), strings.Repeat("f", 32), true, func(context.Context, string) error { return nil }),
	})
	if err == nil {
		t.Fatal("required listener failure unexpectedly succeeded")
	}
	oldReq := httptest.NewRequest(http.MethodGet, oldURL+"?code=x&state="+strings.Repeat("o", 32), nil)
	oldReq.RemoteAddr = "127.0.0.1:1234"
	oldResp := httptest.NewRecorder()
	d.handleAuthorizationCallback(oldResp, oldReq)
	if oldResp.Code != http.StatusOK || oldCalls.Load() != 1 {
		t.Fatalf("failed replacement did not preserve old grant: status=%d calls=%d", oldResp.Code, oldCalls.Load())
	}
	probe, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", newPort))
	if err != nil {
		t.Fatalf("failed replacement leaked tentative listener: %v", err)
	}
	_ = probe.Close()

	manualPort := blockedPort
	views, err := d.replaceAuthorizationCallbacks("manual-owner", []authorizationCallbackBinding{
		callbackTestBinding(t, "manual", fmt.Sprintf("http://127.0.0.1:%d/manual", manualPort), strings.Repeat("m", 32), false, func(context.Context, string) error { t.Error("manual callback must not be granted"); return nil }),
	})
	if err != nil || len(views) != 1 || views[0].Mode != AuthorizationCallbackManual {
		t.Fatalf("optional bind failure should produce manual mode: views=%#v err=%v", views, err)
	}
	d.callbackMu.Lock()
	for _, binding := range d.callbackBindings {
		if binding.spec.ActionID == "manual" {
			d.callbackMu.Unlock()
			t.Fatal("optional bind failure published a callback grant")
		}
	}
	d.callbackMu.Unlock()
}

func TestAuthorizationCallbackRegistryCloseRejectsAcquire(t *testing.T) {
	d := &Daemon{}
	d.closeAuthorizationCallbacks()
	_, err := d.replaceAuthorizationCallbacks("owner", []authorizationCallbackBinding{
		callbackTestBinding(t, "action", fmt.Sprintf("http://127.0.0.1:%d/cb", callbackTestPort(t)), strings.Repeat("c", 32), true, func(context.Context, string) error { return nil }),
	})
	if err == nil {
		t.Fatal("closed registry accepted a new grant")
	}
}

func TestAuthorizationCallbackHandlerRequiresExactLocalGrant(t *testing.T) {
	port := callbackTestPort(t)
	target := fmt.Sprintf("http://127.0.0.1:%d/callback/nested?tenant=blue", port)
	token := strings.Repeat("s", 32)
	var calls atomic.Int32
	d := &Daemon{}
	defer d.closeAuthorizationCallbacks()
	if _, err := d.replaceAuthorizationCallbacks("owner", []authorizationCallbackBinding{
		callbackTestBinding(t, "action", target, token, true, func(_ context.Context, got string) error {
			if got == "" {
				t.Error("invoker received empty callback URL")
			}
			calls.Add(1)
			return nil
		}),
	}); err != nil {
		t.Fatal(err)
	}
	testCases := []struct {
		name, method, host, path, query, remote string
		wantStatus                              int
	}{
		{name: "valid", method: http.MethodGet, host: fmt.Sprintf("127.0.0.1:%d", port), path: "/callback/nested", query: "tenant=blue&code=accepted&state=" + token, remote: "127.0.0.1:4567", wantStatus: http.StatusOK},
		{name: "remote address", method: http.MethodGet, host: fmt.Sprintf("127.0.0.1:%d", port), path: "/callback/nested", query: "tenant=blue&code=x&state=" + token, remote: "203.0.113.9:4567", wantStatus: http.StatusForbidden},
		{name: "method", method: http.MethodPost, host: fmt.Sprintf("127.0.0.1:%d", port), path: "/callback/nested", query: "tenant=blue&code=x&state=" + token, remote: "127.0.0.1:4567", wantStatus: http.StatusMethodNotAllowed},
		{name: "host", method: http.MethodGet, host: "localhost:" + fmt.Sprint(port), path: "/callback/nested", query: "tenant=blue&code=x&state=" + token, remote: "127.0.0.1:4567", wantStatus: http.StatusBadRequest},
		{name: "path", method: http.MethodGet, host: fmt.Sprintf("127.0.0.1:%d", port), path: "/callback/other", query: "tenant=blue&code=x&state=" + token, remote: "127.0.0.1:4567", wantStatus: http.StatusBadRequest},
		{name: "static query", method: http.MethodGet, host: fmt.Sprintf("127.0.0.1:%d", port), path: "/callback/nested", query: "tenant=red&code=x&state=" + token, remote: "127.0.0.1:4567", wantStatus: http.StatusBadRequest},
		{name: "correlation", method: http.MethodGet, host: fmt.Sprintf("127.0.0.1:%d", port), path: "/callback/nested", query: "tenant=blue&code=x&state=" + strings.Repeat("z", 32), remote: "127.0.0.1:4567", wantStatus: http.StatusBadRequest},
		{name: "unknown parameter", method: http.MethodGet, host: fmt.Sprintf("127.0.0.1:%d", port), path: "/callback/nested", query: "tenant=blue&code=x&state=" + token + "&extra=x", remote: "127.0.0.1:4567", wantStatus: http.StatusBadRequest},
		{name: "duplicate response", method: http.MethodGet, host: fmt.Sprintf("127.0.0.1:%d", port), path: "/callback/nested", query: "tenant=blue&code=x&code=y&state=" + token, remote: "127.0.0.1:4567", wantStatus: http.StatusBadRequest},
		{name: "duplicate correlation", method: http.MethodGet, host: fmt.Sprintf("127.0.0.1:%d", port), path: "/callback/nested", query: "tenant=blue&code=x&state=" + token + "&state=" + token, remote: "127.0.0.1:4567", wantStatus: http.StatusBadRequest},
		{name: "malformed query", method: http.MethodGet, host: fmt.Sprintf("127.0.0.1:%d", port), path: "/callback/nested", query: "tenant=blue&code=x&state=" + token + "%zz", remote: "127.0.0.1:4567", wantStatus: http.StatusBadRequest},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "http://"+tc.host+tc.path+"?"+tc.query, nil)
			req.RemoteAddr = tc.remote
			resp := httptest.NewRecorder()
			d.handleAuthorizationCallback(resp, req)
			if resp.Code != tc.wantStatus {
				t.Fatalf("status=%d, want %d", resp.Code, tc.wantStatus)
			}
		})
	}
	if calls.Load() != 1 {
		t.Fatalf("invoker called %d times, want exactly once", calls.Load())
	}
}

func TestAuthorizationCallbackHandlerFailsClosedOnAmbiguousGrants(t *testing.T) {
	port := callbackTestPort(t)
	target := fmt.Sprintf("http://127.0.0.1:%d/callback", port)
	var calls atomic.Int32
	d := &Daemon{}
	defer d.closeAuthorizationCallbacks()
	for _, owner := range []string{"first", "second"} {
		if _, err := d.replaceAuthorizationCallbacks(owner, []authorizationCallbackBinding{
			callbackTestBinding(t, "action-"+owner, target, strings.Repeat("a", 32), true, func(context.Context, string) error { calls.Add(1); return nil }),
		}); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(http.MethodGet, target+"?code=x&state="+strings.Repeat("a", 32), nil)
	req.RemoteAddr = "127.0.0.1:4567"
	resp := httptest.NewRecorder()
	d.handleAuthorizationCallback(resp, req)
	if resp.Code != http.StatusBadRequest || calls.Load() != 0 {
		t.Fatalf("ambiguous grants were not rejected: status=%d calls=%d", resp.Code, calls.Load())
	}
}

func TestAuthorizationCallbackOwnerReplacementChangesToken(t *testing.T) {
	port := callbackTestPort(t)
	target := fmt.Sprintf("http://127.0.0.1:%d/callback", port)
	oldToken, newToken := strings.Repeat("o", 32), strings.Repeat("n", 32)
	var calls atomic.Int32
	d := &Daemon{}
	defer d.closeAuthorizationCallbacks()
	makeBinding := func(token string) authorizationCallbackBinding {
		return callbackTestBinding(t, "action", target, token, true, func(context.Context, string) error { calls.Add(1); return nil })
	}
	if _, err := d.replaceAuthorizationCallbacks("owner", []authorizationCallbackBinding{makeBinding(oldToken)}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.replaceAuthorizationCallbacks("owner", []authorizationCallbackBinding{makeBinding(newToken)}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		token string
		want  int
	}{{oldToken, http.StatusBadRequest}, {newToken, http.StatusOK}} {
		req := httptest.NewRequest(http.MethodGet, target+"?code=x&state="+url.QueryEscape(tc.token), nil)
		req.RemoteAddr = "127.0.0.1:4567"
		resp := httptest.NewRecorder()
		d.handleAuthorizationCallback(resp, req)
		if resp.Code != tc.want {
			t.Fatalf("token %q returned status %d, want %d", tc.token[:1], resp.Code, tc.want)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("invoker called %d times, want only for replacement token", calls.Load())
	}
}
