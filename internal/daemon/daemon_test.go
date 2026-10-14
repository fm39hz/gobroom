package daemon

import (
	"bufio"
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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	openai "github.com/fm39hz/gobroom/internal/adapter/openai"
	"github.com/fm39hz/gobroom/internal/api"
	"github.com/fm39hz/gobroom/internal/auth"
	"github.com/fm39hz/gobroom/internal/controlplane"
	"github.com/fm39hz/gobroom/internal/discovery"
	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/operations"
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

type streamingDaemonAdapter struct {
	started chan struct{}
	release <-chan struct{}
}

type pendingDaemonDeviceAuth struct{ pollStarted chan struct{} }

func (daemonTransformFixture) Definition() kernel.TransformDefinition {
	return kernel.TransformDefinition{Ref: extensions.Ref{Kind: kernel.RequestTransformKind, ID: "fixture.daemon-transform", ContractVersion: 1}, ImplementationVersion: "1", Label: "Daemon test transform", Description: "Fixture for control-plane binding CRUD.", Stage: kernel.TransformBeforeRequirements, Effects: []kernel.TransformEffect{kernel.TransformInput}}
}
func (daemonTransformFixture) Apply(context.Context, *kernel.NormalizedRequest, json.RawMessage) error {
	return nil
}

func (streamingDaemonAdapter) ID() string { return "daemon-stream-control-fixture" }
func (streamingDaemonAdapter) PlanCompatibility(input kernel.CompatibilityContext) kernel.CompatibilityPlan {
	mappings := make([]kernel.FacetMapping, 0, len(input.Policy.RequiredFacets)+1)
	for _, facet := range input.Policy.RequiredFacets {
		mappings = append(mappings, kernel.FacetMapping{Facet: facet, Paths: []string{"fixture"}, Disposition: kernel.FacetPreserved})
	}
	mappings = append(mappings, kernel.FacetMapping{Facet: kernel.FacetWireResponse, Paths: []string{"fixture"}, Disposition: kernel.FacetPreserved})
	return kernel.ComposeCompatibilityPlan([][]kernel.FacetMapping{mappings}, input.Policy)
}
func (streamingDaemonAdapter) Prepare(context.Context, kernel.NormalizedRequest, kernel.Route, kernel.Credential) (kernel.UpstreamRequest, error) {
	return kernel.UpstreamRequest{Method: http.MethodPost, URL: "test://stream"}, nil
}
func (streamingDaemonAdapter) Execute(context.Context, kernel.UpstreamRequest) (kernel.UpstreamResponse, error) {
	return kernel.UpstreamResponse{Status: http.StatusOK, Body: http.NoBody}, nil
}
func (streamingDaemonAdapter) ClassifyError(int, []byte) kernel.ErrorClass {
	return kernel.ErrorTerminal
}
func (a streamingDaemonAdapter) RenderResponse(ctx context.Context, _ kernel.UpstreamResponse, writer http.ResponseWriter, _ normalize.Format, hooks kernel.StreamHooks) error {
	writer.Header().Set("Content-Type", "text/event-stream")
	if hooks.OnFirstByte != nil {
		hooks.OnFirstByte(time.Now())
	}
	if _, err := io.WriteString(writer, "data: stream-open\n\n"); err != nil {
		return err
	}
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
	close(a.started)
	select {
	case <-a.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	if _, err := io.WriteString(writer, "data: [DONE]\n\n"); err != nil {
		return err
	}
	if hooks.OnComplete != nil {
		hooks.OnComplete(kernel.UsageEvent{Status: "ok"})
	}
	return nil
}

func (*pendingDaemonDeviceAuth) ID() string                        { return "daemon-pending-device-auth" }
func (*pendingDaemonDeviceAuth) SetupSchema() provider.SetupSchema { return provider.SetupSchema{} }
func (*pendingDaemonDeviceAuth) Resolve(_ context.Context, input provider.AuthInput) (kernel.Credential, error) {
	return kernel.Credential{ConnectionID: input.ConnectionID, Type: input.Type, Secret: input.Secret}, nil
}
func (*pendingDaemonDeviceAuth) Refresh(_ context.Context, credential kernel.Credential) (kernel.Credential, error) {
	return credential, nil
}
func (*pendingDaemonDeviceAuth) BeginDeviceAuthorization(context.Context) (provider.DeviceAuthorization, error) {
	return provider.DeviceAuthorization{DeviceCode: "ipc-private-code", UserCode: "IPC-CODE", VerificationURL: "https://identity.test/device", ExpiresAt: time.Now().Add(time.Minute), PollInterval: time.Second}, nil
}
func (f *pendingDaemonDeviceAuth) WaitForDeviceAuthorization(ctx context.Context, _ provider.DeviceAuthorization) (kernel.Credential, error) {
	close(f.pollStarted)
	<-ctx.Done()
	return kernel.Credential{}, ctx.Err()
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

type blockingCallbackOAuthFixture struct {
	callbackOAuthFixture
	entered chan struct{}
	release chan struct{}
}

func (f *blockingCallbackOAuthFixture) ExchangeAuthorizationCode(ctx context.Context, code, verifier string) (kernel.Credential, error) {
	close(f.entered)
	select {
	case <-f.release:
		return kernel.Credential{Type: "oauth2", Secret: "stale-issued-access", RefreshToken: "stale-issued-refresh"}, nil
	case <-ctx.Done():
		return kernel.Credential{}, ctx.Err()
	}
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
	runtimeBindings, err := registry.BuildBindings()
	if err != nil {
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
	d := &Daemon{store: s, server: api.NewServer(s), providerRegistry: registry, providerCatalog: providerCatalog, strategyCatalog: strategyCatalog, strategyDefinitions: strategyCatalog.Definitions(), extensionCatalog: extensionCatalog, authFlows: registry.AuthFlowsForBindings(runtimeBindings), providerAuthModes: registry.DefaultCredentialTypes(), providerAuthFlowIDs: registry.AuthFlowIDsByDefinition()}
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
			foundOpenAI = len(definition.AuthModes) == 1 && definition.AuthModes[0].ID == "static-secret" && len(definition.AuthModes[0].SetupSchema.Fields) == 1
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
		DefaultCredentialType: "api_key", AuthModes: []provider.AuthModeDefinition{{CredentialType: "api_key", Auth: provider.PrimitiveRef{Kind: provider.PrimitiveAuth, ID: "static-secret", ContractVersion: 1}, Presentation: kernel.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "}}},
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
		Version: 7, Dependencies: dependencies,
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
		DefaultCredentialType: "api_key", AuthModes: []provider.AuthModeDefinition{{CredentialType: "api_key", Auth: provider.PrimitiveRef{Kind: provider.PrimitiveAuth, ID: "static-secret", ContractVersion: 1}, Presentation: kernel.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "}}},
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
		providerAuthFlowIDs: map[string]map[string]string{"oauth-provider": {"oauth2": "oauth-provider:oauth2"}},
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

func TestConnectionCanBeCreatedWithoutTokenOnlyForInteractiveOAuth(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/oauth-bootstrap.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	node, err := s.CreateProviderNode(store.CreateProviderNodeInput{Name: "Dual-auth provider", Prefix: "device", BaseURL: "https://provider.test/v1", Protocol: "openai_chat", DefinitionID: "device-oauth", AuthMode: "api_key"})
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{store: s,
		providerAuthFlowIDs: map[string]map[string]string{"device-oauth": {"api_key": "device-oauth:api_key", "oauth2": "device-oauth:oauth2"}, "token-only": {"oauth2": "token-only:oauth2"}},
		authFlows: map[string]provider.AuthFlow{
			"device-oauth:api_key": provider.StaticSecretAuth{},
			"device-oauth:oauth2":  provider.OAuthAuth{Config: auth.OAuthConfig{ClientID: "client", AuthURL: "https://identity.test/auth", TokenURL: "https://identity.test/token", DeviceAuthURL: "https://identity.test/device"}},
			"token-only:oauth2":    provider.OAuthAuth{Config: auth.OAuthConfig{ClientID: "client", AuthURL: "https://identity.test/auth", TokenURL: "https://identity.test/token"}},
		},
	}
	if err := d.validateConnectionSecret(context.Background(), node.ID, "api_key", "platform-key"); err != nil {
		t.Fatalf("API-key connection should be accepted beside OAuth: %v", err)
	}
	apiConnection, err := s.CreateConnection(store.CreateConnectionInput{ProviderNodeID: node.ID, Name: "platform key", CredentialType: "api_key", Secret: "platform-key"})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.validateConnectionSecret(context.Background(), node.ID, "oauth2", ""); err != nil {
		t.Fatalf("interactive OAuth should permit an uninitialized connection: %v", err)
	}
	connection, err := s.CreateConnection(store.CreateConnectionInput{ProviderNodeID: node.ID, Name: "new OAuth account", CredentialType: "oauth2"})
	if err != nil {
		t.Fatal(err)
	}
	if connection.ID == "" {
		t.Fatal("empty-token OAuth connection was not persisted")
	}
	if err := d.validateConnectionSecret(context.Background(), node.ID, "oauth2", ""); err != nil {
		t.Fatalf("empty OAuth secret changed validation outcome after persist: %v", err)
	}
	if err := d.validateConnectionSecret(context.Background(), node.ID, "api_key", "platform-key"); err != nil || apiConnection.ID == connection.ID {
		t.Fatalf("dual auth connection modes were not independently retained: api=%#v oauth=%#v err=%v", apiConnection, connection, err)
	}
	tokenOnlyNode, err := s.CreateProviderNode(store.CreateProviderNodeInput{Name: "Token-only OAuth", Prefix: "token-only", BaseURL: "https://provider.test/v1", Protocol: "openai_chat", DefinitionID: "token-only", AuthMode: "oauth2"})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.validateConnectionSecret(context.Background(), tokenOnlyNode.ID, "oauth2", ""); err == nil {
		t.Fatal("OAuth flow without an interactive endpoint accepted an empty credential")
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
		providerAuthFlowIDs: map[string]map[string]string{"oauth": {"oauth2": "oauth:oauth2"}},
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
	deniedChallenge := startedAgain.Result.(authorizationChallenge)
	deniedCallback := "https://identity.test/oauth/callback?error=access_denied&state=" + url.QueryEscape(deniedChallenge.SessionID)
	denied := d.handleIPC(context.Background(), IPCRequest{ID: "denied", Method: "auth.authorization.complete", Params: map[string]any{"sessionID": deniedChallenge.SessionID, "callbackURL": deniedCallback}})
	if denied.OK || !strings.Contains(denied.Error, "denied") {
		t.Fatalf("manual OAuth denial should be reported to CLI: %#v", denied)
	}
	deniedStatus := d.handleIPC(context.Background(), IPCRequest{ID: "denied-status", Method: "auth.authorization.get", Params: map[string]any{"sessionID": deniedChallenge.SessionID}})
	deniedView, deniedStatusOK := deniedStatus.Result.(authorizationStatusView)
	if !deniedStatus.OK || !deniedStatusOK || deniedView.Status != "denied" {
		t.Fatalf("manual OAuth denial did not retain terminal state: %#v error=%q", deniedStatus.Result, deniedStatus.Error)
	}
	thirdStart := d.handleIPC(context.Background(), IPCRequest{ID: "third-start", Method: "auth.authorization.start", Params: map[string]any{"connectionID": connection.ID}})
	if !thirdStart.OK {
		t.Fatalf("new authorization after denial failed: %s", thirdStart.Error)
	}
	third := thirdStart.Result.(authorizationChallenge)
	cancelled := d.handleIPC(context.Background(), IPCRequest{ID: "cancel", Method: "auth.authorization.cancel", Params: map[string]any{"sessionID": third.SessionID}})
	if !cancelled.OK {
		t.Fatalf("cancel authorization: %s", cancelled.Error)
	}
}

func TestAuthorizationCodeExchangeCannotOverwriteNewerConnectionCredential(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/oauth-credential-generation.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	node, err := s.CreateProviderNode(store.CreateProviderNodeInput{Name: "OAuth", Prefix: "oauth", BaseURL: "https://provider.test/v1", Protocol: "openai_chat", DefinitionID: "oauth", AuthMode: "oauth2"})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := s.CreateConnection(store.CreateConnectionInput{ProviderNodeID: node.ID, Name: "account", CredentialType: "oauth2", Secret: "credential-generation-old"})
	if err != nil {
		t.Fatal(err)
	}
	flow := &blockingCallbackOAuthFixture{callbackOAuthFixture: callbackOAuthFixture{redirectURL: "https://identity.test/oauth/callback"}, entered: make(chan struct{}), release: make(chan struct{})}
	d := &Daemon{
		store: s, server: api.NewServer(s),
		providerAuthFlowIDs: map[string]map[string]string{"oauth": {"oauth2": "oauth:oauth2"}},
		authFlows:           map[string]provider.AuthFlow{"oauth:oauth2": flow},
	}
	challenge, err := d.startAuthorization(connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(challenge.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	state := parsed.Query().Get("state")
	callback := "https://identity.test/oauth/callback?code=approved&state=" + url.QueryEscape(state)
	completed := make(chan error, 1)
	var releaseOnce sync.Once
	releaseExchange := func() { releaseOnce.Do(func() { close(flow.release) }) }
	defer releaseExchange()
	go func() {
		_, completeErr := d.completeAuthorization(context.Background(), challenge.SessionID, state, "approved", callback)
		completed <- completeErr
	}()
	select {
	case <-flow.entered:
	case <-time.After(time.Second):
		t.Fatal("authorization exchange did not reach its controlled in-flight point")
	}
	if _, err := s.UpdateConnection(store.UpdateConnectionInput{ID: connection.ID, Secret: "credential-generation-new"}); err != nil {
		releaseExchange()
		t.Fatal(err)
	}
	releaseExchange()
	if err := <-completed; err == nil {
		t.Fatal("stale OAuth exchange succeeded after the connection credential changed")
	}
	stored, ok := s.ConnectionCredentialByID(connection.ID)
	if !ok || stored.Secret != "credential-generation-new" {
		t.Fatalf("newer connection credential was overwritten: stored=%#v ok=%v", stored, ok)
	}
	status, err := d.authorizationStatus(challenge.SessionID)
	if err != nil || status.Status != "failed" {
		t.Fatalf("stale authorization terminal state=%#v err=%v", status, err)
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
		providerAuthFlowIDs: map[string]map[string]string{"loopback-oauth": {"oauth2": "loopback-oauth:auth"}},
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
	deniedStart := d.handleIPC(context.Background(), IPCRequest{ID: "denied-start", Method: "auth.authorization.start", Params: map[string]any{"connectionID": connection.ID}})
	if !deniedStart.OK {
		t.Fatalf("start denied-flow fixture: %s", deniedStart.Error)
	}
	deniedChallenge := deniedStart.Result.(authorizationChallenge)
	deniedCallback := redirectURL + "?error=access_denied&state=" + url.QueryEscape(deniedChallenge.SessionID)
	deniedResponse, err := http.Get(deniedCallback)
	if err != nil {
		t.Fatal(err)
	}
	_ = deniedResponse.Body.Close()
	if deniedResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("denied callback returned HTTP %d", deniedResponse.StatusCode)
	}
	deniedStatus := d.handleIPC(context.Background(), IPCRequest{ID: "denied-status", Method: "auth.authorization.get", Params: map[string]any{"sessionID": deniedChallenge.SessionID}})
	deniedView, deniedOK := deniedStatus.Result.(authorizationStatusView)
	if !deniedStatus.OK || !deniedOK || deniedView.Status != "denied" || deniedView.Error != errOAuthAuthorizationDenied.Error() {
		t.Fatalf("denied OAuth session not retained as a terminal state: %#v error=%q", deniedStatus.Result, deniedStatus.Error)
	}
	stored, ok = s.ConnectionCredentialByID(connection.ID)
	if !ok {
		t.Fatal("connection disappeared after OAuth denial")
	}
	token, err = provider.DecodeOAuthTokenState(stored.Secret)
	if err != nil || token.AccessToken != "loopback-access" {
		t.Fatalf("OAuth denial changed the existing credential: %#v err=%v", token, err)
	}
	startedExpiry := d.handleIPC(context.Background(), IPCRequest{ID: "expiry-start", Method: "auth.authorization.start", Params: map[string]any{"connectionID": connection.ID}})
	if !startedExpiry.OK {
		t.Fatalf("start expiry-flow fixture after denial: %s", startedExpiry.Error)
	}
	expiryChallenge := startedExpiry.Result.(authorizationChallenge)
	d.expireAuthorizationSession(expiryChallenge.SessionID)
	expiredStatus := d.handleIPC(context.Background(), IPCRequest{ID: "expired-status", Method: "auth.authorization.get", Params: map[string]any{"sessionID": expiryChallenge.SessionID}})
	expiredView, expiredOK := expiredStatus.Result.(authorizationStatusView)
	if !expiredStatus.OK || !expiredOK || expiredView.Status != "expired" {
		t.Fatalf("expired OAuth session not retained as a terminal state: %#v error=%q", expiredStatus.Result, expiredStatus.Error)
	}
	d.oauthCallbackMu.Lock()
	remainingListeners = len(d.oauthCallbacks)
	d.oauthCallbackMu.Unlock()
	if remainingListeners != 0 {
		t.Fatalf("OAuth listener remained after denial/expiry: %d", remainingListeners)
	}
	d.cancelAuthorizationSessions()
	d.closeOAuthCallbackListeners()
}

func TestDaemonOwnsDeviceAuthorizationPollingAndKeepsDeviceCodePrivate(t *testing.T) {
	var tokenRequests int
	var tokenRequestMu sync.Mutex
	var tokenRequestTimes []time.Time
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
			tokenRequestMu.Lock()
			tokenRequests++
			requestNumber := tokenRequests
			tokenRequestTimes = append(tokenRequestTimes, time.Now())
			tokenRequestMu.Unlock()
			if requestNumber == 1 {
				return &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":"authorization_pending"}`)), Request: request}, nil
			}
			if requestNumber == 2 {
				return &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":"slow_down"}`)), Request: request}, nil
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
		providerAuthFlowIDs: map[string]map[string]string{"device-oauth": {"oauth2": "device-oauth:oauth2"}},
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
	deadline := time.Now().Add(12 * time.Second)
	for view.Status != "completed" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		response := d.handleIPC(rootCtx, IPCRequest{ID: "device-get", Method: "auth.device.get", Params: map[string]any{"sessionID": view.SessionID}})
		if !response.OK {
			t.Fatal(response.Error)
		}
		view = response.Result.(deviceAuthorizationView)
	}
	tokenRequestMu.Lock()
	requestsAfterFirstFlow := tokenRequests
	requestTimes := append([]time.Time(nil), tokenRequestTimes...)
	tokenRequestMu.Unlock()
	if view.Status != "completed" || requestsAfterFirstFlow < 3 {
		t.Fatalf("device flow did not poll pending/slow_down then complete: status=%q requests=%d error=%q", view.Status, requestsAfterFirstFlow, view.Error)
	}
	if len(requestTimes) < 3 || requestTimes[2].Sub(requestTimes[1]) < 5*time.Second {
		gap := time.Duration(0)
		if len(requestTimes) >= 3 {
			gap = requestTimes[2].Sub(requestTimes[1])
		}
		t.Fatalf("OAuth slow_down did not add the RFC-required five seconds to the polling interval: gap=%s", gap)
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

func TestDaemonStopCancelsDeviceOAuthPollBeforeClosingStore(t *testing.T) {
	tokenEntered := make(chan struct{})
	releaseToken := make(chan struct{})
	var tokenEnteredOnce sync.Once
	identity := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/device":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `{"device_code":"private-shutdown-code","user_code":"SHUT-DOWN","verification_uri":"https://identity.test/activate","expires_in":60,"interval":1}`)
		case "/token":
			tokenEnteredOnce.Do(func() { close(tokenEntered) })
			select {
			case <-request.Context().Done():
			case <-releaseToken:
			}
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer identity.Close()
	defer close(releaseToken)

	root := t.TempDir()
	manifestDir := filepath.Join(root, "providers")
	if err := os.MkdirAll(manifestDir, 0o700); err != nil {
		t.Fatal(err)
	}
	definition := provider.ProviderDefinition{
		ContractVersion: 1, ID: "shutdown-device-oauth", Version: "1", DisplayName: "Shutdown device OAuth",
		DefaultCredentialType: "oauth2",
		AuthModes: []provider.AuthModeDefinition{{
			CredentialType: "oauth2", Auth: provider.PrimitiveRef{Kind: provider.PrimitiveAuth, ID: "oauth2", ContractVersion: 1},
			AuthOptions:  provider.AuthOptions{OAuth: &provider.OAuthFlowOptions{ClientID: "device-client", AuthURL: identity.URL + "/authorize", TokenURL: identity.URL + "/token", DeviceAuthURL: identity.URL + "/device"}},
			Presentation: kernel.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "},
		}},
		Operations: map[provider.Operation]provider.OperationBinding{provider.OperationChat: {
			Protocol: kernel.ProtocolOpenAIChat, TaskRef: provider.OperationRef("chat.generate", 1), ProviderFormat: normalize.FormatOpenAIChat,
			Endpoint:        provider.PrimitiveRef{Kind: provider.PrimitiveEndpoint, ID: "http-json", ContractVersion: 1},
			Transport:       provider.PrimitiveRef{Kind: provider.PrimitiveTransport, ID: "http", ContractVersion: 1},
			RequestCodec:    provider.PrimitiveRef{Kind: provider.PrimitiveRequestCodec, ID: "openai-chat-json", ContractVersion: 1},
			ResponseDecoder: provider.PrimitiveRef{Kind: provider.PrimitiveResponseDecoder, ID: "openai-sse", ContractVersion: 1},
		}},
	}
	manifest, err := provider.EncodeProviderDefinitionJSON(definition)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "shutdown-device-oauth.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	config.DBPath = filepath.Join(root, "gobroom.db")
	config.IPCPath = filepath.Join(root, "gobroom.sock")
	config.ProviderManifestDir = manifestDir
	config.RuntimeSpoolPath = filepath.Join(root, "runtime-spool.json")
	config.ArtifactStoreDir = filepath.Join(root, "artifacts")
	config.HTTPEnabled = false
	startCtx := context.WithValue(context.Background(), oauth2.HTTPClient, identity.Client())
	d := New(config)
	if err := d.Start(startCtx); err != nil {
		t.Fatal(err)
	}
	providerResponse := d.handleIPC(context.Background(), IPCRequest{ID: "provider", Method: "providers.create", Params: map[string]any{
		"name": "Shutdown OAuth", "prefix": "shutdown-oauth", "baseUrl": "https://provider.test/v1", "protocol": "openai_chat", "definitionID": definition.ID,
	}})
	if !providerResponse.OK {
		_ = d.Stop(context.Background())
		t.Fatalf("create provider: %s", providerResponse.Error)
	}
	node := providerResponse.Result.(store.ProviderNode)
	connectionResponse := d.handleIPC(context.Background(), IPCRequest{ID: "connection", Method: "connections.create", Params: map[string]any{
		"providerNodeID": node.ID, "name": "pending OAuth", "credentialType": "oauth2", "secret": "",
	}})
	if !connectionResponse.OK {
		_ = d.Stop(context.Background())
		t.Fatalf("create pending OAuth connection: %s", connectionResponse.Error)
	}
	connection := connectionResponse.Result.(store.ConnectionRecord)
	started := d.handleIPC(context.Background(), IPCRequest{ID: "device-start", Method: "auth.device.start", Params: map[string]any{"connectionID": connection.ID}})
	if !started.OK {
		_ = d.Stop(context.Background())
		t.Fatalf("start device authorization: %s", started.Error)
	}
	select {
	case <-tokenEntered:
	case <-time.After(3 * time.Second):
		_ = d.Stop(context.Background())
		t.Fatal("device OAuth worker never started its token poll")
	}
	stopCtx, cancelStop := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelStop()
	if err := d.Stop(stopCtx); err != nil {
		t.Fatalf("daemon shutdown while device poll was active: %v", err)
	}
	if err := d.store.DB.Ping(); err == nil {
		t.Fatal("daemon shutdown returned before closing the credential store")
	}
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
		providerAuthFlowIDs: map[string]map[string]string{"device-oauth": {"oauth2": "device-oauth:fixture"}},
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

func TestDaemonStopCancelsActiveHTTPRequestBeforeDraining(t *testing.T) {
	runCtx, cancelRun := context.WithCancel(context.Background())
	handlerCancelled := make(chan struct{})
	handlerDone := make(chan struct{})
	cleanupDone := make(chan struct{})
	server := newDaemonHTTPServer("", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer close(handlerDone)
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("started\n"))
		writer.(http.Flusher).Flush()
		<-request.Context().Done()
		close(handlerCancelled)
	}), runCtx)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listener unavailable in test environment: %v", err)
	}
	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() { _ = server.Close() })

	responseResult := make(chan *http.Response, 1)
	requestErr := make(chan error, 1)
	go func() {
		response, err := http.Get("http://" + listener.Addr().String())
		if err != nil {
			requestErr <- err
			return
		}
		responseResult <- response
	}()
	var response *http.Response
	select {
	case response = <-responseResult:
	case err := <-requestErr:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP request did not reach daemon test server")
	}
	line, err := bufio.NewReader(response.Body).ReadString('\n')
	if err != nil || line != "started\n" {
		response.Body.Close()
		t.Fatalf("stream did not start: line=%q err=%v", line, err)
	}
	d := &Daemon{http: server, runCtx: runCtx, runCancel: cancelRun, stop: func() { close(cleanupDone) }}
	stopDone := make(chan error, 1)
	go func() { stopDone <- d.Stop(context.Background()) }()
	select {
	case <-handlerCancelled:
	case <-time.After(2 * time.Second):
		_ = server.Close()
		response.Body.Close()
		t.Fatal("daemon Stop did not cancel the active request context")
	}
	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		_ = server.Close()
		response.Body.Close()
		t.Fatal("daemon Stop did not drain the cancelled HTTP handler")
	}
	select {
	case <-handlerDone:
	default:
		t.Fatal("daemon cleanup completed before the active HTTP handler returned")
	}
	select {
	case <-cleanupDone:
	default:
		t.Fatal("daemon cleanup hook was not invoked")
	}
	_ = response.Body.Close()
	<-serveDone
}

func TestDaemonIPCReloadAndDeviceAuthRemainResponsiveDuringHTTPStream(t *testing.T) {
	upstreamStarted := make(chan struct{})
	releaseUpstream := make(chan struct{})
	var upstreamStartedOnce sync.Once
	var releaseOnce sync.Once
	releaseStream := func() { releaseOnce.Do(func() { close(releaseUpstream) }) }
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/models":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `{"data":[{"id":"stream-model"}]}`)
		case "/v1/chat/completions":
			writer.Header().Set("Content-Type", "text/event-stream")
			writer.Header().Set("Cache-Control", "no-cache")
			_, _ = io.WriteString(writer, "data: {\"id\":\"stream-response\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"first\"},\"finish_reason\":null}]}\n\n")
			if flusher, ok := writer.(http.Flusher); ok {
				flusher.Flush()
			}
			upstreamStartedOnce.Do(func() { close(upstreamStarted) })
			select {
			case <-releaseUpstream:
			case <-request.Context().Done():
				return
			}
			_, _ = io.WriteString(writer, "data: {\"id\":\"stream-response\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	root := t.TempDir()
	config := Config{DBPath: filepath.Join(root, "gobroom.db"), RuntimeSpoolPath: filepath.Join(root, "spool.json"), IPCPath: filepath.Join(root, "run", "gobroom.sock"), ArtifactStoreDir: filepath.Join(root, "artifacts")}
	runCtx, cancelRun := context.WithCancel(context.Background())
	d := New(config)
	if err := d.Start(runCtx); err != nil {
		cancelRun()
		t.Fatal(err)
	}
	defer func() {
		_ = d.Stop(context.Background())
		cancelRun()
	}()
	defer releaseStream()

	callIPC := func(method string, params map[string]any) IPCResponse {
		t.Helper()
		callCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		response, err := CallIPC(callCtx, config.IPCPath, IPCRequest{ID: method, Method: method, Params: params})
		if err != nil {
			t.Fatalf("IPC %s while streaming: %v", method, err)
		}
		return response
	}
	paramsFor := func(value any) map[string]any {
		t.Helper()
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var params map[string]any
		if err := json.Unmarshal(encoded, &params); err != nil {
			t.Fatal(err)
		}
		return params
	}
	createdProvider := callIPC("providers.create", map[string]any{"name": "Streaming fixture", "prefix": "streamfixture", "baseUrl": upstream.URL + "/v1", "protocol": "openai_chat"})
	if !createdProvider.OK {
		t.Fatalf("create streaming provider: %s", createdProvider.Error)
	}
	providerResult, ok := createdProvider.Result.(map[string]any)
	if !ok || providerResult["ID"] == nil {
		t.Fatalf("provider creation result=%#v", createdProvider.Result)
	}
	providerNodeID, _ := providerResult["ID"].(string)
	createdConnection := callIPC("connections.create", map[string]any{"providerNodeID": providerNodeID, "name": "stream account", "credentialType": "api_key", "secret": "stream-secret"})
	if !createdConnection.OK {
		t.Fatalf("create streaming connection: %s", createdConnection.Error)
	}
	connectionResult, ok := createdConnection.Result.(map[string]any)
	if !ok || connectionResult["ID"] == nil {
		t.Fatalf("connection creation result=%#v", createdConnection.Result)
	}
	connectionID, _ := connectionResult["ID"].(string)
	if refreshed := callIPC("providers.refresh_models", map[string]any{"nodeID": providerNodeID, "connectionID": connectionID}); !refreshed.OK {
		t.Fatalf("discover streaming model: %s", refreshed.Error)
	}
	discovered, err := d.store.DiscoveredRoutes(providerNodeID)
	if err != nil || len(discovered) != 1 || discovered[0].ExternalID != "stream-model" {
		t.Fatalf("discovered routes=%#v err=%v", discovered, err)
	}
	physical := store.PhysicalModel{Name: "stream-physical", Identity: kernel.PhysicalIdentity{CanonicalName: "stream-model"}, Sources: []store.RouteReference{{RouteID: discovered[0].ID, Fidelity: kernel.FidelityExact}}, Enabled: true}
	if result := callIPC("physical_models.upsert", paramsFor(physical)); !result.OK {
		t.Fatalf("create physical model: %s", result.Error)
	}
	combo := store.ComboModel{Name: "stream-role", Members: []store.ModelReference{{Kind: store.PhysicalReference, ID: physical.Name}}, Discoverable: true, Enabled: true}
	if result := callIPC("combo_models.upsert", paramsFor(combo)); !result.OK {
		t.Fatalf("publish stream role: %s", result.Error)
	}

	deviceNode, err := d.store.CreateProviderNode(store.CreateProviderNodeInput{Name: "Device auth fixture", Prefix: "devicefixture", BaseURL: "https://identity.test", Protocol: "openai_chat", DefinitionID: "ipc-device-fixture", AuthMode: "oauth2"})
	if err != nil {
		t.Fatal(err)
	}
	deviceConnection, err := d.store.CreateConnection(store.CreateConnectionInput{ProviderNodeID: deviceNode.ID, Name: "device login", CredentialType: "oauth2"})
	if err != nil {
		t.Fatal(err)
	}
	deviceFlow := &pendingDaemonDeviceAuth{pollStarted: make(chan struct{})}
	d.providerAuthFlowIDs[deviceNode.DefinitionID] = map[string]string{"oauth2": deviceFlow.ID()}
	d.authFlows[deviceFlow.ID()] = deviceFlow

	dataPlane := httptest.NewServer(d.server.HandlerWithOptions(api.HandlerOptions{DataPlane: true}))
	defer dataPlane.Close()
	request, err := http.NewRequest(http.MethodPost, dataPlane.URL+"/v1/chat/completions", strings.NewReader(`{"model":"stream-role","messages":[{"role":"user","content":"keep the stream open"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	streamResponse, err := dataPlane.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer streamResponse.Body.Close()
	if streamResponse.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(streamResponse.Body)
		t.Fatalf("stream HTTP status=%d body=%s", streamResponse.StatusCode, body)
	}
	streamReader := bufio.NewReader(streamResponse.Body)
	type lineResult struct {
		line string
		err  error
	}
	firstLine := make(chan lineResult, 1)
	go func() {
		line, err := streamReader.ReadString('\n')
		firstLine <- lineResult{line: line, err: err}
	}()
	select {
	case result := <-firstLine:
		if result.err != nil || !strings.HasPrefix(result.line, "data:") {
			t.Fatalf("first downstream SSE line=%q err=%v", result.line, result.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("downstream stream did not emit its first event")
	}
	select {
	case <-upstreamStarted:
	case <-time.After(time.Second):
		t.Fatal("upstream stream did not reach its blocked interval")
	}

	if status := callIPC("status", nil); !status.OK {
		t.Fatalf("status IPC blocked by active HTTP stream: %s", status.Error)
	}
	if reloaded := callIPC("reload", nil); !reloaded.OK {
		t.Fatalf("reload IPC blocked by active HTTP stream: %s", reloaded.Error)
	}
	authorization := callIPC("auth.device.start", map[string]any{"connectionID": deviceConnection.ID})
	if !authorization.OK {
		t.Fatalf("device auth start IPC blocked by active HTTP stream: %s", authorization.Error)
	}
	authorizationResult, ok := authorization.Result.(map[string]any)
	if !ok {
		t.Fatalf("device auth result=%#v", authorization.Result)
	}
	sessionID, _ := authorizationResult["sessionId"].(string)
	select {
	case <-deviceFlow.pollStarted:
	case <-time.After(time.Second):
		t.Fatal("daemon device-auth worker did not begin polling")
	}
	if current := callIPC("auth.device.get", map[string]any{"sessionID": sessionID}); !current.OK {
		t.Fatalf("device auth status IPC blocked by active HTTP stream: %s", current.Error)
	}
	if cancelled := callIPC("auth.device.cancel", map[string]any{"sessionID": sessionID}); !cancelled.OK {
		t.Fatalf("device auth cancellation IPC blocked by active HTTP stream: %s", cancelled.Error)
	}
	workerDone := make(chan struct{})
	go func() { d.authWG.Wait(); close(workerDone) }()
	select {
	case <-workerDone:
	case <-time.After(time.Second):
		t.Fatal("device authorization worker did not stop after IPC cancellation")
	}

	bodyDone := make(chan struct {
		body []byte
		err  error
	}, 1)
	go func() {
		body, err := io.ReadAll(streamReader)
		bodyDone <- struct {
			body []byte
			err  error
		}{body: body, err: err}
	}()
	select {
	case completed := <-bodyDone:
		t.Fatalf("HTTP stream ended during control/auth IPC before upstream release: body=%q err=%v", completed.body, completed.err)
	default:
	}
	releaseStream()
	select {
	case completed := <-bodyDone:
		if completed.err != nil || !strings.Contains(string(completed.body), "data: [DONE]") {
			t.Fatalf("HTTP stream did not finish after upstream release: body=%q err=%v", completed.body, completed.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP stream remained blocked after upstream release")
	}
}

func TestIPCInferenceProbeWorksWithoutModelDiscoveryOperation(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			t.Errorf("inference probe path=%q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("inference probe used unexpected credentials")
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"O"}}]}`))
	}))
	defer upstream.Close()

	state, err := store.Open(t.TempDir() + "/inference-probe.db")
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	node, err := state.CreateProviderNode(store.CreateProviderNodeInput{Name: "Inference only", Prefix: "probe", BaseURL: upstream.URL + "/v1", Protocol: "openai_chat", DefinitionID: "inference-only"})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := state.CreateConnection(store.CreateConnectionInput{ProviderNodeID: node.ID, Name: "account", CredentialType: "api_key", Secret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	adapter := openai.NewAdapter()
	binding := provider.RuntimeBinding{
		DefinitionID: "inference-only", Operation: provider.OperationChat,
		TaskRef: *provider.OperationRef(normalize.OperationChatGenerate, 1), Protocol: kernel.ProtocolOpenAIChat,
		ProviderFormat: normalize.FormatOpenAIChat, AdapterID: adapter.ID(), Adapter: adapter, AuthModes: map[string]provider.BoundAuthMode{"api_key": {Flow: provider.StaticSecretAuth{}, Presentation: kernel.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "}}},
	}
	d := &Daemon{store: state, providerBindings: map[string]provider.RuntimeBinding{provider.RuntimeBindingKey(binding.DefinitionID, binding.Operation): binding}}
	response := d.handleIPC(context.Background(), IPCRequest{ID: "probe", Method: "connections.test_inference", Params: map[string]any{"connectionID": connection.ID, "modelID": "manual/model-x"}})
	if !response.OK {
		t.Fatalf("inference probe failed: %s", response.Error)
	}
	result, ok := response.Result.(discovery.InferenceTestResult)
	if !ok || result.ConnectionID != connection.ID || result.ModelID != "manual/model-x" || result.HTTPStatus != http.StatusOK {
		t.Fatalf("inference probe result=%#v", response.Result)
	}
	models, err := state.Models()
	if err != nil || len(models) != 0 {
		t.Fatalf("inference IPC probe changed model inventory: %#v err=%v", models, err)
	}
}

func TestIPCOperationProbeSchemaRejectsPayloadBeforeAdapterDispatch(t *testing.T) {
	state, err := store.Open(t.TempDir() + "/operation-probe.db")
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	node, err := state.CreateProviderNode(store.CreateProviderNodeInput{Name: "Audio provider", Prefix: "audio", BaseURL: "https://provider.test/v1", Protocol: "vendor_audio", DefinitionID: "audio-provider"})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := state.CreateConnection(store.CreateConnectionInput{ProviderNodeID: node.ID, Name: "account", CredentialType: "api_key", Secret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	runtimeRegistry, err := provider.NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	operationRef := extensions.Ref{Kind: "operation", ID: "audio.transcribe", ContractVersion: 1}
	if err := runtimeRegistry.Primitives.Operations.RegisterRawPayload(operationRef, "Audio transcription", "Transcribe an audio resource.", json.RawMessage(`{"type":"object","properties":{"audio_url":{"type":"string","minLength":1}},"required":["audio_url"],"additionalProperties":false}`), operations.ReplayNever); err != nil {
		t.Fatal(err)
	}
	adapterCalls := 0
	adapter := daemonExplainAdapter{attempts: &adapterCalls}
	binding := provider.RuntimeBinding{
		DefinitionID: node.DefinitionID, Operation: provider.OperationAudio, TaskRef: operationRef,
		Protocol: kernel.Protocol(node.Protocol), AdapterID: adapter.ID(), Adapter: adapter, AuthModes: map[string]provider.BoundAuthMode{"api_key": {Flow: provider.StaticSecretAuth{}, Presentation: kernel.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "}}},
	}
	d := &Daemon{store: state, providerRegistry: runtimeRegistry, providerBindings: map[string]provider.RuntimeBinding{provider.RuntimeBindingKey(node.DefinitionID, provider.OperationAudio): binding}}
	response := d.handleIPC(context.Background(), IPCRequest{
		ID: "operation-probe", Method: "connections.test_operation",
		Params: map[string]any{"connectionID": connection.ID, "operationID": string(provider.OperationAudio), "modelID": "asr-v4", "payload": map[string]any{"language": "vi"}},
	})
	if response.OK || !strings.Contains(response.Error, "audio_url") || adapterCalls != 0 {
		t.Fatalf("schema-invalid operation payload was not stopped before adapter dispatch: response=%#v adapterCalls=%d", response, adapterCalls)
	}
}

func TestDaemonCompositionRootRegistersStaticProviderModule(t *testing.T) {
	operationRef := extensions.Ref{Kind: "operation", ID: "fixture.audio.inspect", ContractVersion: 1}
	module := provider.RuntimeModuleFunc{Name: "fixture.audio", Init: func(runtime *provider.RuntimeRegistry) error {
		if err := runtime.Primitives.Operations.RegisterRawPayload(operationRef, "Audio inspection", "Inspect an audio resource.", json.RawMessage(`{"type":"object","properties":{"audio_url":{"type":"string","minLength":1}},"required":["audio_url"],"additionalProperties":false}`), operations.ReplayNever); err != nil {
			return err
		}
		return runtime.Primitives.RegisterDefinition(provider.ProviderDefinition{
			ContractVersion: 1, ID: "fixture-audio-provider", Version: "1", DisplayName: "Fixture Audio Provider",
			Capabilities:          provider.CapabilitySet{AudioInput: true},
			DefaultCredentialType: "api_key", AuthModes: []provider.AuthModeDefinition{{CredentialType: "api_key", Auth: provider.PrimitiveRef{Kind: provider.PrimitiveAuth, ID: "static-secret", ContractVersion: 1}, Presentation: kernel.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "}}},
			Operations: map[provider.Operation]provider.OperationBinding{provider.OperationAudio: {
				Protocol: "fixture_audio", TaskRef: &operationRef, ProviderFormat: "multipart-json",
				Endpoint:            provider.PrimitiveRef{Kind: provider.PrimitiveEndpoint, ID: "http-json", ContractVersion: 1},
				Transport:           provider.PrimitiveRef{Kind: provider.PrimitiveTransport, ID: "http", ContractVersion: 1},
				RequestCodec:        provider.PrimitiveRef{Kind: provider.PrimitiveRequestCodec, ID: provider.MultipartRequestCodecID, ContractVersion: 1},
				RequestCodecOptions: json.RawMessage(`{"modelField":"model","payloadField":"request"}`),
				ResponseDecoder:     provider.PrimitiveRef{Kind: provider.PrimitiveResponseDecoder, ID: "openai-sse", ContractVersion: 1},
			}},
		})
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := NewWithProviderModules(Config{DBPath: filepath.Join(t.TempDir(), "module.db"), IPCPath: filepath.Join(t.TempDir(), "module.sock"), ArtifactStoreDir: t.TempDir()}, module)
	if err := d.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer d.Stop(context.Background())
	response := d.handleIPC(ctx, IPCRequest{ID: "catalog", Method: "providers.catalog"})
	if !response.OK {
		t.Fatalf("provider catalog IPC failed: %s", response.Error)
	}
	var definitions []provider.DefinitionMetadata
	encoded, err := json.Marshal(response.Result)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &definitions); err != nil {
		t.Fatal(err)
	}
	for _, definition := range definitions {
		if definition.ID != "fixture-audio-provider" {
			continue
		}
		if len(definition.Operations) != 1 || definition.Operations[0].TaskRef == nil || *definition.Operations[0].TaskRef != operationRef {
			t.Fatalf("module operation was not exposed as its exact task contract: %#v", definition.Operations)
		}
		return
	}
	t.Fatal("static provider module did not participate in daemon runtime composition")
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
		w.Header().Set("X-RateLimit-Remaining", "9")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
		w.Header().Set("Anthropic-Ratelimit-Unified-5h-Status", "rejected")
		w.Header().Set("Anthropic-Ratelimit-Unified-5h-Reset", strconv.FormatInt(reset.Unix(), 10))
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"fault":{"reason":"PLAN_DRAINED","explanation":"weekly budget exhausted","reopens_at":"` + reset.Format(time.RFC3339) + `","period":"weekly","limit":50,"consumed":25,"remaining":25}}`))
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
		DefaultCredentialType: "api_key", AuthModes: []provider.AuthModeDefinition{{CredentialType: "api_key", Auth: provider.PrimitiveRef{Kind: provider.PrimitiveAuth, ID: "static-secret", ContractVersion: 1}, Presentation: kernel.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "}}},
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
				LimitHeaders: []provider.HTTPHeaderLimitSignal{{Name: "request-burst", Kind: "requests", ExhaustedStatusHeader: "Anthropic-Ratelimit-Unified-5h-Status", ExhaustedStatusValues: []string{"rejected"}, ResetHeader: "Anthropic-Ratelimit-Unified-5h-Reset"}},
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
	if err := state.UpsertCatalogModel(store.UpsertCatalogModelInput{ID: "quota-route", ProviderNodeID: node.ID, Kind: "custom", ExternalID: "quota-model", DisplayName: "Quota model", ConnectionIDs: []string{connection.ID}}); err != nil {
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
			cataloguedFixture = len(definition.Operations) == 2 && len(definition.AuthModes) == 1 && definition.AuthModes[0].ID == "static-secret"
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
	var savedConfiguredHeader quota.Snapshot
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
			if snapshot.WindowName == "request-burst" {
				savedConfiguredHeader = snapshot
			}
		}
		if saved.WindowName != "" && sawHeaderWindow && savedConfiguredHeader.WindowName != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if saved.Remaining == nil || *saved.Remaining != 25 || saved.ResetAt == nil || !saved.ResetAt.Equal(reset) || saved.WindowName != "weekly" || !sawHeaderWindow {
		t.Fatalf("daemon did not persist configured quota evidence: %#v", saved)
	}
	if savedConfiguredHeader.Remaining == nil || *savedConfiguredHeader.Remaining != 0 || savedConfiguredHeader.ResetAt == nil || !savedConfiguredHeader.ResetAt.Equal(reset) {
		t.Fatalf("daemon did not persist configured response-header window: %#v", savedConfiguredHeader)
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
		t.Fatal("persisted exhausted configured response-header window should block the route after daemon restart")
	}
	if !second.policy.Usable(route, reset.Add(time.Second)) {
		t.Fatal("persisted route should become eligible after reset without health polling")
	}
}
