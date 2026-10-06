package provider

import (
	"fmt"

	"github.com/fm39hz/gobroom/internal/kernel"
)

type RuntimeBinding struct {
	DefinitionID      string
	Operation         Operation
	EndpointID        string
	Endpoint          kernel.Endpoint
	EndpointOptions   kernel.EndpointOptions
	TransportID       string
	Transport         kernel.Transport
	RequestCodecID    string
	ResponseCodecID   string
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
	result := RuntimeBinding{DefinitionID: definitionID, Operation: operation, EndpointID: binding.Endpoint.ID, Endpoint: endpoint, EndpointOptions: endpointOptions, TransportID: binding.Transport.ID, Transport: transport, RequestCodecID: binding.RequestCodec.ID, ResponseCodecID: binding.ResponseCodec.ID, AuthFlowID: authFlowID, Auth: auth, ModelSourceID: binding.ModelSource.ID, ModelSource: modelSource, UsageSourceID: binding.UsageSource.ID, UsageOptions: binding.UsageOptions, SessionStoreID: sessionStoreID, SessionStore: sessionStore, ErrorClassifierID: errorClassifierID, ErrorClassifier: classifier, QuotaSourceID: binding.QuotaSource.ID, QuotaSource: quotaSource, QuotaWindowName: binding.QuotaWindowName}
	if operation != OperationChat && operation != OperationResponses && operation != OperationMessages {
		return result, nil
	}
	requestCodec, ok := b.registry.requestCodecs[binding.RequestCodec.ID]
	if !ok {
		return RuntimeBinding{}, fmt.Errorf("request codec %q is not registered", binding.RequestCodec.ID)
	}
	responseCodec, ok := b.registry.responseCodecs[binding.ResponseCodec.ID]
	if !ok {
		return RuntimeBinding{}, fmt.Errorf("response codec %q is not registered", binding.ResponseCodec.ID)
	}
	result.AdapterID = RuntimeBindingKey(definitionID, operation)
	result.Adapter = kernel.ComposedAdapter{AdapterID: result.AdapterID, AdapterProtocol: protocolForOperation(operation), Endpoint: endpoint, EndpointOptions: endpointOptions, Request: requestCodec, Transport: transport, Response: responseCodec}
	return result, nil
}

func protocolForOperation(operation Operation) kernel.Protocol {
	switch operation {
	case OperationResponses:
		return kernel.ProtocolOpenAIResponses
	case OperationMessages:
		return kernel.ProtocolAnthropic
	default:
		return kernel.ProtocolOpenAIChat
	}
}

func RuntimeBindingKey(definitionID string, operation Operation) string {
	return definitionID + ":" + string(operation)
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

func OperationForProtocol(protocol kernel.Protocol) (Operation, bool) {
	switch protocol {
	case kernel.ProtocolOpenAIChat, kernel.ProtocolGemini:
		return OperationChat, true
	case kernel.ProtocolOpenAIResponses:
		return OperationResponses, true
	case kernel.ProtocolAnthropic:
		return OperationMessages, true
	default:
		return "", false
	}
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
