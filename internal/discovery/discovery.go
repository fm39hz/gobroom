package discovery

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/store"
)

type Service struct {
	Store    *store.Store
	Client   *http.Client
	Bindings map[string]provider.RuntimeBinding
}
type Result struct {
	NodeID       string `json:"nodeId"`
	ConnectionID string `json:"connectionId"`
	Models       int    `json:"models"`
	Available    int    `json:"available"`
	Complete     bool   `json:"complete"`
	URL          string `json:"url"`
}

type ConnectionTestResult struct {
	ProviderNodeID string         `json:"providerNodeId"`
	ConnectionID   string         `json:"connectionId"`
	Endpoint       string         `json:"endpoint"`
	ModelsFound    int            `json:"modelsFound"`
	Preview        []ModelPreview `json:"preview,omitempty"`
	PreviewLimit   int            `json:"previewLimit"`
	Truncated      bool           `json:"truncated"`
	Complete       bool           `json:"complete"`
}

type ConnectionModelsPreview struct {
	ProviderNodeID string         `json:"providerNodeId"`
	ConnectionID   string         `json:"connectionId"`
	Endpoint       string         `json:"endpoint"`
	ModelsFound    int            `json:"modelsFound"`
	Models         []ModelPreview `json:"models"`
	PreviewLimit   int            `json:"previewLimit"`
	Truncated      bool           `json:"truncated"`
	Complete       bool           `json:"complete"`
}

type ModelPreview struct {
	ID          string                   `json:"id"`
	DisplayName string                   `json:"displayName"`
	Profile     kernel.CapabilityProfile `json:"profile,omitempty"`
}

const connectionTestPreviewLimit = 100
const connectionModelReviewLimit = 5000

// TestConnection queries the provider's configured model-list operation with
// exactly the selected enabled connection. It never writes discovered models;
// the caller can inspect the result before importing the catalog.
func (s Service) TestConnection(ctx context.Context, nodeID, connectionID string) (ConnectionTestResult, error) {
	_, endpoint, catalog, err := s.fetchModels(ctx, nodeID, connectionID, true)
	if err != nil {
		return ConnectionTestResult{}, err
	}
	models := catalog.Models
	previewLimit := min(len(models), connectionTestPreviewLimit)
	preview := make([]ModelPreview, 0, previewLimit)
	for _, model := range models[:previewLimit] {
		preview = append(preview, ModelPreview{ID: model.ID, DisplayName: model.DisplayName, Profile: model.Profile})
	}
	return ConnectionTestResult{ProviderNodeID: nodeID, ConnectionID: connectionID, Endpoint: endpointSummary(endpoint), ModelsFound: len(models), Preview: preview, PreviewLimit: connectionTestPreviewLimit, Truncated: len(models) > previewLimit, Complete: catalog.Complete}, nil
}

// PreviewConnectionModels fetches the model list for review without writing
// catalog or entitlement state. Import remains a separate explicit operation.
func (s Service) PreviewConnectionModels(ctx context.Context, nodeID, connectionID string) (ConnectionModelsPreview, error) {
	_, endpoint, catalog, err := s.fetchModels(ctx, nodeID, connectionID, false)
	if err != nil {
		return ConnectionModelsPreview{}, err
	}
	limit := min(len(catalog.Models), connectionModelReviewLimit)
	models := make([]ModelPreview, 0, limit)
	for _, model := range catalog.Models[:limit] {
		models = append(models, ModelPreview{ID: model.ID, DisplayName: model.DisplayName, Profile: model.Profile})
	}
	return ConnectionModelsPreview{ProviderNodeID: nodeID, ConnectionID: connectionID, Endpoint: endpointSummary(endpoint), ModelsFound: len(catalog.Models), Models: models, PreviewLimit: connectionModelReviewLimit, Truncated: len(catalog.Models) > limit, Complete: catalog.Complete}, nil
}

func (s Service) fetchModels(ctx context.Context, nodeID, connectionID string, test bool) (store.ProviderNode, string, provider.ModelCatalogSnapshot, error) {
	node, err := s.Store.ProviderNode(nodeID)
	if err != nil {
		return store.ProviderNode{}, "", provider.ModelCatalogSnapshot{}, err
	}
	if node.ID == "" {
		return store.ProviderNode{}, "", provider.ModelCatalogSnapshot{}, fmt.Errorf("provider node %q not found", nodeID)
	}
	if node.BaseURL == "" {
		return store.ProviderNode{}, "", provider.ModelCatalogSnapshot{}, fmt.Errorf("provider node %q has no base URL", nodeID)
	}
	if connectionID == "" {
		return store.ProviderNode{}, "", provider.ModelCatalogSnapshot{}, fmt.Errorf("connection ID is required")
	}
	connections, err := s.Store.Connections(nodeID)
	if err != nil {
		return store.ProviderNode{}, "", provider.ModelCatalogSnapshot{}, err
	}
	var selected store.ConnectionRecord
	for _, connection := range connections {
		if connection.ID == connectionID && connection.Enabled {
			selected = connection
			break
		}
	}
	if selected.ID == "" {
		return store.ProviderNode{}, "", provider.ModelCatalogSnapshot{}, fmt.Errorf("enabled connection %q does not belong to provider %q", connectionID, nodeID)
	}
	binding, ok := s.Bindings[provider.RuntimeBindingKey(node.DefinitionID, provider.OperationModels)]
	if !ok || binding.ModelSource == nil || binding.Endpoint == nil {
		return store.ProviderNode{}, "", provider.ModelCatalogSnapshot{}, fmt.Errorf("provider definition %q does not provide model discovery", node.DefinitionID)
	}
	endpoint, err := binding.Endpoint.Resolve(node.BaseURL, node.ModelsPath, binding.EndpointOptions)
	if err != nil {
		return store.ProviderNode{}, "", provider.ModelCatalogSnapshot{}, fmt.Errorf("resolve model-list endpoint: %w", err)
	}
	credential, found := s.Store.ConnectionCredentialByID(connectionID)
	if !found {
		return store.ProviderNode{}, "", provider.ModelCatalogSnapshot{}, fmt.Errorf("enabled credential for connection %q is unavailable", connectionID)
	}
	if binding.Auth == nil {
		return store.ProviderNode{}, "", provider.ModelCatalogSnapshot{}, fmt.Errorf("provider definition %q has no auth flow for model discovery", node.DefinitionID)
	}
	resolvedCredential, err := binding.Auth.Resolve(ctx, provider.AuthInput{ConnectionID: connectionID, Type: credential.Type, Secret: credential.Secret})
	if err != nil {
		return store.ProviderNode{}, "", provider.ModelCatalogSnapshot{}, fmt.Errorf("resolve discovery credential: %w", err)
	}
	input := provider.ModelSourceInput{URL: endpoint, Credential: resolvedCredential, Client: s.Client}
	var catalog provider.ModelCatalogSnapshot
	if test {
		if tester, ok := binding.ModelSource.(provider.ConnectionTestModelCatalogSnapshotSource); ok {
			catalog, err = tester.TestSnapshot(ctx, input)
		} else if tester, ok := binding.ModelSource.(provider.ConnectionTestModelSource); ok {
			catalog.Models, err = tester.Test(ctx, input)
		} else {
			return store.ProviderNode{}, "", provider.ModelCatalogSnapshot{}, fmt.Errorf("model source %q does not provide an active endpoint test", binding.ModelSource.ID())
		}
	} else if source, ok := binding.ModelSource.(provider.ModelCatalogSnapshotSource); ok {
		catalog, err = source.ListSnapshot(ctx, input)
	} else {
		catalog.Models, err = binding.ModelSource.List(ctx, input)
	}
	if err != nil {
		if test {
			return store.ProviderNode{}, "", provider.ModelCatalogSnapshot{}, fmt.Errorf("test model-list endpoint for connection %q: %w", connectionID, err)
		}
		return store.ProviderNode{}, "", provider.ModelCatalogSnapshot{}, err
	}
	return node, endpoint, catalog, nil
}

func endpointSummary(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "configured model-list endpoint"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func (s Service) RefreshNode(ctx context.Context, nodeID string) (Result, error) {
	stored, found := s.Store.ConnectionCredential(nodeID)
	if !found {
		return Result{}, fmt.Errorf("provider node %q has no enabled connection", nodeID)
	}
	return s.RefreshConnection(ctx, nodeID, stored.ID)
}

// RefreshConnection imports model IDs using the explicitly selected enabled
// connection. It is intentionally separate from TestConnection's read-only
// preview, so callers choose when catalog state is mutated.
func (s Service) RefreshConnection(ctx context.Context, nodeID, connectionID string) (Result, error) {
	return s.ImportConnectionModels(ctx, nodeID, connectionID, nil)
}

// ImportConnectionModels imports all returned model IDs when selectedIDs is
// nil, or the requested exact IDs otherwise. Entitlement evidence records the
// complete upstream response even when the user imports only a subset.
func (s Service) ImportConnectionModels(ctx context.Context, nodeID, connectionID string, selectedIDs []string) (Result, error) {
	node, endpoint, catalog, err := s.fetchModels(ctx, nodeID, connectionID, false)
	if err != nil {
		return Result{}, err
	}
	modelsToImport, err := selectModels(catalog.Models, selectedIDs)
	if err != nil {
		return Result{}, err
	}
	for _, model := range modelsToImport {
		id := model.ID
		routeID := "route_" + hash(nodeID+"\x00"+id)
		raw := model.Metadata
		if raw == nil {
			raw = map[string]any{}
		}
		raw["source"] = "models_endpoint"
		if err := s.Store.UpsertCatalogModel(store.UpsertCatalogModelInput{ID: routeID, ProviderNodeID: nodeID, Kind: "discovered", ExternalID: id, DisplayName: model.DisplayName, Profile: model.Profile, Raw: raw}); err != nil {
			return Result{}, err
		}
	}
	externalIDs := make([]string, 0, len(catalog.Models))
	for _, model := range catalog.Models {
		externalIDs = append(externalIDs, model.ID)
	}
	if err := s.Store.RecordConnectionModelSnapshot(connectionID, node.ID, externalIDs, catalog.Complete, "models_endpoint"); err != nil {
		return Result{}, err
	}
	return Result{NodeID: nodeID, ConnectionID: connectionID, Models: len(modelsToImport), Available: len(catalog.Models), Complete: catalog.Complete, URL: endpointSummary(endpoint)}, nil
}

func selectModels(models []provider.ModelDescriptor, selectedIDs []string) ([]provider.ModelDescriptor, error) {
	if selectedIDs == nil {
		return models, nil
	}
	if len(selectedIDs) == 0 {
		return []provider.ModelDescriptor{}, nil
	}
	selected := make(map[string]bool, len(selectedIDs))
	for _, id := range selectedIDs {
		if id == "" {
			return nil, fmt.Errorf("selected model ID must not be empty")
		}
		if selected[id] {
			return nil, fmt.Errorf("selected model ID %q was repeated", id)
		}
		selected[id] = true
	}
	result := make([]provider.ModelDescriptor, 0, len(selected))
	for _, model := range models {
		if selected[model.ID] {
			result = append(result, model)
			delete(selected, model.ID)
		}
	}
	if len(selected) > 0 {
		for id := range selected {
			return nil, fmt.Errorf("selected model ID %q was not returned by the connection catalog", id)
		}
	}
	return result, nil
}

func hash(value string) string { sum := sha1.Sum([]byte(value)); return hex.EncodeToString(sum[:8]) }
