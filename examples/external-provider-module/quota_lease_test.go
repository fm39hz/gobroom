package externalprovider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/pkg/daemon"
	"github.com/fm39hz/gobroom/pkg/provider"
)

const quotaLeaseSourceID = "example.quota-lease-barrier"

type quotaLeaseBarrier struct {
	entered chan string
	release chan struct{}
	done    chan struct{}
	once    sync.Once
}

func (b *quotaLeaseBarrier) releaseFetch() { b.once.Do(func() { close(b.release) }) }

type quotaLeaseModule struct{ barrier *quotaLeaseBarrier }

func (m quotaLeaseModule) ModuleDescriptor() provider.ModuleRef {
	return provider.ModuleRef{ID: "example.quota-lease", Version: "1.0.0", HostContractVersion: provider.RuntimeModuleHostContractVersion}
}

func (m quotaLeaseModule) Register(runtime *provider.RuntimeRegistry) error {
	return runtime.RegisterQuotaSource(quotaLeaseSource{barrier: m.barrier})
}

type quotaLeaseSource struct{ barrier *quotaLeaseBarrier }

func (quotaLeaseSource) ID() string { return quotaLeaseSourceID }
func (s quotaLeaseSource) Fetch(ctx context.Context, request provider.QuotaRequest) ([]provider.QuotaSnapshot, error) {
	select {
	case s.barrier.entered <- request.Credential.ConnectionID:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-s.barrier.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	// Keep all network access on the host-provided, lease-guarded transport.
	defer close(s.barrier.done)
	return (provider.HTTPJSONQuotaSource{WindowName: "lease-only"}).Fetch(ctx, request)
}

// A live 429 starts opportunistic quota enrichment. Replacing its connection
// while the public module source is held must fence the eventual transport
// call and discard any quota result from the obsolete lease.
func TestExternalQuotaLeaseFencesFetchBeforeUpstreamDispatch(t *testing.T) {
	root := t.TempDir()
	manifestDir := filepath.Join(root, "providers")
	if err := os.Mkdir(manifestDir, 0o700); err != nil {
		t.Fatal(err)
	}
	barrier := &quotaLeaseBarrier{entered: make(chan string, 1), release: make(chan struct{}), done: make(chan struct{})}
	defer barrier.releaseFetch()
	definition := opaqueStateManifest(t)
	chatBinding := definition.Operations[provider.OperationChat]
	chatBinding.ErrorClassifier = provider.PrimitiveRef{Kind: provider.PrimitiveErrorClassifier, ID: "http-json", ContractVersion: 1}
	chatBinding.ErrorClassifierOptions = provider.ErrorClassifierOptions{HTTPJSON: &provider.HTTPJSONErrorClassifierOptions{QuotaHTTPStatuses: []int{http.StatusTooManyRequests}}}
	definition.Operations[provider.OperationChat] = chatBinding
	quotaBinding := definition.Operations[provider.OperationQuota]
	quotaBinding.Endpoint = provider.PrimitiveRef{Kind: provider.PrimitiveEndpoint, ID: "http-json", ContractVersion: 1}
	quotaBinding.Transport = provider.PrimitiveRef{Kind: provider.PrimitiveTransport, ID: "http", ContractVersion: 1}
	quotaBinding.QuotaSource = provider.PrimitiveRef{Kind: provider.PrimitiveQuotaSource, ID: quotaLeaseSourceID, ContractVersion: 1}
	quotaBinding.EndpointOptions = provider.EndpointOptions{Path: "/account/quota"}
	definition.Operations[provider.OperationQuota] = quotaBinding
	manifest, err := provider.EncodeManifest(definition)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "quota-lease.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}

	var inferenceCalls, quotaCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chat/completions":
			inferenceCalls.Add(1)
			if r.Header.Get("Authorization") == "Bearer quota-replacement" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"quota-fresh","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
				return
			}
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"too_many_requests"}}`))
		case "/v1/account/quota":
			quotaCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"remaining":7}`))
		default:
			t.Errorf("unexpected upstream path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	config := daemon.Config{DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "daemon.sock"), ProviderManifestDir: manifestDir, HTTPEnabled: true, HTTPAddr: "127.0.0.1:0"}
	service := daemon.NewWithProviderModules(config, opaqueStateModule{}, quotaLeaseModule{barrier: barrier})
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = service.Stop(context.Background()) }()
	defer barrier.releaseFetch()
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
	if err := json.Unmarshal(rpc("providers.create", map[string]any{"name": "Quota lease", "prefix": "quota-lease", "baseUrl": upstream.URL + "/v1", "protocol": "openai_chat", "definitionID": opaqueStateProviderID}), &node); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rpc("connections.create", map[string]any{"providerNodeID": node.ID, "name": "Quota account", "credentialType": "opaque_ticket", "secret": `{"ticket":"quota-seed","subject":"quota-subject"}`}), &connection); err != nil {
		t.Fatal(err)
	}
	_ = rpc("custom_models.upsert", map[string]any{"id": "quota-lease-route", "providerNodeID": node.ID, "kind": "custom", "externalID": "quota-model", "displayName": "Quota model", "connectionIds": []string{connection.ID}})
	_ = rpc("physical_models.upsert", map[string]any{"name": "quota-lease-physical", "identity": map[string]any{"canonicalName": "quota-model"}, "sources": []map[string]any{{"routeId": "quota-lease-route", "fidelity": "exact"}}, "enabled": true})
	_ = rpc("combo_models.upsert", map[string]any{"name": "quota-lease-role", "members": []map[string]any{{"kind": "physical", "id": "quota-lease-physical"}}, "discoverable": true, "enabled": true})
	chatURL := "http://" + service.Addresses().HTTP + "/v1/chat/completions"
	chat := func() int {
		t.Helper()
		response, requestErr := http.Post(chatURL, "application/json", strings.NewReader(`{"model":"quota-lease-role","messages":[{"role":"user","content":"hello"}]}`))
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		defer response.Body.Close()
		return response.StatusCode
	}
	if status := chat(); status < http.StatusBadRequest {
		t.Fatalf("trigger inference status=%d, want upstream rejection", status)
	}
	var fetchedConnection string
	select {
	case fetchedConnection = <-barrier.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("opportunistic quota fetch did not reach its source barrier")
	}
	if fetchedConnection != connection.ID {
		t.Fatalf("quota source connection=%q, want %q", fetchedConnection, connection.ID)
	}
	const replacement = `{"ticket":"quota-replacement","subject":"quota-subject"}`
	if cli := os.Getenv("GOBROOM_TEST_CLI"); cli != "" {
		runDeviceRaceCLI(t, cli, config.IPCPath, "connections", "update", "--id", connection.ID, "--secret", replacement)
	} else {
		_ = rpc("connections.update", map[string]any{"id": connection.ID, "secret": replacement})
	}
	barrier.releaseFetch()
	select {
	case <-barrier.done:
	case <-time.After(5 * time.Second):
		t.Fatal("quota source did not finish after the lease replacement")
	}
	if got := quotaCalls.Load(); got != 0 {
		t.Fatalf("quota upstream received %d request(s) after lease replacement", got)
	}
	// Drain the admitted background worker and persistence before asserting
	// durable absence; the source's return alone is not a publication receipt.
	if err := service.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	chatURL = "http://" + service.Addresses().HTTP + "/v1/chat/completions"
	var snapshots map[string]provider.QuotaSnapshot
	var quotaJSON []byte
	if cli := os.Getenv("GOBROOM_TEST_CLI"); cli != "" {
		quotaJSON = runDeviceRaceCLI(t, cli, config.IPCPath, "quota")
	} else {
		quotaJSON = rpc("quota.list", nil)
	}
	if err := json.Unmarshal(quotaJSON, &snapshots); err != nil {
		t.Fatal(err)
	}
	for _, snapshot := range snapshots {
		if snapshot.WindowName == "lease-only" {
			t.Fatalf("obsolete quota-source result was persisted: %#v", snapshot)
		}
	}
	if got := inferenceCalls.Load(); got != 1 {
		t.Fatalf("inference upstream calls=%d, want 1", got)
	}
	if status := chat(); status != http.StatusOK {
		t.Fatalf("fresh credential inference status=%d", status)
	}
	if inferenceCalls.Load() != 2 || quotaCalls.Load() != 0 {
		t.Fatal("fresh request did not use the new credential independently of the old quota lease")
	}
}
