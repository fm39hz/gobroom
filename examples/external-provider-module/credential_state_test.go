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
		auth := opaqueStateAuth{}
		return provider.NewDeviceAuthorizationDriver(auth, auth.beginDeviceAuthorization, auth.waitForDeviceAuthorization)
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
func (opaqueStateAuth) beginDeviceAuthorization(context.Context, provider.AuthInput) (provider.DeviceAuthorization, error) {
	return provider.DeviceAuthorization{DeviceCode: "private-opaque-device-code", UserCode: "OPAQUE-CODE", VerificationURL: "https://identity.test/device", ExpiresAt: time.Now().Add(time.Minute)}, nil
}
func (opaqueStateAuth) waitForDeviceAuthorization(ctx context.Context, _ provider.DeviceAuthorization) (provider.CredentialState, error) {
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
		Recovery:     provider.CredentialRecoveryRefresh,
		Presentation: provider.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "}}}
	return definition
}

func opaqueStateRecovery498Manifest(t *testing.T) provider.ProviderDefinition {
	t.Helper()
	definition := opaqueStateManifest(t)
	for operation, binding := range definition.Operations {
		binding.ErrorClassifier = provider.PrimitiveRef{Kind: provider.PrimitiveErrorClassifier, ID: "http-json", ContractVersion: 1}
		binding.ErrorClassifierOptions = provider.ErrorClassifierOptions{HTTPJSON: &provider.HTTPJSONErrorClassifierOptions{
			AuthRecoverySignals: []provider.HTTPJSONErrorSignal{{HTTPStatuses: []int{498}, Codes: []string{"credential_expired"}}},
		}}
		definition.Operations[operation] = binding
	}
	return definition
}

func TestExternalOpaqueCredentialStateDeviceSetupPersistsAndStaysPrivate(t *testing.T) {
	root := t.TempDir()
	manifestDir := filepath.Join(root, "providers")
	if err := os.Mkdir(manifestDir, 0o700); err != nil {
		t.Fatal(err)
	}
	encodedManifest, err := provider.EncodeManifest(opaqueStateRecovery498Manifest(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "opaque-state.json"), encodedManifest, 0o600); err != nil {
		t.Fatal(err)
	}
	disabledDefinition := opaqueStateManifest(t)
	disabledDefinition.ID = "opaque-state-disabled"
	disabledDefinition.DisplayName = "Opaque credential state refresh-disabled fixture"
	disabledDefinition.AuthModes[0].Recovery = provider.CredentialRecoveryNone
	disabledManifest, err := provider.EncodeManifest(disabledDefinition)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "opaque-state-disabled.json"), disabledManifest, 0o600); err != nil {
		t.Fatal(err)
	}
	var upstreamCalls atomic.Int32
	var responseMode atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("built-in chat binding used unexpected path %q", r.URL.Path)
		}
		mode := responseMode.Load()
		if mode == 498 && responseMode.CompareAndSwap(498, 0) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(498)
			_, _ = w.Write([]byte(`{"error":{"code":"credential_expired"}}`))
			return
		}
		if mode != 0 {
			w.Header().Set("Content-Type", "application/json")
			switch mode {
			case 401:
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"code":"credential_expired"}}`))
			case 403:
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":{"code":"permission_denied"}}`))
			case 4031:
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":{"code":"quota_exceeded"}}`))
			}
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer refreshed-runtime-ticket" && got != "Bearer device-runtime-ticket" {
			t.Errorf("request resolved to wrong runtime token: %q", got)
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
	var disabledNode, disabledConnection struct {
		ID string `json:"ID"`
	}
	if err := json.Unmarshal(rpc("providers.create", map[string]any{"name": "Opaque disabled", "prefix": "opaque-disabled", "baseUrl": upstream.URL + "/v1", "protocol": "openai_chat", "definitionID": disabledDefinition.ID}), &disabledNode); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rpc("connections.create", map[string]any{"providerNodeID": disabledNode.ID, "name": "Opaque disabled account", "credentialType": "opaque_ticket", "secret": seed}), &disabledConnection); err != nil {
		t.Fatal(err)
	}
	connectionList := rpc("connections.list", nil)
	for _, secret := range []string{"seed-runtime-ticket", "device-runtime-ticket", "workspace-7", "issued_by"} {
		if strings.Contains(string(connectionList), secret) {
			t.Fatalf("connection IPC leaked credential value %q", secret)
		}
	}
	var started daemon.AuthorizationSetupView
	cli := os.Getenv("GOBROOM_TEST_CLI")
	var startJSON []byte
	if cli != "" {
		startJSON = runDeviceRaceCLI(t, cli, config.IPCPath, "connections", "setup", "start", "--connection-id", connection.ID)
	} else {
		startJSON = rpc("auth.setup.start", map[string]any{"connectionID": connection.ID})
	}
	if err := json.Unmarshal(startJSON, &started); err != nil || started.SessionID == "" || started.Status != "awaiting_user" {
		t.Fatalf("setup start=%s err=%v", startJSON, err)
	}
	var hasDisplayCode, hasOpenURL bool
	for _, action := range started.Actions {
		switch action.Kind {
		case provider.AuthorizationDisplayCode:
			hasDisplayCode = action.Code == "OPAQUE-CODE"
		case provider.AuthorizationOpenURL:
			hasOpenURL = action.URL == "https://identity.test/device"
		}
	}
	if !hasDisplayCode || !hasOpenURL {
		t.Fatalf("generic setup instructions missing display code/open URL actions: %+v", started.Actions)
	}
	if strings.Contains(string(startJSON), "private-opaque-device-code") || strings.Contains(string(startJSON), "device-runtime-ticket") {
		t.Fatal("setup start leaked private auth material")
	}
	deadline := time.Now().Add(5 * time.Second)
	var status string
	var completionJSON json.RawMessage
	for time.Now().Before(deadline) {
		if cli != "" {
			completionJSON = runDeviceRaceCLI(t, cli, config.IPCPath, "connections", "setup", "get", "--session-id", started.SessionID)
		} else {
			completionJSON = rpc("auth.setup.get", map[string]any{"sessionID": started.SessionID})
		}
		var result struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(completionJSON, &result); err != nil {
			t.Fatal(err)
		}
		status = result.Status
		if status != "awaiting_user" {
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
	// Build a public model graph and drive a typed HTTP 498 credential refresh.
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
	sendChat := func(model string) {
		t.Helper()
		request, err := json.Marshal(struct {
			Model    string              `json:"model"`
			Messages []map[string]string `json:"messages"`
		}{Model: model, Messages: []map[string]string{{"role": "user", "content": "hello"}}})
		if err != nil {
			t.Fatal(err)
		}
		response, callErr := http.Post("http://"+service.Addresses().HTTP+"/v1/chat/completions", "application/json", strings.NewReader(string(request)))
		if callErr != nil {
			t.Fatal(callErr)
		}
		_ = response.Body.Close()
	}
	activationList := func() []json.RawMessage {
		t.Helper()
		var entries []json.RawMessage
		if err := json.Unmarshal(rpc("credential_activation.list", map[string]any{"limit": 20}), &entries); err != nil {
			t.Fatal(err)
		}
		return entries
	}
	_ = rpc("custom_models.upsert", map[string]any{"id": "route_opaque_disabled", "providerNodeID": disabledNode.ID, "kind": "custom", "externalID": "gpt-test", "displayName": "GPT disabled", "connectionIds": []string{disabledConnection.ID}})
	_ = rpc("physical_models.upsert", map[string]any{"name": "opaque-disabled-physical", "identity": map[string]any{"canonicalName": "gpt-test"}, "sources": []map[string]any{{"routeId": "route_opaque_disabled", "fidelity": "exact"}}, "enabled": true})
	_ = rpc("combo_models.upsert", map[string]any{"name": "opaque-disabled-role", "members": []map[string]any{{"kind": "physical", "id": "opaque-disabled-physical"}}, "discoverable": true, "enabled": true})
	baselineActivations := len(activationList())
	for _, mode := range []int32{403, 4031} {
		label := fmt.Sprintf("denial-%d", mode)
		var denialNode, denialConnection struct {
			ID string `json:"ID"`
		}
		if err := json.Unmarshal(rpc("providers.create", map[string]any{"name": label, "prefix": label, "baseUrl": upstream.URL + "/v1", "protocol": "openai_chat", "definitionID": opaqueStateProviderID}), &denialNode); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(rpc("connections.create", map[string]any{"providerNodeID": denialNode.ID, "name": label, "credentialType": "opaque_ticket", "secret": seed}), &denialConnection); err != nil {
			t.Fatal(err)
		}
		_ = rpc("custom_models.upsert", map[string]any{"id": label + "-route", "providerNodeID": denialNode.ID, "kind": "custom", "externalID": "gpt-test", "displayName": label, "connectionIds": []string{denialConnection.ID}})
		_ = rpc("physical_models.upsert", map[string]any{"name": label + "-physical", "identity": map[string]any{"canonicalName": "gpt-test"}, "sources": []map[string]any{{"routeId": label + "-route", "fidelity": "exact"}}, "enabled": true})
		_ = rpc("combo_models.upsert", map[string]any{"name": label + "-role", "members": []map[string]any{{"kind": "physical", "id": label + "-physical"}}, "discoverable": true, "enabled": true})
		before := upstreamCalls.Load()
		responseMode.Store(mode)
		sendChat(label + "-role")
		responseMode.Store(0)
		if got := upstreamCalls.Load() - before; got != 1 {
			t.Fatalf("403 mode %d caused %d upstream calls, want one and no refresh", mode, got)
		}
		if got := len(activationList()); got != baselineActivations {
			t.Fatalf("403 mode %d created %d credential activation records", mode, got)
		}
	}
	beforeDisabled := upstreamCalls.Load()
	responseMode.Store(http.StatusUnauthorized)
	sendChat("opaque-disabled-role")
	responseMode.Store(0)
	if got := upstreamCalls.Load() - beforeDisabled; got != 1 {
		t.Fatalf("auth mode with recovery none caused %d upstream calls, want one", got)
	}
	if got := len(activationList()); got != baselineActivations {
		t.Fatalf("recovery-disabled 401 created %d credential activation records", got)
	}
	if cli != "" {
		listed := runDeviceRaceCLI(t, cli, config.IPCPath, "connections", "activation-list", "--limit", "20")
		var listedActivations []daemon.CredentialActivationView
		if err := json.Unmarshal(listed, &listedActivations); err != nil || len(listedActivations) != baselineActivations {
			t.Fatalf("denials created CLI activation records: %s", listed)
		}
		status := runDeviceRaceCLI(t, cli, config.IPCPath, "status")
		if len(status) == 0 {
			t.Fatal("CLI status returned no status view after auth denial requests")
		}
	}
	beforeRefresh := upstreamCalls.Load()
	responseMode.Store(498)
	callChat()
	if got := upstreamCalls.Load() - beforeRefresh; got != 2 {
		t.Fatalf("typed 498 caused %d upstream calls, want one refresh retry", got)
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
	beforeRestartProbe := upstreamCalls.Load()
	callChat()
	if upstreamCalls.Load() != beforeRestartProbe+1 {
		t.Fatalf("restart probe did not use persisted refreshed state: upstream calls=%d", upstreamCalls.Load())
	}
	if cli != "" {
		beforeCLIProbe := upstreamCalls.Load()
		runDeviceRaceCLI(t, cli, config.IPCPath, "connections", "test-inference", "--connection-id", connection.ID, "--model-id", "gpt-test")
		if upstreamCalls.Load() != beforeCLIProbe+1 {
			t.Fatalf("installed inference probe did not consume refreshed state: upstream calls=%d", upstreamCalls.Load())
		}
	}
}
