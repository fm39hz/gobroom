package discovery

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/store"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

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
	result, err := service.ImportConnectionModels(context.Background(), node.ID, selected.ID, []string{"model-b"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Models != 1 || result.Available != 3 || !result.Complete {
		t.Fatalf("selective import result=%#v", result)
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
	entitlementsOnly, err := service.ImportConnectionModels(context.Background(), node.ID, selected.ID, []string{})
	if err != nil || entitlementsOnly.Models != 0 || entitlementsOnly.Available != 3 {
		t.Fatalf("entitlements-only apply=%#v err=%v", entitlementsOnly, err)
	}
	models, err = s.Models()
	if err != nil || len(models) != 1 || models[0].ExternalID != "model-b" {
		t.Fatalf("entitlements-only apply imported route IDs: models=%#v err=%v", models, err)
	}
	if _, err := service.ImportConnectionModels(context.Background(), node.ID, selected.ID, []string{"not-returned"}); err == nil {
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
	definition := provider.ProviderDefinition{
		ID: "static-catalog", Version: "1", DisplayName: "Static catalog",
		Auth: provider.PrimitiveRef{Kind: provider.PrimitiveAuth, ID: "static-secret"},
		Operations: map[provider.Operation]provider.OperationBinding{provider.OperationModels: {
			Endpoint:    provider.PrimitiveRef{Kind: provider.PrimitiveEndpoint, ID: "http-json"},
			Transport:   provider.PrimitiveRef{Kind: provider.PrimitiveTransport, ID: "http"},
			ModelSource: provider.PrimitiveRef{Kind: provider.PrimitiveModelSource, ID: "static-models"},
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
	if _, err := (Service{Store: s, Bindings: bindings}).TestConnection(context.Background(), node.ID, connection.ID); err == nil || !strings.Contains(err.Error(), "does not provide an active endpoint test") {
		t.Fatalf("static catalog produced a false-positive endpoint test: %v", err)
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
