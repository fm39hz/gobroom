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
	"time"

	"github.com/fm39hz/gobroom/pkg/daemon"
	"github.com/fm39hz/gobroom/pkg/provider"
)

func audioManifest() provider.ProviderDefinition {
	task := provider.Ref{Kind: "operation", ID: "audio.transcribe", ContractVersion: 1}
	return provider.ProviderDefinition{
		ContractVersion: 1, ID: "example-audio", Version: "1", DisplayName: "Example Audio",
		Capabilities:          provider.CapabilitySet{AudioInput: true},
		DefaultCredentialType: "api_key", AuthModes: []provider.AuthModeDefinition{{CredentialType: "api_key", Auth: provider.PrimitiveRef{Kind: provider.PrimitiveAuth, ID: "example-audio-auth", ContractVersion: 1}, Presentation: provider.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "}}},
		Operations: map[provider.Operation]provider.OperationBinding{provider.OperationAudio: {
			Protocol: "example_audio", TaskRef: &task, ProviderFormat: "example-audio-json",
			Endpoint:        provider.PrimitiveRef{Kind: provider.PrimitiveEndpoint, ID: "http-json", ContractVersion: 1},
			Transport:       provider.PrimitiveRef{Kind: provider.PrimitiveTransport, ID: "http", ContractVersion: 1},
			RequestCodec:    provider.PrimitiveRef{Kind: provider.PrimitiveRequestCodec, ID: "example-audio-json", ContractVersion: 1},
			ResponseDecoder: provider.PrimitiveRef{Kind: provider.PrimitiveResponseDecoder, ID: "example-audio-json", ContractVersion: 1},
		}, provider.OperationQuota: {
			Endpoint:        provider.PrimitiveRef{Kind: provider.PrimitiveEndpoint, ID: "http-json", ContractVersion: 1},
			Transport:       provider.PrimitiveRef{Kind: provider.PrimitiveTransport, ID: "http", ContractVersion: 1},
			QuotaSource:     provider.PrimitiveRef{Kind: provider.PrimitiveQuotaSource, ID: "example-audio-quota", ContractVersion: 1},
			EndpointOptions: provider.EndpointOptions{Path: "/account/usage"},
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
	mode := binding.AuthModes["api_key"]
	credential, err := mode.Flow.Resolve(context.Background(), provider.AuthInput{ConnectionID: "external-connection", Type: "api_key", Secret: "module-secret"})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := binding.Adapter.Prepare(context.Background(), request, provider.Route{BaseURL: "https://audio.example/v1", CredentialPresentation: mode.Presentation}, credential)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(prepared.Body)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Method != http.MethodPost || prepared.URL != "https://audio.example/v1/audio/transcriptions" || prepared.Headers.Get("Authorization") != "Bearer module-secret" || prepared.Headers.Get("X-Example-User") != "user:module-secret" || !strings.Contains(string(body), "audio_url") {
		t.Fatalf("external module produced invalid request: method=%s URL=%s headers=%#v body=%s", prepared.Method, prepared.URL, prepared.Headers, body)
	}
}

func TestExternalModuleImplementsQuotaSourceThroughPublicTypes(t *testing.T) {
	runtime, err := provider.NewRuntimeRegistryWithModules(AudioModule{})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := provider.EncodeManifest(audioManifest())
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.LoadDefinitionJSON(manifest); err != nil {
		t.Fatal(err)
	}
	bindings, err := runtime.BuildBindings()
	if err != nil {
		t.Fatal(err)
	}
	binding := bindings[provider.RuntimeBindingKey("example-audio", provider.OperationQuota)]
	wantSourceRef := provider.Ref{Kind: string(provider.PrimitiveQuotaSource), ID: "example-audio-quota", ContractVersion: 1}
	if binding.QuotaSource == nil || binding.QuotaSourceRef != wantSourceRef {
		t.Fatalf("external quota source was not bound: %#v", binding)
	}
	source := runtime.QuotaSourcesForBindings(bindings)[binding.QuotaSourceRef]
	snapshots, err := source.Fetch(context.Background(), provider.QuotaRequest{Route: provider.Route{NodeID: "audio", CredentialID: "account", ExternalModel: "asr-v4"}})
	if err != nil || len(snapshots) != 1 || snapshots[0].WindowName != "example-daily" || snapshots[0].Remaining == nil || *snapshots[0].Remaining != 42 {
		t.Fatalf("external quota source snapshot=%#v err=%v", snapshots, err)
	}
}

func TestExternalModuleInterpretsBoundedArtifactAndCompilesRequirement(t *testing.T) {
	runtime, err := provider.NewRuntimeRegistryWithModules(AudioModule{})
	if err != nil {
		t.Fatal(err)
	}
	operations, err := runtime.OperationSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	artifactRef := provider.Ref{Kind: provider.ArtifactKind, ID: "example.audio.body", ContractVersion: 1}
	interpreterRef := provider.Ref{Kind: provider.ArtifactInterpreterKind, ID: "example.audio.header", ContractVersion: 1}
	body := []byte("RIFFWAVE")
	bodyRef := provider.BodyRef{ID: "fixture-body", MediaType: "audio/wav", SizeBytes: int64(len(body)), ExpiresAt: time.Now().Add(time.Minute), Replayable: true}
	request := provider.OperationRequest{
		Model: "asr", Operation: audioInspectOperation, OperationContractVersion: 1,
		Artifacts: []provider.ArtifactRef{{
			TypeRef: artifactRef, Role: "audio", Owner: provider.ArtifactOwner{Domain: "client"},
			ReplayScope: provider.ArtifactReplayRequest, Sensitivity: provider.ArtifactPrivate,
			MediaType: bodyRef.MediaType, SizeBytes: bodyRef.SizeBytes, ExpiresAt: bodyRef.ExpiresAt, Body: &bodyRef,
		}},
	}
	opened := 0
	prepared, err := operations.PrepareWithArtifactBodies(context.Background(), request, func(_ context.Context, artifact provider.ArtifactRef, recipient provider.Ref) (io.ReadCloser, error) {
		opened++
		if artifact.TypeRef != artifactRef || recipient != interpreterRef {
			t.Fatalf("interpreter lease request artifact=%#v recipient=%#v", artifact.TypeRef, recipient)
		}
		return io.NopCloser(strings.NewReader(string(body))), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if opened != 1 || len(prepared.ArtifactFacts) != 1 || len(prepared.Requirements) != 1 {
		t.Fatalf("artifact inspection result: opened=%d facts=%#v requirements=%#v", opened, prepared.ArtifactFacts, prepared.Requirements)
	}
	if prepared.Requirements[0].Ref != (provider.Ref{Kind: "feature-evaluator", ID: "example.audio.codec", ContractVersion: 1}) || string(prepared.Requirements[0].Constraints) != `{"codec":"wav"}` {
		t.Fatalf("opaque audio bytes did not become a typed route requirement: %#v", prepared.Requirements)
	}
	request.ArtifactFacts = prepared.ArtifactFacts
	if _, err := operations.Prepare(request); err != nil || opened != 1 {
		t.Fatalf("cached artifact facts were not reused: opened=%d err=%v", opened, err)
	}
}

func TestPublicDaemonHostsExternalProviderModuleAndManifest(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/audio/transcriptions" || request.Header.Get("Authorization") != "Bearer account-secret" || request.Header.Get("X-Example-User") != "user:account-secret" {
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
		if definition.ID == "example-audio" {
			foundAudio, foundQuota := false, false
			for _, operation := range definition.Operations {
				if operation.ID == provider.OperationAudio && operation.TaskRef != nil && operation.TaskRef.ID == "audio.transcribe" {
					foundAudio = true
				}
				if operation.ID == provider.OperationQuota && operation.Primitives["quotaSource"].ID == "example-audio-quota" {
					foundQuota = true
				}
			}
			found = foundAudio && foundQuota
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
