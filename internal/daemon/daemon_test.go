package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/api"
	"github.com/fm39hz/gobroom/internal/auth"
	"github.com/fm39hz/gobroom/internal/controlplane"
	"github.com/fm39hz/gobroom/internal/discovery"
	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/quota"
	runtimehealth "github.com/fm39hz/gobroom/internal/runtime"
	"github.com/fm39hz/gobroom/internal/store"
	"golang.org/x/oauth2"
)

type oauthRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f oauthRoundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type callbackOAuthFixture struct{ redirectURL string }

type daemonExplainAdapter struct{ attempts *int }
type daemonTransformFixture struct{}

func (daemonTransformFixture) Definition() kernel.TransformDefinition {
	return kernel.TransformDefinition{Ref: extensions.Ref{Kind: kernel.RequestTransformKind, ID: "fixture.daemon-transform", ContractVersion: 1}, ImplementationVersion: "1", Label: "Daemon test transform", Description: "Fixture for control-plane binding CRUD.", Stage: kernel.TransformBeforeRequirements, Effects: []kernel.TransformEffect{kernel.TransformInput}}
}
func (daemonTransformFixture) Apply(context.Context, *kernel.NormalizedRequest, json.RawMessage) error {
	return nil
}

func (daemonExplainAdapter) ID() string { return "daemon-explain-fixture" }
func (daemonExplainAdapter) PlanCompatibility(input kernel.CompatibilityContext) kernel.CompatibilityPlan {
	mappings := make([]kernel.FacetMapping, 0, len(input.Policy.RequiredFacets))
	for _, facet := range input.Policy.RequiredFacets {
		mappings = append(mappings, kernel.FacetMapping{Facet: facet, Paths: []string{"fixture"}, Disposition: kernel.FacetPreserved})
	}
	mappings = append(mappings, kernel.FacetMapping{Facet: kernel.FacetWireResponse, Paths: []string{"fixture"}, Disposition: kernel.FacetPreserved})
	return kernel.ComposeCompatibilityPlan([][]kernel.FacetMapping{mappings}, input.Policy)
}
func (a daemonExplainAdapter) Prepare(context.Context, kernel.NormalizedRequest, kernel.Route, kernel.Credential) (kernel.UpstreamRequest, error) {
	(*a.attempts)++
	return kernel.UpstreamRequest{}, nil
}
func (a daemonExplainAdapter) Execute(context.Context, kernel.UpstreamRequest) (kernel.UpstreamResponse, error) {
	(*a.attempts)++
	return kernel.UpstreamResponse{}, nil
}
func (daemonExplainAdapter) ClassifyError(int, []byte) kernel.ErrorClass { return kernel.ErrorTerminal }
func (daemonExplainAdapter) RenderResponse(context.Context, kernel.UpstreamResponse, http.ResponseWriter, normalize.Format, kernel.StreamHooks) error {
	return nil
}

func (callbackOAuthFixture) ID() string                        { return "callback-oauth-fixture" }
func (callbackOAuthFixture) SetupSchema() provider.SetupSchema { return provider.SetupSchema{} }
func (callbackOAuthFixture) Resolve(_ context.Context, input provider.AuthInput) (kernel.Credential, error) {
	return kernel.Credential{ConnectionID: input.ConnectionID, Type: input.Type, Secret: input.Secret}, nil
}
func (callbackOAuthFixture) Refresh(_ context.Context, credential kernel.Credential) (kernel.Credential, error) {
	return credential, nil
}
func (f callbackOAuthFixture) AuthorizationURL(state, verifier string) (string, error) {
	query := url.Values{"client_id": {"fixture"}, "redirect_uri": {f.redirectURL}, "state": {state}, "code_challenge": {oauth2.S256ChallengeFromVerifier(verifier)}, "code_challenge_method": {"S256"}}
	return "https://identity.test/authorize?" + query.Encode(), nil
}
func (callbackOAuthFixture) ExchangeAuthorizationCode(_ context.Context, code, verifier string) (kernel.Credential, error) {
	if code != "loopback-code" || verifier == "" {
		return kernel.Credential{}, fmt.Errorf("unexpected authorization exchange input")
	}
	return kernel.Credential{Type: "oauth2", Secret: "loopback-access", RefreshToken: "loopback-refresh"}, nil
}

type serializedDeviceAuthFixture struct {
	started chan struct{}
	release chan struct{}
	starts  atomic.Int32
}

func (*serializedDeviceAuthFixture) ID() string                        { return "fixture-device-auth" }
func (*serializedDeviceAuthFixture) SetupSchema() provider.SetupSchema { return provider.SetupSchema{} }
func (*serializedDeviceAuthFixture) Resolve(_ context.Context, input provider.AuthInput) (kernel.Credential, error) {
	return kernel.Credential{ConnectionID: input.ConnectionID, Type: input.Type, Secret: input.Secret}, nil
}
func (*serializedDeviceAuthFixture) Refresh(_ context.Context, credential kernel.Credential) (kernel.Credential, error) {
	return credential, nil
}
func (f *serializedDeviceAuthFixture) BeginDeviceAuthorization(ctx context.Context) (provider.DeviceAuthorization, error) {
	f.starts.Add(1)
	f.started <- struct{}{}
	select {
	case <-f.release:
		return provider.DeviceAuthorization{DeviceCode: "private", UserCode: "USER-CODE", VerificationURL: "https://login.test/activate", ExpiresAt: time.Now().Add(time.Minute), PollInterval: time.Second}, nil
	case <-ctx.Done():
		return provider.DeviceAuthorization{}, ctx.Err()
	}
}
func (*serializedDeviceAuthFixture) WaitForDeviceAuthorization(ctx context.Context, _ provider.DeviceAuthorization) (kernel.Credential, error) {
	<-ctx.Done()
	return kernel.Credential{}, ctx.Err()
}

func TestIPCControlCRUDUsesDaemonServices(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	registry, err := provider.NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	providerCatalog, err := registry.DefinitionCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.BuildBindings(); err != nil {
		t.Fatal(err)
	}
	extensionCatalog, err := registry.ExtensionCatalog()
	if err != nil {
		t.Fatal(err)
	}
	extensionSnapshot, err := registry.FreezeCatalog()
	if err != nil {
		t.Fatal(err)
	}
	strategyCatalog, err := kernel.NewStrategyCatalog(extensionSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{store: s, server: api.NewServer(s), providerRegistry: registry, providerCatalog: providerCatalog, strategyCatalog: strategyCatalog, strategyDefinitions: strategyCatalog.Definitions(), extensionCatalog: extensionCatalog}
	response := d.handleIPC(nil, IPCRequest{ID: "1", Method: "providers.create", Params: map[string]any{"name": "G4F", "prefix": "g4f", "baseUrl": "https://example.test/v1", "protocol": "openai_chat"}})
	if !response.OK {
		t.Fatal(response.Error)
	}
	response = d.handleIPC(nil, IPCRequest{ID: "2", Method: "providers.list"})
	if !response.OK {
		t.Fatal(response.Error)
	}
	items, ok := response.Result.([]store.ProviderNode)
	if !ok || len(items) != 1 {
		t.Fatalf("providers=%#v", response.Result)
	}
	response = d.handleIPC(nil, IPCRequest{ID: "3", Method: "strategies.list"})
	definitions, ok := response.Result.([]kernel.StrategyDefinition)
	if !response.OK || !ok || len(definitions) == 0 {
		t.Fatalf("strategy catalog=%#v error=%q", response.Result, response.Error)
	}
	var hasStickyLimit bool
	for _, definition := range definitions {
		if definition.ID == "round-robin-fallback" && len(definition.Options) == 1 && definition.Options[0].Key == "stickyLimit" {
			hasStickyLimit = true
		}
	}
	if !hasStickyLimit {
		t.Fatal("strategy catalog omitted round-robin-fallback's stickyLimit contract")
	}
	response = d.handleIPC(nil, IPCRequest{ID: "loss-ceiling-get", Method: "compatibility.loss-ceiling.get"})
	if !response.OK {
		t.Fatal(response.Error)
	}
	ceiling := kernel.LossPolicyCeiling{AllowOnly: true, Allow: []string{"reasoning.effort.clamped"}, Deny: []string{"tools.history.dropped"}}
	response = d.handleIPC(nil, IPCRequest{ID: "loss-ceiling-set", Method: "compatibility.loss-ceiling.set", Params: map[string]any{"allowOnly": ceiling.AllowOnly, "allow": ceiling.Allow, "deny": ceiling.Deny}})
	if !response.OK {
		t.Fatal(response.Error)
	}
	loadedCeiling, configured, err := s.CompatibilityLossCeiling()
	if err != nil || !configured || !reflect.DeepEqual(loadedCeiling, ceiling) {
		t.Fatalf("IPC loss ceiling=%#v configured=%v err=%v", loadedCeiling, configured, err)
	}
	if snapshotCeiling := d.server.Control().Snapshot().LossCeiling; !reflect.DeepEqual(snapshotCeiling, ceiling) {
		t.Fatalf("IPC loss ceiling did not enter the serving snapshot: %#v", snapshotCeiling)
	}
	response = d.handleIPC(nil, IPCRequest{ID: "4", Method: "providers.catalog"})
	providerDefinitions, ok := response.Result.([]provider.DefinitionMetadata)
	if !response.OK || !ok || len(providerDefinitions) == 0 {
		t.Fatalf("provider definition catalog=%#v error=%q", response.Result, response.Error)
	}
	var foundOpenAI bool
	for _, definition := range providerDefinitions {
		if definition.ID == "openai-compatible-chat" {
			foundOpenAI = definition.Auth.ID == "static-secret" && len(definition.Auth.SetupSchema.Fields) == 1
		}
	}
	if !foundOpenAI {
		t.Fatal("provider catalog did not expose generic auth setup metadata")
	}
	response = d.handleIPC(nil, IPCRequest{ID: "5", Method: "extensions.catalog"})
	view, ok := response.Result.(extensions.CatalogView)
	if !response.OK || !ok || view.Fingerprint == "" || len(view.Schemas) < 4 {
		t.Fatalf("extension catalog=%#v error=%q", response.Result, response.Error)
	}
	d.kernel = &kernel.Kernel{Transforms: kernel.NewRequestTransformRegistry(), ResponseTransforms: kernel.NewResponseTransformRegistry()}
	transform := daemonTransformFixture{}
	if err := d.kernel.Transforms.Register(transform); err != nil {
		t.Fatal(err)
	}
	binding := kernel.TransformBinding{ID: "daemon.test", TransformRef: transform.Definition().Ref, Enabled: true, Scope: kernel.TransformScope{Kind: kernel.TransformScopeDaemon}}
	invalidBinding := binding
	invalidBinding.TransformRef.ID = "fixture.missing-transform"
	response = d.handleIPC(nil, IPCRequest{ID: "transform-invalid", Method: "transform_bindings.upsert", Params: map[string]any{"id": invalidBinding.ID, "transformRef": invalidBinding.TransformRef, "enabled": invalidBinding.Enabled, "scope": invalidBinding.Scope}})
	if response.OK {
		t.Fatal("unknown transform binding unexpectedly succeeded")
	}
	bindings, err := s.TransformBindings()
	if err != nil || len(bindings) != 0 {
		t.Fatalf("invalid binding mutated storage: bindings=%#v err=%v", bindings, err)
	}
	response = d.handleIPC(nil, IPCRequest{ID: "transform-upsert", Method: "transform_bindings.upsert", Params: map[string]any{"id": binding.ID, "transformRef": binding.TransformRef, "enabled": binding.Enabled, "scope": binding.Scope}})
	if !response.OK {
		t.Fatalf("transform binding upsert failed: %s", response.Error)
	}
	response = d.handleIPC(nil, IPCRequest{ID: "transform-list", Method: "transform_bindings.list"})
	bindings, ok = response.Result.([]kernel.TransformBinding)
	if !response.OK || !ok || len(bindings) != 1 || bindings[0].ID != binding.ID {
		t.Fatalf("transform bindings=%#v error=%q", response.Result, response.Error)
	}
	response = d.handleIPC(nil, IPCRequest{ID: "transform-delete", Method: "transform_bindings.delete", Params: map[string]any{"id": binding.ID}})
	if !response.OK {
		t.Fatalf("transform binding delete failed: %s", response.Error)
	}
	bindings, err = s.TransformBindings()
	if err != nil || len(bindings) != 0 {
		t.Fatalf("transform binding remained after delete: bindings=%#v err=%v", bindings, err)
	}
}

func TestRouteExplainIPCPreflightsRequestCompatibility(t *testing.T) {
	dataStore, err := store.Open(t.TempDir() + "/route-explain.db")
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	bindings := map[normalize.Operation]kernel.RouteOperationBinding{
		normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"fixture"}},
	}
	snapshot, err := kernel.BuildSnapshot(kernel.SnapshotInput{
		PublicModels: []kernel.PublicModel{{Name: "junior", TargetRef: "combo-junior"}},
		Routes:       []kernel.Route{{ID: "route-a", Enabled: true, Protocol: kernel.ProtocolOpenAIChat, OperationBindings: bindings}},
		Nodes: []kernel.ModelNode{
			{ID: "physical-qwen", Kind: kernel.ModelPhysical, Members: []kernel.MemberRef{{Kind: kernel.MemberRoute, ID: "route-a", Fidelity: kernel.FidelityExact}}},
			{ID: "combo-junior", Kind: kernel.ModelCombo, Members: []kernel.MemberRef{{Kind: kernel.MemberModel, ID: "physical-qwen"}}},
		},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	policy := runtimehealth.NewPolicyGate()
	engine, err := kernel.New(snapshot, policy, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	attempts := 0
	engine.Adapters["fixture"] = daemonExplainAdapter{attempts: &attempts}
	service := &Daemon{store: dataStore, server: api.NewServer(dataStore), kernel: engine, policy: policy}
	response := service.handleIPC(context.Background(), IPCRequest{
		ID: "route-explain", Method: "routes.explain",
		Params: map[string]any{
			"model": "junior", "requestPath": "/v1/chat/completions",
			"request": map[string]any{"messages": []any{map[string]any{"role": "user", "content": "private fixture text"}}},
		},
	})
	if !response.OK {
		t.Fatalf("route explanation failed: %s", response.Error)
	}
	items, ok := response.Result.([]runtimehealth.RouteExplanation)
	if !ok || len(items) != 1 || len(items[0].Compatibility) != 1 {
		t.Fatalf("route compatibility missing: %#v", response.Result)
	}
	compatibility := items[0].Compatibility[0]
	if !compatibility.Compatible || compatibility.RouteID != "route-a" || compatibility.Plan.Fidelity != kernel.FidelityNative {
		t.Fatalf("unexpected route compatibility: %#v", compatibility)
	}
	if attempts != 0 {
		t.Fatalf("route explanation dispatched upstream request(s): %d", attempts)
	}
	encoded, err := json.Marshal(response.Result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private fixture text") {
		t.Fatal("route explanation leaked request content")
	}
}

func TestConfigApplyPersistsPortableProviderDefinitionAndRequiresRestart(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/portable-provider.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	activeRuntime, err := provider.NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	activeSnapshot, err := activeRuntime.FreezeCatalog()
	if err != nil {
		t.Fatal(err)
	}
	activeStrategies, err := kernel.NewStrategyCatalog(activeSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{store: s, server: api.NewServer(s), providerRegistry: activeRuntime, strategyCatalog: activeStrategies}
	definition := provider.ProviderDefinition{
		ContractVersion: 1, ID: "portable-custom-chat", Version: "1", DisplayName: "Portable custom chat",
		Auth: provider.PrimitiveRef{Kind: provider.PrimitiveAuth, ID: "static-secret", ContractVersion: 1},
		Operations: map[provider.Operation]provider.OperationBinding{provider.OperationChat: {
			Protocol: kernel.ProtocolOpenAIChat, TaskRef: provider.OperationRef(normalize.OperationChatGenerate, 1), ProviderFormat: normalize.FormatOpenAIChat,
			Endpoint:        provider.PrimitiveRef{Kind: provider.PrimitiveEndpoint, ID: "http-json", ContractVersion: 1},
			Transport:       provider.PrimitiveRef{Kind: provider.PrimitiveTransport, ID: "http", ContractVersion: 1},
			RequestCodec:    provider.PrimitiveRef{Kind: provider.PrimitiveRequestCodec, ID: "openai-chat-json", ContractVersion: 1},
			ResponseDecoder: provider.PrimitiveRef{Kind: provider.PrimitiveResponseDecoder, ID: "openai-sse", ContractVersion: 1},
		}},
	}
	candidateRuntime, err := provider.NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if err := candidateRuntime.Primitives.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if _, err := candidateRuntime.BuildBindings(); err != nil {
		t.Fatal(err)
	}
	catalog, err := candidateRuntime.FreezeCatalog()
	if err != nil {
		t.Fatal(err)
	}
	dependencies, err := catalog.LockDependencies([]extensions.Ref{provider.ProviderDefinitionRef(definition.ID, definition.ContractVersion)})
	if err != nil {
		t.Fatal(err)
	}
	bundle := controlplane.ConfigBundle{
		Version: 5, Dependencies: dependencies,
		Providers:           []store.ProviderNode{{ID: "portable-node", Name: "Portable", Prefix: "portable", BaseURL: "https://provider.test/v1", Protocol: string(kernel.ProtocolOpenAIChat), DefinitionID: definition.ID, ModelsPath: "/models", AuthMode: "api_key", Enabled: true}},
		ProviderDefinitions: []provider.ProviderDefinition{definition},
	}
	encodedBundle, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	var params map[string]any
	if err := json.Unmarshal(encodedBundle, &params); err != nil {
		t.Fatal(err)
	}
	response := d.handleIPC(context.Background(), IPCRequest{ID: "apply-portable-provider", Method: "config.apply", Params: params})
	if !response.OK {
		t.Fatalf("portable provider bundle apply failed: %s", response.Error)
	}
	result, ok := response.Result.(map[string]any)
	if !ok || result["restartRequired"] != true {
		t.Fatalf("portable definition apply did not disclose frozen-catalog restart: %#v", response.Result)
	}
	stored, err := s.ProviderDefinitions()
	if err != nil || len(stored) != 1 || !provider.SameProviderDefinition(stored[0], definition) {
		t.Fatalf("portable provider definition persistence=%#v err=%v", stored, err)
	}
	restartedRuntime, err := provider.NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range stored {
		if err := restartedRuntime.RegisterDefinitionIfAbsent(item); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := restartedRuntime.BuildBindings(); err != nil {
		t.Fatalf("persisted definition did not bind after daemon restart: %v", err)
	}
	if len(d.server.Control().Snapshot().Routes) != 0 {
		t.Fatal("old frozen runtime snapshot partially adopted a new provider definition")
	}
}

func TestDaemonLoadsPortableProviderDefinitionFromStoreBeforeRuntimeBinding(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/gobroom.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	definition := provider.ProviderDefinition{
		ContractVersion: 1, ID: "portable-startup-chat", Version: "1", DisplayName: "Portable startup chat",
		Auth: provider.PrimitiveRef{Kind: provider.PrimitiveAuth, ID: "static-secret", ContractVersion: 1},
		Operations: map[provider.Operation]provider.OperationBinding{provider.OperationChat: {
			Protocol: kernel.ProtocolOpenAIChat, TaskRef: provider.OperationRef(normalize.OperationChatGenerate, 1), ProviderFormat: normalize.FormatOpenAIChat,
			Endpoint:        provider.PrimitiveRef{Kind: provider.PrimitiveEndpoint, ID: "http-json", ContractVersion: 1},
			Transport:       provider.PrimitiveRef{Kind: provider.PrimitiveTransport, ID: "http", ContractVersion: 1},
			RequestCodec:    provider.PrimitiveRef{Kind: provider.PrimitiveRequestCodec, ID: "openai-chat-json", ContractVersion: 1},
			ResponseDecoder: provider.PrimitiveRef{Kind: provider.PrimitiveResponseDecoder, ID: "openai-sse", ContractVersion: 1},
		}},
	}
	tx, err := s.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceProviderDefinitionsInTx(tx, []provider.ProviderDefinition{definition}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	registry, err := provider.NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	daemon := &Daemon{store: s}
	if err := daemon.loadStoredProviderDefinitions(registry); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.BuildBindings(); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Primitives.Definition(definition.ID); !ok {
		t.Fatal("persisted portable provider definition was not registered on startup")
	}
	bindings, err := registry.BuildBindings()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := bindings[provider.RuntimeBindingKey(definition.ID, provider.OperationChat)]; !ok {
		t.Fatal("persisted portable provider definition did not bind its operation")
	}
}

func TestLogsListReturnsRecentStructuredRecordsWithBoundedLimit(t *testing.T) {
	d := &Daemon{}
	d.storeLog(LogRecord{At: time.Date(2026, 10, 7, 1, 0, 0, 0, time.UTC), Level: "info", Message: "older"})
	d.storeLog(LogRecord{At: time.Date(2026, 10, 7, 1, 1, 0, 0, time.UTC), Level: "info", Message: "HTTP request", RequestID: "trace-1", Method: "POST", Path: "/v1/chat/completions", Status: 200, Model: "junior", Operation: normalize.OperationChatGenerate})
	d.storeLog(LogRecord{At: time.Date(2026, 10, 7, 1, 2, 0, 0, time.UTC), Level: "warn", Message: "newest"})

	response := d.handleIPC(context.Background(), IPCRequest{ID: "logs", Method: "logs.list", Params: map[string]any{"limit": float64(2)}})
	items, ok := response.Result.([]LogRecord)
	if !response.OK || !ok || len(items) != 2 {
		t.Fatalf("logs response=%#v error=%q", response.Result, response.Error)
	}
	if items[0].RequestID != "trace-1" || items[0].Path != "/v1/chat/completions" || items[0].Model != "junior" || items[1].Message != "newest" {
		t.Fatalf("recent log window=%#v", items)
	}
	invalid := d.handleIPC(context.Background(), IPCRequest{ID: "bad-limit", Method: "logs.list", Params: map[string]any{"limit": float64(257)}})
	if invalid.OK {
		t.Fatalf("out-of-range log limit accepted: %#v", invalid)
	}
}

func TestComboIPCExposureProjectsOnlySelectedNameToOpenAIModels(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/combo-exposure.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	server := api.NewServer(s)
	strategyCatalog, err := kernel.NewBuiltinStrategyCatalog()
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{store: s, server: server, strategyCatalog: strategyCatalog}
	params := map[string]any{
		"name": "junior", "members": []any{},
		"strategy":     map[string]any{"ref": kernel.StrategyRef("ordered-fallback", 1), "config": map[string]any{}},
		"discoverable": false, "enabled": true,
	}
	response := d.handleIPC(context.Background(), IPCRequest{ID: "combo-hidden", Method: "combo_models.upsert", Params: params})
	if !response.OK {
		t.Fatalf("create hidden Combo: %s", response.Error)
	}
	getModels := func() []map[string]any {
		t.Helper()
		recorder := httptest.NewRecorder()
		server.HandlerWithOptions(api.HandlerOptions{DataPlane: true}).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET /v1/models status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		var result struct {
			Data []map[string]any `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result.Data
	}
	if models := getModels(); len(models) != 0 {
		t.Fatalf("hidden Combo leaked into /v1/models: %#v", models)
	}
	params["discoverable"] = true
	response = d.handleIPC(context.Background(), IPCRequest{ID: "combo-exposed", Method: "combo_models.upsert", Params: params})
	if !response.OK {
		t.Fatalf("expose Combo: %s", response.Error)
	}
	models := getModels()
	if len(models) != 1 || models[0]["id"] != "junior" {
		t.Fatalf("/v1/models projection=%#v, want only junior", models)
	}
}

func TestPrefixValidationUsesProtocolDefaultDefinition(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/prefix.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := (&Daemon{store: s}).validatePrefix("gemini", "", "gemini"); err != nil {
		t.Fatalf("Gemini protocol's default definition should permit its built-in prefix: %v", err)
	}
}

func TestProviderCreationDefaultsConnectionAuthFromDefinition(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/provider-auth.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	d := &Daemon{
		store: s, server: api.NewServer(s),
		providerAuthModes:   map[string]string{"oauth-provider": "oauth2"},
		providerAuthFlowIDs: map[string]string{"oauth-provider": "oauth-provider:oauth2"},
		authFlows: map[string]provider.AuthFlow{
			"oauth-provider:oauth2": provider.OAuthAuth{Config: auth.OAuthConfig{ClientID: "client", AuthURL: "https://oauth.test/authorize", TokenURL: "https://oauth.test/token"}},
		},
	}
	response := d.handleIPC(nil, IPCRequest{ID: "provider", Method: "providers.create", Params: map[string]any{
		"name": "OAuth provider", "prefix": "oauth-test", "baseUrl": "https://provider.test/v1",
		"protocol": "openai_chat", "definitionID": "oauth-provider",
	}})
	if !response.OK {
		t.Fatal(response.Error)
	}
	node, ok := response.Result.(store.ProviderNode)
	if !ok || node.AuthMode != "oauth2" {
		t.Fatalf("provider defaults=%#v", response.Result)
	}
	response = d.handleIPC(nil, IPCRequest{ID: "connection", Method: "connections.create", Params: map[string]any{
		"providerNodeID": node.ID, "name": "account", "credentialType": node.AuthMode, "secret": `{"access_token":"access","refresh_token":"refresh"}`,
	}})
	if !response.OK {
		t.Fatal(response.Error)
	}
	connections, err := s.Connections(node.ID)
	if err != nil || len(connections) != 1 || connections[0].CredentialType != "oauth2" {
		t.Fatalf("connections=%#v err=%v", connections, err)
	}
}

func TestRefreshedCredentialPersistenceRetainsOAuthClientSecret(t *testing.T) {
	credential := kernel.Credential{Secret: "new-access", RefreshToken: "new-refresh", ClientID: "client", ClientSecret: "secret", ExpiresAt: time.Now().Add(time.Hour)}
	stored, err := refreshedCredentialSecret(credential)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal([]byte(stored), &state); err != nil {
		t.Fatal(err)
	}
	if state["access_token"] != credential.Secret || state["refresh_token"] != credential.RefreshToken || state["client_id"] != credential.ClientID || state["client_secret"] != credential.ClientSecret {
		t.Fatalf("persisted OAuth state=%#v", state)
	}
}

func TestDaemonOwnsOAuthStatePKCEExchangeAndRejectsCallbackReplay(t *testing.T) {
	transport := oauthRoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		form, err := url.ParseQuery(string(body))
		if err != nil {
			return nil, err
		}
		if got := form.Get("grant_type"); got != "authorization_code" {
			t.Errorf("grant_type=%q", got)
		}
		if got := form.Get("code_verifier"); got == "" {
			t.Error("PKCE verifier missing from exchange")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"access_token":"issued-access","refresh_token":"issued-refresh","expires_in":3600,"token_type":"Bearer"}`)), Request: r}, nil
	})

	s, err := store.Open(t.TempDir() + "/oauth-coordinator.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	node, err := s.CreateProviderNode(store.CreateProviderNodeInput{Name: "OAuth", Prefix: "oauth", BaseURL: "https://provider.test/v1", Protocol: "openai_chat", DefinitionID: "oauth", AuthMode: "oauth2"})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := s.CreateConnection(store.CreateConnectionInput{ProviderNodeID: node.ID, Name: "test account", CredentialType: "oauth2"})
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{
		store: s, server: api.NewServer(s),
		providerAuthFlowIDs: map[string]string{"oauth": "oauth:oauth2"},
		authFlows: map[string]provider.AuthFlow{"oauth:oauth2": provider.OAuthAuth{Config: auth.OAuthConfig{
			ClientID: "client", AuthURL: "https://identity.test/authorize", TokenURL: "https://identity.test/token",
			RedirectURL: "https://identity.test/oauth/callback", Scopes: []string{"models.read"},
		}}},
	}
	started := d.handleIPC(context.Background(), IPCRequest{ID: "start", Method: "auth.authorization.start", Params: map[string]any{"connectionID": connection.ID}})
	if !started.OK {
		t.Fatal(started.Error)
	}
	challenge := started.Result.(authorizationChallenge)
	if challenge.CallbackMode != "manual" {
		t.Fatalf("non-loopback HTTPS callback should use explicit manual handoff: %q", challenge.CallbackMode)
	}
	duplicate := d.handleIPC(context.Background(), IPCRequest{ID: "duplicate", Method: "auth.authorization.start", Params: map[string]any{"connectionID": connection.ID}})
	if duplicate.OK {
		t.Fatal("second pending OAuth session for the same connection was accepted")
	}
	parsedURL, err := url.Parse(challenge.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	state := parsedURL.Query().Get("state")
	challengeValue := parsedURL.Query().Get("code_challenge")
	if state != challenge.SessionID || challengeValue == "" || parsedURL.Query().Get("code_challenge_method") != "S256" {
		t.Fatalf("authorization challenge does not carry daemon state + S256 PKCE: %#v", parsedURL.Query())
	}
	if _, err := d.completeAuthorization(context.Background(), challenge.SessionID, "wrong-state", "code", ""); err == nil {
		t.Fatal("wrong OAuth state was accepted")
	}
	callback := "https://identity.test/oauth/callback?code=approved-code&state=" + url.QueryEscape(state)
	wrongTarget := d.handleIPC(context.Background(), IPCRequest{ID: "wrong-target", Method: "auth.authorization.complete", Params: map[string]any{"sessionID": challenge.SessionID, "callbackURL": "https://attacker.test/oauth/callback?code=approved-code&state=" + url.QueryEscape(state)}})
	if wrongTarget.OK {
		t.Fatal("callback from an unbound redirect target was accepted")
	}
	exchangeContext := context.WithValue(context.Background(), oauth2.HTTPClient, &http.Client{Transport: transport})
	completed := d.handleIPC(exchangeContext, IPCRequest{ID: "complete", Method: "auth.authorization.complete", Params: map[string]any{"sessionID": challenge.SessionID, "callbackURL": callback}})
	if !completed.OK {
		t.Fatal(completed.Error)
	}
	stored, ok := s.ConnectionCredentialByID(connection.ID)
	if !ok {
		t.Fatal("authorized connection not available")
	}
	tokenState, err := provider.DecodeOAuthTokenState(stored.Secret)
	if err != nil || tokenState.AccessToken != "issued-access" || tokenState.RefreshToken != "issued-refresh" || tokenState.ClientID != "client" {
		t.Fatalf("persisted OAuth token state=%#v err=%v", tokenState, err)
	}
	replay := d.handleIPC(context.Background(), IPCRequest{ID: "replay", Method: "auth.authorization.complete", Params: map[string]any{"sessionID": challenge.SessionID, "state": state, "code": "approved-code"}})
	if replay.OK {
		t.Fatal("OAuth callback replay was accepted")
	}
	startedAgain := d.handleIPC(context.Background(), IPCRequest{ID: "start-again", Method: "auth.authorization.start", Params: map[string]any{"connectionID": connection.ID}})
	if !startedAgain.OK {
		t.Fatalf("new authorization after completion failed: %s", startedAgain.Error)
	}
	second := startedAgain.Result.(authorizationChallenge)
	cancelled := d.handleIPC(context.Background(), IPCRequest{ID: "cancel", Method: "auth.authorization.cancel", Params: map[string]any{"sessionID": second.SessionID}})
	if !cancelled.OK {
		t.Fatalf("cancel authorization: %s", cancelled.Error)
	}
}

func TestAuthorizationCodeLoopbackCallbackCompletesWithoutFrontendExchangeCall(t *testing.T) {
	probe, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	redirectURL := fmt.Sprintf("http://127.0.0.1:%d/oauth/callback", port)
	s, err := store.Open(t.TempDir() + "/oauth-loopback.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	node, err := s.CreateProviderNode(store.CreateProviderNodeInput{Name: "OAuth", Prefix: "oauth", BaseURL: "https://provider.test/v1", Protocol: "openai_chat", DefinitionID: "loopback-oauth", AuthMode: "oauth2"})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := s.CreateConnection(store.CreateConnectionInput{ProviderNodeID: node.ID, Name: "loopback account", CredentialType: "oauth2"})
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{
		store: s, server: api.NewServer(s),
		providerAuthFlowIDs: map[string]string{"loopback-oauth": "loopback-oauth:auth"},
		authFlows:           map[string]provider.AuthFlow{"loopback-oauth:auth": callbackOAuthFixture{redirectURL: redirectURL}},
	}
	started := d.handleIPC(context.Background(), IPCRequest{ID: "start", Method: "auth.authorization.start", Params: map[string]any{"connectionID": connection.ID}})
	if !started.OK {
		t.Fatal(started.Error)
	}
	challenge := started.Result.(authorizationChallenge)
	if challenge.CallbackMode != "loopback" {
		t.Fatalf("loopback redirect did not activate callback server: %#v", challenge)
	}
	forged := fmt.Sprintf("http://127.0.0.1:%d/attacker?error=access_denied&state=%s", port, url.QueryEscape(challenge.SessionID))
	forgedResponse, err := http.Get(forged)
	if err != nil {
		t.Fatal(err)
	}
	_ = forgedResponse.Body.Close()
	if forgedResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("forged callback target returned status %d", forgedResponse.StatusCode)
	}
	if status, err := d.authorizationStatus(challenge.SessionID); err != nil || status.Status != "awaiting_user" {
		t.Fatalf("wrong-path error callback altered session: status=%#v err=%v", status, err)
	}
	callback := redirectURL + "?code=loopback-code&state=" + url.QueryEscape(challenge.SessionID)
	response, err := http.Get(callback)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK || !strings.Contains(string(body), "Authorization complete") {
		t.Fatalf("loopback callback response: status=%d body=%q readErr=%v", response.StatusCode, body, readErr)
	}
	status := d.handleIPC(context.Background(), IPCRequest{ID: "status", Method: "auth.authorization.get", Params: map[string]any{"sessionID": challenge.SessionID}})
	view, ok := status.Result.(authorizationStatusView)
	if !status.OK || !ok || view.Status != "completed" || view.CallbackMode != "loopback" {
		t.Fatalf("authorization terminal status=%#v error=%q", status.Result, status.Error)
	}
	stored, ok := s.ConnectionCredentialByID(connection.ID)
	if !ok {
		t.Fatal("connection credential missing after callback")
	}
	token, err := provider.DecodeOAuthTokenState(stored.Secret)
	if err != nil || token.AccessToken != "loopback-access" || token.RefreshToken != "loopback-refresh" {
		t.Fatalf("loopback exchange credential=%#v err=%v", token, err)
	}
	d.oauthCallbackMu.Lock()
	remainingListeners := len(d.oauthCallbacks)
	d.oauthCallbackMu.Unlock()
	if remainingListeners != 0 {
		t.Fatalf("callback listener remained after completed session: %d", remainingListeners)
	}
	d.cancelAuthorizationSessions()
	d.closeOAuthCallbackListeners()
}

func TestDaemonOwnsDeviceAuthorizationPollingAndKeepsDeviceCodePrivate(t *testing.T) {
	var tokenRequests int
	transport := oauthRoundTripperFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/device":
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"device_code":"private-device-code","user_code":"ABCD-EFGH","verification_uri":"https://identity.test/activate","verification_uri_complete":"https://identity.test/activate?user_code=ABCD-EFGH","expires_in":60,"interval":1}`)), Request: request}, nil
		case "/token":
			body, err := io.ReadAll(request.Body)
			if err != nil {
				return nil, err
			}
			form, err := url.ParseQuery(string(body))
			if err != nil {
				return nil, err
			}
			if form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" || form.Get("device_code") != "private-device-code" {
				t.Errorf("unexpected device token request: %#v", form)
			}
			tokenRequests++
			if tokenRequests == 1 {
				return &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":"authorization_pending"}`)), Request: request}, nil
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"access_token":"device-access","refresh_token":"device-refresh","expires_in":3600,"token_type":"Bearer"}`)), Request: request}, nil
		default:
			return nil, fmt.Errorf("unexpected OAuth URL %s", request.URL)
		}
	})

	s, err := store.Open(t.TempDir() + "/oauth-device.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	node, err := s.CreateProviderNode(store.CreateProviderNodeInput{Name: "Device OAuth", Prefix: "device", BaseURL: "https://provider.test/v1", Protocol: "openai_chat", DefinitionID: "device-oauth", AuthMode: "oauth2"})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := s.CreateConnection(store.CreateConnectionInput{ProviderNodeID: node.ID, Name: "device account", CredentialType: "oauth2"})
	if err != nil {
		t.Fatal(err)
	}
	rootCtx, stop := context.WithCancel(context.WithValue(context.Background(), oauth2.HTTPClient, &http.Client{Transport: transport}))
	defer stop()
	d := &Daemon{
		store: s, server: api.NewServer(s), runCtx: rootCtx,
		providerAuthFlowIDs: map[string]string{"device-oauth": "device-oauth:oauth2"},
		authFlows: map[string]provider.AuthFlow{"device-oauth:oauth2": provider.OAuthAuth{Config: auth.OAuthConfig{
			ClientID: "device-client", DeviceAuthURL: "https://identity.test/device", TokenURL: "https://identity.test/token",
		}}},
	}
	started := d.handleIPC(rootCtx, IPCRequest{ID: "device-start", Method: "auth.device.start", Params: map[string]any{"connectionID": connection.ID}})
	if !started.OK {
		t.Fatal(started.Error)
	}
	view := started.Result.(deviceAuthorizationView)
	encodedView, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedView), "private-device-code") || view.UserCode != "ABCD-EFGH" || view.VerificationURL != "https://identity.test/activate" {
		t.Fatalf("device action leaked private code or omitted public instructions: %s", encodedView)
	}
	deadline := time.Now().Add(4 * time.Second)
	for view.Status != "completed" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		response := d.handleIPC(rootCtx, IPCRequest{ID: "device-get", Method: "auth.device.get", Params: map[string]any{"sessionID": view.SessionID}})
		if !response.OK {
			t.Fatal(response.Error)
		}
		view = response.Result.(deviceAuthorizationView)
	}
	if view.Status != "completed" || tokenRequests < 2 {
		t.Fatalf("device flow did not poll pending then complete: status=%q requests=%d error=%q", view.Status, tokenRequests, view.Error)
	}
	stored, ok := s.ConnectionCredentialByID(connection.ID)
	if !ok {
		t.Fatal("authorized connection unavailable after device flow")
	}
	tokenState, err := provider.DecodeOAuthTokenState(stored.Secret)
	if err != nil || tokenState.AccessToken != "device-access" || tokenState.RefreshToken != "device-refresh" {
		t.Fatalf("stored device token state=%#v err=%v", tokenState, err)
	}
	second := d.handleIPC(rootCtx, IPCRequest{ID: "device-start-cancel", Method: "auth.device.start", Params: map[string]any{"connectionID": connection.ID}})
	if !second.OK {
		t.Fatal(second.Error)
	}
	secondView := second.Result.(deviceAuthorizationView)
	cancelled := d.handleIPC(rootCtx, IPCRequest{ID: "device-cancel", Method: "auth.device.cancel", Params: map[string]any{"sessionID": secondView.SessionID}})
	if !cancelled.OK {
		t.Fatal(cancelled.Error)
	}
	stop()
	d.authWG.Wait()
}

func TestDeviceAuthorizationReservesConnectionBeforeStartingProviderFlow(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/oauth-device-reservation.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	node, err := s.CreateProviderNode(store.CreateProviderNodeInput{Name: "Device OAuth", Prefix: "device", BaseURL: "https://provider.test/v1", Protocol: "openai_chat", DefinitionID: "device-oauth", AuthMode: "oauth2"})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := s.CreateConnection(store.CreateConnectionInput{ProviderNodeID: node.ID, Name: "device account", CredentialType: "oauth2"})
	if err != nil {
		t.Fatal(err)
	}
	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	flow := &serializedDeviceAuthFixture{started: make(chan struct{}, 2), release: make(chan struct{})}
	d := &Daemon{
		store: s, server: api.NewServer(s), runCtx: rootCtx,
		providerAuthFlowIDs: map[string]string{"device-oauth": "device-oauth:fixture"},
		authFlows:           map[string]provider.AuthFlow{"device-oauth:fixture": flow},
	}
	firstDone := make(chan IPCResponse, 1)
	go func() {
		firstDone <- d.handleIPC(context.Background(), IPCRequest{ID: "first", Method: "auth.device.start", Params: map[string]any{"connectionID": connection.ID}})
	}()
	select {
	case <-flow.started:
	case <-time.After(time.Second):
		t.Fatal("first device authorization did not enter provider start")
	}
	second := d.handleIPC(context.Background(), IPCRequest{ID: "second", Method: "auth.device.start", Params: map[string]any{"connectionID": connection.ID}})
	if second.OK || flow.starts.Load() != 1 {
		t.Fatalf("concurrent device authorization was not serialized: response=%#v starts=%d", second, flow.starts.Load())
	}
	close(flow.release)
	first := <-firstDone
	if !first.OK {
		t.Fatalf("first device authorization failed: %s", first.Error)
	}
	view := first.Result.(deviceAuthorizationView)
	if cancelled := d.handleIPC(context.Background(), IPCRequest{ID: "cancel", Method: "auth.device.cancel", Params: map[string]any{"sessionID": view.SessionID}}); !cancelled.OK {
		t.Fatal(cancelled.Error)
	}
	cancel()
	d.authWG.Wait()
}

func TestCredentialRefreshLocksAreScopedPerConnection(t *testing.T) {
	d := &Daemon{}
	releaseFirst := d.lockCredentialRefresh("account-a")
	defer releaseFirst()
	acquiredOther := make(chan struct{})
	go func() {
		release := d.lockCredentialRefresh("account-b")
		close(acquiredOther)
		release()
	}()
	select {
	case <-acquiredOther:
	case <-time.After(time.Second):
		t.Fatal("refresh for another connection was blocked by the first connection's lock")
	}
}

func TestDaemonPersistsManifestClassifiedQuotaEvidenceAcrossRestart(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listener unavailable in test environment: %v", err)
	}
	reset := time.Now().Add(2 * time.Minute).UTC().Truncate(time.Second)
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"catalog-only-model"}]}`))
			return
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("upstream path=%q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"fault":{"reason":"PLAN_DRAINED","explanation":"weekly budget exhausted","reopens_at":"` + reset.Format(time.RFC3339) + `","period":"weekly","limit":50,"consumed":50,"remaining":0}}`))
	}))
	upstream.Listener = listener
	upstream.Start()
	defer upstream.Close()

	root := t.TempDir()
	manifestDir := filepath.Join(root, "providers")
	if err := os.MkdirAll(manifestDir, 0o700); err != nil {
		t.Fatal(err)
	}
	definition := provider.ProviderDefinition{ContractVersion: 1, ID: "quota-fixture", Version: "1", DisplayName: "Quota fixture",
		Auth: provider.PrimitiveRef{Kind: provider.PrimitiveAuth, ID: "static-secret", ContractVersion: 1},
		Operations: map[provider.Operation]provider.OperationBinding{provider.OperationChat: {
			Protocol:        kernel.ProtocolOpenAIChat,
			TaskRef:         provider.OperationRef("chat.generate", 1),
			Endpoint:        provider.PrimitiveRef{Kind: provider.PrimitiveEndpoint, ID: "http-json", ContractVersion: 1},
			Transport:       provider.PrimitiveRef{Kind: provider.PrimitiveTransport, ID: "http", ContractVersion: 1},
			RequestCodec:    provider.PrimitiveRef{Kind: provider.PrimitiveRequestCodec, ID: "openai-chat-json", ContractVersion: 1},
			ResponseDecoder: provider.PrimitiveRef{Kind: provider.PrimitiveResponseDecoder, ID: "openai-sse", ContractVersion: 1},
			ErrorClassifier: provider.PrimitiveRef{Kind: provider.PrimitiveErrorClassifier, ID: "http-json", ContractVersion: 1},
			ErrorClassifierOptions: provider.ErrorClassifierOptions{HTTPJSON: &provider.HTTPJSONErrorClassifierOptions{
				QuotaScope: kernel.ScopeConnection,
				CodePath:   "/fault/reason", MessagePath: "/fault/explanation", ResetAtPath: "/fault/reopens_at",
				WindowNamePath: "/fault/period", WindowLimitPath: "/fault/limit", WindowUsedPath: "/fault/consumed", WindowRemainingPath: "/fault/remaining",
				QuotaWindowKind: "requests", QuotaCodes: []string{"PLAN_DRAINED"}, QuotaMessageTokens: []string{"budget exhausted"},
			}},
		}, provider.OperationModels: {
			Endpoint:    provider.PrimitiveRef{Kind: provider.PrimitiveEndpoint, ID: "http-json", ContractVersion: 1},
			Transport:   provider.PrimitiveRef{Kind: provider.PrimitiveTransport, ID: "http", ContractVersion: 1},
			ModelSource: provider.PrimitiveRef{Kind: provider.PrimitiveModelSource, ID: "openai-models", ContractVersion: 1},
		}},
	}
	manifest, err := provider.EncodeProviderDefinitionJSON(definition)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "quota-fixture.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(root, "gobroom.db")
	state, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	node, err := state.CreateProviderNode(store.CreateProviderNodeInput{Name: "Quota fixture", Prefix: "quota-fixture", BaseURL: upstream.URL + "/v1", Protocol: "openai_chat", DefinitionID: definition.ID})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := state.CreateConnection(store.CreateConnectionInput{ProviderNodeID: node.ID, Name: "account", CredentialType: "api_key", Secret: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	if err := state.UpsertCatalogModel(store.UpsertCatalogModelInput{ID: "quota-route", ProviderNodeID: node.ID, Kind: "custom", ExternalID: "quota-model", DisplayName: "Quota model"}); err != nil {
		t.Fatal(err)
	}
	if err := state.UpsertPhysicalModel(store.PhysicalModel{Name: "quota-physical", Sources: []store.RouteReference{{RouteID: "quota-route", Fidelity: kernel.FidelityExact}}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := state.UpsertComboModel(store.ComboModel{Name: "quota-role", Members: []store.ModelReference{{Kind: store.PhysicalReference, ID: "quota-physical"}}, Discoverable: true, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}

	config := Config{DBPath: dbPath, IPCPath: filepath.Join(root, "run", "gobroom.sock"), ProviderManifestDir: manifestDir}
	first := New(config)
	if err := first.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	providerCatalogResponse := first.handleIPC(context.Background(), IPCRequest{ID: "provider-catalog", Method: "providers.catalog"})
	providerDefinitions, ok := providerCatalogResponse.Result.([]provider.DefinitionMetadata)
	if !providerCatalogResponse.OK || !ok {
		t.Fatalf("manifest provider catalog failed: result=%#v error=%q", providerCatalogResponse.Result, providerCatalogResponse.Error)
	}
	var cataloguedFixture bool
	for _, definition := range providerDefinitions {
		if definition.ID == "quota-fixture" {
			cataloguedFixture = len(definition.Operations) == 2 && definition.Auth.ID == "static-secret"
		}
	}
	if !cataloguedFixture {
		t.Fatal("provider definition loaded from manifest was not available to the generic control catalog")
	}
	connTest := first.handleIPC(context.Background(), IPCRequest{ID: "test-connection", Method: "connections.test", Params: map[string]any{"connectionID": connection.ID}})
	if !connTest.OK {
		t.Fatalf("connection test failed: %s", connTest.Error)
	}
	preview, ok := connTest.Result.(discovery.ConnectionTestResult)
	if !ok || preview.ConnectionID != connection.ID || preview.ModelsFound != 1 || len(preview.Preview) != 1 || preview.Preview[0].ID != "catalog-only-model" {
		t.Fatalf("connection test preview=%#v", connTest.Result)
	}
	modelPreview := first.handleIPC(context.Background(), IPCRequest{ID: "preview-models", Method: "connections.preview_models", Params: map[string]any{"connectionID": connection.ID}})
	if !modelPreview.OK {
		t.Fatalf("connection model review failed: %s", modelPreview.Error)
	}
	fullPreview, ok := modelPreview.Result.(discovery.ConnectionModelsPreview)
	if !ok || fullPreview.ConnectionID != connection.ID || fullPreview.ModelsFound != 1 || len(fullPreview.Models) != 1 || fullPreview.Models[0].ID != "catalog-only-model" {
		t.Fatalf("full connection preview=%#v", modelPreview.Result)
	}
	catalog, err := first.store.Models()
	if err != nil || len(catalog) != 1 {
		t.Fatalf("read-only connection test changed catalog: models=%#v err=%v", catalog, err)
	}
	importResult := first.handleIPC(context.Background(), IPCRequest{ID: "refresh-connection", Method: "providers.refresh_models", Params: map[string]any{"nodeID": node.ID, "connectionID": connection.ID}})
	if !importResult.OK {
		t.Fatalf("selected-connection model refresh failed: %s", importResult.Error)
	}
	catalog, err = first.store.Models()
	if err != nil || len(catalog) != 2 {
		t.Fatalf("selected-connection refresh did not import model catalog: models=%#v err=%v", catalog, err)
	}
	request, err := normalize.Map("/v1/chat/completions", http.Header{}, map[string]any{
		"model": "quota-role", "stream": false,
		"messages": []any{map[string]any{"role": "user", "content": "please answer"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	responseWriter := httptest.NewRecorder()
	if err := first.kernel.Execute(context.Background(), request.Request, kernel.Credential{}, responseWriter); err == nil || !strings.Contains(err.Error(), "no usable route") {
		t.Fatalf("expected classified upstream quota failure, got err=%v body=%s", err, responseWriter.Body.String())
	}
	var saved quota.Snapshot
	var sawHeaderWindow bool
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshots, loadErr := first.store.QuotaSnapshots()
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		for _, snapshot := range snapshots {
			if snapshot.WindowName == "weekly" {
				saved = snapshot
			}
			if snapshot.WindowName == "rate_limit" {
				sawHeaderWindow = true
			}
		}
		if saved.WindowName != "" && sawHeaderWindow {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if saved.Remaining == nil || *saved.Remaining != 0 || saved.ResetAt == nil || !saved.ResetAt.Equal(reset) || saved.WindowName != "weekly" || !sawHeaderWindow {
		t.Fatalf("daemon did not persist configured quota evidence: %#v", saved)
	}
	if err := first.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	second := New(config)
	if err := second.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer second.Stop(context.Background())
	var route kernel.Route
	for _, candidate := range second.kernel.Snapshots.Load().Routes {
		if candidate.ExternalModel == "quota-model" && candidate.CredentialID == connection.ID {
			route = candidate
			break
		}
	}
	if route.ID == "" {
		t.Fatal("restart did not rebuild the provider route")
	}
	healthState := second.policy.Health.Snapshot()[route.ID]
	if healthState.LastCause != kernel.CauseQuotaExhausted || healthState.LastScope != kernel.ScopeConnection {
		t.Fatalf("passive health cause/scope was not restored: %#v", healthState)
	}
	if second.policy.Usable(route, time.Now()) {
		t.Fatal("persisted exhausted quota should block the route after daemon restart")
	}
	if !second.policy.Usable(route, reset.Add(time.Second)) {
		t.Fatal("persisted route should become eligible after reset without health polling")
	}
}
