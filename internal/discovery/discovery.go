package discovery

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/operations"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/store"
)

type Service struct {
	Store      *store.Store
	Client     *http.Client
	Bindings   map[string]provider.RuntimeBinding
	Operations *operations.Snapshot
}
type Result struct {
	NodeID        string                        `json:"nodeId"`
	ConnectionID  string                        `json:"connectionId"`
	Models        int                           `json:"models"`
	ModelIDs      []string                      `json:"modelIds,omitempty"`
	Available     int                           `json:"available"`
	CatalogModels int                           `json:"catalogModels"`
	Evidence      provider.ModelCatalogEvidence `json:"evidence"`
	Complete      bool                          `json:"complete"`
	URL           string                        `json:"url"`
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
	ProviderNodeID   string                        `json:"providerNodeId"`
	ConnectionID     string                        `json:"connectionId"`
	Endpoint         string                        `json:"endpoint"`
	Evidence         provider.ModelCatalogEvidence `json:"evidence"`
	AssignedModelIDs []string                      `json:"assignedModelIds,omitempty"`
	ModelsFound      int                           `json:"modelsFound"`
	Models           []ModelPreview                `json:"models"`
	PreviewLimit     int                           `json:"previewLimit"`
	Truncated        bool                          `json:"truncated"`
	Complete         bool                          `json:"complete"`
}

type fetchedModelCatalog struct {
	Node     store.ProviderNode
	Endpoint string
	Snapshot provider.ModelCatalogSnapshot
}

func (f fetchedModelCatalog) EndpointLabel() string {
	if f.Snapshot.Evidence == provider.ModelCatalogProviderRegistry {
		return "static model catalog"
	}
	return endpointSummary(f.Endpoint)
}

type InferenceTestResult struct {
	ProviderNodeID string `json:"providerNodeId"`
	ConnectionID   string `json:"connectionId"`
	ModelID        string `json:"modelId"`
	Operation      string `json:"operation"`
	Endpoint       string `json:"endpoint"`
	HTTPStatus     int    `json:"httpStatus"`
	ResponseBytes  int    `json:"responseBytes"`
}

type OperationTestResult struct {
	ProviderNodeID    string `json:"providerNodeId"`
	ConnectionID      string `json:"connectionId"`
	Operation         string `json:"operation"`
	Task              string `json:"task"`
	ModelID           string `json:"modelId"`
	Endpoint          string `json:"endpoint"`
	HTTPStatus        int    `json:"httpStatus"`
	ResponseBytes     int    `json:"responseBytes"`
	ResponseTruncated bool   `json:"responseTruncated,omitempty"`
	ResponseMediaType string `json:"responseMediaType,omitempty"`
	ResponseJSON      bool   `json:"responseJSON"`
}

type ModelPreview struct {
	ID          string                   `json:"id"`
	DisplayName string                   `json:"displayName"`
	Profile     kernel.CapabilityProfile `json:"profile,omitempty"`
}

type ModelImportOptions struct {
	ModelIDs         []string
	EntitlementsOnly bool
}

const connectionTestPreviewLimit = 100
const connectionModelReviewLimit = 5000

// TestConnection queries the provider's configured model-list operation with
// exactly the selected enabled connection. It never writes discovered models;
// the caller can inspect the result before importing the catalog.
func (s Service) TestConnection(ctx context.Context, nodeID, connectionID string) (ConnectionTestResult, error) {
	fetched, err := s.fetchModels(ctx, nodeID, connectionID, true)
	if err != nil {
		return ConnectionTestResult{}, err
	}
	catalog := fetched.Snapshot
	models := catalog.Models
	previewLimit := min(len(models), connectionTestPreviewLimit)
	preview := make([]ModelPreview, 0, previewLimit)
	for _, model := range models[:previewLimit] {
		preview = append(preview, ModelPreview{ID: model.ID, DisplayName: model.DisplayName, Profile: model.Profile})
	}
	return ConnectionTestResult{ProviderNodeID: nodeID, ConnectionID: connectionID, Endpoint: fetched.EndpointLabel(), ModelsFound: len(models), Preview: preview, PreviewLimit: connectionTestPreviewLimit, Truncated: len(models) > previewLimit, Complete: catalog.Complete}, nil
}

const inferenceProbeResponseLimit = 64 << 10
const operationProbeResponseLimit = 64 << 10

// TestOperation sends a user-supplied, schema-validated payload through one
// exact provider operation binding. It is a connection smoke test, not a
// serving request: it does not route, record usage, or mutate health/catalog
// state. The request may be billable or create a provider-side resource.
func (s Service) TestOperation(ctx context.Context, connectionID, operationID, modelID string, payload json.RawMessage) (OperationTestResult, error) {
	if strings.TrimSpace(operationID) == "" || strings.TrimSpace(modelID) == "" {
		return OperationTestResult{}, fmt.Errorf("exact provider operation and upstream model ID are required")
	}
	if len(payload) == 0 {
		return OperationTestResult{}, fmt.Errorf("operation probe payload is required")
	}
	if s.Operations == nil {
		return OperationTestResult{}, fmt.Errorf("operation catalog is unavailable")
	}
	credential, found := s.Store.ConnectionCredentialByID(connectionID)
	if !found {
		return OperationTestResult{}, fmt.Errorf("enabled credential for connection %q is unavailable", connectionID)
	}
	node, err := s.Store.ProviderNodeByConnection(connectionID)
	if err != nil {
		return OperationTestResult{}, fmt.Errorf("resolve provider for connection %q: %w", connectionID, err)
	}
	connections, err := s.Store.Connections(node.ID)
	if err != nil {
		return OperationTestResult{}, err
	}
	var selected store.ConnectionRecord
	for _, item := range connections {
		if item.ID == connectionID && item.Enabled {
			selected = item
			break
		}
	}
	if selected.ID == "" {
		return OperationTestResult{}, fmt.Errorf("enabled connection %q does not belong to provider %q", connectionID, node.ID)
	}
	operation := provider.Operation(operationID)
	binding, ok := s.Bindings[provider.RuntimeBindingKey(node.DefinitionID, operation)]
	if !ok || binding.Adapter == nil || binding.TaskRef.Kind != "operation" || binding.TaskRef.ContractVersion == 0 {
		return OperationTestResult{}, fmt.Errorf("provider definition %q has no executable binding for operation %q", node.DefinitionID, operationID)
	}
	authMode, authModeExists := binding.AuthModes[selected.CredentialType]
	if !authModeExists || authMode.Flow == nil {
		return OperationTestResult{}, fmt.Errorf("provider definition %q does not bind credential type %q", node.DefinitionID, selected.CredentialType)
	}
	if binding.TaskRef.ID == string(normalize.OperationChatGenerate) {
		return OperationTestResult{}, fmt.Errorf("chat generation probes must use the bounded test-inference operation")
	}
	request := normalize.Request{
		Model: modelID, Operation: normalize.Operation(binding.TaskRef.ID),
		OperationContractVersion: binding.TaskRef.ContractVersion,
		OperationPayload:         append(json.RawMessage(nil), payload...),
		SourceFormat:             binding.ProviderFormat,
	}
	prepared, err := s.Operations.Prepare(request)
	if err != nil {
		return OperationTestResult{}, fmt.Errorf("validate payload for operation %q: %w", operationID, err)
	}
	request.OperationPayload = prepared.InputPayload
	ctx, cancel := context.WithTimeout(ctx, time.Duration(prepared.ResourceBounds.DeadlineMillis)*time.Millisecond)
	defer cancel()
	resolvedCredential, err := authMode.Flow.Resolve(ctx, provider.AuthInput{ConnectionID: connectionID, Type: credential.Type, Secret: credential.Secret})
	if err != nil {
		return OperationTestResult{}, fmt.Errorf("resolve operation-probe credential: %w", err)
	}
	if err := resolvedCredential.ValidateRuntimeContext(); err != nil {
		return OperationTestResult{}, err
	}
	task := normalize.Operation(binding.TaskRef.ID)
	route := kernel.Route{
		ID: connectionID + ":operation-probe", NodeID: node.ID, DefinitionID: node.DefinitionID,
		DisplayPrefix: node.Prefix, ExternalModel: modelID, Protocol: binding.Protocol,
		BaseURL: node.BaseURL, CredentialID: connectionID, CredentialType: selected.CredentialType,
		CredentialPresentation: authMode.Presentation,
		Enabled:                true, OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{
			task: {ContractVersion: binding.TaskRef.ContractVersion, AdapterIDs: []string{binding.AdapterID}},
		},
	}
	upstream, err := binding.Adapter.Prepare(ctx, request, route, resolvedCredential)
	if err != nil {
		return OperationTestResult{}, fmt.Errorf("prepare operation probe %q: %w", operationID, err)
	}
	response, err := binding.Adapter.Execute(ctx, upstream)
	if err != nil {
		return OperationTestResult{}, fmt.Errorf("send operation probe %q: %w", operationID, err)
	}
	if response.Body != nil {
		defer response.Body.Close()
	}
	limit := min(int64(operationProbeResponseLimit), prepared.ResourceBounds.MaxOutputBytes)
	if limit <= 0 {
		return OperationTestResult{}, fmt.Errorf("operation %q has no response capture budget", operationID)
	}
	result := OperationTestResult{
		ProviderNodeID: node.ID, ConnectionID: connectionID, Operation: operationID,
		Task: binding.TaskRef.ID, ModelID: modelID, Endpoint: endpointSummary(upstream.URL),
		HTTPStatus: response.Status,
	}
	if response.Headers != nil {
		result.ResponseMediaType = response.Headers.Get("Content-Type")
	}
	var capturedBody []byte
	if response.Body != nil {
		body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
		if err != nil {
			return result, fmt.Errorf("read operation-probe response: %w", err)
		}
		result.ResponseTruncated = int64(len(body)) > limit
		result.ResponseBytes = len(body)
		if int64(len(body)) > limit {
			result.ResponseBytes = int(limit)
			body = body[:limit]
		}
		capturedBody = body
		result.ResponseJSON = len(body) > 0 && json.Valid(body)
	}
	if response.Status < http.StatusOK || response.Status >= http.StatusMultipleChoices {
		class := binding.Adapter.ClassifyError(response.Status, capturedBody)
		return result, fmt.Errorf("operation probe returned HTTP %d (%s)", response.Status, class)
	}
	return result, nil
}

// TestInference probes one exact upstream model through the selected enabled
// connection. It is independent of model-list discovery and does not mutate
// the catalog, entitlement evidence, health state or usage history. The probe
// is a real billable inference request with a one-token output budget.
func (s Service) TestInference(ctx context.Context, connectionID, modelID string) (InferenceTestResult, error) {
	if strings.TrimSpace(modelID) == "" {
		return InferenceTestResult{}, fmt.Errorf("exact upstream model ID is required")
	}
	credential, found := s.Store.ConnectionCredentialByID(connectionID)
	if !found {
		return InferenceTestResult{}, fmt.Errorf("enabled credential for connection %q is unavailable", connectionID)
	}
	node, err := s.Store.ProviderNodeByConnection(connectionID)
	if err != nil {
		return InferenceTestResult{}, fmt.Errorf("resolve provider for connection %q: %w", connectionID, err)
	}
	connections, err := s.Store.Connections(node.ID)
	if err != nil {
		return InferenceTestResult{}, err
	}
	var selected store.ConnectionRecord
	for _, item := range connections {
		if item.ID == connectionID && item.Enabled {
			selected = item
			break
		}
	}
	if selected.ID == "" {
		return InferenceTestResult{}, fmt.Errorf("enabled connection %q does not belong to provider %q", connectionID, node.ID)
	}
	var binding provider.RuntimeBinding
	for _, operation := range []provider.Operation{provider.OperationChat, provider.OperationResponses, provider.OperationMessages} {
		candidate, ok := s.Bindings[provider.RuntimeBindingKey(node.DefinitionID, operation)]
		if ok && candidate.Adapter != nil && candidate.Protocol == kernel.Protocol(node.Protocol) && candidate.TaskRef.ID == string(normalize.OperationChatGenerate) {
			binding = candidate
			break
		}
	}
	if binding.Adapter == nil {
		return InferenceTestResult{}, fmt.Errorf("provider definition %q has no compatible inference operation for a connection probe", node.DefinitionID)
	}
	authMode, authModeExists := binding.AuthModes[credential.Type]
	if !authModeExists || authMode.Flow == nil {
		return InferenceTestResult{}, fmt.Errorf("provider definition %q does not bind credential type %q", node.DefinitionID, credential.Type)
	}
	resolvedCredential, err := authMode.Flow.Resolve(ctx, provider.AuthInput{ConnectionID: connectionID, Type: credential.Type, Secret: credential.Secret})
	if err != nil {
		return InferenceTestResult{}, fmt.Errorf("resolve inference credential: %w", err)
	}
	if err := resolvedCredential.ValidateRuntimeContext(); err != nil {
		return InferenceTestResult{}, err
	}
	sourceFormat, path, body, err := inferenceProbePayload(binding.Operation, modelID)
	if err != nil {
		return InferenceTestResult{}, err
	}
	normalized, err := normalize.Map(path, http.Header{}, body)
	if err != nil {
		return InferenceTestResult{}, fmt.Errorf("build inference probe: %w", err)
	}
	request := normalized.Request
	request.Model = modelID
	request.Operation = normalize.Operation(binding.TaskRef.ID)
	request.OperationContractVersion = binding.TaskRef.ContractVersion
	request.SourceFormat = sourceFormat
	request.Stream = false
	route := kernel.Route{
		ID: connectionID + ":inference-probe", NodeID: node.ID, DefinitionID: node.DefinitionID,
		DisplayPrefix: node.Prefix, ExternalModel: modelID, Protocol: binding.Protocol,
		BaseURL: node.BaseURL, CredentialID: connectionID, CredentialType: selected.CredentialType,
		CredentialPresentation: authMode.Presentation,
		Enabled:                true, OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{
			request.Operation: {ContractVersion: binding.TaskRef.ContractVersion, AdapterIDs: []string{binding.AdapterID}},
		},
	}
	upstream, err := binding.Adapter.Prepare(ctx, request, route, resolvedCredential)
	if err != nil {
		return InferenceTestResult{}, fmt.Errorf("prepare inference probe for model %q: %w", modelID, err)
	}
	response, err := binding.Adapter.Execute(ctx, upstream)
	if err != nil {
		return InferenceTestResult{}, fmt.Errorf("send inference probe for model %q: %w", modelID, err)
	}
	if response.Body != nil {
		defer response.Body.Close()
	}
	result := InferenceTestResult{ProviderNodeID: node.ID, ConnectionID: connectionID, ModelID: modelID, Operation: string(binding.Operation), Endpoint: endpointSummary(upstream.URL), HTTPStatus: response.Status}
	if response.Body == nil {
		return result, fmt.Errorf("inference probe returned an empty response body")
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, inferenceProbeResponseLimit+1))
	if err != nil {
		return result, fmt.Errorf("read inference probe response: %w", err)
	}
	if len(payload) > inferenceProbeResponseLimit {
		return result, fmt.Errorf("inference probe response exceeded %d bytes", inferenceProbeResponseLimit)
	}
	result.ResponseBytes = len(payload)
	if response.Status < http.StatusOK || response.Status >= http.StatusMultipleChoices {
		class := binding.Adapter.ClassifyError(response.Status, payload)
		return result, fmt.Errorf("inference probe returned HTTP %d (%s)", response.Status, class)
	}
	if len(payload) == 0 || !json.Valid(payload) {
		return result, fmt.Errorf("inference probe returned a non-JSON or empty response")
	}
	return result, nil
}

func inferenceProbePayload(operation provider.Operation, modelID string) (normalize.Format, string, map[string]any, error) {
	const prompt = "Reply with OK."
	switch operation {
	case provider.OperationChat:
		return normalize.FormatOpenAIChat, "/v1/chat/completions", map[string]any{
			"model": modelID, "max_tokens": 1,
			"messages": []any{map[string]any{"role": "user", "content": prompt}},
		}, nil
	case provider.OperationResponses:
		return normalize.FormatOpenAIResponses, "/v1/responses", map[string]any{
			"model": modelID, "max_output_tokens": 1, "input": prompt,
		}, nil
	case provider.OperationMessages:
		return normalize.FormatAnthropic, "/v1/messages", map[string]any{
			"model": modelID, "max_tokens": 1,
			"messages": []any{map[string]any{"role": "user", "content": prompt}},
		}, nil
	default:
		return "", "", nil, fmt.Errorf("provider operation %q has no generic inference probe", operation)
	}
}

// PreviewConnectionModels fetches the model list for review without writing
// catalog or entitlement state. Import remains a separate explicit operation.
func (s Service) PreviewConnectionModels(ctx context.Context, nodeID, connectionID string) (ConnectionModelsPreview, error) {
	fetched, err := s.fetchModels(ctx, nodeID, connectionID, false)
	if err != nil {
		return ConnectionModelsPreview{}, err
	}
	catalog := fetched.Snapshot
	limit := min(len(catalog.Models), connectionModelReviewLimit)
	models := make([]ModelPreview, 0, limit)
	for _, model := range catalog.Models[:limit] {
		models = append(models, ModelPreview{ID: model.ID, DisplayName: model.DisplayName, Profile: model.Profile})
	}
	var assignedModelIDs []string
	if catalog.Evidence == provider.ModelCatalogProviderRegistry {
		assignedModelIDs, err = s.Store.StaticCatalogAssignedModels(connectionID, nodeID)
		if err != nil {
			return ConnectionModelsPreview{}, err
		}
	}
	return ConnectionModelsPreview{ProviderNodeID: nodeID, ConnectionID: connectionID, Endpoint: fetched.EndpointLabel(), Evidence: catalog.Evidence, AssignedModelIDs: assignedModelIDs, ModelsFound: len(catalog.Models), Models: models, PreviewLimit: connectionModelReviewLimit, Truncated: len(catalog.Models) > limit, Complete: catalog.Complete}, nil
}

func (s Service) fetchModels(ctx context.Context, nodeID, connectionID string, test bool) (fetchedModelCatalog, error) {
	node, err := s.Store.ProviderNode(nodeID)
	if err != nil {
		return fetchedModelCatalog{}, err
	}
	if node.ID == "" {
		return fetchedModelCatalog{}, fmt.Errorf("provider node %q not found", nodeID)
	}
	if node.BaseURL == "" {
		return fetchedModelCatalog{}, fmt.Errorf("provider node %q has no base URL", nodeID)
	}
	if connectionID == "" {
		return fetchedModelCatalog{}, fmt.Errorf("connection ID is required")
	}
	connections, err := s.Store.Connections(nodeID)
	if err != nil {
		return fetchedModelCatalog{}, err
	}
	var selected store.ConnectionRecord
	for _, connection := range connections {
		if connection.ID == connectionID && connection.Enabled {
			selected = connection
			break
		}
	}
	if selected.ID == "" {
		return fetchedModelCatalog{}, fmt.Errorf("enabled connection %q does not belong to provider %q", connectionID, nodeID)
	}
	binding, ok := s.Bindings[provider.RuntimeBindingKey(node.DefinitionID, provider.OperationModels)]
	if !ok || binding.ModelSource == nil {
		return fetchedModelCatalog{}, fmt.Errorf("provider definition %q does not provide model discovery", node.DefinitionID)
	}
	if static, isStatic := binding.ModelSource.(provider.StaticModelCatalogSource); isStatic && static.StaticCatalog() {
		if test {
			return fetchedModelCatalog{}, fmt.Errorf("model source %q is a static catalog and cannot test connection health", binding.ModelSource.ID())
		}
		catalog, err := static.ListSnapshot(ctx, provider.ModelSourceInput{})
		if err != nil {
			return fetchedModelCatalog{}, err
		}
		return fetchedModelCatalog{Node: node, Snapshot: catalog}, nil
	}
	if binding.Endpoint == nil || binding.Transport == nil {
		return fetchedModelCatalog{}, fmt.Errorf("model source %q requires endpoint and transport primitives", binding.ModelSource.ID())
	}
	endpoint, err := binding.Endpoint.Resolve(node.BaseURL, node.ModelsPath, binding.EndpointOptions)
	if err != nil {
		return fetchedModelCatalog{}, fmt.Errorf("resolve model-list endpoint: %w", err)
	}
	credential, found := s.Store.ConnectionCredentialByID(connectionID)
	if !found {
		return fetchedModelCatalog{}, fmt.Errorf("enabled credential for connection %q is unavailable", connectionID)
	}
	authMode, authModeExists := binding.AuthModes[credential.Type]
	if !authModeExists || authMode.Flow == nil {
		return fetchedModelCatalog{}, fmt.Errorf("provider definition %q does not bind credential type %q for model discovery", node.DefinitionID, credential.Type)
	}
	resolvedCredential, err := authMode.Flow.Resolve(ctx, provider.AuthInput{ConnectionID: connectionID, Type: credential.Type, Secret: credential.Secret})
	if err != nil {
		return fetchedModelCatalog{}, fmt.Errorf("resolve discovery credential: %w", err)
	}
	if err := resolvedCredential.ValidateRuntimeContext(); err != nil {
		return fetchedModelCatalog{}, err
	}
	input := provider.ModelSourceInput{URL: endpoint, Credential: resolvedCredential, Presentation: authMode.Presentation, Client: s.Client}
	var catalog provider.ModelCatalogSnapshot
	if test {
		if tester, ok := binding.ModelSource.(provider.ConnectionTestModelCatalogSnapshotSource); ok {
			catalog, err = tester.TestSnapshot(ctx, input)
		} else if tester, ok := binding.ModelSource.(provider.ConnectionTestModelSource); ok {
			catalog.Models, err = tester.Test(ctx, input)
		} else {
			return fetchedModelCatalog{}, fmt.Errorf("model source %q does not provide an active endpoint test", binding.ModelSource.ID())
		}
	} else if source, ok := binding.ModelSource.(provider.ModelCatalogSnapshotSource); ok {
		catalog, err = source.ListSnapshot(ctx, input)
	} else {
		catalog.Models, err = binding.ModelSource.List(ctx, input)
	}
	if err != nil {
		if test {
			return fetchedModelCatalog{}, fmt.Errorf("test model-list endpoint for connection %q: %w", connectionID, err)
		}
		return fetchedModelCatalog{}, err
	}
	if catalog.Evidence == "" {
		catalog.Evidence = provider.ModelCatalogConnectionObserved
	}
	return fetchedModelCatalog{Node: node, Endpoint: endpoint, Snapshot: catalog}, nil
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
	node, err := s.Store.ProviderNode(nodeID)
	if err != nil {
		return Result{}, err
	}
	if binding, ok := s.Bindings[provider.RuntimeBindingKey(node.DefinitionID, provider.OperationModels)]; ok {
		if source, static := binding.ModelSource.(provider.StaticModelCatalogSource); static && source.StaticCatalog() {
			return Result{}, fmt.Errorf("static model catalogs require an explicitly selected connection")
		}
	}
	return s.RefreshConnection(ctx, nodeID, stored.ID)
}

// RefreshConnection imports model IDs using the explicitly selected enabled
// connection. It is intentionally separate from TestConnection's read-only
// preview, so callers choose when catalog state is mutated.
func (s Service) RefreshConnection(ctx context.Context, nodeID, connectionID string) (Result, error) {
	return s.ImportConnectionModels(ctx, nodeID, connectionID, ModelImportOptions{})
}

// ImportConnectionModels imports all returned model IDs when selectedIDs is
// nil, or the requested exact IDs otherwise. Entitlement evidence records the
// complete connection-observed response even when the user imports a subset.
// Provider-registry catalogs instead synchronize explicit assignments and do
// not manufacture entitlement evidence.
func (s Service) ImportConnectionModels(ctx context.Context, nodeID, connectionID string, options ModelImportOptions) (Result, error) {
	fetched, err := s.fetchModels(ctx, nodeID, connectionID, false)
	if err != nil {
		return Result{}, err
	}
	catalog := fetched.Snapshot
	if options.EntitlementsOnly && options.ModelIDs != nil {
		return Result{}, fmt.Errorf("entitlements-only import cannot be combined with selected model IDs")
	}
	if options.EntitlementsOnly && catalog.Evidence == provider.ModelCatalogProviderRegistry {
		return Result{}, fmt.Errorf("static provider registries have no connection entitlement evidence to record")
	}
	evidenceSource := "models_endpoint"
	if catalog.Evidence == provider.ModelCatalogProviderRegistry {
		evidenceSource = "static_model_catalog"
	}
	selectedIDs := options.ModelIDs
	if options.EntitlementsOnly {
		selectedIDs = []string{}
	}
	modelsToImport, err := selectModels(catalog.Models, selectedIDs)
	if err != nil {
		return Result{}, err
	}
	var selectedRouteIDs []string
	var selectedModelIDs []string
	for _, model := range modelsToImport {
		id := model.ID
		routeID := "route_" + hash(nodeID+"\x00"+id)
		raw := make(map[string]any, len(model.Metadata)+1)
		for key, value := range model.Metadata {
			raw[key] = value
		}
		raw["source"] = evidenceSource
		if err := s.Store.UpsertCatalogModel(store.UpsertCatalogModelInput{ID: routeID, ProviderNodeID: nodeID, Kind: "discovered", ExternalID: id, DisplayName: model.DisplayName, Profile: model.Profile, Raw: raw}); err != nil {
			return Result{}, err
		}
		selectedRouteIDs = append(selectedRouteIDs, routeID)
		selectedModelIDs = append(selectedModelIDs, id)
	}
	externalIDs := make([]string, 0, len(catalog.Models))
	for _, model := range catalog.Models {
		externalIDs = append(externalIDs, model.ID)
	}
	available := len(catalog.Models)
	modelIDs := externalIDs
	if catalog.Evidence == provider.ModelCatalogProviderRegistry {
		if err := s.Store.SetStaticCatalogAssignments(connectionID, fetched.Node.ID, selectedRouteIDs); err != nil {
			return Result{}, err
		}
		available = 0
		modelIDs = selectedModelIDs
	} else {
		if err := s.Store.RecordConnectionModelSnapshot(connectionID, fetched.Node.ID, externalIDs, catalog.Complete, evidenceSource); err != nil {
			return Result{}, err
		}
		if err := s.Store.ClearInvalidModelHealth(connectionID, externalIDs); err != nil {
			return Result{}, err
		}
	}
	return Result{NodeID: nodeID, ConnectionID: connectionID, Models: len(modelsToImport), ModelIDs: modelIDs, Available: available, CatalogModels: len(catalog.Models), Evidence: catalog.Evidence, Complete: catalog.Complete, URL: fetched.EndpointLabel()}, nil
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
