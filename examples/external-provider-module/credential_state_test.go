package externalprovider

import (
	"context"
	"encoding/json"
	"fmt"
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
	opaqueStateProviderID = "example-opaque-state"
	opaqueStateAuthID     = "example-opaque-state-auth"
)

type opaqueStateModule struct{}

func (opaqueStateModule) ModuleDescriptor() provider.ModuleRef {
	return provider.ModuleRef{ID: "example.opaque-state", Version: "1.0.0", HostContractVersion: provider.RuntimeModuleHostContractVersion}
}
func (opaqueStateModule) Register(runtime *provider.RuntimeRegistry) error {
	return runtime.RegisterAuthFactory(opaqueStateAuthID, func(provider.AuthOptions) (provider.AuthFlow, error) {
		return opaqueStateAuth{}, nil
	})
}

type opaqueStateAuth struct{}
type opaqueCredentialDocument struct {
	Ticket    string            `json:"ticket"`
	Subject   string            `json:"subject"`
	Workspace string            `json:"workspace"`
	Metadata  map[string]string `json:"metadata"`
}

func (opaqueStateAuth) ID() string { return opaqueStateAuthID }
func (opaqueStateAuth) SetupSchema() provider.SetupSchema {
	return provider.SetupSchema{Fields: []provider.SetupField{{Name: "credential", Type: "string", Required: true, Secret: true}}}
}
func (opaqueStateAuth) Resolve(_ context.Context, input provider.AuthInput) (provider.Credential, error) {
	var state opaqueCredentialDocument
	if err := json.Unmarshal([]byte(input.Secret), &state); err != nil || state.Ticket == "" || state.Subject == "" {
		return provider.Credential{}, fmt.Errorf("opaque fixture credential is incomplete")
	}
	if state.Ticket == "refreshed-runtime-ticket" && (state.Workspace != "workspace-7" || state.Metadata["issued_by"] != "device-fixture" || state.Metadata["refresh_marker"] != "persisted-by-refresh") {
		return provider.Credential{}, fmt.Errorf("refreshed opaque fixture metadata did not survive persistence")
	}
	return provider.Credential{ConnectionID: input.ConnectionID, Type: input.Type, Secret: state.Ticket,
		AuthContext: provider.AuthContext{"subject": state.Subject, "workspace": state.Workspace, "refreshMarker": state.Metadata["refresh_marker"]}}, nil
}
func (opaqueStateAuth) Refresh(_ context.Context, input provider.AuthInput, _ provider.Credential) (provider.CredentialState, error) {
	var state opaqueCredentialDocument
	if err := json.Unmarshal([]byte(input.Secret), &state); err != nil {
		return provider.CredentialState{}, err
	}
	state.Ticket = "refreshed-runtime-ticket"
	state.Subject = "refreshed-subject"
	state.Metadata["refresh_marker"] = "persisted-by-refresh"
	encoded, err := json.Marshal(state)
	if err != nil {
		return provider.CredentialState{}, err
	}
	return provider.NewCredentialState(string(encoded))
}
func (opaqueStateAuth) CanAuthorizeCode() bool   { return false }
func (opaqueStateAuth) CanAuthorizeDevice() bool { return true }
func (opaqueStateAuth) BeginDeviceAuthorization(context.Context) (provider.DeviceAuthorization, error) {
	return provider.DeviceAuthorization{DeviceCode: "private-opaque-device-code", UserCode: "OPAQUE-CODE", VerificationURL: "https://identity.test/device", ExpiresAt: time.Now().Add(time.Minute)}, nil
}
func (opaqueStateAuth) WaitForDeviceAuthorization(ctx context.Context, _ provider.DeviceAuthorization) (provider.CredentialState, error) {
	select {
	case <-ctx.Done():
		return provider.CredentialState{}, ctx.Err()
	default:
	}
	encoded := `{"ticket":"device-runtime-ticket","subject":"subject-opaque","workspace":"workspace-7","metadata":{"issued_by":"device-fixture"}}`
	return provider.NewCredentialState(encoded)
}

func TestExternalCredentialStateFormattingDoesNotExposePayload(t *testing.T) {
	const private = `{"ticket":"private-format-ticket","subject":"private-format-subject"}`
	state, err := provider.NewCredentialState(private)
	if err != nil {
		t.Fatal(err)
	}
	formatted := fmt.Sprintf("%v", state)
	jsonState, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{formatted, string(jsonState)} {
		if strings.Contains(output, "private-format-ticket") || strings.Contains(output, "private-format-subject") || strings.Contains(output, private) {
			t.Fatalf("credential state formatting exposed its payload: %q", output)
		}
	}
	if _, err := provider.NewCredentialState(strings.Repeat("x", provider.MaxCredentialStateBytes+1)); err == nil {
		t.Fatal("credential state larger than the public limit was accepted")
	}
}

func opaqueStateManifest(t *testing.T) provider.ProviderDefinition {
	t.Helper()
	runtime, err := provider.NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := runtime.Primitives.Definition(provider.ProviderDefinitionRef("openai-compatible-chat", 1))
	if !ok {
		t.Fatal("built-in OpenAI-compatible chat definition is missing")
	}
	definition.ID = opaqueStateProviderID
	definition.DisplayName = "Opaque credential state fixture"
	definition.DefaultCredentialType = "opaque_ticket"
	definition.AuthModes = []provider.AuthModeDefinition{{CredentialType: "opaque_ticket",
		Auth:         provider.PrimitiveRef{Kind: provider.PrimitiveAuth, ID: opaqueStateAuthID, ContractVersion: 1},
		Presentation: provider.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "}}}
	return definition
}

func TestExternalOpaqueCredentialStateDeviceSetupPersistsAndStaysPrivate(t *testing.T) {
	root := t.TempDir()
	manifestDir := filepath.Join(root, "providers")
	if err := os.Mkdir(manifestDir, 0o700); err != nil {
		t.Fatal(err)
	}
	encodedManifest, err := provider.EncodeManifest(opaqueStateManifest(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "opaque-state.json"), encodedManifest, 0o600); err != nil {
		t.Fatal(err)
	}
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callNumber := upstreamCalls.Add(1)
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("built-in chat binding used unexpected path %q", r.URL.Path)
		}
		if callNumber == 1 {
			if got := r.Header.Get("Authorization"); got != "Bearer device-runtime-ticket" {
				t.Errorf("device state resolved to wrong runtime token: %q", got)
			}
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer refreshed-runtime-ticket" {
			t.Errorf("refreshed state resolved to wrong runtime token: %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["model"] != "gpt-test" {
			t.Errorf("built-in OpenAI request body=%v err=%v", body, err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-fixture","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()
	config := daemon.Config{DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "daemon.sock"), ProviderManifestDir: manifestDir, HTTPEnabled: true, HTTPAddr: "127.0.0.1:0"}
	service := daemon.NewWithProviderModules(config, opaqueStateModule{})
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	rpc := func(method string, params map[string]any) json.RawMessage {
		t.Helper()
		response, callErr := daemon.CallIPC(context.Background(), config.IPCPath, daemon.IPCRequest{Method: method, Params: params})
		if callErr != nil || !response.OK {
			t.Fatalf("%s: ok=%v err=%v", method, response.OK, callErr)
		}
		data, marshalErr := json.Marshal(response.Result)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return data
	}
	var node, connection struct {
		ID string `json:"ID"`
	}
	if err := json.Unmarshal(rpc("providers.create", map[string]any{"name": "Opaque", "prefix": "opaque", "baseUrl": upstream.URL + "/v1", "protocol": "openai_chat", "definitionID": opaqueStateProviderID}), &node); err != nil {
		t.Fatal(err)
	}
	seed := `{"ticket":"seed-runtime-ticket","subject":"seed-subject","workspace":"workspace-7","metadata":{"issued_by":"seed"}}`
	if err := json.Unmarshal(rpc("connections.create", map[string]any{"providerNodeID": node.ID, "name": "Opaque account", "credentialType": "opaque_ticket", "secret": seed}), &connection); err != nil {
		t.Fatal(err)
	}
	connectionList := rpc("connections.list", nil)
	for _, secret := range []string{"seed-runtime-ticket", "device-runtime-ticket", "workspace-7", "issued_by"} {
		if strings.Contains(string(connectionList), secret) {
			t.Fatalf("connection IPC leaked credential value %q", secret)
		}
	}
	var started struct {
		SessionID string `json:"sessionId"`
	}
	cli := os.Getenv("GOBROOM_TEST_CLI")
	var startJSON []byte
	if cli != "" {
		startJSON = runDeviceRaceCLI(t, cli, config.IPCPath, "connections", "device-start", "--connection-id", connection.ID)
	} else {
		startJSON = rpc("auth.device.start", map[string]any{"connectionID": connection.ID})
	}
	if err := json.Unmarshal(startJSON, &started); err != nil || started.SessionID == "" {
		t.Fatalf("device start=%s err=%v", startJSON, err)
	}
	if strings.Contains(string(startJSON), "private-opaque-device-code") || strings.Contains(string(startJSON), "device-runtime-ticket") {
		t.Fatal("device start leaked private auth material")
	}
	deadline := time.Now().Add(5 * time.Second)
	var status string
	var completionJSON json.RawMessage
	for time.Now().Before(deadline) {
		if cli != "" {
			completionJSON = runDeviceRaceCLI(t, cli, config.IPCPath, "connections", "device-get", "--session-id", started.SessionID)
		} else {
			completionJSON = rpc("auth.device.get", map[string]any{"sessionID": started.SessionID})
		}
		var result struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(completionJSON, &result); err != nil {
			t.Fatal(err)
		}
		status = result.Status
		if status != "starting" && status != "awaiting_user" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status != "completed" {
		t.Fatalf("device setup status=%q response=%s", status, completionJSON)
	}
	for _, secret := range []string{"device-runtime-ticket", "workspace-7", "issued_by", "device-fixture"} {
		if strings.Contains(string(completionJSON), secret) {
			t.Fatalf("device completion leaked persisted state field %q: %s", secret, completionJSON)
		}
	}
	connectionList = rpc("connections.list", nil)
	for _, secret := range []string{"device-runtime-ticket", "workspace-7", "device-fixture", "issued_by"} {
		if strings.Contains(string(connectionList), secret) {
			t.Fatalf("connection IPC leaked opaque state field %q", secret)
		}
	}
	// Build a public model graph and drive a real OpenAI-compatible HTTP 401 refresh.
	if err := json.Unmarshal(rpc("custom_models.upsert", map[string]any{"id": "route_opaque", "providerNodeID": node.ID, "kind": "custom", "externalID": "gpt-test", "displayName": "GPT test", "connectionIds": []string{connection.ID}}), new(map[string]any)); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rpc("physical_models.upsert", map[string]any{"name": "opaque-physical", "identity": map[string]any{"canonicalName": "gpt-test"}, "sources": []map[string]any{{"routeId": "route_opaque", "fidelity": "exact"}}, "enabled": true}), new(map[string]any)); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rpc("combo_models.upsert", map[string]any{"name": "opaque-role", "members": []map[string]any{{"kind": "physical", "id": "opaque-physical"}}, "discoverable": true, "enabled": true}), new(map[string]any)); err != nil {
		t.Fatal(err)
	}
	chatRequest := `{"model":"opaque-role","messages":[{"role":"user","content":"hello"}]}`
	callChat := func() {
		t.Helper()
		response, callErr := http.Post("http://"+service.Addresses().HTTP+"/v1/chat/completions", "application/json", strings.NewReader(chatRequest))
		if callErr != nil {
			t.Fatal(callErr)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("chat response status=%d", response.StatusCode)
		}
	}
	callChat()
	if upstreamCalls.Load() != 2 {
		t.Fatalf("401 did not cause exactly one credential refresh/retry: upstream calls=%d", upstreamCalls.Load())
	}
	configExport := rpc("config.export", nil)
	for _, secret := range []string{"device-runtime-ticket", "refreshed-runtime-ticket", "workspace-7", "device-fixture", "persisted-by-refresh"} {
		if strings.Contains(string(configExport), secret) {
			t.Fatalf("configuration export leaked opaque state field %q", secret)
		}
	}
	connectionList = rpc("connections.list", nil)
	for _, secret := range []string{"device-runtime-ticket", "refreshed-runtime-ticket", "workspace-7", "device-fixture", "persisted-by-refresh"} {
		if strings.Contains(string(connectionList), secret) {
			t.Fatalf("connection IPC leaked refreshed state field %q", secret)
		}
	}
	if err := service.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	service = daemon.NewWithProviderModules(config, opaqueStateModule{})
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer service.Stop(context.Background())
	callChat()
	if upstreamCalls.Load() != 3 {
		t.Fatalf("restart probe did not use persisted refreshed state: upstream calls=%d", upstreamCalls.Load())
	}
	if cli != "" {
		runDeviceRaceCLI(t, cli, config.IPCPath, "connections", "test-inference", "--connection-id", connection.ID, "--model-id", "gpt-test")
		if upstreamCalls.Load() != 4 {
			t.Fatalf("installed inference probe did not consume refreshed state: upstream calls=%d", upstreamCalls.Load())
		}
	}
}
