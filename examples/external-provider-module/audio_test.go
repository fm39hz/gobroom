package externalprovider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/pkg/daemon"
	"github.com/fm39hz/gobroom/pkg/provider"
)

func audioManifest() provider.ProviderDefinition {
	task := provider.Ref{Kind: "operation", ID: "audio.transcribe", ContractVersion: 1}
	return provider.ProviderDefinition{
		ContractVersion: 1, ID: "example-audio", Version: "1", DisplayName: "Example Audio",
		Capabilities: provider.CapabilitySet{AudioInput: true},
		Auth:         provider.PrimitiveRef{Kind: provider.PrimitiveAuth, ID: "static-secret", ContractVersion: 1},
		Operations: map[provider.Operation]provider.OperationBinding{provider.OperationAudio: {
			Protocol: "example_audio", TaskRef: &task, ProviderFormat: "example-audio-json",
			Endpoint:        provider.PrimitiveRef{Kind: provider.PrimitiveEndpoint, ID: "http-json", ContractVersion: 1},
			Transport:       provider.PrimitiveRef{Kind: provider.PrimitiveTransport, ID: "http", ContractVersion: 1},
			RequestCodec:    provider.PrimitiveRef{Kind: provider.PrimitiveRequestCodec, ID: "example-audio-json", ContractVersion: 1},
			ResponseDecoder: provider.PrimitiveRef{Kind: provider.PrimitiveResponseDecoder, ID: "example-audio-json", ContractVersion: 1},
		}},
	}
}

func writeManifest(t *testing.T, directory string) {
	t.Helper()
	data, err := provider.EncodeManifest(audioManifest())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "audio.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestExternalModuleBindsManifestAndPreparesRequest(t *testing.T) {
	runtime, err := provider.NewRuntimeRegistryWithModules(AudioModule{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := provider.EncodeManifest(audioManifest())
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.LoadDefinitionJSON(data); err != nil {
		t.Fatal(err)
	}
	bindings, err := runtime.BuildBindings()
	if err != nil {
		t.Fatal(err)
	}
	binding, ok := bindings[provider.RuntimeBindingKey("example-audio", provider.OperationAudio)]
	if !ok || binding.Adapter == nil || binding.TaskRef.ID != "audio.transcribe" {
		t.Fatalf("public module API did not bind exact provider task: %#v", binding)
	}
	request := provider.NormalizedRequest{Model: "asr-v4", Operation: "audio.transcribe", OperationContractVersion: 1, OperationPayload: json.RawMessage(`{"audio_url":"https://assets.test/sample.wav"}`)}
	prepared, err := binding.Adapter.Prepare(context.Background(), request, provider.Route{BaseURL: "https://audio.example/v1"}, provider.Credential{Secret: "module-secret"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(prepared.Body)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Method != http.MethodPost || prepared.URL != "https://audio.example/v1/audio/transcriptions" || prepared.Headers.Get("Authorization") != "Bearer module-secret" || !strings.Contains(string(body), "audio_url") {
		t.Fatalf("external module produced invalid request: method=%s URL=%s headers=%#v body=%s", prepared.Method, prepared.URL, prepared.Headers, body)
	}
}

func TestPublicDaemonHostsExternalProviderModuleAndManifest(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/audio/transcriptions" || request.Header.Get("Authorization") != "Bearer account-secret" {
			t.Errorf("unexpected module request %s %s auth=%q", request.Method, request.URL.Path, request.Header.Get("Authorization"))
		}
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil || payload["audio_url"] != "https://assets.test/sample.wav" {
			t.Errorf("provider payload=%#v err=%v", payload, err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"text":"fixture transcript"}`)
	}))
	defer upstream.Close()

	manifestDir := t.TempDir()
	writeManifest(t, manifestDir)
	runtimeDir := t.TempDir()
	configuration := daemon.Config{
		DBPath: filepath.Join(runtimeDir, "fixture.db"), IPCPath: filepath.Join(runtimeDir, "fixture.sock"),
		ProviderManifestDir: manifestDir, ArtifactStoreDir: filepath.Join(runtimeDir, "artifacts"),
	}
	service := daemon.NewWithProviderModules(configuration, AudioModule{})
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer service.Stop(context.Background())

	catalogResponse, err := daemon.CallIPC(context.Background(), configuration.IPCPath, daemon.IPCRequest{ID: "catalog", Method: "providers.catalog"})
	if err != nil || !catalogResponse.OK {
		t.Fatalf("provider metadata IPC response=%#v err=%v", catalogResponse, err)
	}
	catalogJSON, err := json.Marshal(catalogResponse.Result)
	if err != nil {
		t.Fatal(err)
	}
	var definitions []provider.DefinitionMetadata
	if err := json.Unmarshal(catalogJSON, &definitions); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, definition := range definitions {
		if definition.ID == "example-audio" && len(definition.Operations) == 1 && definition.Operations[0].TaskRef != nil && definition.Operations[0].TaskRef.ID == "audio.transcribe" {
			found = true
		}
	}
	if !found {
		t.Fatalf("external manifest/module metadata absent from daemon catalog: %s", catalogJSON)
	}

	providerResponse, err := daemon.CallIPC(context.Background(), configuration.IPCPath, daemon.IPCRequest{
		ID: "provider", Method: "providers.create", Params: map[string]any{
			"name": "Example Audio", "prefix": "audio", "baseURL": upstream.URL + "/v1",
			"protocol": "example_audio", "definitionID": "example-audio", "modelsPath": "/models", "authMode": "api_key",
		},
	})
	if err != nil || !providerResponse.OK {
		t.Fatalf("provider create response=%#v err=%v", providerResponse, err)
	}
	var node struct {
		ID string `json:"ID"`
	}
	providerJSON, _ := json.Marshal(providerResponse.Result)
	if err := json.Unmarshal(providerJSON, &node); err != nil || node.ID == "" {
		t.Fatalf("provider create result=%s err=%v", providerJSON, err)
	}
	connectionResponse, err := daemon.CallIPC(context.Background(), configuration.IPCPath, daemon.IPCRequest{
		ID: "connection", Method: "connections.create", Params: map[string]any{
			"providerNodeID": node.ID, "name": "primary", "credentialType": "api_key", "secret": "account-secret",
		},
	})
	if err != nil || !connectionResponse.OK {
		t.Fatalf("connection create response=%#v err=%v", connectionResponse, err)
	}
	var connection struct {
		ID string `json:"ID"`
	}
	connectionJSON, _ := json.Marshal(connectionResponse.Result)
	if err := json.Unmarshal(connectionJSON, &connection); err != nil || connection.ID == "" {
		t.Fatalf("connection create result=%s err=%v", connectionJSON, err)
	}
	probe, err := daemon.CallIPC(context.Background(), configuration.IPCPath, daemon.IPCRequest{
		ID: "probe", Method: "connections.test_operation", Params: map[string]any{
			"connectionID": connection.ID, "operationID": "audio", "modelID": "asr-v4",
			"payload": json.RawMessage(`{"audio_url":"https://assets.test/sample.wav"}`),
		},
	})
	if err != nil || !probe.OK {
		t.Fatalf("non-chat operation probe response=%#v err=%v", probe, err)
	}
}
