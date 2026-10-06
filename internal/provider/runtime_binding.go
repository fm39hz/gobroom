package provider

import (
	"fmt"
	"sort"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

type RuntimeBinding struct {
	DefinitionID      string
	Operation         Operation
	Task              normalize.Operation
	Protocol          kernel.Protocol
	ProviderFormat    normalize.Format
	EndpointID        string
	Endpoint          kernel.Endpoint
	EndpointOptions   kernel.EndpointOptions
	TransportID       string
	Transport         kernel.Transport
	RequestCodecID    string
	ResponseDecoderID string
	AdapterID         string
	Adapter           kernel.ProviderAdapter
	AuthFlowID        string
	Auth              AuthFlow
	ModelSourceID     string
	ModelSource       ModelSource
	UsageSourceID     string
	UsageOptions      kernel.UsageSourceOptions
	SessionStoreID    string
	SessionStore      kernel.SessionStore
	ErrorClassifierID string
	ErrorClassifier   ErrorClassifier
	QuotaSourceID     string
	QuotaSource       QuotaSource
	QuotaWindowName   string
}

// DefinitionMetadata is the safe, frontend-facing projection of a provider
// definition. It deliberately excludes credentials, auth options, endpoint
// options and arbitrary defaults while exposing the schemas needed to build
// generic setup forms.
type DefinitionMetadata struct {
	ID           string              `json:"id"`
	Version      string              `json:"version"`
	DisplayName  string              `json:"displayName"`
	Aliases      []string            `json:"aliases,omitempty"`
	Capabilities CapabilitySet       `json:"capabilities"`
	Auth         AuthMetadata        `json:"auth"`
	Operations   []OperationMetadata `json:"operations"`
}

type AuthMetadata struct {
	ID          string      `json:"id"`
	SetupSchema SetupSchema `json:"setupSchema"`
}

type OperationMetadata struct {
	ID             Operation           `json:"id"`
	Protocol       kernel.Protocol     `json:"protocol,omitempty"`
	Task           normalize.Operation `json:"task,omitempty"`
	ProviderFormat normalize.Format    `json:"providerFormat,omitempty"`
	Primitives     map[string]string   `json:"primitives"`
}

// DefinitionCatalog returns deterministic, secret-free metadata for generic
// control clients. Provider-specific behavior remains described by primitive
// IDs rather than frontend branches.
func (r *RuntimeRegistry) DefinitionCatalog() ([]DefinitionMetadata, error) {
	if r == nil || r.Primitives == nil {
		return nil, fmt.Errorf("provider runtime registry is not initialized")
	}
	definitions := r.Primitives.Definitions()
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].ID < definitions[j].ID })
	result := make([]DefinitionMetadata, 0, len(definitions))
	for _, definition := range definitions {
		flow, err := r.Auth.Build(definition.Auth.ID, definition.AuthOptions)
		if err != nil {
			return nil, fmt.Errorf("provider %q auth metadata: %w", definition.ID, err)
		}
		item := DefinitionMetadata{
			ID: definition.ID, Version: definition.Version,
			DisplayName:  definition.DisplayName,
			Aliases:      append([]string(nil), definition.Aliases...),
			Capabilities: definition.Capabilities,
			Auth:         AuthMetadata{ID: flow.ID(), SetupSchema: flow.SetupSchema()},
		}
		item.Auth.SetupSchema.Fields = append([]SetupField(nil), item.Auth.SetupSchema.Fields...)
		operations := make([]string, 0, len(definition.Operations))
		for operation := range definition.Operations {
			operations = append(operations, string(operation))
		}
		sort.Strings(operations)
		for _, rawOperation := range operations {
			operation := Operation(rawOperation)
			binding := definition.Operations[operation]
			primitives := map[string]string{
				"endpoint":        binding.Endpoint.ID,
				"transport":       binding.Transport.ID,
				"requestCodec":    binding.RequestCodec.ID,
				"responseDecoder": binding.ResponseDecoder.ID,
				"modelSource":     binding.ModelSource.ID,
				"usageSource":     binding.UsageSource.ID,
				"quotaSource":     binding.QuotaSource.ID,
				"errorClassifier": binding.ErrorClassifier.ID,
			}
			for primitive, id := range primitives {
				if id == "" {
					delete(primitives, primitive)
				}
			}
			item.Operations = append(item.Operations, OperationMetadata{
				ID: operation, Protocol: binding.Protocol, Task: binding.Task,
				ProviderFormat: binding.ProviderFormat, Primitives: primitives,
			})
		}
		result = append(result, item)
	}
	return result, nil
}

type RuntimeBindingBuilder struct {
	registry *RuntimeRegistry
}

func NewRuntimeBindingBuilder(registry *RuntimeRegistry) *RuntimeBindingBuilder {
	return &RuntimeBindingBuilder{registry: registry}
}

func (b *RuntimeBindingBuilder) Build(definitionID string, operation Operation) (RuntimeBinding, error) {
	if b == nil || b.registry == nil || b.registry.Primitives == nil {
		return RuntimeBinding{}, fmt.Errorf("runtime binding registry is not initialized")
	}
	definition, ok := b.registry.Primitives.Definition(definitionID)
	if !ok {
		return RuntimeBinding{}, fmt.Errorf("provider definition %q is not registered", definitionID)
	}
	binding, ok := definition.Operations[operation]
	if !ok {
		return RuntimeBinding{}, fmt.Errorf("provider %q has no %q operation", definitionID, operation)
	}
	var auth AuthFlow
	authFlowID := ""
	if definition.Auth.ID != "" {
		var err error
		auth, err = b.registry.Auth.Build(definition.Auth.ID, definition.AuthOptions)
		if err != nil {
			return RuntimeBinding{}, fmt.Errorf("build auth flow %q: %w", definition.Auth.ID, err)
		}
		authFlowID = AuthBindingKey(definition.ID, auth.ID())
	}
	var modelSource ModelSource
	if binding.ModelSource.ID != "" {
		var found bool
		modelSource, found = b.registry.modelSources[binding.ModelSource.ID]
		if !found {
			return RuntimeBinding{}, fmt.Errorf("model source %q is not registered", binding.ModelSource.ID)
		}
	}
	var sessionStore kernel.SessionStore
	sessionStoreID := ""
	if definition.Session.ID != "" {
		var found bool
		sessionStore, found = b.registry.sessionStores[definition.Session.ID]
		if !found {
			return RuntimeBinding{}, fmt.Errorf("session store %q is not registered", definition.Session.ID)
		}
		sessionStoreID = definition.Session.ID
	}
	if binding.UsageSource.ID != "" {
		if _, found := b.registry.usageSources[binding.UsageSource.ID]; !found {
			return RuntimeBinding{}, fmt.Errorf("usage source %q is not registered", binding.UsageSource.ID)
		}
	}
	var classifier ErrorClassifier
	errorClassifierID := binding.ErrorClassifier.ID
	if binding.ErrorClassifier.ID != "" {
		var found bool
		classifier, found = b.registry.errorClassifiers[binding.ErrorClassifier.ID]
		if !found {
			return RuntimeBinding{}, fmt.Errorf("error classifier %q is not registered", binding.ErrorClassifier.ID)
		}
		if options := binding.ErrorClassifierOptions.HTTPJSON; options != nil && !options.Empty() {
			configurable, ok := classifier.(ConfigurableErrorClassifier)
			if !ok {
				return RuntimeBinding{}, fmt.Errorf("error classifier %q does not accept HTTP/JSON options", binding.ErrorClassifier.ID)
			}
			configured, err := configurable.WithOptions(binding.ErrorClassifierOptions)
			if err != nil {
				return RuntimeBinding{}, fmt.Errorf("configure error classifier %q: %w", binding.ErrorClassifier.ID, err)
			}
			classifier = configured
			errorClassifierID = RuntimeErrorClassifierKey(definitionID, operation, binding.ErrorClassifier.ID)
		}
	}
	var quotaSource QuotaSource
	if binding.QuotaSource.ID != "" {
		var found bool
		quotaSource, found = b.registry.quotaSources[binding.QuotaSource.ID]
		if !found {
			return RuntimeBinding{}, fmt.Errorf("quota source %q is not registered", binding.QuotaSource.ID)
		}
	}
	endpoint, ok := b.registry.endpoints[binding.Endpoint.ID]
	if !ok {
		return RuntimeBinding{}, fmt.Errorf("endpoint %q is not registered", binding.Endpoint.ID)
	}
	transport, ok := b.registry.transports[binding.Transport.ID]
	if !ok {
		return RuntimeBinding{}, fmt.Errorf("transport %q is not registered", binding.Transport.ID)
	}
	query := make(map[string]string, len(binding.EndpointOptions.Query))
	for key, value := range binding.EndpointOptions.Query {
		query[key] = value
	}
	endpointOptions := binding.EndpointOptions
	endpointOptions.Query = query
	task := binding.Task
	if task == "" && binding.RequestCodec.ID != "" {
		task = normalize.Operation(operation)
	}
	result := RuntimeBinding{DefinitionID: definitionID, Operation: operation, Task: task, Protocol: binding.Protocol, ProviderFormat: binding.ProviderFormat, EndpointID: binding.Endpoint.ID, Endpoint: endpoint, EndpointOptions: endpointOptions, TransportID: binding.Transport.ID, Transport: transport, RequestCodecID: binding.RequestCodec.ID, ResponseDecoderID: binding.ResponseDecoder.ID, AuthFlowID: authFlowID, Auth: auth, ModelSourceID: binding.ModelSource.ID, ModelSource: modelSource, UsageSourceID: binding.UsageSource.ID, UsageOptions: binding.UsageOptions, SessionStoreID: sessionStoreID, SessionStore: sessionStore, ErrorClassifierID: errorClassifierID, ErrorClassifier: classifier, QuotaSourceID: binding.QuotaSource.ID, QuotaSource: quotaSource, QuotaWindowName: binding.QuotaWindowName}
	if binding.RequestCodec.ID == "" && binding.ResponseDecoder.ID == "" {
		return result, nil
	}
	requestCodec, ok := b.registry.requestCodecs[binding.RequestCodec.ID]
	if !ok {
		return RuntimeBinding{}, fmt.Errorf("request codec %q is not registered", binding.RequestCodec.ID)
	}
	responseDecoder, ok := b.registry.responseDecoders[binding.ResponseDecoder.ID]
	if !ok {
		return RuntimeBinding{}, fmt.Errorf("response codec %q is not registered", binding.ResponseDecoder.ID)
	}
	result.AdapterID = RuntimeBindingKey(definitionID, operation)
	if result.Protocol == "" {
		result.Protocol = kernel.Protocol(operation)
	}
	result.Adapter = kernel.ComposedAdapter{AdapterID: result.AdapterID, Endpoint: endpoint, EndpointOptions: endpointOptions, Request: requestCodec, Transport: transport, Response: responseDecoder, Renderers: b.registry.ResponseRenderers(), ProviderFormat: result.ProviderFormat}
	return result, nil
}

func RuntimeBindingKey(definitionID string, operation Operation) string {
	return definitionID + ":" + string(operation)
}

func RuntimeBindingsForProtocol(bindings map[string]RuntimeBinding, definitionID string, protocol kernel.Protocol) []RuntimeBinding {
	result := make([]RuntimeBinding, 0)
	for _, binding := range bindings {
		if binding.DefinitionID == definitionID && binding.Adapter != nil && binding.Protocol == protocol {
			result = append(result, binding)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Operation < result[j].Operation })
	return result
}

func RuntimeErrorClassifierKey(definitionID string, operation Operation, primitiveID string) string {
	return RuntimeBindingKey(definitionID, operation) + ":error:" + primitiveID
}

func AuthBindingKey(definitionID, authFlowID string) string {
	if definitionID == "" || authFlowID == "" {
		return ""
	}
	return definitionID + ":" + authFlowID
}

func (r *RuntimeRegistry) BuildBindings() (map[string]RuntimeBinding, error) {
	builder := NewRuntimeBindingBuilder(r)
	result := map[string]RuntimeBinding{}
	for _, definition := range r.Primitives.Definitions() {
		for operation := range definition.Operations {
			binding, err := builder.Build(definition.ID, operation)
			if err != nil {
				return nil, err
			}
			result[RuntimeBindingKey(definition.ID, operation)] = binding
		}
	}
	return result, nil
}
