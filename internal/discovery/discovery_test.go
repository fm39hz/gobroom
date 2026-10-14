package discovery

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	openai "github.com/fm39hz/gobroom/internal/adapter/openai"
	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/operations"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/store"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type operationProbeAdapterFixture struct{ client *http.Client }

func (a operationProbeAdapterFixture) ID() string { return "fixture-operation-probe" }
func (a operationProbeAdapterFixture) PlanCompatibility(kernel.CompatibilityContext) kernel.CompatibilityPlan {
	return kernel.CompatibilityPlan{Supported: true}
}
func (a operationProbeAdapterFixture) Prepare(_ context.Context, request kernel.NormalizedRequest, route kernel.Route, credential kernel.Credential) (kernel.UpstreamRequest, error) {
	endpoint, err := (kernel.HTTPJSONEndpoint{}).Resolve(route.BaseURL, "/audio/transcriptions", kernel.EndpointOptions{})
	if err != nil {
		return kernel.UpstreamRequest{}, err
	}
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+credential.Secret)
	headers.Set("Content-Type", "application/json")
	return kernel.UpstreamRequest{Method: http.MethodPost, URL: endpoint, Headers: headers, Body: strings.NewReader(string(request.OperationPayload))}, nil
}
func (a operationProbeAdapterFixture) Execute(ctx context.Context, upstream kernel.UpstreamRequest) (kernel.UpstreamResponse, error) {
	request, err := http.NewRequestWithContext(ctx, upstream.Method, upstream.URL, upstream.Body)
	if err != nil {
		return kernel.UpstreamResponse{}, err
	}
	request.Header = upstream.Headers.Clone()
	response, err := a.client.Do(request)
	if err != nil {
		return kernel.UpstreamResponse{}, err
	}
	return kernel.UpstreamResponse{Status: response.StatusCode, Headers: response.Header, Body: response.Body}, nil
}
func (operationProbeAdapterFixture) ClassifyError(status int, _ []byte) kernel.ErrorClass {
	if status >= http.StatusBadRequest {
		return kernel.ErrorTerminal
	}
	return kernel.ErrorRetryable
}
func (operationProbeAdapterFixture) RenderResponse(context.Context, kernel.UpstreamResponse, http.ResponseWriter, normalize.Format, kernel.StreamHooks) error {
	return nil
}

func TestRefreshNodeNormalizesModelsCatalog(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	node, err := s.CreateProviderNode(store.CreateProviderNodeInput{Name: "Provider", Prefix: "p", BaseURL: "https://provider.test/v1", Protocol: "openai_chat"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateConnection(store.CreateConnectionInput{ProviderNodeID: node.ID, Name: "primary", CredentialType: "api_key", Secret: "secret"}); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"model-a"},{"name":"model-b"},{"id":"model-a"}]}`)), Header: make(http.Header)}, nil
	})}
	registry, err := provider.NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := registry.BuildBindings()
	if err != nil {
		t.Fatal(err)
	}
	result, err := (Service{Store: s, Client: client, Bindings: bindings}).RefreshNode(context.Background(), node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Models != 2 {
		t.Fatalf("result=%#v", result)
	}
	models, err := s.Models()
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("models=%#v", models)
	}
}

func TestAnthropicConnectionModelDiscoveryUsesMessagesDefinitionAndAPIKey(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/anthropic.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	node, err := s.CreateProviderNode(store.CreateProviderNodeInput{Name: "Anthropic", Prefix: "anthropic", BaseURL: "https://api.anthropic.com/v1", Protocol: "anthropic"})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := s.CreateConnection(store.CreateConnectionInput{ProviderNodeID: node.ID, Name: "primary", CredentialType: "api_key", Secret: "anthropic-key"})
	if err != nil {
		t.Fatal(err)
	}
	page := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/models" {
			t.Errorf("path=%q", request.URL.Path)
		}
		if request.Header.Get("x-api-key") != "anthropic-key" || request.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("Anthropic headers missing: %#v", request.Header)
		}
		page++
		payload := `{"data":[{"id":"claude-sonnet-4","display_name":"Claude Sonnet 4"}],"has_more":false,"last_id":"claude-sonnet-4"}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(payload)), Header: make(http.Header)}, nil
	})}
	registry, err := provider.NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := registry.BuildBindings()
	if err != nil {
		t.Fatal(err)
	}
	preview, err := (Service{Store: s, Client: client, Bindings: bindings}).TestConnection(context.Background(), node.ID, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if page != 1 || !preview.Complete || preview.ModelsFound != 1 || preview.Preview[0].ID != "claude-sonnet-4" {
		t.Fatalf("preview=%#v requests=%d", preview, page)
	}
}

func TestSelectiveImportStoresOnlyChosenRoutesAndConnectionEntitlements(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/selective.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	node, err := s.CreateProviderNode(store.CreateProviderNodeInput{Name: "Provider", Prefix: "p", BaseURL: "https://provider.test/v1", Protocol: "openai_chat"})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := s.CreateConnection(store.CreateConnectionInput{ProviderNodeID: node.ID, Name: "selected", CredentialType: "api_key", Secret: "selected-key"})
	if err != nil {
		t.Fatal(err)
	}
	unverified, err := s.CreateConnection(store.CreateConnectionInput{ProviderNodeID: node.ID, Name: "unverified", CredentialType: "api_key", Secret: "other-key"})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bearer selected-key" {
			t.Errorf("selective preview/import used wrong account: %q", request.Header.Get("Authorization"))
		}
		body := `{"data":[{"id":"model-a"},{"id":"model-b"},{"id":"model-c"}]}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	registry, err := provider.NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := registry.BuildBindings()
	if err != nil {
		t.Fatal(err)
	}
	service := Service{Store: s, Client: client, Bindings: bindings}
	preview, err := service.PreviewConnectionModels(context.Background(), node.ID, selected.ID)
	if err != nil || preview.ModelsFound != 3 || !preview.Complete || len(preview.Models) != 3 {
		t.Fatalf("read-only import preview=%#v err=%v", preview, err)
	}
	if models, err := s.Models(); err != nil || len(models) != 0 {
		t.Fatalf("preview wrote catalog rows: %#v err=%v", models, err)
	}
	if err := s.SaveModelAvailability(store.ModelAvailability{ConnectionID: selected.ID, ModelRef: "model-b", Status: "invalid", Reason: "model_not_found", ErrorCode: string(kernel.ScopeRoute)}); err != nil {
		t.Fatal(err)
	}
	result, err := service.ImportConnectionModels(context.Background(), node.ID, selected.ID, ModelImportOptions{ModelIDs: []string{"model-b"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Models != 1 || result.Available != 3 || !result.Complete || !reflect.DeepEqual(result.ModelIDs, []string{"model-a", "model-b", "model-c"}) {
		t.Fatalf("selective import result=%#v", result)
	}
	availability, err := s.ModelAvailabilities()
	if err != nil || len(availability) != 1 || availability[0].Status != "available" {
		t.Fatalf("positive discovery did not clear the exact model invalidation: %#v err=%v", availability, err)
	}
	models, err := s.Models()
	if err != nil || len(models) != 1 || models[0].ExternalID != "model-b" {
		t.Fatalf("imported catalog=%#v err=%v", models, err)
	}
	routes, err := s.Routes()
	if err != nil || len(routes) != 1 || routes[0].CredentialID != selected.ID {
		t.Fatalf("route was expanded to an unverified connection: routes=%#v err=%v", routes, err)
	}
	discovered, err := s.DiscoveredRoutes(node.ID)
	if err != nil || len(discovered) != 1 || len(discovered[0].ConnectionAvailability) != 2 {
		t.Fatalf("connection evidence view=%#v err=%v", discovered, err)
	}
	states := map[string]string{}
	for _, evidence := range discovered[0].ConnectionAvailability {
		states[evidence.ConnectionID] = evidence.Status
	}
	if states[selected.ID] != store.EntitlementAvailable || states[unverified.ID] != store.EntitlementUnknown {
		t.Fatalf("connection entitlement evidence=%#v", states)
	}
	entitlementsOnly, err := service.ImportConnectionModels(context.Background(), node.ID, selected.ID, ModelImportOptions{EntitlementsOnly: true})
	if err != nil || entitlementsOnly.Models != 0 || entitlementsOnly.Available != 3 {
		t.Fatalf("entitlements-only apply=%#v err=%v", entitlementsOnly, err)
	}
	models, err = s.Models()
	if err != nil || len(models) != 1 || models[0].ExternalID != "model-b" {
		t.Fatalf("entitlements-only apply imported route IDs: models=%#v err=%v", models, err)
	}
	if _, err := service.ImportConnectionModels(context.Background(), node.ID, selected.ID, ModelImportOptions{ModelIDs: []string{"not-returned"}}); err == nil {
		t.Fatal("selected upstream ID missing from the fetched catalog must be rejected")
	}
}

func TestConnectionTestUsesSelectedCredentialAndRefreshImportsOnDemand(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	node, err := s.CreateProviderNode(store.CreateProviderNodeInput{Name: "Provider", Prefix: "p", BaseURL: "https://provider.test/v1", Protocol: "openai_chat"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateConnection(store.CreateConnectionInput{ProviderNodeID: node.ID, Name: "primary", CredentialType: "api_key", Secret: "priority-key", Priority: 10}); err != nil {
		t.Fatal(err)
	}
	selected, err := s.CreateConnection(store.CreateConnectionInput{ProviderNodeID: node.ID, Name: "alternate", CredentialType: "api_key", Secret: "selected-key", Priority: 20})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/models" {
			t.Errorf("path=%q", request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer selected-key" {
			t.Errorf("test used wrong connection credential: Authorization=%q", got)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"model-a"},{"id":"model-b"}]}`)), Header: make(http.Header)}, nil
	})}
	registry, err := provider.NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := registry.BuildBindings()
	if err != nil {
		t.Fatal(err)
	}
	service := Service{Store: s, Client: client, Bindings: bindings}
	preview, err := service.TestConnection(context.Background(), node.ID, selected.ID)
	if err != nil {
		t.Fatal(err)
	}
	if preview.ConnectionID != selected.ID || preview.ModelsFound != 2 || len(preview.Preview) != 2 || preview.Endpoint != "https://provider.test/v1/models" {
		t.Fatalf("connection test preview=%#v", preview)
	}
	models, err := s.Models()
	if err != nil || len(models) != 0 {
		t.Fatalf("read-only connection test mutated the model catalog: models=%#v err=%v", models, err)
	}
	imported, err := service.RefreshConnection(context.Background(), node.ID, selected.ID)
	if err != nil {
		t.Fatal(err)
	}
	if imported.ConnectionID != selected.ID || imported.Models != 2 {
		t.Fatalf("explicit connection refresh=%#v", imported)
	}
	models, err = s.Models()
	if err != nil || len(models) != 2 {
		t.Fatalf("explicit refresh did not import model IDs: models=%#v err=%v", models, err)
	}
}

func TestConnectionTestRejectsStaticCatalogAsEndpointProbe(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	registry, err := provider.NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	definition := provider.ProviderDefinition{ContractVersion: 1, ID: "static-catalog", Version: "1", DisplayName: "Static catalog",
		DefaultCredentialType: "api_key", AuthModes: []provider.AuthModeDefinition{{CredentialType: "api_key", Auth: provider.PrimitiveRef{Kind: provider.PrimitiveAuth, ID: "static-secret", ContractVersion: 1}, Presentation: kernel.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "}}},
		Operations: map[provider.Operation]provider.OperationBinding{provider.OperationModels: {
			ModelSource:        provider.PrimitiveRef{Kind: provider.PrimitiveModelSource, ID: "static-models", ContractVersion: 1},
			ModelSourceOptions: json.RawMessage(`{"models":[{"id":"model-a","displayName":"Model A"},{"id":"model-b"}]}`),
		}},
	}
	if err := registry.Primitives.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	bindings, err := registry.BuildBindings()
	if err != nil {
		t.Fatal(err)
	}
	node, err := s.CreateProviderNode(store.CreateProviderNodeInput{Name: "Static", Prefix: "static", BaseURL: "https://static.test", Protocol: "openai_chat", DefinitionID: definition.ID})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := s.CreateConnection(store.CreateConnectionInput{ProviderNodeID: node.ID, Name: "account", CredentialType: "api_key", Secret: "key"})
	if err != nil {
		t.Fatal(err)
	}
	otherConnection, err := s.CreateConnection(store.CreateConnectionInput{ProviderNodeID: node.ID, Name: "other account", CredentialType: "api_key", Secret: "other-key"})
	if err != nil {
		t.Fatal(err)
	}
	service := Service{Store: s, Bindings: bindings}
	if _, err := service.TestConnection(context.Background(), node.ID, connection.ID); err == nil || !strings.Contains(err.Error(), "static catalog") {
		t.Fatalf("static catalog produced a false-positive endpoint test: %v", err)
	}
	if _, err := service.ImportConnectionModels(context.Background(), node.ID, connection.ID, ModelImportOptions{EntitlementsOnly: true}); err == nil || !strings.Contains(err.Error(), "no connection entitlement evidence") {
		t.Fatalf("static catalog accepted an entitlements-only import: %v", err)
	}
	preview, err := service.PreviewConnectionModels(context.Background(), node.ID, connection.ID)
	if err != nil || preview.Endpoint != "static model catalog" || preview.ModelsFound != 2 || !preview.Complete || preview.Models[0].ID != "model-a" {
		t.Fatalf("static model preview=%#v err=%v", preview, err)
	}
	result, err := service.ImportConnectionModels(context.Background(), node.ID, connection.ID, ModelImportOptions{ModelIDs: []string{"model-a"}})
	if err != nil || result.Models != 1 || result.Available != 0 || result.CatalogModels != 2 || result.Evidence != provider.ModelCatalogProviderRegistry || !reflect.DeepEqual(result.ModelIDs, []string{"model-a"}) {
		t.Fatalf("selected static model import=%#v err=%v", result, err)
	}
	models, err := s.Models()
	if err != nil || len(models) != 1 || models[0].ExternalID != "model-a" || !reflect.DeepEqual(models[0].ConnectionIDs, []string{connection.ID}) || !reflect.DeepEqual(models[0].ConnectionAssignments, []store.ModelConnectionAssignment{{ConnectionID: connection.ID, Source: store.ModelAssignmentStaticCatalog}}) {
		t.Fatalf("static catalog import evidence=%#v err=%v", models, err)
	}
	entitlements, err := s.ConnectionModelEntitlements(node.ID)
	if err != nil || len(entitlements) != 0 {
		t.Fatalf("static provider catalog was mislabeled as observed entitlement: %#v err=%v", entitlements, err)
	}
	routes, err := s.Routes()
	if err != nil || len(routes) != 1 || routes[0].CredentialID != connection.ID {
		t.Fatalf("static route did not expand only for its explicitly assigned connection: %#v err=%v", routes, err)
	}
	assignedRoutes, err := s.DiscoveredRoutes(node.ID)
	assignedHere := false
	if len(assignedRoutes) == 1 {
		for _, evidence := range assignedRoutes[0].ConnectionAvailability {
			if evidence.ConnectionID == connection.ID && evidence.Status == "assigned" && evidence.Source == string(store.ModelAssignmentStaticCatalog) {
				assignedHere = true
			}
		}
	}
	if err != nil || len(assignedRoutes) != 1 || !assignedHere {
		t.Fatalf("static provider assignment evidence=%#v err=%v", assignedRoutes, err)
	}
	if _, err := service.ImportConnectionModels(context.Background(), node.ID, connection.ID, ModelImportOptions{ModelIDs: []string{}}); err != nil {
		t.Fatalf("clear static model assignments: %v", err)
	}
	routes, err = s.Routes()
	if err != nil || len(routes) != 0 {
		t.Fatalf("cleared static assignment remained routable: %#v err=%v", routes, err)
	}
	if assigned, err := s.StaticCatalogAssignedModels(connection.ID, node.ID); err != nil || len(assigned) != 0 {
		t.Fatalf("cleared static assignments=%v err=%v", assigned, err)
	}
	if available, err := s.StaticCatalogAssignedModels(otherConnection.ID, node.ID); err != nil || len(available) != 0 {
		t.Fatalf("static assignment leaked to another connection=%v err=%v", available, err)
	}
}

func TestInferenceProbeWorksWithoutModelListOperationAndDoesNotImport(t *testing.T) {
	var upstreamCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		upstreamCalls++
		if request.URL.Path != "/v1/chat/completions" {
			t.Errorf("inference path=%q", request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer selected-secret" {
			t.Errorf("inference used credential %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode inference body: %v", err)
		}
		if body["model"] != "manual/model-x" || body["max_tokens"] != float64(1) {
			t.Errorf("inference model/token bound=%#v", body)
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
	connection, err := state.CreateConnection(store.CreateConnectionInput{ProviderNodeID: node.ID, Name: "account", CredentialType: "api_key", Secret: "selected-secret"})
	if err != nil {
		t.Fatal(err)
	}
	adapter := openai.NewAdapter()
	task := *provider.OperationRef(normalize.OperationChatGenerate, 1)
	binding := provider.RuntimeBinding{DefinitionID: "inference-only", Operation: provider.OperationChat, TaskRef: task, Protocol: kernel.ProtocolOpenAIChat, ProviderFormat: normalize.FormatOpenAIChat, AdapterID: adapter.ID(), Adapter: adapter, AuthModes: map[string]provider.BoundAuthMode{"api_key": {Flow: provider.StaticSecretAuth{}, Presentation: kernel.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "}}}}
	service := Service{Store: state, Bindings: map[string]provider.RuntimeBinding{provider.RuntimeBindingKey("inference-only", provider.OperationChat): binding}}
	result, err := service.TestInference(context.Background(), connection.ID, "manual/model-x")
	if err != nil {
		t.Fatal(err)
	}
	if result.ConnectionID != connection.ID || result.ModelID != "manual/model-x" || result.HTTPStatus != http.StatusOK || result.ResponseBytes == 0 || upstreamCalls != 1 {
		t.Fatalf("inference probe result=%#v calls=%d", result, upstreamCalls)
	}
	models, err := state.Models()
	if err != nil || len(models) != 0 {
		t.Fatalf("inference probe mutated model inventory: models=%#v err=%v", models, err)
	}
	if routes, err := state.Routes(); err != nil || len(routes) != 0 {
		t.Fatalf("inference probe created routable state: routes=%#v err=%v", routes, err)
	}
}

func TestInferenceProbeRequiresAnInferenceOperation(t *testing.T) {
	state, err := store.Open(t.TempDir() + "/inference-missing.db")
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	node, err := state.CreateProviderNode(store.CreateProviderNodeInput{Name: "Catalog only", Prefix: "catalog", BaseURL: "https://provider.test/v1", Protocol: "openai_chat", DefinitionID: "catalog-only"})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := state.CreateConnection(store.CreateConnectionInput{ProviderNodeID: node.ID, Name: "account", CredentialType: "api_key", Secret: "key"})
	if err != nil {
		t.Fatal(err)
	}
	service := Service{Store: state, Bindings: map[string]provider.RuntimeBinding{}}
	if _, err := service.TestInference(context.Background(), connection.ID, "manual/model-x"); err == nil || !strings.Contains(err.Error(), "no compatible inference operation") {
		t.Fatalf("missing inference operation was accepted: %v", err)
	}
}

func TestOperationProbeValidatesExactNonChatPayloadAndDoesNotRecordUsage(t *testing.T) {
	var upstreamCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		upstreamCalls++
		if request.Method != http.MethodPost || request.URL.Path != "/v1/audio/transcriptions" {
			t.Errorf("operation probe request=%s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer operation-secret" {
			t.Errorf("operation probe used credential %q", got)
		}
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode operation payload: %v", err)
		}
		if payload["audio_url"] != "https://assets.example/audio.wav" || payload["language"] != "vi" {
			t.Errorf("operation payload changed: %#v", payload)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"text":"transcribed output"}`)
	}))
	defer upstream.Close()

	state, err := store.Open(t.TempDir() + "/operation-probe.db")
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	node, err := state.CreateProviderNode(store.CreateProviderNodeInput{Name: "Audio provider", Prefix: "audio", BaseURL: upstream.URL + "/v1", Protocol: "vendor_audio", DefinitionID: "audio-provider"})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := state.CreateConnection(store.CreateConnectionInput{ProviderNodeID: node.ID, Name: "account", CredentialType: "api_key", Secret: "operation-secret"})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := provider.NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	operationRef := extensions.Ref{Kind: "operation", ID: "audio.transcribe", ContractVersion: 1}
	if err := registry.Primitives.Operations.RegisterRawPayload(operationRef, "Audio transcription", "Transcribe an audio resource.", json.RawMessage(`{"type":"object","properties":{"audio_url":{"type":"string","minLength":1},"language":{"type":"string"}},"required":["audio_url"],"additionalProperties":false}`), operations.ReplayNever); err != nil {
		t.Fatal(err)
	}
	operationSnapshot, err := registry.OperationSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	adapter := operationProbeAdapterFixture{client: upstream.Client()}
	binding := provider.RuntimeBinding{
		DefinitionID: node.DefinitionID, Operation: provider.OperationAudio, TaskRef: operationRef,
		Protocol: kernel.Protocol(node.Protocol), ProviderFormat: normalize.Format("vendor-audio"),
		AdapterID: adapter.ID(), Adapter: adapter, AuthModes: map[string]provider.BoundAuthMode{"api_key": {Flow: provider.StaticSecretAuth{}}},
	}
	service := Service{
		Store: state, Bindings: map[string]provider.RuntimeBinding{provider.RuntimeBindingKey(node.DefinitionID, provider.OperationAudio): binding},
		Operations: operationSnapshot,
	}
	payload := json.RawMessage(`{"audio_url":"https://assets.example/audio.wav","language":"vi"}`)
	result, err := service.TestOperation(context.Background(), connection.ID, string(provider.OperationAudio), "asr-v4", payload)
	if err != nil {
		t.Fatal(err)
	}
	if result.Operation != string(provider.OperationAudio) || result.Task != operationRef.ID || result.ModelID != "asr-v4" || result.HTTPStatus != http.StatusOK || !result.ResponseJSON || upstreamCalls != 1 {
		t.Fatalf("operation probe result=%#v calls=%d", result, upstreamCalls)
	}
	encoded, err := json.Marshal(result)
	if err != nil || strings.Contains(string(encoded), "transcribed output") || strings.Contains(string(encoded), "audio_url") {
		t.Fatalf("operation probe result retained input/output content: %s err=%v", encoded, err)
	}
	if _, err := service.TestOperation(context.Background(), connection.ID, string(provider.OperationAudio), "asr-v4", json.RawMessage(`{"language":"vi"}`)); err == nil || !strings.Contains(err.Error(), "audio_url") {
		t.Fatalf("schema-invalid payload was not rejected before dispatch: %v", err)
	}
	if upstreamCalls != 1 {
		t.Fatalf("invalid operation payload dispatched to provider: calls=%d", upstreamCalls)
	}
	chatBinding := binding
	chatBinding.Operation = provider.OperationChat
	chatBinding.TaskRef = *provider.OperationRef(normalize.OperationChatGenerate, 1)
	service.Bindings[provider.RuntimeBindingKey(node.DefinitionID, provider.OperationChat)] = chatBinding
	if _, err := service.TestOperation(context.Background(), connection.ID, string(provider.OperationChat), "asr-v4", json.RawMessage(`{"model":"asr-v4"}`)); err == nil || !strings.Contains(err.Error(), "bounded test-inference") {
		t.Fatalf("generic operation probe bypassed the one-token chat contract: %v", err)
	}
	if upstreamCalls != 1 {
		t.Fatalf("chat payload reached generic probe adapter: calls=%d", upstreamCalls)
	}
	events, err := state.UsageEvents(10)
	if err != nil || len(events) != 0 {
		t.Fatalf("smoke test polluted usage history: events=%#v err=%v", events, err)
	}
}

func TestConnectionTestUsesSelectedCredentialAndRefreshCanImportThatConnection(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	node, err := s.CreateProviderNode(store.CreateProviderNodeInput{Name: "Provider", Prefix: "p", BaseURL: "https://provider.test/v1", Protocol: "openai_chat"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateConnection(store.CreateConnectionInput{ProviderNodeID: node.ID, Name: "primary", CredentialType: "api_key", Secret: "priority-key", Priority: 10}); err != nil {
		t.Fatal(err)
	}
	selected, err := s.CreateConnection(store.CreateConnectionInput{ProviderNodeID: node.ID, Name: "alternate", CredentialType: "api_key", Secret: "selected-key", Priority: 20})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/models" {
			t.Errorf("path=%q", request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer selected-key" {
			t.Errorf("test used wrong account credential: Authorization=%q", got)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"model-a"},{"id":"model-b"}]}`)), Header: make(http.Header)}, nil
	})}
	registry, err := provider.NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := registry.BuildBindings()
	if err != nil {
		t.Fatal(err)
	}
	service := Service{Store: s, Client: client, Bindings: bindings}
	preview, err := service.TestConnection(context.Background(), node.ID, selected.ID)
	if err != nil {
		t.Fatal(err)
	}
	if preview.ConnectionID != selected.ID || preview.ModelsFound != 2 || len(preview.Preview) != 2 || preview.Endpoint != "https://provider.test/v1/models" {
		t.Fatalf("connection test preview=%#v", preview)
	}
	models, err := s.Models()
	if err != nil || len(models) != 0 {
		t.Fatalf("read-only test changed the catalog: models=%#v err=%v", models, err)
	}
	imported, err := service.RefreshConnection(context.Background(), node.ID, selected.ID)
	if err != nil {
		t.Fatal(err)
	}
	if imported.ConnectionID != selected.ID || imported.Models != 2 {
		t.Fatalf("selected connection import=%#v", imported)
	}
	models, err = s.Models()
	if err != nil || len(models) != 2 {
		t.Fatalf("explicit refresh did not import model routes: models=%#v err=%v", models, err)
	}
}

func TestConnectionTestUsesSelectedAccountAndPreviewsWithoutImporting(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	node, err := s.CreateProviderNode(store.CreateProviderNodeInput{Name: "Provider", Prefix: "p", BaseURL: "https://provider.test/v1", Protocol: "openai_chat"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateConnection(store.CreateConnectionInput{ProviderNodeID: node.ID, Name: "primary", CredentialType: "api_key", Secret: "priority-key", Priority: 10}); err != nil {
		t.Fatal(err)
	}
	selected, err := s.CreateConnection(store.CreateConnectionInput{ProviderNodeID: node.ID, Name: "alternate", CredentialType: "api_key", Secret: "selected-key", Priority: 20})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("endpoint=%s", r.URL)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer selected-key" {
			t.Errorf("test used the wrong connection credential: Authorization=%q", got)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"model-a"},{"id":"model-b"}]}`)), Header: make(http.Header)}, nil
	})}
	registry, err := provider.NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := registry.BuildBindings()
	if err != nil {
		t.Fatal(err)
	}
	service := Service{Store: s, Client: client, Bindings: bindings}
	result, err := service.TestConnection(context.Background(), node.ID, selected.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.ConnectionID != selected.ID || result.ModelsFound != 2 || result.Endpoint != "https://provider.test/v1/models" || len(result.Preview) != 2 {
		t.Fatalf("test preview=%#v", result)
	}
	models, err := s.Models()
	if err != nil || len(models) != 0 {
		t.Fatalf("connection test must not mutate the model catalog: models=%#v err=%v", models, err)
	}
	imported, err := service.RefreshConnection(context.Background(), node.ID, selected.ID)
	if err != nil {
		t.Fatal(err)
	}
	if imported.ConnectionID != selected.ID || imported.Models != 2 {
		t.Fatalf("explicit connection refresh=%#v", imported)
	}
	models, err = s.Models()
	if err != nil || len(models) != 2 {
		t.Fatalf("explicit refresh did not import returned IDs: models=%#v err=%v", models, err)
	}
}
