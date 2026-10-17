package externalprovider

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/pkg/daemon"
)

func TestExternalArtifactOperationProbeStreamsOverUnixWithoutPublicAPI(t *testing.T) {
	root := t.TempDir()
	artifactDir := filepath.Join(root, "spools")
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/audio/inspect" || r.Header.Get("Authorization") != "Bearer account-secret" {
			t.Errorf("probe selected wrong endpoint or credential: %s", r.URL.Path)
			w.WriteHeader(400)
			return
		}
		reader, err := r.MultipartReader()
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		parts := map[string]string{}
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Error(err)
				break
			}
			data, err := io.ReadAll(part)
			if err != nil {
				t.Error(err)
			}
			parts[part.FormName()] = string(data)
			if part.FormName() == "file" && (part.FileName() != "audio.wav" || part.Header.Get("Content-Type") != "audio/wav") {
				t.Error("file metadata changed in provider codec")
			}
			_ = part.Close()
		}
		if parts["model"] != "asr-v4" || parts["request"] != "{}" || parts["file"] != "RIFFWAVE" {
			t.Errorf("probe multipart parts=%v", parts)
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"text":"private transcript must not be returned"}`)
	}))
	defer upstream.Close()
	manifestDir := filepath.Join(root, "providers")
	if err := os.Mkdir(manifestDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, manifestDir)
	config := daemon.Config{DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "daemon.sock"), ProviderManifestDir: manifestDir, ArtifactStoreDir: artifactDir, HTTPEnabled: false}
	service := daemon.NewWithProviderModules(config, AudioModule{})
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer service.Stop(context.Background())
	if service.Addresses().HTTP != "" {
		t.Fatal("operation probe required a public API listener")
	}
	rpc := func(method string, params map[string]any) json.RawMessage {
		t.Helper()
		response, err := daemon.CallIPC(context.Background(), config.IPCPath, daemon.IPCRequest{Method: method, Params: params})
		if err != nil || !response.OK {
			t.Fatalf("%s: %#v %v", method, response, err)
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
	if err := json.Unmarshal(rpc("providers.create", map[string]any{"name": "Probe only", "prefix": "audio-probe", "baseUrl": upstream.URL + "/v1", "protocol": "example_audio", "definitionID": "example-audio"}), &node); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rpc("connections.create", map[string]any{"providerNodeID": node.ID, "name": "Probe account", "credentialType": "api_key", "secret": "account-secret"}), &connection); err != nil {
		t.Fatal(err)
	}
	localFile := filepath.Join(root, "private-local-audio.wav")
	if err := os.WriteFile(localFile, []byte("RIFFWAVE"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := daemon.OperationProbeRequest{ConnectionID: connection.ID, OperationID: audioInspectOperation, ModelID: "asr-v4", Payload: json.RawMessage(`{}`)}
	files := []daemon.ProbeFileSpec{{Role: "audio", Path: localFile, MediaType: "audio/wav"}}
	assertEmptySpool := func() {
		t.Helper()
		err := filepath.WalkDir(artifactDir, func(path string, item fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !item.IsDir() {
				t.Errorf("probe spool remained after response: %s", filepath.Base(path))
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	response, err := daemon.CallOperationProbe(context.Background(), config.IPCPath, input, files)
	if err != nil || !response.OK {
		t.Fatalf("artifact probe=%#v %v", response, err)
	}
	result, err := json.Marshal(response.Result)
	if err != nil {
		t.Fatal(err)
	}
	var summary struct {
		HTTPStatus    int    `json:"httpStatus"`
		ModelID       string `json:"modelId"`
		ResponseBytes int    `json:"responseBytes"`
	}
	if err := json.Unmarshal(result, &summary); err != nil || summary.HTTPStatus != 200 || summary.ModelID != "asr-v4" || summary.ResponseBytes == 0 {
		t.Fatalf("probe summary=%s %v", result, err)
	}
	if strings.Contains(string(result), localFile) || strings.Contains(string(result), "private transcript") || strings.Contains(string(result), "RIFFWAVE") {
		t.Fatal("probe leaked local path/input/output body")
	}
	assertEmptySpool()
	if _, err := daemon.CallOperationProbe(context.Background(), config.IPCPath, input, []daemon.ProbeFileSpec{{Role: "undeclared", Path: localFile, MediaType: "audio/wav"}}); err == nil {
		t.Fatal("undeclared artifact role was accepted")
	}
	assertEmptySpool()
	invalidFile := filepath.Join(root, "invalid.wav")
	if err := os.WriteFile(invalidFile, []byte("BAD!WAVE"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := daemon.CallOperationProbe(context.Background(), config.IPCPath, input, []daemon.ProbeFileSpec{{Role: "audio", Path: invalidFile, MediaType: "audio/wav"}}); err == nil {
		t.Fatal("artifact interpreter was bypassed")
	}
	assertEmptySpool()
	if calls.Load() != 1 {
		t.Fatalf("invalid probe inputs reached upstream: %d", calls.Load())
	}
	if cli := os.Getenv("GOBROOM_TEST_CLI"); cli != "" {
		payloadPath, filesPath := filepath.Join(root, "payload.json"), filepath.Join(root, "files.json")
		if err := os.WriteFile(payloadPath, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(files)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filesPath, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		output, err := exec.CommandContext(ctx, cli, "--ipc", config.IPCPath, "connections", "test-operation", "--connection-id", connection.ID, "--operation", audioInspectOperation, "--model-id", "asr-v4", "--payload-file", payloadPath, "--artifacts-file", filesPath).CombinedOutput()
		if err != nil {
			t.Fatalf("installed artifact CLI: %v %s", err, output)
		}
		if err := json.Unmarshal(output, &summary); err != nil || summary.HTTPStatus != 200 {
			t.Fatalf("installed probe summary=%s %v", output, err)
		}
		if calls.Load() != 2 {
			t.Fatalf("installed CLI did not dispatch exactly once: %d", calls.Load())
		}
		assertEmptySpool()
	}
	var health, quotas map[string]json.RawMessage
	if err := json.Unmarshal(rpc("health.list", nil), &health); err != nil || len(health) != 0 {
		t.Fatalf("probe mutated health: %v %v", health, err)
	}
	if err := json.Unmarshal(rpc("quota.list", nil), &quotas); err != nil || len(quotas) != 0 {
		t.Fatalf("probe mutated quota: %v %v", quotas, err)
	}
	var usage, models []json.RawMessage
	if err := json.Unmarshal(rpc("usage.list", map[string]any{"limit": 10}), &usage); err != nil || len(usage) != 0 {
		t.Fatalf("probe mutated usage: %v %v", usage, err)
	}
	if err := json.Unmarshal(rpc("models.list", nil), &models); err != nil || len(models) != 0 {
		t.Fatalf("probe imported models: %v %v", models, err)
	}
}
