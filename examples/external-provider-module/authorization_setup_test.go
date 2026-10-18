package externalprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
	setupActionID         = "example-authorization-setup-auth"
	setupActionProviderID = "example-authorization-setup"
)

var (
	setupActionFormRef = provider.Ref{Kind: provider.SchemaKind, ID: "example.authorization.setup.form", ContractVersion: 1}
	setupActionOTPRef  = provider.Ref{Kind: provider.SchemaKind, ID: "example.authorization.setup.otp", ContractVersion: 1}
)

type setupActionModule struct{ pollCompleted chan<- struct{} }

func (setupActionModule) ModuleDescriptor() provider.ModuleRef {
	return provider.ModuleRef{ID: "example.authorization-setup", Version: "1.0.0", HostContractVersion: provider.RuntimeModuleHostContractVersion}
}
func (m setupActionModule) Register(runtime *provider.RuntimeRegistry) error {
	for _, schema := range []struct {
		ref provider.Ref
		raw json.RawMessage
	}{
		{setupActionFormRef, json.RawMessage(`{"type":"object","properties":{"manualCookie":{"type":"string","minLength":1},"proof":{"type":"string","minLength":1}},"required":["manualCookie","proof"],"additionalProperties":false}`)},
		{setupActionOTPRef, json.RawMessage(`{"type":"object","properties":{"otp":{"type":"string","pattern":"^[0-9]{6}$"}},"required":["otp"],"additionalProperties":false}`)},
	} {
		if err := runtime.RegisterSchemaDocument(schema.ref, schema.raw); err != nil {
			return err
		}
	}
	return runtime.RegisterAuthFactory(setupActionID, func(provider.AuthOptions) (provider.AuthFlow, error) {
		return setupActionFlow{pollCompleted: m.pollCompleted}, nil
	})
}

type setupActionPrivateState struct {
	Stage        string    `json:"stage"`
	ExpiresAt    time.Time `json:"expiresAt"`
	ManualCookie string    `json:"manualCookie,omitempty"`
	Proof        string    `json:"proof,omitempty"`
}
type setupActionFlow struct{ pollCompleted chan<- struct{} }

func (setupActionFlow) ID() string { return setupActionID }
func (setupActionFlow) SetupSchema() provider.SetupSchema {
	return opaqueStateAuth{}.SetupSchema()
}
func (setupActionFlow) Resolve(ctx context.Context, input provider.AuthInput) (provider.Credential, error) {
	return opaqueStateAuth{}.Resolve(ctx, input)
}
func (setupActionFlow) Refresh(ctx context.Context, input provider.AuthInput, current provider.Credential) (provider.CredentialState, error) {
	return opaqueStateAuth{}.Refresh(ctx, input, current)
}
func (setupActionFlow) BeginAuthorization(_ context.Context, _ provider.AuthInput) (provider.AuthorizationTransition, error) {
	expires := time.Now().Add(3 * time.Minute)
	return setupActionTransition(setupActionPrivateState{Stage: "form", ExpiresAt: expires}, []provider.AuthorizationAction{
		{ID: "visit", Kind: provider.AuthorizationOpenURL, Label: "Open identity page", URL: "https://identity.test/authorize"},
		{ID: "code", Kind: provider.AuthorizationDisplayCode, Label: "Reference code", Code: "TEST-SETUP-CODE"},
		{ID: "form", Kind: provider.AuthorizationInput, Label: "Paste account proof", InputSchemaRef: &setupActionFormRef},
	}, 0)
}
func (f setupActionFlow) AdvanceAuthorization(_ context.Context, opaque provider.AuthorizationState, event provider.AuthorizationEvent) (provider.AuthorizationTransition, error) {
	var state setupActionPrivateState
	if err := json.Unmarshal([]byte(opaque.Encoded()), &state); err != nil {
		return provider.AuthorizationTransition{}, fmt.Errorf("invalid private setup state")
	}
	if !state.ExpiresAt.After(time.Now()) {
		return provider.AuthorizationTransition{}, context.DeadlineExceeded
	}
	switch state.Stage {
	case "form":
		if event.Kind != provider.AuthorizationSubmit || event.ActionID != "form" {
			return provider.AuthorizationTransition{}, fmt.Errorf("unexpected form event")
		}
		var value struct {
			ManualCookie string `json:"manualCookie"`
			Proof        string `json:"proof"`
		}
		if err := json.Unmarshal(event.Value, &value); err != nil || value.ManualCookie == "" || value.Proof == "" {
			return provider.AuthorizationTransition{}, fmt.Errorf("invalid proof")
		}
		state.Stage, state.ManualCookie, state.Proof = "otp", value.ManualCookie, value.Proof
		return setupActionTransition(state, []provider.AuthorizationAction{{ID: "otp", Kind: provider.AuthorizationInput, Label: "Enter one time code", InputSchemaRef: &setupActionOTPRef}}, 0)
	case "otp":
		if event.Kind != provider.AuthorizationSubmit || event.ActionID != "otp" {
			return provider.AuthorizationTransition{}, fmt.Errorf("unexpected OTP event")
		}
		var value struct {
			OTP string `json:"otp"`
		}
		if json.Unmarshal(event.Value, &value) != nil || value.OTP != "123456" {
			return provider.AuthorizationTransition{}, fmt.Errorf("invalid OTP")
		}
		state.Stage = "poll"
		return setupActionTransition(state, nil, 150*time.Millisecond)
	case "poll":
		if event.Kind != provider.AuthorizationPoll {
			return provider.AuthorizationTransition{}, fmt.Errorf("poll required")
		}
		if state.ManualCookie != "cookie-from-browser" || state.Proof != "proof-from-browser" {
			return provider.AuthorizationTransition{}, fmt.Errorf("private proof state was lost")
		}
		encoded, err := json.Marshal(opaqueCredentialDocument{Ticket: "authorization-setup-ticket", Subject: "setup-subject", Workspace: "workspace-7", Metadata: map[string]string{"issued_by": "authorization-setup"}})
		if err != nil {
			return provider.AuthorizationTransition{}, err
		}
		credential, err := provider.NewCredentialState(string(encoded))
		if err != nil {
			return provider.AuthorizationTransition{}, err
		}
		select {
		case f.pollCompleted <- struct{}{}:
		default:
		}
		return provider.AuthorizationTransition{Credential: &credential}, nil
	default:
		return provider.AuthorizationTransition{}, fmt.Errorf("unknown authorization stage")
	}
}
func setupActionTransition(state setupActionPrivateState, actions []provider.AuthorizationAction, poll time.Duration) (provider.AuthorizationTransition, error) {
	encoded, err := json.Marshal(state)
	if err != nil {
		return provider.AuthorizationTransition{}, err
	}
	opaque, err := provider.NewAuthorizationState(string(encoded))
	if err != nil {
		return provider.AuthorizationTransition{}, err
	}
	return provider.AuthorizationTransition{State: opaque, Actions: actions, PollAfter: poll, ExpiresAt: state.ExpiresAt}, nil
}

func setupActionManifest(t *testing.T) provider.ProviderDefinition {
	t.Helper()
	definition := opaqueStateManifest(t)
	definition.ID = setupActionProviderID
	definition.DisplayName = "Separate module generic authorization setup"
	definition.AuthModes[0].Auth.ID = setupActionID
	return definition
}

func TestExternalGenericAuthorizationSetupPersistsAndActivatesCredential(t *testing.T) {
	root := t.TempDir()
	manifestDir := filepath.Join(root, "providers")
	if err := os.Mkdir(manifestDir, 0o700); err != nil {
		t.Fatal(err)
	}
	encoded, err := provider.EncodeManifest(setupActionManifest(t))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(manifestDir, "authorization-setup.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	var authorization atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization.Store(r.Header.Get("Authorization"))
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"fixture","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()
	config := daemon.Config{DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "daemon.sock"), ProviderManifestDir: manifestDir, HTTPEnabled: true, HTTPAddr: "127.0.0.1:0"}
	pollCompleted := make(chan struct{}, 1)
	module := setupActionModule{pollCompleted: pollCompleted}
	service := daemon.NewWithProviderModules(config, module)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = service.Stop(ctx)
	})
	if err = service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	rpc := func(method string, params map[string]any) (json.RawMessage, error) {
		t.Helper()
		response, e := daemon.CallIPC(context.Background(), config.IPCPath, daemon.IPCRequest{Method: method, Params: params})
		if e != nil {
			return nil, e
		}
		if !response.OK {
			return nil, fmt.Errorf("%s: %s", method, response.Error)
		}
		data, e := json.Marshal(response.Result)
		if e != nil {
			t.Fatal(e)
		}
		return data, nil
	}
	requireOK := func(method string, params map[string]any) json.RawMessage {
		t.Helper()
		raw, err := rpc(method, params)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	var node, connection struct {
		ID string `json:"ID"`
	}
	if err = json.Unmarshal(requireOK("providers.create", map[string]any{"name": "Setup fixture", "prefix": "setup", "baseUrl": upstream.URL + "/v1", "protocol": "openai_chat", "definitionID": setupActionProviderID}), &node); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(requireOK("connections.create", map[string]any{"providerNodeID": node.ID, "name": "Empty credential account", "credentialType": "opaque_ticket", "secret": ""}), &connection); err != nil {
		t.Fatal(err)
	}
	catalog := requireOK("providers.catalog", map[string]any{})
	var definitions []provider.DefinitionMetadata
	if err := json.Unmarshal(catalog, &definitions); err != nil {
		t.Fatal(err)
	}
	advertised := false
	for _, definition := range definitions {
		if definition.ID != setupActionProviderID {
			continue
		}
		for _, mode := range definition.AuthModes {
			advertised = advertised || mode.ActionAuthorization
		}
	}
	if !advertised {
		t.Fatal("provider catalog did not advertise action authorization")
	}
	cli := os.Getenv("GOBROOM_TEST_CLI")
	call := func(method string, params map[string]any, args ...string) json.RawMessage {
		t.Helper()
		if cli == "" {
			return requireOK(method, params)
		}
		return runDeviceRaceCLI(t, cli, config.IPCPath, args...)
	}
	var view daemon.AuthorizationSetupView
	start := call("auth.setup.start", map[string]any{"connectionID": connection.ID}, "connections", "setup", "start", "--connection-id", connection.ID)
	if err = json.Unmarshal(start, &view); err != nil || view.SessionID == "" {
		t.Fatalf("start response=%s err=%v", start, err)
	}
	if view.Status != "awaiting_user" || len(view.Actions) != 3 || !view.ExpiresAt.After(time.Now()) {
		t.Fatalf("initial view=%+v", view)
	}
	if strings.Contains(string(mustJSON(t, view)), "private") || strings.Contains(string(mustJSON(t, view)), "manualCookie") {
		t.Fatal("private provider state leaked into setup view")
	}
	if view.Revision == 0 {
		t.Fatal("initial setup revision was not returned")
	}
	if _, err := rpc("auth.setup.advance", map[string]any{"sessionID": view.SessionID, "actionID": "form", "revision": view.Revision - 1, "value": map[string]any{"manualCookie": "x", "proof": "y"}}); err == nil {
		t.Fatal("stale revision was accepted")
	}
	if _, err := rpc("auth.setup.advance", map[string]any{"sessionID": view.SessionID, "actionID": "form", "revision": view.Revision, "value": map[string]any{"manualCookie": "x", "proof": "y", "extra": "rejected"}}); err == nil {
		t.Fatal("schema-invalid input was accepted")
	}
	view = advanceSetupAction(t, call, cli, view, "form", map[string]any{"manualCookie": "cookie-from-browser", "proof": "proof-from-browser"})
	view = advanceSetupAction(t, call, cli, view, "otp", map[string]any{"otp": "123456"})
	if view.Status != "awaiting_user" || len(view.Actions) != 0 {
		t.Fatalf("poll transition=%+v", view)
	}
	// No status get, CLI command or frontend task drives this polling step.
	select {
	case <-pollCompleted:
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not poll without a frontend")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		raw := call("auth.setup.get", map[string]any{"sessionID": view.SessionID}, "connections", "setup", "get", "--session-id", view.SessionID)
		if err = json.Unmarshal(raw, &view); err != nil {
			t.Fatal(err)
		}
		if view.Status == "completed" && view.Activation != nil && view.Activation.Status == "active" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if view.Status != "completed" || view.Activation == nil || view.Activation.Status != "active" {
		t.Fatalf("daemon polling did not complete setup: %+v", view)
	}
	// A completed setup writes the credential to the connection store. Restart
	// the daemon before routing to prove the credential survives independently.
	if err = service.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	service = daemon.NewWithProviderModules(config, module)
	if err = service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		method string
		params map[string]any
	}{
		{"custom_models.upsert", map[string]any{"id": "route_setup_chat", "providerNodeID": node.ID, "kind": "custom", "externalID": "gpt-test", "displayName": "Setup chat", "profile": map[string]any{}, "connectionIds": []string{connection.ID}}},
		{"physical_models.upsert", map[string]any{"name": "setup-physical", "identity": map[string]any{"canonicalName": "gpt-test"}, "sources": []map[string]any{{"routeId": "route_setup_chat", "fidelity": "exact"}}, "enabled": true}},
		{"combo_models.upsert", map[string]any{"name": "setup-chat", "members": []map[string]any{{"kind": "physical", "id": "setup-physical"}}, "discoverable": true, "enabled": true}},
	} {
		requireOK(item.method, item.params)
	}
	chat := `{"model":"setup-chat","messages":[{"role":"user","content":"hello"}]}`
	response, e := http.Post("http://"+service.Addresses().HTTP+"/v1/chat/completions", "application/json", strings.NewReader(chat))
	if e != nil {
		t.Fatal(e)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != 200 || authorization.Load() != "Bearer authorization-setup-ticket" {
		t.Fatalf("persisted credential did not drive combo inference: status=%d", response.StatusCode)
	}
	var secondConnection struct {
		ID string `json:"ID"`
	}
	if err = json.Unmarshal(requireOK("connections.create", map[string]any{"providerNodeID": node.ID, "name": "Cancelled account", "credentialType": "opaque_ticket", "secret": ""}), &secondConnection); err != nil {
		t.Fatal(err)
	}
	var cancelled daemon.AuthorizationSetupView
	if err = json.Unmarshal(call("auth.setup.start", map[string]any{"connectionID": secondConnection.ID}, "connections", "setup", "start", "--connection-id", secondConnection.ID), &cancelled); err != nil || cancelled.SessionID == "" {
		t.Fatalf("cancel setup start: %v", err)
	}
	cancelResult := call("auth.setup.cancel", map[string]any{"sessionID": cancelled.SessionID}, "connections", "setup", "cancel", "--session-id", cancelled.SessionID)
	var cancelledView daemon.AuthorizationSetupView
	if err := json.Unmarshal(cancelResult, &cancelledView); err != nil || cancelledView.Status != "cancelled" {
		t.Fatalf("cancel failed: %s", cancelResult)
	}
	if _, err := rpc("auth.setup.advance", map[string]any{"sessionID": cancelled.SessionID, "actionID": "form", "revision": cancelled.Revision, "value": map[string]any{"manualCookie": "x", "proof": "y"}}); err == nil {
		t.Fatal("cancelled setup accepted another action")
	}
	if err = service.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func advanceSetupAction(t *testing.T, call func(string, map[string]any, ...string) json.RawMessage, cli string, view daemon.AuthorizationSetupView, action string, value map[string]any) daemon.AuthorizationSetupView {
	t.Helper()
	var raw json.RawMessage
	if cli != "" {
		path := filepath.Join(t.TempDir(), "setup-input.json")
		data, _ := json.Marshal(value)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		raw = call("auth.setup.advance", nil, "connections", "setup", "submit", "--session-id", view.SessionID, "--action-id", action, "--revision", fmt.Sprint(view.Revision), "--input-file", path)
	} else {
		raw = call("auth.setup.advance", map[string]any{"sessionID": view.SessionID, "actionID": action, "revision": view.Revision, "value": value})
	}
	var result daemon.AuthorizationSetupView
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
