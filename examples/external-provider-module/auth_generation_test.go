package externalprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/pkg/daemon"
	"github.com/fm39hz/gobroom/pkg/provider"
)

const (
	deviceRaceProviderID = "example-audio-device-race"
	deviceRaceAuthID     = "example-audio-device-race-auth"
)

type deviceRaceBarrier struct {
	entered     chan struct{}
	release     chan struct{}
	enteredOnce sync.Once
}

type deviceRaceModule struct{ barrier *deviceRaceBarrier }

func (m deviceRaceModule) ModuleDescriptor() provider.ModuleRef {
	return provider.ModuleRef{ID: "example.audio-device-race", Version: "1.0.0", HostContractVersion: provider.RuntimeModuleHostContractVersion}
}

func (m deviceRaceModule) Register(runtime *provider.RuntimeRegistry) error {
	return runtime.RegisterAuthFactory(deviceRaceAuthID, func(provider.AuthOptions) (provider.AuthFlow, error) {
		return deviceRaceAuth{barrier: m.barrier}, nil
	})
}

type deviceRaceAuth struct{ barrier *deviceRaceBarrier }

func (deviceRaceAuth) ID() string { return deviceRaceAuthID }
func (deviceRaceAuth) SetupSchema() provider.SetupSchema {
	return provider.SetupSchema{Fields: []provider.SetupField{{Name: "secret", Type: "string", Required: true, Secret: true}}}
}
func (deviceRaceAuth) Resolve(_ context.Context, input provider.AuthInput) (provider.Credential, error) {
	if input.Secret == "" {
		return provider.Credential{}, fmt.Errorf("credential secret is required")
	}
	return provider.Credential{ConnectionID: input.ConnectionID, Type: input.Type, Secret: input.Secret}, nil
}
func (deviceRaceAuth) Refresh(_ context.Context, input provider.AuthInput, _ provider.Credential) (provider.CredentialState, error) {
	return provider.NewCredentialState(input.Secret)
}
func (deviceRaceAuth) CanAuthorizeCode() bool   { return false }
func (deviceRaceAuth) CanAuthorizeDevice() bool { return true }
func (a deviceRaceAuth) BeginDeviceAuthorization(context.Context) (provider.DeviceAuthorization, error) {
	return provider.DeviceAuthorization{DeviceCode: "private-test-device-code", UserCode: "TEST-CODE", VerificationURL: "https://identity.test/device", ExpiresAt: time.Now().Add(time.Minute)}, nil
}
func (a deviceRaceAuth) WaitForDeviceAuthorization(ctx context.Context, _ provider.DeviceAuthorization) (provider.CredentialState, error) {
	a.barrier.enteredOnce.Do(func() { close(a.barrier.entered) })
	select {
	case <-a.barrier.release:
		return provider.NewCredentialState("late-device-token")
	case <-ctx.Done():
		return provider.CredentialState{}, ctx.Err()
	}
}

func deviceRaceManifest() provider.ProviderDefinition {
	definition := audioManifest()
	definition.ID = deviceRaceProviderID
	definition.DisplayName = "Audio device authorization generation fixture"
	definition.DefaultCredentialType = "oauth2"
	definition.AuthModes = []provider.AuthModeDefinition{{
		CredentialType: "oauth2",
		Auth:           provider.PrimitiveRef{Kind: provider.PrimitiveAuth, ID: deviceRaceAuthID, ContractVersion: 1},
		Presentation:   provider.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "},
	}}
	return definition
}

func TestExternalDeviceAuthorizationCannotOverwriteNewerConnectionCredential(t *testing.T) {
	root := t.TempDir()
	manifestDir := filepath.Join(root, "providers")
	if err := os.Mkdir(manifestDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// AudioModule's artifact descriptor pins its original provider recipient.
	// Keep that definition registered even though this probe uses no artifacts.
	writeManifest(t, manifestDir)
	manifest, err := provider.EncodeManifest(deviceRaceManifest())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "audio-device-race.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}

	observedAuth := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observedAuth <- r.Header.Get("Authorization")
		if r.URL.Path != "/v1/audio/transcriptions" {
			t.Errorf("unexpected audio operation path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"ok"}`))
	}))
	defer upstream.Close()

	barrier := &deviceRaceBarrier{entered: make(chan struct{}), release: make(chan struct{})}
	config := daemon.Config{DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "daemon.sock"), ProviderManifestDir: manifestDir, HTTPEnabled: false}
	service := daemon.NewWithProviderModules(config, AudioModule{}, deviceRaceModule{barrier: barrier})
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer service.Stop(context.Background())

	rpc := func(method string, params map[string]any) json.RawMessage {
		t.Helper()
		response, callErr := daemon.CallIPC(context.Background(), config.IPCPath, daemon.IPCRequest{Method: method, Params: params})
		if callErr != nil || !response.OK {
			t.Fatalf("%s: ok=%v error=%v", method, response.OK, callErr)
		}
		encoded, marshalErr := json.Marshal(response.Result)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return encoded
	}
	var node, connection struct {
		ID string `json:"ID"`
	}
	if err := json.Unmarshal(rpc("providers.create", map[string]any{"name": "Device race", "prefix": "device-race", "baseUrl": upstream.URL + "/v1", "protocol": "example_audio", "definitionID": deviceRaceProviderID}), &node); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rpc("connections.create", map[string]any{"providerNodeID": node.ID, "name": "Race account", "credentialType": "oauth2", "secret": "original"}), &connection); err != nil {
		t.Fatal(err)
	}
	var started struct {
		SessionID string `json:"sessionId"`
	}
	cli := os.Getenv("GOBROOM_TEST_CLI")
	if cli != "" {
		if err := json.Unmarshal(runDeviceRaceCLI(t, cli, config.IPCPath, "connections", "device-start", "--connection-id", connection.ID), &started); err != nil {
			t.Fatalf("installed CLI device-start response: %v", err)
		}
	} else if err := json.Unmarshal(rpc("auth.device.start", map[string]any{"connectionID": connection.ID}), &started); err != nil {
		t.Fatal(err)
	}
	if started.SessionID == "" {
		t.Fatal("device start response missing session ID")
	}
	select {
	case <-barrier.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("device authorization did not reach its wait barrier")
	}

	if cli != "" {
		runDeviceRaceCLI(t, cli, config.IPCPath, "connections", "update", "--id", connection.ID, "--secret", "manual-new")
	} else {
		response, callErr := daemon.CallIPC(context.Background(), config.IPCPath, daemon.IPCRequest{Method: "connections.update", Params: map[string]any{"id": connection.ID, "secret": "manual-new"}})
		if callErr != nil || !response.OK {
			t.Fatalf("connections.update failed: ok=%v error=%v", response.OK, callErr)
		}
	}
	close(barrier.release)

	deadline := time.Now().Add(5 * time.Second)
	var final struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	for time.Now().Before(deadline) {
		var encoded []byte
		if cli != "" {
			encoded = runDeviceRaceCLI(t, cli, config.IPCPath, "connections", "device-get", "--session-id", started.SessionID)
		} else {
			response, callErr := daemon.CallIPC(context.Background(), config.IPCPath, daemon.IPCRequest{Method: "auth.device.get", Params: map[string]any{"sessionID": started.SessionID}})
			if callErr != nil || !response.OK {
				t.Fatalf("device status request failed: ok=%v error=%v", response.OK, callErr)
			}
			encoded, _ = json.Marshal(response.Result)
		}
		if err := json.Unmarshal(encoded, &final); err != nil {
			t.Fatal(err)
		}
		if final.Status != "awaiting_user" && final.Status != "starting" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if final.Status != "failed" {
		t.Fatalf("late device completion status=%q; expected failed stale-generation commit", final.Status)
	}

	payloadPath := filepath.Join(root, "audio-payload.json")
	if err := os.WriteFile(payloadPath, []byte(`{"audio_url":"https://assets.test/sample.wav"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if cli != "" {
		runDeviceRaceCLI(t, cli, config.IPCPath, "connections", "test-operation", "--connection-id", connection.ID, "--operation", "audio", "--model-id", "asr-v4", "--payload-file", payloadPath)
	} else {
		probe := daemon.OperationProbeRequest{ConnectionID: connection.ID, OperationID: "audio", ModelID: "asr-v4", Payload: json.RawMessage(`{"audio_url":"https://assets.test/sample.wav"}`)}
		probeResponse, probeErr := daemon.CallIPC(context.Background(), config.IPCPath, daemon.IPCRequest{Method: "connections.test_operation", Params: mustJSONParams(t, probe)})
		if probeErr != nil || !probeResponse.OK {
			t.Fatalf("audio operation probe failed: ok=%v error=%v", probeResponse.OK, probeErr)
		}
	}
	select {
	case header := <-observedAuth:
		if header != "Bearer manual-new" {
			t.Fatal("audio request did not use the newer credential")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no upstream audio request")
	}
}

func runDeviceRaceCLI(t *testing.T, cli, ipc string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	commandArgs := append([]string{"--ipc", ipc}, args...)
	output, err := exec.CommandContext(ctx, cli, commandArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("installed CLI command failed: %v (%s)", err, output)
	}
	return output
}

func mustJSONParams(t *testing.T, value any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var params map[string]any
	if err := json.Unmarshal(encoded, &params); err != nil {
		t.Fatal(err)
	}
	return params
}
