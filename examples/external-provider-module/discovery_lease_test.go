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
	"sync/atomic"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/pkg/daemon"
	"github.com/fm39hz/gobroom/pkg/provider"
)

type discoveryLeaseFixture struct {
	t       *testing.T
	config  daemon.Config
	service *daemon.Daemon
}

func newDiscoveryLeaseFixture(t *testing.T, root, upstreamURL string, definition provider.ProviderDefinition, modules ...provider.RuntimeModule) (*discoveryLeaseFixture, string, string) {
	t.Helper()
	manifestDir := filepath.Join(root, "providers")
	if err := os.Mkdir(manifestDir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest, err := provider.EncodeManifest(definition)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "discovery-lease.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	config := daemon.Config{DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "daemon.sock"), ProviderManifestDir: manifestDir, HTTPEnabled: true, HTTPAddr: "127.0.0.1:0"}
	service := daemon.NewWithProviderModules(config, modules...)
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	fixture := &discoveryLeaseFixture{t: t, config: config, service: service}
	t.Cleanup(func() { _ = fixture.service.Stop(context.Background()) })
	rpc := fixture.rpc
	var node, connection struct {
		ID string `json:"ID"`
	}
	fixture.mustRPC("providers.create", map[string]any{"name": "Discovery lease", "prefix": "discovery-lease", "baseUrl": upstreamURL + "/v1", "protocol": "openai_chat", "definitionID": definition.ID}, &node)
	fixture.mustRPC("connections.create", map[string]any{"providerNodeID": node.ID, "name": "Discovery account", "credentialType": "opaque_ticket", "secret": discoveryLeaseSecret("lease-old")}, &connection)
	_ = rpc("status", nil)
	return fixture, node.ID, connection.ID
}

func discoveryLeaseSecret(ticket string) string {
	encoded, _ := json.Marshal(opaqueCredentialDocument{Ticket: ticket, Subject: "discovery-subject"})
	return string(encoded)
}

func (f *discoveryLeaseFixture) rpc(method string, params map[string]any) daemon.IPCResponse {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	response, err := daemon.CallIPC(ctx, f.config.IPCPath, daemon.IPCRequest{Method: method, Params: params})
	if err != nil && response.Error == "" {
		f.t.Fatalf("%s IPC: %v", method, err)
	}
	return response
}

func (f *discoveryLeaseFixture) mustRPC(method string, params map[string]any, target any) json.RawMessage {
	f.t.Helper()
	response := f.rpc(method, params)
	if !response.OK {
		f.t.Fatalf("%s failed: %s", method, response.Error)
	}
	encoded, err := json.Marshal(response.Result)
	if err != nil {
		f.t.Fatal(err)
	}
	if target != nil {
		if err := json.Unmarshal(encoded, target); err != nil {
			f.t.Fatalf("decode %s result %s: %v", method, encoded, err)
		}
	}
	return encoded
}

func (f *discoveryLeaseFixture) updateSecret(connectionID, secret string) {
	f.t.Helper()
	if cli := os.Getenv("GOBROOM_TEST_CLI"); cli != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		output, err := exec.CommandContext(ctx, cli, "--ipc", f.config.IPCPath, "connections", "update", "--id", connectionID, "--secret", secret).CombinedOutput()
		if err != nil {
			f.t.Fatalf("installed connection update: %v %s", err, output)
		}
		return
	}
	if response := f.rpc("connections.update", map[string]any{"id": connectionID, "secret": secret}); !response.OK {
		f.t.Fatalf("connections.update failed: %s", response.Error)
	}
}

func (f *discoveryLeaseFixture) expectCLIError(args ...string) {
	f.t.Helper()
	cli := os.Getenv("GOBROOM_TEST_CLI")
	if cli == "" {
		f.t.Fatal("expectCLIError requires GOBROOM_TEST_CLI")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, cli, append([]string{"--ipc", f.config.IPCPath}, args...)...).CombinedOutput()
	if err == nil {
		f.t.Fatalf("installed CLI unexpectedly succeeded: %s", output)
	}
}

func waitDiscoveryLeaseSignal[T any](t *testing.T, signal <-chan T, label string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func TestExternalDiscoveryLeaseFencesPaginationAndCatalogImport(t *testing.T) {
	root := t.TempDir()
	firstPageEntered := make(chan struct{})
	releaseFirstPage := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseFirstPage) }) }
	var calls atomic.Int32
	var redirectMode atomic.Bool
	var redirectCalls atomic.Int32
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirectCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer redirectTarget.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("unexpected catalog path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.Header.Get("Authorization") {
		case "Bearer lease-old":
			if calls.Add(1) == 1 {
				close(firstPageEntered)
				<-releaseFirstPage
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"first-model","display_name":"First model"}],"has_more":true,"last_id":"first"}`)
		case "Bearer lease-new":
			calls.Add(1)
			if redirectMode.Load() {
				http.Redirect(w, r, redirectTarget.URL+"/stolen", http.StatusFound)
				return
			}
			if r.URL.Query().Get("after_id") != "" {
				t.Errorf("fresh catalog unexpectedly paginated: query=%s", r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"fresh-model","display_name":"Fresh model"}],"has_more":false,"last_id":"fresh-model"}`)
		default:
			t.Errorf("catalog request used an unexpected authorization header")
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer upstream.Close()
	definition := opaqueStateManifest(t)
	modelsBinding := definition.Operations[provider.OperationModels]
	modelsBinding.ModelSource = provider.PrimitiveRef{Kind: provider.PrimitiveModelSource, ID: "anthropic-models", ContractVersion: 1}
	definition.Operations[provider.OperationModels] = modelsBinding
	fixture, nodeID, connectionID := newDiscoveryLeaseFixture(t, root, upstream.URL, definition, opaqueStateModule{}, credentialLeaseModule{barrier: &credentialLeaseBarrier{entered: make(chan string, 1), release: make(chan struct{})}})
	defer release() // unblock the HTTP handler before the upstream server closes

	refreshResult := make(chan daemon.IPCResponse, 1)
	if cli := os.Getenv("GOBROOM_TEST_CLI"); cli != "" {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			output, err := exec.CommandContext(ctx, cli, "--ipc", fixture.config.IPCPath, "connections", "refresh-models", "--node-id", nodeID, "--connection-id", connectionID).CombinedOutput()
			refreshResult <- daemon.IPCResponse{OK: err == nil, Error: string(output)}
		}()
	} else {
		go func() {
			refreshResult <- fixture.rpc("providers.refresh_models", map[string]any{"nodeID": nodeID, "connectionID": connectionID})
		}()
	}
	waitDiscoveryLeaseSignal(t, firstPageEntered, "first catalog page")
	fixture.updateSecret(connectionID, discoveryLeaseSecret("lease-new"))
	// The control plane remains responsive while the discovery response is held.
	fixture.mustRPC("status", nil, nil)
	release()
	select {
	case response := <-refreshResult:
		if response.OK {
			t.Fatalf("refresh using replaced lease unexpectedly succeeded: %s", response.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fenced catalog refresh did not return after releasing page one")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("obsolete refresh made %d HTTP requests, want only its blocked first page", got)
	}
	assertEmptyCatalog := func() {
		t.Helper()
		for _, method := range []string{"models.list", "discovered_models.list"} {
			params := map[string]any(nil)
			if method == "discovered_models.list" {
				params = map[string]any{"providerNodeID": nodeID}
			}
			encoded := fixture.mustRPC(method, params, nil)
			var rows []json.RawMessage
			if err := json.Unmarshal(encoded, &rows); err != nil || len(rows) != 0 {
				t.Fatalf("fenced refresh mutated %s: %s (%v)", method, encoded, err)
			}
		}
	}
	assertEmptyCatalog()

	var preview []byte
	if cli := os.Getenv("GOBROOM_TEST_CLI"); cli != "" {
		before := calls.Load()
		runDeviceRaceCLI(t, cli, fixture.config.IPCPath, "connections", "refresh-models", "--node-id", nodeID, "--connection-id", connectionID)
		if calls.Load() != before+1 {
			t.Fatalf("fresh installed-CLI refresh made %d requests, want one", calls.Load()-before)
		}
		preview = runDeviceRaceCLI(t, cli, fixture.config.IPCPath, "connections", "preview-models", "--connection-id", connectionID)
	} else {
		response := fixture.rpc("providers.refresh_models", map[string]any{"nodeID": nodeID, "connectionID": connectionID})
		if !response.OK {
			t.Fatalf("fresh catalog refresh failed: %s", response.Error)
		}
		if calls.Load() != 2 {
			t.Fatal("fresh IPC refresh made more than one request")
		}
		preview = fixture.mustRPC("connections.preview_models", map[string]any{"connectionID": connectionID}, nil)
	}
	if !strings.Contains(string(preview), "fresh-model") || strings.Contains(string(preview), "first-model") {
		t.Fatalf("fresh preview has unexpected catalog: %s", preview)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("stale page + fresh import + explicit preview made %d HTTP requests, want exactly three", got)
	}
	var discovered []json.RawMessage
	if err := json.Unmarshal(fixture.mustRPC("discovered_models.list", map[string]any{"providerNodeID": nodeID}, nil), &discovered); err != nil || len(discovered) != 1 || !strings.Contains(string(discovered[0]), "fresh-model") {
		t.Fatalf("fresh import not persisted in discovered models: %s (%v)", discovered, err)
	}
	// The guarded client must not follow a response to another issuer. Check
	// this through either public CLI or IPC path, then verify the target stayed
	// untouched.
	redirectMode.Store(true)
	before := calls.Load()
	if cli := os.Getenv("GOBROOM_TEST_CLI"); cli != "" {
		// runDeviceRaceCLI treats expected daemon rejections as failures.
		fixture.expectCLIError("connections", "test", "--connection-id", connectionID)
	} else if response := fixture.rpc("connections.test", map[string]any{"connectionID": connectionID}); response.OK {
		t.Fatal("model-list test unexpectedly accepted a redirect response")
	}
	if calls.Load() != before+1 || redirectCalls.Load() != 0 {
		t.Fatalf("redirect handling: model-source requests=%d redirect target requests=%d", calls.Load()-before, redirectCalls.Load())
	}
	if err := fixture.service.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	fixture.service = daemon.NewWithProviderModules(fixture.config, opaqueStateModule{}, credentialLeaseModule{barrier: &credentialLeaseBarrier{entered: make(chan string, 1), release: make(chan struct{})}})
	if err := fixture.service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	var afterRestart []json.RawMessage
	if err := json.Unmarshal(fixture.mustRPC("discovered_models.list", map[string]any{"providerNodeID": nodeID}, nil), &afterRestart); err != nil || len(afterRestart) != 1 || !strings.Contains(string(afterRestart[0]), "fresh-model") || strings.Contains(string(afterRestart[0]), "first-model") {
		t.Fatalf("persisted catalog after restart=%s (%v)", afterRestart, err)
	}
}

func TestExternalInferenceProbeLeaseFencesPreparedCredential(t *testing.T) {
	root := t.TempDir()
	barrier := &credentialLeaseBarrier{entered: make(chan string, 1), release: make(chan struct{})}
	var releaseOnce sync.Once
	releasePreparation := func() { releaseOnce.Do(func() { close(barrier.release) }) }
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer probe-new" {
			t.Errorf("inference probe used unexpected endpoint or credential: %s %q", r.URL.Path, r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"probe","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	definition := opaqueStateManifest(t)
	chatBinding := definition.Operations[provider.OperationChat]
	chatBinding.RequestCodec = provider.PrimitiveRef{Kind: provider.PrimitiveRequestCodec, ID: credentialLeaseCodecID, ContractVersion: 1}
	definition.Operations[provider.OperationChat] = chatBinding
	fixture, nodeID, connectionID := newDiscoveryLeaseFixture(t, root, upstream.URL, definition, opaqueStateModule{}, credentialLeaseModule{barrier: barrier})
	defer releasePreparation()
	connectionIDs := []string{connectionID}
	_ = fixture.mustRPC("custom_models.upsert", map[string]any{"id": "probe-route", "providerNodeID": nodeID, "kind": "custom", "externalID": "probe-model", "displayName": "Probe model", "connectionIds": connectionIDs}, nil)
	_ = fixture.mustRPC("physical_models.upsert", map[string]any{"name": "probe-physical", "identity": map[string]any{"canonicalName": "probe-model"}, "sources": []map[string]any{{"routeId": "probe-route", "fidelity": "exact"}}, "enabled": true}, nil)
	_ = fixture.mustRPC("combo_models.upsert", map[string]any{"name": "probe-role", "members": []map[string]any{{"kind": "physical", "id": "probe-physical"}}, "discoverable": true, "enabled": true}, nil)

	probeResult := make(chan daemon.IPCResponse, 1)
	if cli := os.Getenv("GOBROOM_TEST_CLI"); cli != "" {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			output, err := exec.CommandContext(ctx, cli, "--ipc", fixture.config.IPCPath, "connections", "test-inference", "--connection-id", connectionID, "--model-id", "probe-model").CombinedOutput()
			probeResult <- daemon.IPCResponse{OK: err == nil, Error: string(output)}
		}()
	} else {
		go func() {
			probeResult <- fixture.rpc("connections.test_inference", map[string]any{"connectionID": connectionID, "modelID": "probe-model"})
		}()
	}
	waitDiscoveryLeaseSignal(t, barrier.entered, "inference codec preparation")
	fixture.updateSecret(connectionID, discoveryLeaseSecret("probe-new"))
	fixture.mustRPC("status", nil, nil)
	releasePreparation()
	select {
	case response := <-probeResult:
		if response.OK {
			t.Fatalf("probe with replaced prepared credential unexpectedly succeeded: %s", response.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fenced inference probe did not return after preparation resumed")
	}
	if got := upstreamCalls.Load(); got != 0 {
		t.Fatalf("stale inference probe sent %d upstream request(s), want zero", got)
	}
	if cli := os.Getenv("GOBROOM_TEST_CLI"); cli != "" {
		runDeviceRaceCLI(t, cli, fixture.config.IPCPath, "connections", "test-inference", "--connection-id", connectionID, "--model-id", "probe-model")
	} else {
		response := fixture.rpc("connections.test_inference", map[string]any{"connectionID": connectionID, "modelID": "probe-model"})
		if !response.OK {
			t.Fatalf("fresh inference probe failed: %s", response.Error)
		}
	}
	if got := upstreamCalls.Load(); got != 1 {
		t.Fatalf("fresh inference probe dispatched %d requests, want exactly one", got)
	}
}
