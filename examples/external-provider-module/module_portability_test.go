package externalprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/pkg/daemon"
	"github.com/fm39hz/gobroom/pkg/provider"
)

type portabilityHost struct {
	config  daemon.Config
	service *daemon.Daemon
	modules []provider.RuntimeModule
}

func newPortabilityHost(t *testing.T, modules ...provider.RuntimeModule) *portabilityHost {
	return newPortabilityHostWithBundle(t, nil, modules...)
}

func newPortabilityHostWithBundle(t *testing.T, bundle []byte, modules ...provider.RuntimeModule) *portabilityHost {
	t.Helper()
	// Keep Unix socket paths independent of long subtest names.
	root, err := os.MkdirTemp("", "gobroom-portability-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	h := &portabilityHost{config: daemon.Config{DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "daemon.sock"), ArtifactStoreDir: filepath.Join(root, "artifacts"), HTTPEnabled: true, HTTPAddr: "127.0.0.1:0", HTTPToken: "client-key-4b7e"}, modules: modules}
	if bundle != nil {
		h.config.ConfigBundlePath = filepath.Join(root, "config.json")
		if err := os.WriteFile(h.config.ConfigBundlePath, bundle, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h.service = daemon.NewWithProviderModules(h.config, modules...)
	if err := h.service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := h.service.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return h
}

func (h *portabilityHost) restart(t *testing.T) {
	t.Helper()
	if err := h.service.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.service = daemon.NewWithProviderModules(h.config, h.modules...)
	if err := h.service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func portabilityCall(t *testing.T, ipc, method string, params map[string]any) daemon.IPCResponse {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := daemon.CallIPC(ctx, ipc, daemon.IPCRequest{Method: method, Params: params})
	if err != nil {
		t.Fatalf("%s transport: %v", method, err)
	}
	return r
}

func portabilityOK(t *testing.T, ipc, method string, params map[string]any) json.RawMessage {
	t.Helper()
	r := portabilityCall(t, ipc, method, params)
	if !r.OK {
		t.Fatalf("%s: %s", method, r.Error)
	}
	b, err := json.Marshal(r.Result)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func portabilityParams(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var params map[string]any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&params); err != nil {
		t.Fatal(err)
	}
	return params
}

func portabilityCLI(t *testing.T, binary, ipc string, input []byte, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, append([]string{"--ipc", ipc, "--json"}, args...)...)
	cmd.Stdin = bytes.NewReader(input)
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("installed CLI %v: %v: %s", args, err, b)
	}
	return b
}

func TestIndependentModuleBundleFreshTargetRestart(t *testing.T) {
	runModulePortability(t, "")
}

func TestInstalledCLIModuleBundleFreshTargetRestart(t *testing.T) {
	cli := os.Getenv("GOBROOM_TEST_CLI")
	if cli == "" {
		t.Skip("dedicated installed gate requires GOBROOM_TEST_CLI")
	}
	runModulePortability(t, cli)
}

func runModulePortability(t *testing.T, cli string) {
	t.Helper()
	source := newFailureClientFixture(t, http.StatusAccepted)
	exported := portabilityOK(t, source.ipc, "config.export", nil)
	if cli != "" {
		exported = portabilityCLI(t, cli, source.ipc, nil, "config-export")
	}
	var view struct {
		Version      int             `json:"version"`
		Dependencies json.RawMessage `json:"dependencies"`
		Connections  []struct {
			ID string `json:"id"`
		} `json:"connections"`
		Definitions []provider.ProviderDefinition `json:"providerDefinitions"`
	}
	if err := json.Unmarshal(exported, &view); err != nil {
		t.Fatal(err)
	}
	if view.Version != 9 || len(view.Connections) != 1 || len(view.Definitions) != 1 || len(view.Dependencies) == 0 {
		t.Fatal("incomplete source bundle")
	}
	if strings.Contains(string(exported), "private-job-credential-4b7e") {
		t.Fatal("credential entered portable bundle")
	}
	assertPortabilityPins(t, view.Dependencies)
	target := newPortabilityHostWithBundle(t, exported, JobModule{}, newFailureClientModule{})
	if target.config.ProviderManifestDir != "" {
		t.Fatal("target must not inherit source manifests")
	}
	var applied []byte
	if cli == "" {
		applied = portabilityOK(t, target.config.IPCPath, "config.apply", portabilityParams(t, exported))
	} else {
		applied = portabilityCLI(t, cli, target.config.IPCPath, exported, "config-apply")
	}
	var activation struct {
		RestartRequired bool `json:"restartRequired"`
	}
	if err := json.Unmarshal(applied, &activation); err != nil || activation.RestartRequired {
		t.Fatalf("bootstrap definition must already be active: %s err=%v", applied, err)
	}
	if source.calls.Load() != 0 {
		t.Fatal("import dispatched inference")
	}
	// Provision separately, then retain the file for a restart to prove
	// reapplication preserves the locally supplied credential.
	portabilityOK(t, target.config.IPCPath, "connections.update", map[string]any{"id": view.Connections[0].ID, "secret": "private-job-credential-4b7e"})
	target.restart(t)
	// The subsequent fresh instance must recover the portable definition from
	// SQLite, not from the original file or source manifest directory.
	target.config.ConfigBundlePath = ""
	validateWire := failureClientWireValidator(t)
	for turn := range 2 {
		if turn > 0 {
			target.restart(t)
		}
		var current struct {
			Dependencies json.RawMessage `json:"dependencies"`
		}
		if err := json.Unmarshal(portabilityOK(t, target.config.IPCPath, "config.export", nil), &current); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(portabilityParams(t, current.Dependencies), portabilityParams(t, view.Dependencies)) {
			t.Fatal("exact dependency pins changed across import/restart")
		}
		assertPortabilityModels(t, target)
		if cli != "" {
			var status struct {
				Status  string `json:"status"`
				Control uint64 `json:"controlSnapshotVersion"`
				Serving uint64 `json:"servingSnapshotVersion"`
			}
			b := portabilityCLI(t, cli, target.config.IPCPath, nil, "status")
			if err := json.Unmarshal(b, &status); err != nil || status.Status != "ok" || status.Control == 0 || status.Control != status.Serving {
				t.Fatalf("installed target status=%s err=%v", b, err)
			}
		}
		source.mode.Store(0)
		before := source.calls.Load()
		status, _, body := portabilityJobCall(t, target, "portable-success")
		var result jobEnvelope
		if err := json.Unmarshal(body, &result); err != nil || status != 200 || result.Result.ID != "client-job-42" || len(result.Artifacts) != 1 {
			t.Fatalf("imported job serving failed: HTTP%d body=%s err=%v", status, body, err)
		}
		if source.calls.Load()-before != 1 {
			t.Fatal("imported job dispatched more than once")
		}
	}
	// A real decoder failure can legitimately cool this sole route across
	// restart. Inject it only after proving both healthy restart generations.
	source.mode.Store(1)
	before := source.calls.Load()
	status, headers, body := portabilityJobCall(t, target, "portable-failure")
	assertFailureClientWire(t, validateWire, status, headers, body, "portable-failure", "failure-role", "upstream_error", "upstream_error", "upstream request failed", 502, provider.EffectAccepted)
	if source.calls.Load()-before != 1 {
		t.Fatal("imported serving replayed accepted work")
	}
}

func assertPortabilityPins(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var lock struct {
		Modules    []provider.ModuleRef `json:"modules"`
		Extensions []struct {
			Ref            provider.Ref `json:"ref"`
			Implementation string       `json:"implementationVersion"`
			Digest         string       `json:"descriptorSha256"`
		} `json:"extensions"`
		Schemas []struct {
			Ref    provider.Ref `json:"ref"`
			Digest string       `json:"sha256"`
		} `json:"schemas"`
	}
	if err := json.Unmarshal(raw, &lock); err != nil {
		t.Fatal(err)
	}
	for _, want := range []provider.ModuleRef{{ID: "example.jobs", Version: "2.0.0", HostContractVersion: 14}, {ID: "example.jobs.failure-client", Version: "1.0.0", HostContractVersion: 14}} {
		found := false
		for _, got := range lock.Modules {
			found = found || got == want
		}
		if !found {
			t.Fatalf("missing required compiled module pin: %#v", want)
		}
	}
	for _, want := range []provider.Ref{{Kind: "operation", ID: "example.jobs.create", ContractVersion: 2}, {Kind: "request_codec", ID: "example.jobs.create", ContractVersion: 1}, {Kind: "response_decoder", ID: "example.jobs.failure-client.create", ContractVersion: 2}} {
		found := false
		for _, pin := range lock.Extensions {
			if pin.Ref == want && pin.Implementation != "" && len(pin.Digest) == 64 {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing exact extension pin: %#v", want)
		}
	}
	for _, want := range []provider.Ref{{Kind: "schema", ID: "example.jobs.create.input", ContractVersion: 1}, {Kind: "schema", ID: "example.jobs.result", ContractVersion: 2}} {
		found := false
		for _, pin := range lock.Schemas {
			if pin.Ref == want && len(pin.Digest) == 64 {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing exact schema pin: %#v", want)
		}
	}
}

func assertPortabilityModels(t *testing.T, target *portabilityHost) {
	t.Helper()
	r, err := http.NewRequest(http.MethodGet, "http://"+target.service.Addresses().HTTP+"/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer client-key-4b7e")
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&list); err != nil || response.StatusCode != 200 || len(list.Data) != 1 || list.Data[0].ID != "failure-role" {
		t.Fatalf("imported published models=%#v status=%d err=%v", list, response.StatusCode, err)
	}
}

func portabilityJobCall(t *testing.T, target *portabilityHost, requestID string) (int, http.Header, []byte) {
	t.Helper()
	r, err := http.NewRequest(http.MethodPost, "http://"+target.service.Addresses().HTTP+"/v1/failure-client/jobs/create", strings.NewReader(`{"model":"failure-role","operationPayload":{"prompt":"render a fixture"}}`))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer client-key-4b7e")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Request-ID", requestID)
	r.Header.Set("Idempotency-Key", "portability-"+requestID)
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	b, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, response.Header.Clone(), b
}
