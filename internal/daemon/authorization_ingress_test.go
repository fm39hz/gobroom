package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/provider"
)

func proxiedCallbackSpec(t *testing.T, path string, required bool) provider.AuthorizationCallback {
	t.Helper()
	state, err := provider.NewAuthorizationState(strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	return provider.AuthorizationCallback{
		ActionID: "continue", URL: "https://auth.example" + path + "?tenant=static",
		CorrelationParameter: "state", CorrelationValue: state,
		ResponseParameters: []string{"code"}, Required: required,
	}
}

func proxiedIngressRequest(d *Daemon, method, host, target, remote string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.Host = host
	req.RemoteAddr = remote
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	response := httptest.NewRecorder()
	d.handleAuthorizationIngress(response, req)
	return response
}

func TestProxiedHTTPSCallbackActivatesOnlyAfterReadyAndPublishesMode(t *testing.T) {
	spec := proxiedCallbackSpec(t, "/oauth/return", true)
	var calls atomic.Int32
	var d *Daemon
	var id string
	flow := &genericSetupFlow{begin: genericSetupTransition(t, `{"private":"ingress-state"}`, genericSetupAction())}
	flow.begin.Callbacks = []provider.AuthorizationCallback{spec}
	flow.begin.PrepareCallbacks = true
	flow.advance = func(_ context.Context, state provider.AuthorizationState, event provider.AuthorizationEvent) (provider.AuthorizationTransition, error) {
		if event.Kind == provider.AuthorizationCallbacksReady {
			if len(event.Callbacks) != 1 || event.Callbacks[0].Mode != provider.AuthorizationCallbackProxiedHTTPS || event.Callbacks[0].URL != spec.URL {
				t.Errorf("unexpected callback-ready resources: %+v", event.Callbacks)
			}
			q := url.Values{"tenant": {"static"}, "state": {spec.CorrelationValue.Encoded()}, "code": {"early"}}
			r := proxiedIngressRequest(d, http.MethodGet, "auth.example", "/oauth/return?"+q.Encode(), "127.0.0.1:1234", nil)
			if r.Code == http.StatusOK {
				t.Error("callback was accepted before activation")
			}
			next := flow.begin
			next.PrepareCallbacks = false
			return next, nil
		}
		if event.Kind != provider.AuthorizationSubmit || state.Encoded() != `{"private":"ingress-state"}` {
			t.Errorf("unexpected callback event/state: %s %s", event.Kind, state)
		}
		calls.Add(1)
		credential, err := provider.NewCredentialState("proxied-callback-credential")
		return provider.AuthorizationTransition{Credential: &credential}, err
	}
	d, id = genericSetupDaemon(t, flow)
	callbackInputCatalog(t, d)
	// The request is exercised inside the ready callback above, before transition activation.
	server := httptest.NewServer(http.HandlerFunc(d.handleAuthorizationIngress))
	d.callbackIngress = &authorizationCallbackIngress{origin: mustIngressURL(t, "https://auth.example"), address: server.Listener.Addr().String()}
	t.Cleanup(server.Close)
	t.Cleanup(func() { d.cancelAuthorizationSessions(); d.authWG.Wait(); d.closeAuthorizationCallbacks() })
	view, err := d.startSetupAuthorization(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.CallbackModes) != 1 || view.CallbackModes[0].Mode != provider.AuthorizationCallbackProxiedHTTPS {
		t.Fatalf("callback mode not exposed: %+v", view.CallbackModes)
	}
	encoded, _ := json.Marshal(view)
	if strings.Contains(string(encoded), "auth.example") || strings.Contains(string(encoded), "tenant") || strings.Contains(string(encoded), "state") {
		t.Fatalf("public setup view leaked callback URL or private query: %s", encoded)
	}
	q := url.Values{"tenant": {"static"}, "state": {spec.CorrelationValue.Encoded()}, "code": {"accepted"}}
	accepted := proxiedIngressRequest(d, http.MethodGet, "auth.example", "/oauth/return?"+q.Encode(), "127.0.0.1:1234", nil)
	if accepted.Code != http.StatusOK || calls.Load() != 1 {
		t.Fatalf("valid callback status=%d calls=%d", accepted.Code, calls.Load())
	}
	replay := proxiedIngressRequest(d, http.MethodGet, "auth.example", "/oauth/return?"+q.Encode(), "127.0.0.1:1234", nil)
	if replay.Code == http.StatusOK || calls.Load() != 1 {
		t.Fatalf("callback replay status=%d calls=%d", replay.Code, calls.Load())
	}
}

func callbackInputCatalog(t *testing.T, d *Daemon) {
	t.Helper()
	catalog := extensions.NewCatalog()
	document, err := extensions.BindSchemaDocument(genericSetupSchemaRef, json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","minLength":1}},"required":["url"],"additionalProperties":false}`))
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

func mustIngressURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestProxiedHTTPSIngressRejectsOriginAndRequestMismatches(t *testing.T) {
	spec := proxiedCallbackSpec(t, "/oauth/return", true)
	var calls atomic.Int32
	d := &Daemon{callbackIngress: &authorizationCallbackIngress{origin: mustIngressURL(t, "https://auth.example"), address: "127.0.0.1:1"}}
	defer d.closeAuthorizationCallbacks()
	if _, err := d.replaceAuthorizationCallbacks("owner", []authorizationCallbackBinding{{spec: spec, invoke: func(context.Context, string) error { calls.Add(1); return nil }}}); err != nil {
		t.Fatal(err)
	}
	validQuery := "tenant=static&state=" + spec.CorrelationValue.Encoded() + "&code=ok"
	for _, tc := range []struct {
		name, method, host, target, remote string
		headers                            map[string]string
		status                             int
	}{
		{"wrong host", "GET", "evil.example", "/oauth/return?" + validQuery, "127.0.0.1:1", nil, http.StatusBadRequest},
		{"wrong path", "GET", "auth.example", "/other?" + validQuery, "127.0.0.1:1", nil, http.StatusBadRequest},
		{"escaped path", "GET", "auth.example", "/oauth/%72eturn?" + validQuery, "127.0.0.1:1", nil, http.StatusBadRequest},
		{"static query mismatch", "GET", "auth.example", "/oauth/return?tenant=other&state=" + spec.CorrelationValue.Encoded() + "&code=ok", "127.0.0.1:1", nil, http.StatusBadRequest},
		{"nonce mismatch", "GET", "auth.example", "/oauth/return?tenant=static&state=" + strings.Repeat("x", 32) + "&code=ok", "127.0.0.1:1", nil, http.StatusBadRequest},
		{"duplicate query", "GET", "auth.example", "/oauth/return?" + validQuery + "&code=other", "127.0.0.1:1", nil, http.StatusBadRequest},
		{"unexpected response key", "GET", "auth.example", "/oauth/return?" + validQuery + "&extra=x", "127.0.0.1:1", nil, http.StatusBadRequest},
		{"wrong method", "POST", "auth.example", "/oauth/return?" + validQuery, "127.0.0.1:1", nil, http.StatusMethodNotAllowed},
		{"nonloopback remote", "GET", "auth.example", "/oauth/return?" + validQuery, "203.0.113.4:1", nil, http.StatusForbidden},
		{"forwarded wrong host", "GET", "evil.example", "/oauth/return?" + validQuery, "127.0.0.1:1", map[string]string{"Forwarded": "host=auth.example;proto=https", "X-Forwarded-Host": "auth.example", "X-Forwarded-Proto": "https"}, http.StatusBadRequest},
		{"forwarded cannot grant origin", "GET", "auth.example", "/oauth/return?" + validQuery, "203.0.113.4:1", map[string]string{"X-Forwarded-For": "127.0.0.1"}, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := calls.Load()
			response := proxiedIngressRequest(d, tc.method, tc.host, tc.target, tc.remote, tc.headers)
			if response.Code != tc.status || calls.Load() != before {
				t.Fatalf("status=%d want=%d calls=%d want unchanged from %d", response.Code, tc.status, calls.Load(), before)
			}
		})
	}
}

func TestProxiedHTTPSOptionalCallbacksWithoutMatchingOriginAreManual(t *testing.T) {
	for _, required := range []bool{false, true} {
		t.Run(map[bool]string{false: "optional", true: "required"}[required], func(t *testing.T) {
			spec := proxiedCallbackSpec(t, "/return", required)
			flow := &genericSetupFlow{begin: genericSetupTransition(t, `{"private":"state"}`, genericSetupAction())}
			flow.begin.Callbacks = []provider.AuthorizationCallback{spec}
			d, id := genericSetupDaemon(t, flow)
			t.Cleanup(func() { d.cancelAuthorizationSessions(); d.authWG.Wait(); d.closeAuthorizationCallbacks() })
			view, err := d.startSetupAuthorization(context.Background(), id)
			if required {
				if err == nil {
					t.Fatal("required HTTPS callback without configured ingress did not fail closed")
				}
				if listeners, reservations := callbackReservationSnapshot(d); listeners != 0 || reservations != 0 {
					t.Fatalf("required failure left listeners=%d reservations=%d", listeners, reservations)
				}
				return
			}
			if err != nil || len(view.CallbackModes) != 1 || view.CallbackModes[0].Mode != provider.AuthorizationCallbackManual {
				t.Fatalf("optional callback should be manual: view=%+v err=%v", view, err)
			}
			if listeners, reservations := callbackReservationSnapshot(d); listeners != 0 || reservations != 0 {
				t.Fatalf("manual mode opened callback resources: listeners=%d reservations=%d", listeners, reservations)
			}
		})
	}
}

func TestProxiedHTTPSCallbacksIsolateOwnersAndCorrelationValues(t *testing.T) {
	d := &Daemon{callbackIngress: &authorizationCallbackIngress{origin: mustIngressURL(t, "https://auth.example"), address: "127.0.0.1:1"}}
	defer d.closeAuthorizationCallbacks()
	var first, second atomic.Int32
	firstSpec := proxiedCallbackSpec(t, "/shared", true)
	secondSpec := firstSpec
	secondState, err := provider.NewAuthorizationState(strings.Repeat("t", 32))
	if err != nil {
		t.Fatal(err)
	}
	secondSpec.CorrelationValue = secondState
	for _, entry := range []struct {
		owner string
		spec  provider.AuthorizationCallback
		count *atomic.Int32
	}{{"owner-one", firstSpec, &first}, {"owner-two", secondSpec, &second}} {
		if _, err := d.replaceAuthorizationCallbacks(entry.owner, []authorizationCallbackBinding{{spec: entry.spec, invoke: func(context.Context, string) error { entry.count.Add(1); return nil }}}); err != nil {
			t.Fatalf("register %s: %v", entry.owner, err)
		}
	}
	request := func(token provider.AuthorizationState) *httptest.ResponseRecorder {
		query := url.Values{"tenant": {"static"}, "state": {token.Encoded()}, "code": {"ok"}}
		return proxiedIngressRequest(d, http.MethodGet, "auth.example", "/shared?"+query.Encode(), "127.0.0.1:1", nil)
	}
	if response := request(firstSpec.CorrelationValue); response.Code != http.StatusOK || first.Load() != 1 || second.Load() != 0 {
		t.Fatalf("first owner's callback status=%d first=%d second=%d", response.Code, first.Load(), second.Load())
	}
	if response := request(secondSpec.CorrelationValue); response.Code != http.StatusOK || first.Load() != 1 || second.Load() != 1 {
		t.Fatalf("second owner's callback status=%d first=%d second=%d", response.Code, first.Load(), second.Load())
	}
}

func TestProxiedHTTPSOriginCaseAndDefaultPortPreserveRegisteredTarget(t *testing.T) {
	spec := proxiedCallbackSpec(t, "/oauth/return", true)
	spec.URL = "https://AUTH.EXAMPLE:443/oauth/return?tenant=static"
	d := &Daemon{callbackIngress: &authorizationCallbackIngress{origin: mustIngressURL(t, "https://auth.example"), address: "127.0.0.1:1"}}
	defer d.closeAuthorizationCallbacks()
	var delivered string
	if _, err := d.replaceAuthorizationCallbacks("owner", []authorizationCallbackBinding{{spec: spec, invoke: func(_ context.Context, raw string) error { delivered = raw; return nil }}}); err != nil {
		t.Fatal(err)
	}
	query := url.Values{"tenant": {"static"}, "state": {spec.CorrelationValue.Encoded()}, "code": {"accepted"}}
	response := proxiedIngressRequest(d, http.MethodGet, "auth.example", "/oauth/return?"+query.Encode(), "127.0.0.1:1", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("equivalent origin rejected: %d", response.Code)
	}
	parsed, err := url.Parse(delivered)
	if err != nil || parsed.Host != "AUTH.EXAMPLE:443" || parsed.Scheme != "https" {
		t.Fatalf("driver target changed: %q %v", delivered, err)
	}
	if sameCallbackOrigin(mustIngressURL(t, "https://auth.example:444"), d.callbackIngress.origin) {
		t.Fatal("different port was treated as same origin")
	}
}
