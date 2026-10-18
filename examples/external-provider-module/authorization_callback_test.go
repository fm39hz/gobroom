package externalprovider

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/pkg/daemon"
	"github.com/fm39hz/gobroom/pkg/provider"
)

const (
	callbackProviderID = "example-external-callback"
	callbackAuthID     = "example-external-callback-auth"
	callbackActionID   = "callback-result"
)

var callbackInputSchemaRef = provider.Ref{Kind: provider.SchemaKind, ID: "example.external-callback.input", ContractVersion: 1}

type callbackFixtureModule struct {
	port        int
	correlation *atomic.Value
}

func (callbackFixtureModule) ModuleDescriptor() provider.ModuleRef {
	return provider.ModuleRef{ID: "example.external-callback", Version: "1.0.0", HostContractVersion: provider.RuntimeModuleHostContractVersion}
}

func (m callbackFixtureModule) Register(runtime *provider.RuntimeRegistry) error {
	if err := runtime.RegisterSchemaDocument(callbackInputSchemaRef, json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","minLength":1}},"required":["url"],"additionalProperties":false}`)); err != nil {
		return err
	}
	return runtime.RegisterAuthFactory(callbackAuthID, func(provider.AuthOptions) (provider.AuthFlow, error) {
		return callbackFixtureAuth{port: m.port, correlation: m.correlation}, nil
	})
}

// callbackFixtureAuth is deliberately an ordinary external AuthorizationFlow.
// It composes the opaque-state credential primitive and independently owns
// the meaning of its callback response fields.
type callbackFixtureAuth struct {
	port        int
	correlation *atomic.Value
}

type callbackFixtureState struct {
	Stage       string `json:"stage"`
	TargetURL   string `json:"targetUrl"`
	Correlation string `json:"correlation"`
}

func (callbackFixtureAuth) ID() string                        { return callbackAuthID }
func (callbackFixtureAuth) SetupSchema() provider.SetupSchema { return opaqueStateAuth{}.SetupSchema() }
func (callbackFixtureAuth) Resolve(ctx context.Context, input provider.AuthInput) (provider.Credential, error) {
	return opaqueStateAuth{}.Resolve(ctx, input)
}
func (callbackFixtureAuth) Refresh(ctx context.Context, input provider.AuthInput, current provider.Credential) (provider.CredentialState, error) {
	return opaqueStateAuth{}.Refresh(ctx, input, current)
}

func (a callbackFixtureAuth) BeginAuthorization(_ context.Context, _ provider.AuthInput) (provider.AuthorizationTransition, error) {
	if a.port < 1 {
		return provider.AuthorizationTransition{}, fmt.Errorf("callback test port is missing")
	}
	correlation, err := provider.NewAuthorizationState(rand.Text() + rand.Text())
	if err != nil {
		return provider.AuthorizationTransition{}, err
	}
	if a.correlation != nil {
		a.correlation.Store(correlation.Encoded())
	}
	target := fmt.Sprintf("http://127.0.0.1:%d/identity/return?tenant=external-module", a.port)
	encoded, err := json.Marshal(callbackFixtureState{Stage: "callback", TargetURL: target, Correlation: correlation.Encoded()})
	if err != nil {
		return provider.AuthorizationTransition{}, err
	}
	state, err := provider.NewAuthorizationState(string(encoded))
	if err != nil {
		return provider.AuthorizationTransition{}, err
	}
	return provider.AuthorizationTransition{
		State:   state,
		Actions: []provider.AuthorizationAction{{ID: callbackActionID, Kind: provider.AuthorizationInput, Label: "Receive account response", InputSchemaRef: &callbackInputSchemaRef}},
		Callbacks: []provider.AuthorizationCallback{{
			ActionID: callbackActionID, URL: target,
			CorrelationParameter: "ticket", CorrelationValue: correlation,
			ResponseParameters: []string{"proof", "deny"},
		}},
		ExpiresAt: time.Now().Add(2 * time.Minute),
	}, nil
}

func (callbackFixtureAuth) AdvanceAuthorization(_ context.Context, opaque provider.AuthorizationState, event provider.AuthorizationEvent) (provider.AuthorizationTransition, error) {
	var state callbackFixtureState
	if err := json.Unmarshal([]byte(opaque.Encoded()), &state); err != nil || state.Stage != "callback" {
		return provider.AuthorizationTransition{}, fmt.Errorf("invalid callback fixture state")
	}
	if event.Kind != provider.AuthorizationSubmit || event.ActionID != callbackActionID {
		return provider.AuthorizationTransition{}, fmt.Errorf("unexpected callback action")
	}
	var input provider.AuthorizationCallbackInput
	if err := json.Unmarshal(event.Value, &input); err != nil || input.URL == "" {
		return provider.AuthorizationTransition{}, fmt.Errorf("callback URL is required")
	}
	callbackURL, err := url.Parse(input.URL)
	if err != nil {
		return provider.AuthorizationTransition{}, fmt.Errorf("invalid callback URL")
	}
	query := callbackURL.Query()
	expected, err := url.Parse(state.TargetURL)
	if err != nil || callbackURL.Scheme != expected.Scheme || callbackURL.Host != expected.Host || callbackURL.EscapedPath() != expected.EscapedPath() || query.Get("ticket") != state.Correlation || query.Get("tenant") != "external-module" {
		return provider.AuthorizationTransition{}, fmt.Errorf("callback target or correlation is invalid")
	}
	if query.Get("deny") == "yes" {
		return provider.AuthorizationTransition{Failure: provider.AuthorizationDenied}, nil
	}
	if query.Get("proof") == "" {
		return provider.AuthorizationTransition{}, fmt.Errorf("callback response is incomplete")
	}
	encoded, err := json.Marshal(opaqueCredentialDocument{Ticket: query.Get("proof"), Subject: query.Get("proof"), Workspace: "workspace-7", Metadata: map[string]string{"issued_by": "callback-fixture"}})
	if err != nil {
		return provider.AuthorizationTransition{}, err
	}
	credential, err := provider.NewCredentialState(string(encoded))
	if err != nil {
		return provider.AuthorizationTransition{}, err
	}
	return provider.AuthorizationTransition{Credential: &credential}, nil
}

func callbackFixtureManifest(t *testing.T) provider.ProviderDefinition {
	t.Helper()
	definition := opaqueStateManifest(t)
	definition.ID = callbackProviderID
	definition.DisplayName = "External module callback fixture"
	definition.AuthModes[0].Auth.ID = callbackAuthID
	return definition
}

type callbackRPC struct {
	t      *testing.T
	config daemon.Config
}

func (r callbackRPC) call(method string, params map[string]any) json.RawMessage {
	r.t.Helper()
	if cli := os.Getenv("GOBROOM_TEST_CLI"); cli != "" {
		var input struct {
			ConnectionID string          `json:"connectionID"`
			SessionID    string          `json:"sessionID"`
			ActionID     string          `json:"actionID"`
			Revision     uint64          `json:"revision"`
			Value        json.RawMessage `json:"value"`
		}
		encoded, err := json.Marshal(params)
		if err != nil {
			r.t.Fatal(err)
		}
		if err := json.Unmarshal(encoded, &input); err != nil {
			r.t.Fatal(err)
		}
		var args []string
		switch method {
		case "auth.setup.start":
			args = []string{"connections", "setup", "start", "--connection-id", input.ConnectionID}
		case "auth.setup.get":
			args = []string{"connections", "setup", "get", "--session-id", input.SessionID}
		case "auth.setup.cancel":
			args = []string{"connections", "setup", "cancel", "--session-id", input.SessionID}
		case "auth.setup.advance":
			path := filepath.Join(r.t.TempDir(), "callback-input.json")
			if err := os.WriteFile(path, input.Value, 0o600); err != nil {
				r.t.Fatal(err)
			}
			args = []string{"connections", "setup", "submit", "--session-id", input.SessionID, "--action-id", input.ActionID, "--revision", fmt.Sprint(input.Revision), "--input-file", path}
		}
		if args != nil {
			return runDeviceRaceCLI(r.t, cli, r.config.IPCPath, args...)
		}
	}
	response, err := daemon.CallIPC(context.Background(), r.config.IPCPath, daemon.IPCRequest{Method: method, Params: params})
	if err != nil || !response.OK {
		r.t.Fatalf("%s: ok=%v err=%v", method, response.OK, err)
	}
	encoded, err := json.Marshal(response.Result)
	if err != nil {
		r.t.Fatal(err)
	}
	return encoded
}

func TestExternalModuleCallbackGrantsCredentialAndSurvivesRestart(t *testing.T) {
	root := t.TempDir()
	manifestDir := filepath.Join(root, "providers")
	if err := os.Mkdir(manifestDir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest, err := provider.EncodeManifest(callbackFixtureManifest(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "external-callback.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	upstream := httpServer(t)
	defer upstream.Close()
	portListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := portListener.Addr().(*net.TCPAddr).Port
	if err := portListener.Close(); err != nil {
		t.Fatal(err)
	}
	config := daemon.Config{DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "daemon.sock"), ProviderManifestDir: manifestDir, HTTPEnabled: true, HTTPAddr: "127.0.0.1:0"}
	var correlationValue atomic.Value
	module := callbackFixtureModule{port: port, correlation: &correlationValue}
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
	var node, connection struct{ ID string }
	if err := json.Unmarshal(rpc.call("providers.create", map[string]any{"name": "Callback fixture", "prefix": "callback", "baseUrl": upstream.URL + "/v1", "protocol": "openai_chat", "definitionID": callbackProviderID}), &node); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rpc.call("connections.create", map[string]any{"providerNodeID": node.ID, "name": "Callback account", "credentialType": "opaque_ticket", "secret": ""}), &connection); err != nil {
		t.Fatal(err)
	}
	view := decodeCallbackView(t, rpc.call("auth.setup.start", map[string]any{"connectionID": connection.ID}))
	if view.SessionID == "" || view.Status != "awaiting_user" || view.Revision == 0 || len(view.CallbackModes) != 1 || view.CallbackModes[0].ActionID != callbackActionID || string(view.CallbackModes[0].Mode) != "loopback" {
		t.Fatalf("callback setup view did not expose loopback mode: %+v", view)
	}
	callbackTarget := fmt.Sprintf("http://127.0.0.1:%d/identity/return", port)
	callbackValues := url.Values{}
	callbackValues.Set("tenant", "external-module")
	callbackValues.Set("ticket", correlationValue.Load().(string))
	callbackValues.Set("proof", "callback-runtime-ticket")
	requestURL, err := url.Parse(callbackTarget)
	if err != nil {
		t.Fatal(err)
	}
	requestURL.RawQuery = callbackValues.Encode()
	callbackClient := &http.Client{Timeout: 3 * time.Second}
	request := func(target *url.URL) int {
		t.Helper()
		response, requestErr := callbackClient.Get(target.String())
		if requestErr != nil {
			return 0
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, response.Body)
		return response.StatusCode
	}
	invalid := *requestURL
	invalidValues := invalid.Query()
	invalidValues.Set("tenant", "wrong")
	invalid.RawQuery = invalidValues.Encode()
	if status := request(&invalid); status == http.StatusOK {
		t.Fatal("callback with wrong static query was accepted")
	}
	invalidValues = requestURL.Query()
	invalidValues.Add("proof", "duplicate")
	invalid.RawQuery = invalidValues.Encode()
	if status := request(&invalid); status == http.StatusOK {
		t.Fatal("callback with duplicate response parameter was accepted")
	}
	invalidValues = requestURL.Query()
	invalidValues.Set("ticket", "wrong-correlation")
	invalid.RawQuery = invalidValues.Encode()
	if status := request(&invalid); status == http.StatusOK {
		t.Fatal("callback with wrong correlation value was accepted")
	}
	invalid = *requestURL
	invalid.Path = "/wrong/path"
	if status := request(&invalid); status == http.StatusOK {
		t.Fatal("callback on an unregistered path was accepted")
	}
	invalid = *requestURL
	invalid.Host = fmt.Sprintf("localhost:%d", port)
	if status := request(&invalid); status == http.StatusOK {
		t.Fatal("callback on an unregistered host was accepted")
	}
	invalidValues = requestURL.Query()
	invalidValues.Set("unexpected", "value")
	invalid = *requestURL
	invalid.RawQuery = invalidValues.Encode()
	if status := request(&invalid); status == http.StatusOK {
		t.Fatal("callback with an unknown parameter was accepted")
	}
	responseStatus := request(requestURL)
	if responseStatus != http.StatusOK {
		t.Fatalf("correlated callback status=%d", responseStatus)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		view = decodeCallbackView(t, rpc.call("auth.setup.get", map[string]any{"sessionID": view.SessionID}))
		if view.Status == "completed" && view.Activation != nil && view.Activation.Status == "active" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if view.Status != "completed" || view.Activation == nil || view.Activation.Status != "active" {
		t.Fatalf("callback did not complete setup: status=%s", view.Status)
	}
	stop()
	service = daemon.NewWithProviderModules(config, module)
	start()
	rpc.call("custom_models.upsert", map[string]any{"id": "callback-route", "providerNodeID": node.ID, "kind": "custom", "externalID": "gpt-callback", "displayName": "Callback model", "connectionIds": []string{connection.ID}})
	rpc.call("physical_models.upsert", map[string]any{"name": "callback-physical", "identity": map[string]any{"canonicalName": "gpt-callback"}, "sources": []map[string]any{{"routeId": "callback-route", "fidelity": "exact"}}, "enabled": true})
	rpc.call("combo_models.upsert", map[string]any{"name": "callback-role", "members": []map[string]any{{"kind": "physical", "id": "callback-physical"}}, "discoverable": true, "enabled": true})
	requestBody := strings.NewReader(`{"model":"callback-role","messages":[{"role":"user","content":"hello"}]}`)
	inference, err := http.Post("http://"+service.Addresses().HTTP+"/v1/chat/completions", "application/json", requestBody)
	if err != nil {
		t.Fatal(err)
	}
	_ = inference.Body.Close()
	if inference.StatusCode != http.StatusOK {
		t.Fatalf("inference after restart status=%d", inference.StatusCode)
	}
	view = decodeCallbackView(t, rpc.call("auth.setup.start", map[string]any{"connectionID": connection.ID}))
	rpc.call("auth.setup.cancel", map[string]any{"sessionID": view.SessionID})
	cancelledValues := url.Values{"tenant": {"external-module"}, "ticket": {correlationValue.Load().(string)}, "proof": {"late-callback"}}
	requestURL.RawQuery = cancelledValues.Encode()
	if status := request(requestURL); status == http.StatusOK {
		t.Fatal("callback after setup cancellation was accepted")
	}
	// Occupying the actual provider callback address forces explicit manual
	// mode; submitting the URL still uses the same input schema and driver.
	occupied, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	view = decodeCallbackView(t, rpc.call("auth.setup.start", map[string]any{"connectionID": connection.ID}))
	if len(view.CallbackModes) != 1 || view.CallbackModes[0].Mode != daemon.AuthorizationCallbackManual {
		t.Fatalf("busy callback port did not produce manual mode: %+v", view.CallbackModes)
	}
	manual := *requestURL
	manual.RawQuery = url.Values{"tenant": {"external-module"}, "ticket": {correlationValue.Load().(string)}, "proof": {"callback-runtime-ticket"}}.Encode()
	view = decodeCallbackView(t, rpc.call("auth.setup.advance", map[string]any{"sessionID": view.SessionID, "revision": view.Revision, "actionID": callbackActionID, "value": provider.AuthorizationCallbackInput{URL: manual.String()}}))
	if view.Status != "completed" || view.Activation == nil || !view.Activation.CredentialCommitted {
		t.Fatalf("manual callback did not commit credential: %+v", view)
	}
	_ = occupied.Close()
	view = decodeCallbackView(t, rpc.call("auth.setup.start", map[string]any{"connectionID": connection.ID}))
	denied := *requestURL
	denied.RawQuery = url.Values{"tenant": {"external-module"}, "ticket": {correlationValue.Load().(string)}, "deny": {"yes"}}.Encode()
	if status := request(&denied); status != http.StatusOK {
		t.Fatalf("typed denial callback status=%d", status)
	}
	view = decodeCallbackView(t, rpc.call("auth.setup.get", map[string]any{"sessionID": view.SessionID}))
	if view.Status != "denied" || view.Activation != nil {
		t.Fatalf("denial did not retain terminal state: %+v", view)
	}
}

func decodeCallbackView(t *testing.T, raw json.RawMessage) daemon.AuthorizationSetupView {
	t.Helper()
	var view daemon.AuthorizationSetupView
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatal(err)
	}
	return view
}

func httpServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected upstream path %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer callback-runtime-ticket" {
			t.Errorf("inference authorization=%q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"callback-fixture","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
}
