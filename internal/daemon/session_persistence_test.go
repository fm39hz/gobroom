package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/sessionpersistence"
)

const sessionFailureMarker = "private-session-write-error-4d8a"

type rejectingSessionSave struct{ *kernel.MemorySessionStore }

func (*rejectingSessionSave) ID() string { return "fixture-reject-session-save" }
func (*rejectingSessionSave) Save(context.Context, kernel.Route, string, kernel.SessionState) error {
	return errors.New(sessionFailureMarker)
}

func TestDaemonSessionPersistenceFailureDoesNotReplayInference(t *testing.T) {
	runSessionFailureAcceptance(t, false)
}
func TestInstalledCLISessionPersistenceFailureVisibility(t *testing.T) {
	if os.Getenv("GOBROOM_TEST_CLI") == "" {
		t.Skip("explicit installed-session gate requires GOBROOM_TEST_CLI")
	}
	runSessionFailureAcceptance(t, true)
}

func runSessionFailureAcceptance(t *testing.T, installed bool) {
	t.Helper()
	for _, custom := range []bool{false, true} {
		t.Run(fmt.Sprintf("custom-save-error-%t", custom), func(t *testing.T) {
			var calls atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var input struct {
					Model    string `json:"model"`
					Previous string `json:"previous_response_id"`
				}
				if r.URL.Path != "/v1/responses" || json.NewDecoder(r.Body).Decode(&input) != nil || input.Model != "fixture" {
					http.Error(w, "invalid fixture input", 400)
					return
				}
				n := calls.Add(1)
				previous := ""
				if !custom && n == 2 {
					previous = "session-response-1"
				}
				if n > 2 || input.Previous != previous {
					t.Errorf("replay or lost live state: call=%d previous=%q want=%q", n, input.Previous, previous)
					http.Error(w, "invalid continuation", 500)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(struct {
					ID     string          `json:"id"`
					Object string          `json:"object"`
					Status string          `json:"status"`
					Output json.RawMessage `json:"output"`
				}{
					fmt.Sprintf("session-response-%d", n), "response", "completed", json.RawMessage(`[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]`),
				})
			}))
			defer upstream.Close()
			root := t.TempDir()
			manifests := filepath.Join(root, "providers")
			if err := os.Mkdir(manifests, 0o700); err != nil {
				t.Fatal(err)
			}
			registry, err := provider.NewRuntimeRegistry()
			if err != nil {
				t.Fatal(err)
			}
			definition, ok := registry.Primitives.Definition(provider.ProviderDefinitionRef("openai-compatible-responses", 1))
			if !ok {
				t.Fatal("missing Responses definition")
			}
			definition.ID = "fixture-session-persistence"
			if custom {
				definition.Session.ID = "fixture-reject-session-save"
			}
			manifest, err := provider.EncodeManifest(definition)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(manifests, "provider.json"), manifest, 0o600); err != nil {
				t.Fatal(err)
			}
			module := provider.RuntimeModuleFunc{Name: "fixture.session-save", Version: "1", HostContractVersion: provider.RuntimeModuleHostContractVersion, Init: func(r *provider.RuntimeRegistry) error {
				return r.RegisterSessionStore(&rejectingSessionSave{kernel.NewMemorySessionStore()})
			}}
			config := Config{DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "daemon.sock"), HTTPEnabled: true, HTTPAddr: "127.0.0.1:0", HTTPControl: true, HTTPControlAddr: "127.0.0.1:0", HTTPControlToken: "fixture-session-admin", ProviderManifestDir: manifests}
			d := NewWithProviderModules(config, module)
			if err := d.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = d.Stop(context.Background()) }()
			rpc := func(method string, params map[string]any) []byte {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				response, err := CallIPC(ctx, config.IPCPath, IPCRequest{Method: method, Params: params})
				if err != nil || !response.OK {
					t.Fatalf("%s: %v / %s", method, err, response.Error)
				}
				data, err := json.Marshal(response.Result)
				if err != nil {
					t.Fatal(err)
				}
				return data
			}
			var node, connection struct{ ID string }
			if err := json.Unmarshal(rpc("providers.create", map[string]any{"name": "Session fixture", "prefix": "session-fixture", "baseUrl": upstream.URL + "/v1", "protocol": "openai_responses", "definitionID": definition.ID}), &node); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(rpc("connections.create", map[string]any{"providerNodeID": node.ID, "name": "Fixture", "credentialType": "api_key", "secret": "private-session-key"}), &connection); err != nil {
				t.Fatal(err)
			}
			rpc("custom_models.upsert", map[string]any{"id": "session-route", "providerNodeID": node.ID, "kind": "custom", "externalID": "fixture", "displayName": "Fixture", "connectionIds": []string{connection.ID}})
			rpc("physical_models.upsert", map[string]any{"name": "session-physical", "identity": map[string]any{"canonicalName": "fixture"}, "sources": []map[string]any{{"routeId": "session-route", "fidelity": "exact"}}, "enabled": true})
			rpc("combo_models.upsert", map[string]any{"name": "session-role", "members": []map[string]any{{"kind": "physical", "id": "session-physical"}}, "enabled": true, "discoverable": true})
			if !custom {
				if _, err := d.store.DB.Exec(`CREATE TRIGGER reject_session_write BEFORE INSERT ON provider_sessions BEGIN SELECT RAISE(FAIL,'private-session-write-error-4d8a'); END`); err != nil {
					t.Fatal(err)
				}
			}
			client := &http.Client{Timeout: 5 * time.Second}
			call := func() {
				t.Helper()
				request, _ := http.NewRequest(http.MethodPost, "http://"+d.Addresses().HTTP+"/v1/responses", strings.NewReader(`{"model":"session-role","input":"hello"}`))
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("X-Session-ID", "private-session-client")
				response, err := client.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil || response.StatusCode != 200 || !bytes.Contains(body, []byte(`"ok"`)) {
					t.Fatalf("inference failed: %d %s / %v", response.StatusCode, body, err)
				}
			}
			read := func() (string, sessionpersistence.Status, []byte) {
				t.Helper()
				raw := rpc("status", nil)
				if installed {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					var err error
					raw, err = exec.CommandContext(ctx, os.Getenv("GOBROOM_TEST_CLI"), "--ipc", config.IPCPath, "status").CombinedOutput()
					cancel()
					if err != nil {
						t.Fatalf("installed CLI: %v %s", err, raw)
					}
				}
				var view struct {
					Status  string                    `json:"status"`
					Session sessionpersistence.Status `json:"sessionPersistence"`
				}
				if err := json.Unmarshal(raw, &view); err != nil {
					t.Fatal(err)
				}
				return view.Status, view.Session, raw
			}
			wait := func(want sessionpersistence.Status) []byte {
				t.Helper()
				deadline := time.Now().Add(5 * time.Second)
				for {
					state, counts, raw := read()
					if counts == want && state == "degraded" {
						return raw
					}
					if time.Now().After(deadline) {
						t.Fatalf("status=%s counts=%+v want=%+v", state, counts, want)
					}
					time.Sleep(time.Millisecond)
				}
			}
			call()
			want := sessionpersistence.Status{Received: 1, Failed: 1}
			if custom {
				want = sessionpersistence.Status{SaveErrors: 1}
			}
			wait(want)
			if !custom {
				if _, err := d.store.DB.Exec(`DROP TRIGGER reject_session_write`); err != nil {
					t.Fatal(err)
				}
			}
			call()
			want = sessionpersistence.Status{Received: 2, Persisted: 1, Failed: 1}
			if custom {
				want = sessionpersistence.Status{SaveErrors: 2}
			}
			raw := wait(want)
			request, _ := http.NewRequest(http.MethodGet, "http://"+d.Addresses().HTTPControl+"/api/status", nil)
			request.Header.Set("Authorization", "Bearer "+config.HTTPControlToken)
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			httpStatus, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil || response.StatusCode != 200 {
				t.Fatalf("control read: %v", err)
			}
			var view struct {
				Status  string                    `json:"status"`
				Session sessionpersistence.Status `json:"sessionPersistence"`
			}
			if err := json.Unmarshal(httpStatus, &view); err != nil || view.Status != "degraded" || view.Session != want {
				t.Fatalf("control status=%s / %v", httpStatus, err)
			}
			logs := rpc("logs.list", map[string]any{"limit": 100})
			for _, output := range [][]byte{raw, httpStatus, logs} {
				for _, private := range []string{sessionFailureMarker, "private-session-key", "private-session-client", "session-response-1"} {
					if bytes.Contains(output, []byte(private)) {
						t.Fatalf("session data in operational view: %s", output)
					}
				}
			}
			var records []struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(logs, &records); err != nil {
				t.Fatal(err)
			}
			notices := 0
			for _, record := range records {
				if strings.Contains(record.Message, "session persistence") {
					notices++
				}
			}
			if notices != 1 || calls.Load() != 2 {
				t.Fatalf("duplicate notification or replay: notices=%d calls=%d", notices, calls.Load())
			}
			if err := d.Stop(context.Background()); !errors.Is(err, ErrSessionPersistenceFailureObserved) || strings.Contains(err.Error(), sessionFailureMarker) {
				t.Fatalf("unsafe/missing stop failure: %v", err)
			}
			if err := d.Stop(context.Background()); !errors.Is(err, ErrSessionPersistenceFailureObserved) {
				t.Fatalf("repeated Stop lost failure: %v", err)
			}
		})
	}
}
