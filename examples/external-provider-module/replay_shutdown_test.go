package externalprovider

import (
	"context"
	"encoding/json"
	"io"
	"net"
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

// The frozen replay factory deliberately returns success after cancellation.
// Shutdown must still wait for that in-flight replay preparation to drain.
type shutdownReplayModule struct {
	entered, cancelled, release chan struct{}
	prepares                    atomic.Int32
	releaseOnce                 sync.Once
}

func (m *shutdownReplayModule) unblock() { m.releaseOnce.Do(func() { close(m.release) }) }

func (*shutdownReplayModule) ModuleDescriptor() provider.ModuleRef {
	return provider.ModuleRef{ID: "example.replay-shutdown", Version: "1", HostContractVersion: provider.RuntimeModuleHostContractVersion}
}

func (m *shutdownReplayModule) Register(r *provider.RuntimeRegistry) error {
	op := "example.replay-shutdown.create"
	ref := provider.Ref{Kind: "operation", ID: op, ContractVersion: 1}
	if err := r.Primitives.Operations.RegisterRawPayload(ref, "Create", "Shutdown replay fixture", json.RawMessage(`{"type":"object"}`), provider.ReplayWithKey); err != nil {
		return err
	}
	if err := r.RegisterRequestCodec(shutdownReplayCodec{m}); err != nil {
		return err
	}
	if err := r.RegisterResponseDecoder(shutdownReplayDecoder{}); err != nil {
		return err
	}
	if err := r.RegisterResponseRenderer(jobRenderer{}); err != nil {
		return err
	}
	return r.RegisterClientIngress(jobIngress{JSONOperationIngress: provider.JSONOperationIngress{CodecID: "example.replay-shutdown.json", IngressContractVersion: 1, Operation: op, OperationContractVersion: 1, Endpoints: []provider.ClientIngressRoute{{Method: http.MethodPost, Path: "/v1/replay-shutdown"}}}})
}

type shutdownReplayCodec struct{ module *shutdownReplayModule }

func (shutdownReplayCodec) ID() string { return "example.replay-shutdown.create" }
func (shutdownReplayCodec) DescribeCompatibility(in provider.CompatibilityContext) []provider.FacetMapping {
	var out []provider.FacetMapping
	for _, facet := range provider.RequiredRequestFacets(in.Request) {
		out = append(out, provider.FacetMapping{Facet: facet, Paths: []string{"operationPayload"}, Disposition: provider.FacetTranslated})
	}
	return out
}
func (c shutdownReplayCodec) Prepare(ctx context.Context, _ provider.NormalizedRequest, _ provider.Route, cred provider.Credential) (provider.UpstreamRequest, error) {
	c.module.prepares.Add(1)
	upstream, err := provider.PrepareBytesRequest(ctx, http.MethodPost, "/create", make(http.Header), []byte(`{}`), cred, provider.CredentialAuthorization(provider.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "}), true)
	if err != nil {
		return upstream, err
	}
	factory := upstream.Replay
	upstream.Replay = func(ctx context.Context, credential provider.Credential) (provider.UpstreamRequest, error) {
		c.module.prepares.Add(1)
		close(c.module.entered)
		<-ctx.Done()
		close(c.module.cancelled)
		<-c.module.release
		// Simulate an extension that returns a valid prepared request late.
		return factory(context.Background(), credential)
	}
	return upstream, nil
}

type shutdownReplayDecoder struct{}

func (shutdownReplayDecoder) ID() string { return "example.replay-shutdown.create" }
func (shutdownReplayDecoder) PossibleEvents() []provider.ResponseEventKind {
	return []provider.ResponseEventKind{provider.EventTextDelta, provider.EventResponseComplete}
}
func (shutdownReplayDecoder) ClassifyError(int, []byte) provider.ErrorClass {
	return provider.ErrorRetryable
}
func (shutdownReplayDecoder) Decode(_ context.Context, response provider.UpstreamResponse, emit func(provider.ResponseEvent) error, _ provider.StreamHooks) error {
	defer response.Body.Close()
	b, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	if err = emit(provider.ResponseEvent{Kind: provider.EventTextDelta, Text: string(b)}); err != nil {
		return err
	}
	return emit(provider.ResponseEvent{Kind: provider.EventResponseComplete})
}

func TestPublicDaemonStopCancelsAndDrainsScopedReplayPreparation(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if upstreamCalls.Add(1) == 1 { // Effect may have happened; response is ambiguous.
			hj, _ := w.(http.Hijacker)
			conn, _, err := hj.Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer upstream.Close()
	root := t.TempDir()
	manifestDir := filepath.Join(root, "providers")
	if err := os.Mkdir(manifestDir, 0o700); err != nil {
		t.Fatal(err)
	}
	const operation = "example.replay-shutdown.create"
	manifest := provider.ProviderDefinition{ContractVersion: 1, ID: "example-replay-shutdown", Version: "1", DisplayName: "Replay shutdown", DefaultCredentialType: "api_key",
		AuthModes:  []provider.AuthModeDefinition{{CredentialType: "api_key", Auth: provider.PrimitiveRef{Kind: provider.PrimitiveAuth, ID: "static-secret", ContractVersion: 1}, Presentation: provider.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "}}},
		Operations: map[provider.Operation]provider.OperationBinding{provider.Operation(operation): {Protocol: "replay_shutdown", TaskRef: &provider.Ref{Kind: "operation", ID: operation, ContractVersion: 1}, ProviderFormat: "example.replay-shutdown.wire", Endpoint: provider.PrimitiveRef{Kind: provider.PrimitiveEndpoint, ID: "http-json", ContractVersion: 1}, Transport: provider.PrimitiveRef{Kind: provider.PrimitiveTransport, ID: "http", ContractVersion: 1}, RequestCodec: provider.PrimitiveRef{Kind: provider.PrimitiveRequestCodec, ID: operation, ContractVersion: 1}, ResponseDecoder: provider.PrimitiveRef{Kind: provider.PrimitiveResponseDecoder, ID: operation, ContractVersion: 1}, Idempotency: provider.IdempotencyPolicy{Header: "Idempotency-Key", MinRetentionSeconds: 60}}},
	}
	encoded, err := provider.EncodeManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(manifestDir, "replay.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	config := daemon.Config{DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "daemon.sock"), ProviderManifestDir: manifestDir, ArtifactStoreDir: filepath.Join(root, "artifacts"), HTTPEnabled: true, HTTPAddr: "127.0.0.1:0"}
	module := &shutdownReplayModule{entered: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
	service := daemon.NewWithProviderModules(config, module)
	if err = service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	stopped := false
	defer func() {
		if !stopped {
			module.unblock()
			_ = service.Stop(context.Background())
		}
	}()
	rpc := func(method string, params map[string]any) string {
		t.Helper()
		res, e := daemon.CallIPC(context.Background(), config.IPCPath, daemon.IPCRequest{Method: method, Params: params})
		if e != nil || !res.OK {
			t.Fatalf("%s: %#v %v", method, res, e)
		}
		var v struct {
			ID string `json:"ID"`
		}
		b, _ := json.Marshal(res.Result)
		if e = json.Unmarshal(b, &v); e != nil {
			t.Fatal(e)
		}
		return v.ID
	}
	node := rpc("providers.create", map[string]any{"name": "fixture", "prefix": "replay", "baseUrl": upstream.URL, "protocol": "replay_shutdown", "definitionID": "example-replay-shutdown"})
	connection := rpc("connections.create", map[string]any{"providerNodeID": node, "name": "fixture", "credentialType": "api_key", "secret": "secret"})
	rpc("custom_models.upsert", map[string]any{"id": "replay-route", "providerNodeID": node, "kind": "custom", "externalID": "model", "displayName": "model", "connectionIds": []string{connection}})
	rpc("physical_models.upsert", map[string]any{"name": "replay-physical", "identity": map[string]any{"canonicalName": "model"}, "sources": []map[string]any{{"routeId": "replay-route", "fidelity": "exact"}}, "enabled": true})
	rpc("combo_models.upsert", map[string]any{"name": "replay-role", "members": []map[string]any{{"kind": "physical", "id": "replay-physical"}}, "discoverable": true, "enabled": true})
	boundHTTP := service.Addresses().HTTP
	request, err := http.NewRequest(http.MethodPost, "http://"+boundHTTP+"/v1/replay-shutdown", strings.NewReader(`{"model":"replay-role","operationPayload":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "client-key")
	responseDone := make(chan struct{})
	success := atomic.Bool{}
	go func() {
		defer close(responseDone)
		response, e := http.DefaultClient.Do(request)
		if e == nil {
			success.Store(response.StatusCode < 300)
			_ = response.Body.Close()
		}
	}()
	select {
	case <-module.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("replay preparation did not enter")
	}
	stopDone := make(chan error, 1)
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if cli := os.Getenv("GOBROOM_TEST_CLI"); cli != "" {
		runDeviceRaceCLI(t, cli, config.IPCPath, "status")
		runDeviceRaceCLI(t, cli, config.IPCPath, "shutdown")
		go func() { stopDone <- service.Wait(stopCtx) }()
	} else {
		go func() { stopDone <- service.Stop(stopCtx) }()
	}
	select {
	case <-module.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not cancel replay context")
	}
	select {
	case err = <-stopDone:
		t.Fatalf("Stop returned before replay preparation drained: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	module.unblock()
	select {
	case err = <-stopDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not finish after replay preparation release")
	}
	stopped = true
	select {
	case <-responseDone:
	case <-time.After(5 * time.Second):
		t.Fatal("request handler did not drain")
	}
	if success.Load() || module.prepares.Load() != 2 || upstreamCalls.Load() != 1 {
		t.Fatalf("success=%v prepares=%d upstream calls=%d", success.Load(), module.prepares.Load(), upstreamCalls.Load())
	}
	if service.Addresses().HTTP != "" {
		t.Fatal("HTTP listener remains published after Stop")
	}
	rebound, err := net.Listen("tcp", boundHTTP)
	if err != nil {
		t.Fatalf("shutdown retained the actual data listener: %v", err)
	}
	if err := rebound.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := daemon.NewWithProviderModules(config, &shutdownReplayModule{entered: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})})
	if err = reopened.Start(context.Background()); err != nil {
		t.Fatalf("reopen daemon database after Stop: %v", err)
	}
	ctx, cancelReopen := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelReopen()
	if err = reopened.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}
