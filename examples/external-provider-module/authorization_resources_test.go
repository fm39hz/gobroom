package externalprovider

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/pkg/daemon"
	"github.com/fm39hz/gobroom/pkg/provider"
)

const (
	resourceProviderID = "example-assigned-callback"
	resourceAuthID     = "example-assigned-callback-auth"
	resourceActionID   = "account-callback"
)

var resourceInputRef = provider.Ref{Kind: provider.SchemaKind, ID: "example.assigned-callback.input", ContractVersion: 1}

type assignedCallbackModule struct{ issuer string }

func (assignedCallbackModule) ModuleDescriptor() provider.ModuleRef {
	return provider.ModuleRef{ID: "example.assigned-callback", Version: "1.0.0", HostContractVersion: provider.RuntimeModuleHostContractVersion}
}
func (m assignedCallbackModule) Register(r *provider.RuntimeRegistry) error {
	if err := r.RegisterSchemaDocument(resourceInputRef, json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","minLength":1}},"required":["url"],"additionalProperties":false}`)); err != nil {
		return err
	}
	return r.RegisterAuthFactory(resourceAuthID, func(provider.AuthOptions) (provider.AuthFlow, error) {
		return assignedCallbackAuth{issuer: m.issuer}, nil
	})
}

type assignedCallbackAuth struct{ issuer string }
type assignedCallbackState struct {
	Stage       string    `json:"stage"`
	URL         string    `json:"url"`
	Correlation string    `json:"correlation"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

func (assignedCallbackAuth) ID() string { return resourceAuthID }
func (assignedCallbackAuth) SetupSchema() provider.SetupSchema {
	return opaqueStateAuth{}.SetupSchema()
}
func (assignedCallbackAuth) Resolve(ctx context.Context, in provider.AuthInput) (provider.Credential, error) {
	return opaqueStateAuth{}.Resolve(ctx, in)
}
func (assignedCallbackAuth) Refresh(ctx context.Context, in provider.AuthInput, current provider.Credential) (provider.CredentialState, error) {
	return opaqueStateAuth{}.Refresh(ctx, in, current)
}
func (a assignedCallbackAuth) BeginAuthorization(_ context.Context, _ provider.AuthInput) (provider.AuthorizationTransition, error) {
	correlation, err := provider.NewAuthorizationState(rand.Text() + rand.Text())
	if err != nil {
		return provider.AuthorizationTransition{}, err
	}
	expires := time.Now().Add(3 * time.Minute)
	spec := provider.AuthorizationCallback{ActionID: resourceActionID, URL: "http://127.0.0.1:0/identity/return?tenant=assigned", CorrelationParameter: "ticket", CorrelationValue: correlation, ResponseParameters: []string{"proof"}, Required: true}
	return assignedCallbackTransition(assignedCallbackState{Stage: "waiting", URL: spec.URL, ExpiresAt: expires, Correlation: correlation.Encoded()}, []provider.AuthorizationAction{{ID: resourceActionID, Kind: provider.AuthorizationInput, Label: "Complete identity approval", InputSchemaRef: &resourceInputRef}}, []provider.AuthorizationCallback{spec}, expires)
}
func (a assignedCallbackAuth) AdvanceAuthorization(ctx context.Context, opaque provider.AuthorizationState, event provider.AuthorizationEvent) (provider.AuthorizationTransition, error) {
	var state assignedCallbackState
	if err := json.Unmarshal([]byte(opaque.Encoded()), &state); err != nil || !state.ExpiresAt.After(time.Now()) {
		return provider.AuthorizationTransition{}, fmt.Errorf("invalid private authorization state")
	}
	switch event.Kind {
	case provider.AuthorizationCallbacksReady:
		if state.Stage != "waiting" || len(event.Callbacks) != 1 || event.Callbacks[0].ActionID != resourceActionID || event.Callbacks[0].Mode != provider.AuthorizationCallbackLoopback {
			return provider.AuthorizationTransition{}, fmt.Errorf("daemon callback resource missing")
		}
		u, err := url.Parse(event.Callbacks[0].URL)
		if err != nil || u.Port() == "" || u.Port() == "0" || u.Path != "/identity/return" || u.Query().Get("tenant") != "assigned" {
			return provider.AuthorizationTransition{}, fmt.Errorf("invalid daemon callback URL")
		}
		// The issuer probes during registration. A 400 proves the daemon has
		// already bound the actual socket, while the grant is not active yet.
		registration := url.Values{"redirect_uri": {u.String()}, "ticket": {state.Correlation}}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.issuer+"/register", strings.NewReader(registration.Encode()))
		if err != nil {
			return provider.AuthorizationTransition{}, err
		}
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return provider.AuthorizationTransition{}, err
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return provider.AuthorizationTransition{}, fmt.Errorf("issuer registration status %d", response.StatusCode)
		}
		state.Stage = "registered"
		state.URL = u.String()
		correlation, err := provider.NewAuthorizationState(state.Correlation)
		if err != nil {
			return provider.AuthorizationTransition{}, err
		}
		spec := provider.AuthorizationCallback{ActionID: resourceActionID, CorrelationParameter: "ticket", CorrelationValue: correlation, ResponseParameters: []string{"proof"}, Required: true}
		spec.URL = u.String()
		consent, _ := url.Parse(a.issuer + "/consent")
		q := consent.Query()
		q.Set("redirect_uri", spec.URL)
		q.Set("ticket", state.Correlation)
		consent.RawQuery = q.Encode()
		return assignedCallbackTransition(state, []provider.AuthorizationAction{{ID: "open-consent", Kind: provider.AuthorizationOpenURL, Label: "Approve access", URL: consent.String()}, {ID: resourceActionID, Kind: provider.AuthorizationInput, Label: "Complete identity approval", InputSchemaRef: &resourceInputRef}}, []provider.AuthorizationCallback{spec}, state.ExpiresAt)
	case provider.AuthorizationSubmit:
		if state.Stage != "registered" || event.ActionID != resourceActionID {
			return provider.AuthorizationTransition{}, fmt.Errorf("unexpected callback submit")
		}
		var input provider.AuthorizationCallbackInput
		if err := json.Unmarshal(event.Value, &input); err != nil || input.URL == "" {
			return provider.AuthorizationTransition{}, fmt.Errorf("callback URL required")
		}
		got, err := url.Parse(input.URL)
		expected, expectedErr := url.Parse(state.URL)
		if err != nil || expectedErr != nil || got.Scheme != expected.Scheme || got.Host != expected.Host || got.EscapedPath() != expected.EscapedPath() || got.Query().Get("tenant") != "assigned" || got.Query().Get("ticket") != state.Correlation || got.Query().Get("proof") == "" {
			return provider.AuthorizationTransition{}, fmt.Errorf("callback target or response invalid")
		}
		encoded, err := json.Marshal(opaqueCredentialDocument{Ticket: got.Query().Get("proof"), Subject: "assigned-subject", Workspace: "workspace-7", Metadata: map[string]string{"issued_by": "assigned-callback"}})
		if err != nil {
			return provider.AuthorizationTransition{}, err
		}
		credential, err := provider.NewCredentialState(string(encoded))
		if err != nil {
			return provider.AuthorizationTransition{}, err
		}
		return provider.AuthorizationTransition{Credential: &credential}, nil
	default:
		return provider.AuthorizationTransition{}, fmt.Errorf("unexpected authorization event %q", event.Kind)
	}
}
func assignedCallbackTransition(state assignedCallbackState, actions []provider.AuthorizationAction, callbacks []provider.AuthorizationCallback, expires time.Time) (provider.AuthorizationTransition, error) {
	b, err := json.Marshal(state)
	if err != nil {
		return provider.AuthorizationTransition{}, err
	}
	opaque, err := provider.NewAuthorizationState(string(b))
	if err != nil {
		return provider.AuthorizationTransition{}, err
	}
	return provider.AuthorizationTransition{State: opaque, Actions: actions, Callbacks: callbacks, ExpiresAt: expires}, nil
}

func assignedCallbackManifest(t *testing.T) provider.ProviderDefinition {
	t.Helper()
	d := opaqueStateManifest(t)
	d.ID = resourceProviderID
	d.DisplayName = "Daemon assigned callback fixture"
	d.AuthModes[0].Auth.ID = resourceAuthID
	return d
}

// This E2E deliberately uses only public daemon/provider contracts. The
// daemon allocates and holds each callback socket before notifying the driver.
func TestExternalModuleDaemonAssignedCallbackResources(t *testing.T) {
	root := t.TempDir()
	manifests := filepath.Join(root, "providers")
	if err := os.Mkdir(manifests, 0o700); err != nil {
		t.Fatal(err)
	}
	encoded, err := provider.EncodeManifest(assignedCallbackManifest(t))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(manifests, "assigned-callback.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	type registration struct {
		mu            sync.Mutex
		redirects     []string
		earlyStatuses []int
	}
	var observed registration
	firstEntered, allowFirst := make(chan struct{}), make(chan struct{})
	var firstOnce, releaseOnce sync.Once
	releaseFirst := func() { releaseOnce.Do(func() { close(allowFirst) }) }
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/register" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		_ = r.ParseForm()
		redirect := r.Form.Get("redirect_uri")
		observed.mu.Lock()
		observed.redirects = append(observed.redirects, redirect)
		observed.mu.Unlock()
		probeURL, err := url.Parse(redirect)
		if err != nil {
			http.Error(w, "invalid redirect", 500)
			return
		}
		query := probeURL.Query()
		query.Set("ticket", r.Form.Get("ticket"))
		query.Set("proof", "premature-proof")
		probeURL.RawQuery = query.Encode()
		client := &http.Client{Timeout: 2 * time.Second}
		probe, err := client.Get(probeURL.String())
		if err != nil {
			t.Errorf("probe allocated callback: %v", err)
			http.Error(w, "probe failed", 500)
			return
		}
		_, _ = io.Copy(io.Discard, probe.Body)
		_ = probe.Body.Close()
		observed.mu.Lock()
		observed.earlyStatuses = append(observed.earlyStatuses, probe.StatusCode)
		observed.mu.Unlock()
		if probe.StatusCode != http.StatusBadRequest {
			http.Error(w, "listener was not held before grant", 500)
			return
		}
		first := false
		firstOnce.Do(func() { first = true; close(firstEntered) })
		if first {
			select {
			case <-allowFirst:
			case <-r.Context().Done():
				return
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer issuer.Close()
	defer releaseFirst()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "path", 404)
			return
		}
		if r.Header.Get("Authorization") != "Bearer assigned-runtime-ticket" {
			http.Error(w, "credential", 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"assigned","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	config := daemon.Config{DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "daemon.sock"), ProviderManifestDir: manifests, HTTPEnabled: true, HTTPAddr: "127.0.0.1:0"}
	module := assignedCallbackModule{issuer: issuer.URL}
	service := daemon.NewWithProviderModules(config, module)
	start := func() {
		t.Helper()
		if err := service.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	stop := func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := service.Stop(ctx); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = service.Stop(ctx)
	})
	start()
	rpc := callbackRPC{t: t, config: config}
	var node struct {
		ID string `json:"ID"`
	}
	if err = json.Unmarshal(rpc.call("providers.create", map[string]any{"name": "Assigned", "prefix": "assigned", "baseUrl": upstream.URL + "/v1", "protocol": "openai_chat", "definitionID": resourceProviderID}), &node); err != nil {
		t.Fatal(err)
	}
	createConnection := func(name string) string {
		t.Helper()
		var c struct {
			ID string `json:"ID"`
		}
		if e := json.Unmarshal(rpc.call("connections.create", map[string]any{"providerNodeID": node.ID, "name": name, "credentialType": "opaque_ticket", "secret": ""}), &c); e != nil {
			t.Fatal(e)
		}
		return c.ID
	}
	first, second := createConnection("first account"), createConnection("second account")
	type startResult struct {
		view daemon.AuthorizationSetupView
		err  error
	}
	firstResult := make(chan startResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		response, err := daemon.CallIPC(ctx, config.IPCPath, daemon.IPCRequest{Method: "auth.setup.start", Params: map[string]any{"connectionID": first}})
		if err != nil {
			firstResult <- startResult{err: err}
			return
		}
		encoded, err := json.Marshal(response.Result)
		if err != nil {
			firstResult <- startResult{err: err}
			return
		}
		var view daemon.AuthorizationSetupView
		err = json.Unmarshal(encoded, &view)
		firstResult <- startResult{view: view, err: err}
	}()
	select {
	case <-firstEntered:
	case result := <-firstResult:
		t.Fatalf("first setup returned before registration barrier: %v", result.err)
	case <-time.After(5 * time.Second):
		t.Fatal("registration barrier not reached")
	}
	// The second setup and control plane must remain responsive while the
	// first driver's issuer exchange is blocked.
	view2 := decodeCallbackView(t, rpc.call("auth.setup.start", map[string]any{"connectionID": second}))
	_ = rpc.call("status", nil)
	releaseFirst()
	firstDone := <-firstResult
	if firstDone.err != nil {
		t.Fatal(firstDone.err)
	}
	view1 := firstDone.view
	if view1.Revision != 1 || view2.Revision != 1 || len(view1.CallbackModes) != 1 || len(view2.CallbackModes) != 1 || view1.CallbackModes[0].Mode != daemon.AuthorizationCallbackLoopback || view2.CallbackModes[0].Mode != daemon.AuthorizationCallbackLoopback {
		t.Fatalf("initial public views: %+v %+v", view1, view2)
	}
	getConsentRedirect := func(v daemon.AuthorizationSetupView) *url.URL {
		t.Helper()
		for _, a := range v.Actions {
			if a.Kind == provider.AuthorizationOpenURL {
				consent, e := url.Parse(a.URL)
				if e != nil {
					t.Fatal(e)
				}
				redirect, e := url.Parse(consent.Query().Get("redirect_uri"))
				if e != nil {
					t.Fatal(e)
				}
				return redirect
			}
		}
		t.Fatalf("consent action missing: %+v", v)
		return nil
	}
	r1, r2 := getConsentRedirect(view1), getConsentRedirect(view2)
	if r1.Port() == "" || r1.Port() == "0" || r2.Port() == "" || r2.Port() == "0" || r1.Port() == r2.Port() || r1.Hostname() != "127.0.0.1" || r2.Hostname() != "127.0.0.1" || r1.Path != "/identity/return" || r2.Path != "/identity/return" || r1.Query().Get("tenant") != "assigned" || r2.Query().Get("tenant") != "assigned" {
		t.Fatalf("bad or shared callback URLs: %s %s", r1, r2)
	}
	observed.mu.Lock()
	if len(observed.redirects) != 2 || len(observed.earlyStatuses) != 2 || observed.earlyStatuses[0] != http.StatusBadRequest || observed.earlyStatuses[1] != http.StatusBadRequest {
		t.Fatalf("issuer registration probes: redirects=%v statuses=%v", observed.redirects, observed.earlyStatuses)
	}
	observed.mu.Unlock()
	consentTicket := func(view daemon.AuthorizationSetupView) string {
		t.Helper()
		for _, action := range view.Actions {
			if action.Kind == provider.AuthorizationOpenURL {
				parsed, err := url.Parse(action.URL)
				if err != nil {
					t.Fatal(err)
				}
				return parsed.Query().Get("ticket")
			}
		}
		t.Fatal("consent action missing")
		return ""
	}
	tickets := map[string]string{r1.String(): consentTicket(view1), r2.String(): consentTicket(view2)}
	makeCallback := func(target *url.URL, proof string) *url.URL {
		u := *target
		q := u.Query()
		q.Set("ticket", tickets[target.String()])
		q.Set("proof", proof)
		u.RawQuery = q.Encode()
		return &u
	}
	client := &http.Client{Timeout: 3 * time.Second}
	request := func(u *url.URL) int {
		t.Helper()
		resp, e := client.Get(u.String())
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}
	if status := request(makeCallback(r1, "assigned-runtime-ticket")); status != http.StatusOK {
		t.Fatalf("first callback status %d", status)
	}
	if status := request(makeCallback(r2, "assigned-runtime-ticket")); status != http.StatusOK {
		t.Fatalf("second callback listener closed by first grant: status %d", status)
	}
	waitComplete := func(v daemon.AuthorizationSetupView) daemon.AuthorizationSetupView {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			v = decodeCallbackView(t, rpc.call("auth.setup.get", map[string]any{"sessionID": v.SessionID}))
			if v.Status == "completed" && v.Activation != nil && v.Activation.Status == "active" {
				return v
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("authorization did not activate: %+v", v)
		return v
	}
	view1 = waitComplete(view1)
	view2 = waitComplete(view2)
	stop()
	service = daemon.NewWithProviderModules(config, module)
	start()
	rpc.call("custom_models.upsert", map[string]any{"id": "assigned-route", "providerNodeID": node.ID, "kind": "custom", "externalID": "assigned-model", "displayName": "Assigned model", "connectionIds": []string{first}})
	rpc.call("physical_models.upsert", map[string]any{"name": "assigned-physical", "identity": map[string]any{"canonicalName": "assigned-model"}, "sources": []map[string]any{{"routeId": "assigned-route", "fidelity": "exact"}}, "discoverable": true, "enabled": true})
	rpc.call("combo_models.upsert", map[string]any{"name": "assigned-combo", "members": []map[string]any{{"kind": "physical", "id": "assigned-physical"}}, "discoverable": true, "enabled": true})
	for _, model := range []string{"assigned-physical", "assigned-combo"} {
		body, err := json.Marshal(struct {
			Model    string              `json:"model"`
			Messages []map[string]string `json:"messages"`
		}{Model: model, Messages: []map[string]string{{"role": "user", "content": "hello"}}})
		if err != nil {
			t.Fatal(err)
		}
		resp, requestErr := http.Post("http://"+service.Addresses().HTTP+"/v1/chat/completions", "application/json", strings.NewReader(string(body)))
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("post-restart %s inference=%d", model, resp.StatusCode)
		}
	}
	// A fresh pending grant must release its allocated port on cancellation.
	pending := decodeCallbackView(t, rpc.call("auth.setup.start", map[string]any{"connectionID": first}))
	pendingRedirect := getConsentRedirect(pending)
	rpc.call("auth.setup.cancel", map[string]any{"sessionID": pending.SessionID})
	probe, err := http.Get(pendingRedirect.String())
	if err == nil {
		_ = probe.Body.Close()
		if probe.StatusCode == http.StatusOK {
			t.Fatal("cancelled callback remained active")
		}
	}
}
