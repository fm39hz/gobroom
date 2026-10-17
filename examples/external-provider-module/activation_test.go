package externalprovider

import (
	"context"
	"database/sql"
	"encoding/json"
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

func TestExternalCredentialRefreshActivationRetryAndRestartRecovery(t *testing.T) {
	for _, recovery := range []string{"cli-retry", "restart"} {
		t.Run(recovery, func(t *testing.T) {
			root := t.TempDir()
			manifestDir := filepath.Join(root, "providers")
			if err := os.Mkdir(manifestDir, 0o700); err != nil {
				t.Fatal(err)
			}
			manifest, err := provider.EncodeManifest(opaqueStateManifest(t))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(manifestDir, "opaque-state.json"), manifest, 0o600); err != nil {
				t.Fatal(err)
			}

			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				call := calls.Add(1)
				if r.URL.Path != "/v1/chat/completions" {
					t.Errorf("unexpected upstream path %q", r.URL.Path)
				}
				if call == 1 {
					if got := r.Header.Get("Authorization"); got != "Bearer seed-runtime-ticket" {
						t.Errorf("initial request credential=%q", got)
					}
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if got := r.Header.Get("Authorization"); got != "Bearer refreshed-runtime-ticket" {
					t.Errorf("retried request credential=%q", got)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"chatcmpl-activation","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
			}))
			defer upstream.Close()

			config := daemon.Config{DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "daemon.sock"), ProviderManifestDir: manifestDir, HTTPEnabled: true, HTTPAddr: "127.0.0.1:0"}
			service := daemon.NewWithProviderModules(config, opaqueStateModule{})
			start := func() {
				t.Helper()
				if err := service.Start(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			stop := func() {
				t.Helper()
				if err := service.Stop(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			start()
			stopped := false
			defer func() {
				if !stopped {
					_ = service.Stop(context.Background())
				}
			}()

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
			if err := json.Unmarshal(rpc("providers.create", map[string]any{"name": "Activation fixture", "prefix": "activation", "baseUrl": upstream.URL + "/v1", "protocol": "openai_chat", "definitionID": opaqueStateProviderID}), &node); err != nil {
				t.Fatal(err)
			}
			seed := `{"ticket":"seed-runtime-ticket","subject":"seed-subject","workspace":"workspace-7","metadata":{"issued_by":"device-fixture"}}`
			if err := json.Unmarshal(rpc("connections.create", map[string]any{"providerNodeID": node.ID, "name": "Activation account", "credentialType": "opaque_ticket", "secret": seed}), &connection); err != nil {
				t.Fatal(err)
			}
			_ = rpc("custom_models.upsert", map[string]any{"id": "activation-route", "providerNodeID": node.ID, "kind": "custom", "externalID": "gpt-activation", "displayName": "Activation model", "connectionIds": []string{connection.ID}})
			_ = rpc("physical_models.upsert", map[string]any{"name": "activation-physical", "identity": map[string]any{"canonicalName": "gpt-activation"}, "sources": []map[string]any{{"routeId": "activation-route", "fidelity": "exact"}}, "enabled": true})
			_ = rpc("combo_models.upsert", map[string]any{"name": "activation-role", "members": []map[string]any{{"kind": "physical", "id": "activation-physical"}}, "discoverable": true, "enabled": true})

			db, err := sql.Open("sqlite", config.DBPath)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`CREATE TRIGGER fail_activation_snapshot_insert BEFORE INSERT ON settings WHEN NEW.key='snapshot_version' BEGIN SELECT RAISE(FAIL,'injected snapshot version persistence failure'); END`); err != nil {
				db.Close()
				t.Fatal(err)
			}

			chatRequest := `{"model":"activation-role","messages":[{"role":"user","content":"hello"}]}`
			response, err := http.Post("http://"+service.Addresses().HTTP+"/v1/chat/completions", "application/json", strings.NewReader(chatRequest))
			if err != nil {
				db.Close()
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK || calls.Load() != 2 {
				db.Close()
				t.Fatalf("refresh request status=%d upstreamCalls=%d", response.StatusCode, calls.Load())
			}

			type activationView = daemon.CredentialActivationView
			readActivation := func() (activationView, json.RawMessage) {
				t.Helper()
				data := rpc("credential_activation.get", map[string]any{"connectionID": connection.ID})
				var view activationView
				if err := json.Unmarshal(data, &view); err != nil {
					t.Fatal(err)
				}
				return view, data
			}
			deadline := time.Now().Add(5 * time.Second)
			var activation activationView
			var activationJSON json.RawMessage
			for time.Now().Before(deadline) {
				activation, activationJSON = readActivation()
				if activation.Attempts >= 1 && activation.Progress.ControlPublished && activation.Progress.ServingPublished && !activation.Progress.VersionPersisted {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if !activation.CredentialCommitted || activation.ConnectionID != connection.ID || activation.Status != "pending" || activation.Attempts < 1 || activation.ErrorCode == "" || !activation.Progress.RoutingReset || activation.Progress.ControlVersion == 0 || !activation.Progress.ControlPublished || activation.Progress.VersionPersisted || activation.Progress.ServingVersion != activation.Progress.ControlVersion || !activation.Progress.ServingPublished {
				db.Close()
				t.Fatalf("post-refresh activation=%s", activationJSON)
			}
			for _, secret := range []string{"seed-runtime-ticket", "refreshed-runtime-ticket", "workspace-7", "device-fixture"} {
				if strings.Contains(string(activationJSON), secret) {
					db.Close()
					t.Fatalf("activation view leaked credential material %q", secret)
				}
			}
			cli := os.Getenv("GOBROOM_TEST_CLI")
			if cli != "" {
				listed := runDeviceRaceCLI(t, cli, config.IPCPath, "connections", "activation-list", "--limit", "10")
				if strings.Contains(string(listed), "refreshed-runtime-ticket") {
					t.Fatal("activation list exposed private state")
				}
				statusJSON := runDeviceRaceCLI(t, cli, config.IPCPath, "status")
				var status struct {
					Status  string `json:"status"`
					Control uint64 `json:"controlSnapshotVersion"`
					Serving uint64 `json:"servingSnapshotVersion"`
				}
				if err := json.Unmarshal(statusJSON, &status); err != nil || status.Status != "degraded" || status.Control == 0 || status.Control != status.Serving {
					t.Fatalf("published-but-unpersisted status=%s %v", statusJSON, err)
				}
			}
			if recovery == "restart" {
				stop()
				stopped = true
			}
			if _, err := db.Exec(`DROP TRIGGER fail_activation_snapshot_insert`); err != nil {
				db.Close()
				t.Fatal(err)
			}

			if recovery == "cli-retry" {
				if cli != "" {
					runDeviceRaceCLI(t, cli, config.IPCPath, "connections", "activation-get", "--connection-id", connection.ID)
					runDeviceRaceCLI(t, cli, config.IPCPath, "connections", "activation-retry", "--connection-id", connection.ID)
				} else {
					_ = rpc("credential_activation.retry", map[string]any{"connectionID": connection.ID})
				}
			} else {
				service = daemon.NewWithProviderModules(config, opaqueStateModule{})
				start()
				stopped = false
			}
			if cli != "" {
				runDeviceRaceCLI(t, cli, config.IPCPath, "status")
			}
			recoveryDeadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(recoveryDeadline) {
				activation, activationJSON = readActivation()
				if activation.Status == "active" {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if activation.Status != "active" || !activation.Progress.RoutingReset || !activation.Progress.ControlPublished || !activation.Progress.VersionPersisted || !activation.Progress.ServingPublished || activation.Progress.ServingVersion != activation.Progress.ControlVersion {
				db.Close()
				t.Fatalf("recovered activation=%s", activationJSON)
			}
			if _, err := db.Exec(`DROP TRIGGER IF EXISTS fail_activation_snapshot_insert`); err != nil {
				t.Error(err)
			}
			if err := db.Close(); err != nil {
				t.Error(err)
			}
		})
	}
}
