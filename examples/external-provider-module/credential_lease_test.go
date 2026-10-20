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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/pkg/daemon"
	"github.com/fm39hz/gobroom/pkg/provider"
)

const credentialLeaseCodecID = "example.credential-lease-barrier"

type credentialLeaseBarrier struct {
	entered chan string
	release chan struct{}
	blocked atomic.Bool
}

type credentialLeaseModule struct{ barrier *credentialLeaseBarrier }

func (m credentialLeaseModule) ModuleDescriptor() provider.ModuleRef {
	return provider.ModuleRef{ID: "example.credential-lease", Version: "1.0.0", HostContractVersion: provider.RuntimeModuleHostContractVersion}
}

func (m credentialLeaseModule) Register(runtime *provider.RuntimeRegistry) error {
	return runtime.RegisterRequestCodec(credentialLeaseRequestCodec{barrier: m.barrier})
}

type credentialLeaseRequestCodec struct{ barrier *credentialLeaseBarrier }

func (credentialLeaseRequestCodec) ID() string { return credentialLeaseCodecID }
func (credentialLeaseRequestCodec) DescribeCompatibility(provider.CompatibilityContext) []provider.FacetMapping {
	return []provider.FacetMapping{{Facet: provider.FacetWireRequest, Paths: []string{"chat"}, Disposition: provider.FacetTranslated}}
}
func (c credentialLeaseRequestCodec) Prepare(ctx context.Context, request provider.NormalizedRequest, route provider.Route, credential provider.Credential) (provider.UpstreamRequest, error) {
	if c.barrier.blocked.CompareAndSwap(false, true) {
		select {
		case c.barrier.entered <- credential.ConnectionID:
		case <-ctx.Done():
			return provider.UpstreamRequest{}, ctx.Err()
		}
		select {
		case <-c.barrier.release:
		case <-ctx.Done():
			return provider.UpstreamRequest{}, ctx.Err()
		}
	}
	payload, err := json.Marshal(map[string]any{"model": route.ExternalModel, "messages": request.Raw["messages"]})
	if err != nil {
		return provider.UpstreamRequest{}, err
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	return provider.PrepareBytesRequest(ctx, http.MethodPost, "/chat/completions", headers, payload, credential,
		provider.CredentialAuthorization(route.CredentialPresentation), false)
}

// This public-daemon race replaces a connection while an external module's
// request codec is still preparing wire data. The final dispatch fence must
// reject the already-resolved credential before the shared HTTP transport.
func TestExternalCredentialLeaseFencesPreparedCredentialBeforeUpstreamDispatch(t *testing.T) {
	root := t.TempDir()
	manifestDir := filepath.Join(root, "providers")
	if err := os.Mkdir(manifestDir, 0o700); err != nil {
		t.Fatal(err)
	}

	barrier := &credentialLeaseBarrier{entered: make(chan string, 1), release: make(chan struct{})}
	var releaseOnce sync.Once
	releasePreparation := func() { releaseOnce.Do(func() { close(barrier.release) }) }
	definition := opaqueStateManifest(t)
	binding := definition.Operations[provider.OperationChat]
	binding.RequestCodec = provider.PrimitiveRef{Kind: provider.PrimitiveRequestCodec, ID: credentialLeaseCodecID, ContractVersion: 1}
	definition.Operations[provider.OperationChat] = binding
	manifest, err := provider.EncodeManifest(definition)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "credential-lease.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}

	var upstreamCalls atomic.Int32
	observedAuthorization := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		select {
		case observedAuthorization <- r.Header.Get("Authorization"):
		default:
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected upstream path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-lease","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()

	config := daemon.Config{DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "daemon.sock"), ProviderManifestDir: manifestDir, HTTPEnabled: true, HTTPAddr: "127.0.0.1:0"}
	service := daemon.NewWithProviderModules(config, opaqueStateModule{}, credentialLeaseModule{barrier: barrier})
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = service.Stop(context.Background()) }()
	defer releasePreparation()

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
	var node struct {
		ID string `json:"ID"`
	}
	if err := json.Unmarshal(rpc("providers.create", map[string]any{"name": "Lease fence", "prefix": "lease-fence", "baseUrl": upstream.URL + "/v1", "protocol": "openai_chat", "definitionID": definition.ID}), &node); err != nil {
		t.Fatal(err)
	}
	const seed = `{"ticket":"lease-seed","subject":"fixture-subject"}`
	var connections []struct {
		ID string `json:"ID"`
	}
	for _, name := range []string{"Lease account A", "Lease account B"} {
		var connection struct {
			ID string `json:"ID"`
		}
		if err := json.Unmarshal(rpc("connections.create", map[string]any{"providerNodeID": node.ID, "name": name, "credentialType": "opaque_ticket", "secret": seed}), &connection); err != nil {
			t.Fatal(err)
		}
		connections = append(connections, connection)
	}
	connectionIDs := []string{connections[0].ID, connections[1].ID}
	_ = rpc("custom_models.upsert", map[string]any{"id": "credential-lease-route", "providerNodeID": node.ID, "kind": "custom", "externalID": "lease-test", "displayName": "Lease test", "connectionIds": connectionIDs})
	_ = rpc("physical_models.upsert", map[string]any{"name": "credential-lease-physical", "identity": map[string]any{"canonicalName": "lease-test"}, "sources": []map[string]any{{"routeId": "credential-lease-route", "fidelity": "exact"}}, "enabled": true})
	_ = rpc("combo_models.upsert", map[string]any{"name": "credential-lease-role", "members": []map[string]any{{"kind": "physical", "id": "credential-lease-physical"}}, "discoverable": true, "enabled": true})

	chatURL := "http://" + service.Addresses().HTTP + "/v1/chat/completions"
	chatRequest := `{"model":"credential-lease-role","messages":[{"role":"user","content":"hello"}]}`
	firstResult := make(chan int, 1)
	go func() {
		response, requestErr := http.Post(chatURL, "application/json", strings.NewReader(chatRequest))
		if requestErr != nil {
			firstResult <- 0
			return
		}
		_ = response.Body.Close()
		firstResult <- response.StatusCode
	}()

	var preparedConnection string
	select {
	case preparedConnection = <-barrier.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("request codec did not reach its preparation barrier")
	}
	if preparedConnection == "" {
		t.Fatal("request codec received no connection binding")
	}
	// Exercise the installed CLI when provided, otherwise use the daemon's
	// public connections.update control method while preparation is blocked.
	const replacement = `{"ticket":"lease-replacement","subject":"fixture-subject"}`
	updateSecret := func(connectionID string) {
		t.Helper()
		if cli := os.Getenv("GOBROOM_TEST_CLI"); cli != "" {
			runDeviceRaceCLI(t, cli, config.IPCPath, "connections", "update", "--id", connectionID, "--secret", replacement)
		} else {
			_ = rpc("connections.update", map[string]any{"id": connectionID, "secret": replacement})
		}
	}
	updateSecret(preparedConnection)
	for _, connection := range connections {
		if connection.ID != preparedConnection {
			updateSecret(connection.ID)
		}
	}
	// A control request must complete even while the data-plane codec is held.
	_ = rpc("status", nil)
	releasePreparation()

	select {
	case status := <-firstResult:
		if status < http.StatusBadRequest {
			t.Fatalf("in-flight request status=%d, want credential-fence rejection", status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight request did not finish after preparation resumed")
	}
	if got := upstreamCalls.Load(); got != 0 {
		t.Fatalf("upstream saw %d request(s) after the prepared credential was replaced", got)
	}

	fresh, err := http.Post(chatURL, "application/json", strings.NewReader(chatRequest))
	if err != nil {
		t.Fatal(err)
	}
	_ = fresh.Body.Close()
	if fresh.StatusCode != http.StatusOK {
		t.Fatalf("fresh inference status=%d, want success", fresh.StatusCode)
	}
	if got := upstreamCalls.Load(); got != 1 {
		t.Fatalf("upstream request count=%d after fresh inference, want exactly one", got)
	}
	select {
	case got := <-observedAuthorization:
		if got != "Bearer lease-replacement" {
			t.Fatalf("fresh inference authorization=%q, want the replacement credential", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upstream did not report authorization for fresh inference")
	}
}
