package externalprovider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/pkg/daemon"
	"github.com/fm39hz/gobroom/pkg/provider"
)

const audioLanguageTransformID = "example.audio.set-language"

var audioLanguageOptionsSchema = json.RawMessage(`{"type":"object","properties":{"language":{"type":"string","minLength":1}},"required":["language"],"additionalProperties":false}`)

type audioLanguageOptions struct {
	Language string `json:"language"`
}

type audioLanguageTransform struct{}

func (audioLanguageTransform) Definition() provider.TransformDefinition {
	optionsRef := provider.Ref{Kind: provider.SchemaKind, ID: "example.audio.set-language.options", ContractVersion: 1}
	return provider.TransformDefinition{
		Ref:                   provider.Ref{Kind: provider.RequestTransformKind, ID: audioLanguageTransformID, ContractVersion: 1},
		ImplementationVersion: "1",
		Label:                 "Set audio transcription language",
		Description:           "Set the language field in an audio transcription payload.",
		OptionsSchemaRef:      &optionsRef,
		Stage:                 provider.TransformBeforeRequirements,
		Effects:               []provider.TransformEffect{provider.TransformInput},
	}
}

func (audioLanguageTransform) Apply(_ context.Context, request *provider.NormalizedRequest, rawOptions json.RawMessage) error {
	var options audioLanguageOptions
	if err := json.Unmarshal(rawOptions, &options); err != nil {
		return err
	}
	var payload struct {
		AudioURL string `json:"audio_url"`
		Language string `json:"language,omitempty"`
	}
	if err := json.Unmarshal(request.OperationPayload, &payload); err != nil {
		return err
	}
	if options.Language == "__invalid_audio_url__" {
		payload.AudioURL = ""
	} else {
		payload.Language = options.Language
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request.OperationPayload = encoded
	return nil
}

func (audioLanguageModule) ModuleDescriptor() provider.ModuleRef {
	return provider.ModuleRef{ID: "example.audio-transform", Version: "1.0.0", HostContractVersion: provider.RuntimeModuleHostContractVersion}
}

type audioLanguageModule struct{}

func (audioLanguageModule) Register(runtime *provider.RuntimeRegistry) error {
	return runtime.RegisterRequestTransform(audioLanguageTransform{}, audioLanguageOptionsSchema)
}

func TestExternalAudioRequestTransformBindingLifecycle(t *testing.T) {
	root := t.TempDir()
	manifestDir := filepath.Join(root, "providers")
	if err := os.Mkdir(manifestDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, manifestDir)

	type observedPayload struct {
		AudioURL string `json:"audio_url"`
		Language string `json:"language"`
	}
	var mu sync.Mutex
	var upstreamPayloads []observedPayload
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			t.Errorf("unexpected upstream path %q", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		var payload observedPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode upstream audio payload: %v", err)
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		mu.Lock()
		upstreamPayloads = append(upstreamPayloads, payload)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"text":"fixture transcript"}`)
	}))
	defer upstream.Close()

	config := daemon.Config{
		DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "daemon.sock"),
		ProviderManifestDir: manifestDir, HTTPEnabled: true, HTTPAddr: "127.0.0.1:0",
	}
	newDaemon := func() *daemon.Daemon {
		service := daemon.NewWithProviderModules(config, AudioModule{}, audioLanguageModule{})
		if err := service.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		return service
	}
	service := newDaemon()
	defer func() { _ = service.Stop(context.Background()) }()

	rpc := func(method string, params map[string]any) json.RawMessage {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		response, err := daemon.CallIPC(ctx, config.IPCPath, daemon.IPCRequest{Method: method, Params: params})
		if err != nil || !response.OK {
			t.Fatalf("%s IPC: response=%#v err=%v", method, response, err)
		}
		encoded, err := json.Marshal(response.Result)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	requestAudio := func() int {
		t.Helper()
		response, err := http.Post("http://"+service.Addresses().HTTP+"/v1/audio/transcriptions", "application/json", strings.NewReader(`{"model":"audio-role","operationPayload":{"audio_url":"https://assets.test/sample.wav"}}`))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			t.Fatalf("audio operation status=%d body=%s", response.StatusCode, body)
		}
		return response.StatusCode
	}
	lastPayload := func() observedPayload {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		if len(upstreamPayloads) == 0 {
			t.Fatal("audio operation did not reach upstream")
		}
		return upstreamPayloads[len(upstreamPayloads)-1]
	}
	assertPayload := func(want observedPayload) {
		t.Helper()
		if got := lastPayload(); got != want {
			t.Fatalf("upstream payload=%#v, want %#v", got, want)
		}
	}

	var node, connection struct {
		ID string `json:"ID"`
	}
	if err := json.Unmarshal(rpc("providers.create", map[string]any{
		"name": "Audio transform", "prefix": "audio-transform", "baseUrl": upstream.URL + "/v1",
		"protocol": "example_audio", "definitionID": "example-audio",
	}), &node); err != nil || node.ID == "" {
		t.Fatalf("provider node=%#v err=%v", node, err)
	}
	if err := json.Unmarshal(rpc("connections.create", map[string]any{
		"providerNodeID": node.ID, "name": "Audio account", "credentialType": "api_key", "secret": "account-secret",
	}), &connection); err != nil || connection.ID == "" {
		t.Fatalf("connection=%#v err=%v", connection, err)
	}
	for _, setup := range []struct {
		method string
		params map[string]any
	}{
		{"custom_models.upsert", map[string]any{
			"id": "route_audio_transform", "providerNodeID": node.ID, "kind": "custom", "externalID": "asr-v4", "displayName": "ASR v4",
			"connectionIds": []string{connection.ID},
		}},
		{"physical_models.upsert", map[string]any{
			"name": "audio-transform-physical", "identity": map[string]any{"canonicalName": "asr-v4"},
			"sources": []map[string]any{{"routeId": "route_audio_transform", "fidelity": "exact"}}, "enabled": true,
		}},
		{"combo_models.upsert", map[string]any{
			"name": "audio-role", "members": []map[string]any{{"kind": "physical", "id": "audio-transform-physical"}},
			"discoverable": true, "enabled": true,
		}},
	} {
		if response, err := daemon.CallIPC(context.Background(), config.IPCPath, daemon.IPCRequest{Method: setup.method, Params: setup.params}); err != nil || !response.OK {
			t.Fatalf("%s: response=%#v err=%v", setup.method, response, err)
		}
	}

	transformRef := provider.Ref{Kind: provider.RequestTransformKind, ID: audioLanguageTransformID, ContractVersion: 1}
	invalid := map[string]any{
		"id": "model.invalid-options", "transformRef": transformRef, "enabled": true,
		"scope":   provider.TransformScope{Kind: provider.TransformScopeModel, ID: "audio-role"},
		"options": json.RawMessage(`{"unexpected":true}`),
	}
	badOptions, err := daemon.CallIPC(context.Background(), config.IPCPath, daemon.IPCRequest{Method: "transform_bindings.upsert", Params: invalid})
	if err == nil || badOptions.OK {
		t.Fatalf("malformed transform options unexpectedly persisted: response=%#v err=%v", badOptions, err)
	}
	var listed []provider.TransformBinding
	if err := json.Unmarshal(rpc("transform_bindings.list", nil), &listed); err != nil || len(listed) != 0 {
		t.Fatalf("failed options upsert changed persisted bindings: %#v err=%v", listed, err)
	}

	base := observedPayload{AudioURL: "https://assets.test/sample.wav"}
	requestAudio()
	assertPayload(base)

	daemonBinding := provider.TransformBinding{
		ID: "daemon.audio-language", TransformRef: transformRef, Enabled: true,
		Scope:   provider.TransformScope{Kind: provider.TransformScopeDaemon},
		Options: json.RawMessage(`{"language":"en"}`),
	}
	modelBinding := provider.TransformBinding{
		ID: "model.audio-language", TransformRef: transformRef, Enabled: true,
		Scope:   provider.TransformScope{Kind: provider.TransformScopeModel, ID: "audio-role"},
		Options: json.RawMessage(`{"language":"fr"}`),
	}
	for _, binding := range []provider.TransformBinding{daemonBinding, modelBinding} {
		if err := json.Unmarshal(rpc("transform_bindings.upsert", map[string]any{
			"id": binding.ID, "transformRef": binding.TransformRef, "enabled": binding.Enabled,
			"scope": binding.Scope, "options": binding.Options,
		}), new(provider.TransformBinding)); err != nil {
			t.Fatalf("decode persisted binding: %v", err)
		}
	}
	requestAudio()
	assertPayload(observedPayload{AudioURL: base.AudioURL, Language: "fr"})

	modelBinding.Options = json.RawMessage(`{"language":"de"}`)
	if _, err := daemon.CallIPC(context.Background(), config.IPCPath, daemon.IPCRequest{Method: "transform_bindings.upsert", Params: map[string]any{
		"id": modelBinding.ID, "transformRef": modelBinding.TransformRef, "enabled": true,
		"scope": modelBinding.Scope, "options": modelBinding.Options,
	}}); err != nil {
		t.Fatal(err)
	}
	requestAudio()
	assertPayload(observedPayload{AudioURL: base.AudioURL, Language: "de"})

	if err := service.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	service = newDaemon()
	var reloaded []provider.TransformBinding
	if err := json.Unmarshal(rpc("transform_bindings.list", nil), &reloaded); err != nil || len(reloaded) != 2 {
		t.Fatalf("restart did not restore transform bindings: %#v err=%v", reloaded, err)
	}
	requestAudio()
	assertPayload(observedPayload{AudioURL: base.AudioURL, Language: "de"})

	// A legal option value deliberately produces an operation payload that fails
	// the audio.transcribe input schema; no request may escape to upstream.
	invalidOutput := map[string]any{
		"id": "model.invalid-output", "transformRef": transformRef, "enabled": true,
		"scope":   provider.TransformScope{Kind: provider.TransformScopeModel, ID: "audio-role"},
		"options": json.RawMessage(`{"language":"__invalid_audio_url__"}`),
	}
	if _, err := daemon.CallIPC(context.Background(), config.IPCPath, daemon.IPCRequest{Method: "transform_bindings.upsert", Params: invalidOutput}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	beforeInvalid := len(upstreamPayloads)
	mu.Unlock()
	badRequest, err := http.Post("http://"+service.Addresses().HTTP+"/v1/audio/transcriptions", "application/json", strings.NewReader(`{"model":"audio-role","operationPayload":{"audio_url":"https://assets.test/sample.wav"}}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, badRequest.Body)
	_ = badRequest.Body.Close()
	if badRequest.StatusCode < 400 {
		t.Fatalf("schema-invalid transformed payload returned HTTP %d", badRequest.StatusCode)
	}
	mu.Lock()
	afterInvalid := len(upstreamPayloads)
	mu.Unlock()
	if afterInvalid != beforeInvalid {
		t.Fatalf("invalid transformed payload reached upstream: before=%d after=%d", beforeInvalid, afterInvalid)
	}
	if response, err := daemon.CallIPC(context.Background(), config.IPCPath, daemon.IPCRequest{Method: "transform_bindings.delete", Params: map[string]any{"id": "model.invalid-output"}}); err != nil || !response.OK {
		t.Fatalf("delete invalid-output binding: response=%#v err=%v", response, err)
	}

	modelBinding.Enabled = false
	if _, err := daemon.CallIPC(context.Background(), config.IPCPath, daemon.IPCRequest{Method: "transform_bindings.upsert", Params: map[string]any{
		"id": modelBinding.ID, "transformRef": transformRef, "enabled": false,
		"scope": modelBinding.Scope, "options": modelBinding.Options,
	}}); err != nil {
		t.Fatal(err)
	}
	requestAudio()
	assertPayload(observedPayload{AudioURL: base.AudioURL, Language: "en"})
	if response, err := daemon.CallIPC(context.Background(), config.IPCPath, daemon.IPCRequest{Method: "transform_bindings.delete", Params: map[string]any{"id": daemonBinding.ID}}); err != nil || !response.OK {
		t.Fatalf("delete daemon binding: response=%#v err=%v", response, err)
	}
	requestAudio()
	assertPayload(base)
	if response, err := daemon.CallIPC(context.Background(), config.IPCPath, daemon.IPCRequest{Method: "transform_bindings.delete", Params: map[string]any{"id": modelBinding.ID}}); err != nil || !response.OK {
		t.Fatalf("delete model binding: response=%#v err=%v", response, err)
	}
	if cli := os.Getenv("GOBROOM_TEST_CLI"); cli != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		output, err := exec.CommandContext(ctx, cli, "--ipc", config.IPCPath, "status").CombinedOutput()
		if err != nil {
			t.Fatalf("installed CLI status: %v %s", err, output)
		}
	}
}
