package externalprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/pkg/daemon"
	"github.com/fm39hz/gobroom/pkg/provider"
)

func TestPublicDaemonRejectsAuthorizationSetupAfterDefinitionOwnerChanges(t *testing.T) {
	for _, aba := range []bool{false, true} {
		name := "rebind"
		if aba {
			name = "aba-rebind"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			manifestDir := filepath.Join(root, "providers")
			if err := os.Mkdir(manifestDir, 0o700); err != nil {
				t.Fatal(err)
			}
			// AudioModule's artifact recipient is pinned to the original audio definition.
			writeManifest(t, manifestDir)
			for _, version := range []uint64{1, 2} {
				definition := deviceRaceManifest()
				definition.ContractVersion = version
				encoded, err := provider.EncodeManifest(definition)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(manifestDir, fmt.Sprintf("device-race-v%d.json", version))
				if err := os.WriteFile(path, encoded, 0o600); err != nil {
					t.Fatal(err)
				}
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
				response, err := daemon.CallIPC(context.Background(), config.IPCPath, daemon.IPCRequest{Method: method, Params: params})
				if err != nil || !response.OK {
					t.Fatalf("%s: ok=%v error=%v", method, response.OK, err)
				}
				encoded, err := json.Marshal(response.Result)
				if err != nil {
					t.Fatal(err)
				}
				return encoded
			}
			var node, connection struct {
				ID string `json:"ID"`
			}
			if err := json.Unmarshal(rpc("providers.create", map[string]any{"name": "Owner race", "prefix": "owner-race", "baseUrl": upstream.URL + "/v1", "protocol": "example_audio", "definitionID": deviceRaceProviderID, "definitionContractVersion": 1}), &node); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(rpc("connections.create", map[string]any{"providerNodeID": node.ID, "name": "Owner account", "credentialType": "oauth2", "secret": "original-secret"}), &connection); err != nil {
				t.Fatal(err)
			}

			cli := os.Getenv("GOBROOM_TEST_CLI")
			var started daemon.AuthorizationSetupView
			if cli != "" {
				if err := json.Unmarshal(runDeviceRaceCLI(t, cli, config.IPCPath, "connections", "setup", "start", "--connection-id", connection.ID), &started); err != nil {
					t.Fatal(err)
				}
			} else if err := json.Unmarshal(rpc("auth.setup.start", map[string]any{"connectionID": connection.ID}), &started); err != nil {
				t.Fatal(err)
			}
			if started.SessionID == "" || started.Status != "awaiting_user" {
				t.Fatalf("setup start response missing session or awaiting_user status: %+v", started)
			}
			select {
			case <-barrier.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("device authorization did not reach its wait barrier")
			}

			updateOwner := func(version uint64) {
				t.Helper()
				params := map[string]any{"id": node.ID, "definitionID": deviceRaceProviderID, "definitionContractVersion": version}
				if cli != "" {
					versionArg := fmt.Sprint(version)
					runDeviceRaceCLI(t, cli, config.IPCPath, "providers", "update", "--id", node.ID, "--definition-id", deviceRaceProviderID, "--definition-contract-version", versionArg)
				} else {
					rpc("providers.update", params)
				}
			}
			updateOwner(2)
			if aba {
				updateOwner(1)
			}
			close(barrier.release)

			deadline := time.Now().Add(5 * time.Second)
			var status struct {
				Status string `json:"status"`
			}
			for time.Now().Before(deadline) {
				var encoded []byte
				if cli != "" {
					encoded = runDeviceRaceCLI(t, cli, config.IPCPath, "connections", "setup", "get", "--session-id", started.SessionID)
				} else {
					encoded = rpc("auth.setup.get", map[string]any{"sessionID": started.SessionID})
				}
				if err := json.Unmarshal(encoded, &status); err != nil {
					t.Fatal(err)
				}
				if status.Status != "awaiting_user" {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if status.Status != "failed" {
				t.Fatalf("device completion status=%q; expected failure after owner change", status.Status)
			}

			payloadPath := filepath.Join(root, "audio-payload.json")
			if err := os.WriteFile(payloadPath, []byte(`{"audio_url":"https://assets.test/sample.wav"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if cli != "" {
				runDeviceRaceCLI(t, cli, config.IPCPath, "connections", "test-operation", "--connection-id", connection.ID, "--operation", "audio", "--model-id", "asr-v4", "--payload-file", payloadPath)
			} else {
				probe := daemon.OperationProbeRequest{ConnectionID: connection.ID, OperationID: "audio", ModelID: "asr-v4", Payload: json.RawMessage(`{"audio_url":"https://assets.test/sample.wav"}`)}
				response, err := daemon.CallIPC(context.Background(), config.IPCPath, daemon.IPCRequest{Method: "connections.test_operation", Params: mustJSONParams(t, probe)})
				if err != nil || !response.OK {
					t.Fatalf("audio operation probe failed: ok=%v error=%v", response.OK, err)
				}
			}
			select {
			case auth := <-observedAuth:
				if auth != "Bearer original-secret" {
					t.Fatalf("audio request used %q; expected original credential", auth)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("no upstream audio request")
			}

			// A fresh setup belongs to the current owner and must still work.
			var successor daemon.AuthorizationSetupView
			var successorStart []byte
			if cli != "" {
				successorStart = runDeviceRaceCLI(t, cli, config.IPCPath, "connections", "setup", "start", "--connection-id", connection.ID)
			} else {
				successorStart = rpc("auth.setup.start", map[string]any{"connectionID": connection.ID})
			}
			if err := json.Unmarshal(successorStart, &successor); err != nil || successor.SessionID == "" {
				t.Fatalf("fresh device setup: %v", err)
			}
			deadline = time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				var encoded []byte
				if cli != "" {
					encoded = runDeviceRaceCLI(t, cli, config.IPCPath, "connections", "setup", "get", "--session-id", successor.SessionID)
				} else {
					encoded = rpc("auth.setup.get", map[string]any{"sessionID": successor.SessionID})
				}
				if err := json.Unmarshal(encoded, &status); err != nil {
					t.Fatal(err)
				}
				if status.Status != "awaiting_user" {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if status.Status != "completed" {
				t.Fatalf("new owner's fresh setup status=%q", status.Status)
			}
			if cli != "" {
				runDeviceRaceCLI(t, cli, config.IPCPath, "connections", "test-operation", "--connection-id", connection.ID, "--operation", "audio", "--model-id", "asr-v4", "--payload-file", payloadPath)
			} else {
				rpc("connections.test_operation", mustJSONParams(t, daemon.OperationProbeRequest{ConnectionID: connection.ID, OperationID: "audio", ModelID: "asr-v4", Payload: json.RawMessage(`{"audio_url":"https://assets.test/sample.wav"}`)}))
			}
			select {
			case auth := <-observedAuth:
				if auth != "Bearer late-device-token" {
					t.Fatal("fresh setup did not replace current owner's credential")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("no upstream request after fresh setup")
			}
		})
	}
}
