package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

func extensionRef(ref PrimitiveRef) extensions.Ref {
	return extensions.Ref{Kind: string(ref.Kind), ID: ref.ID, ContractVersion: ref.ContractVersion}
}

type RuntimeBinding struct {
	DefinitionID        string
	DefinitionRef       extensions.Ref
	Operation           Operation
	TaskRef             extensions.Ref
	IdempotencyHeader   string
	Protocol            kernel.Protocol
	ProviderFormat      normalize.Format
	EndpointID          string
	EndpointRef         extensions.Ref
	Endpoint            kernel.Endpoint
	EndpointOptions     kernel.EndpointOptions
	TransportID         string
	TransportRef        extensions.Ref
	Transport           kernel.Transport
	RequestCodecID      string
	RequestCodecOptions json.RawMessage
	ResponseDecoderID   string
	AdapterID           string
	Adapter             kernel.ProviderAdapter
	AuthModes           map[string]BoundAuthMode
	ModelSourceID       string
	ModelSource         ModelSource
	UsageSourceID       string
	UsageSourceRef      extensions.Ref
	UsageOptions        kernel.UsageSourceOptions
	SessionStoreID      string
	SessionStoreRef     extensions.Ref
	SessionStore        kernel.SessionStore
	ErrorClassifierID   string
	ErrorClassifierRef  extensions.Ref
	ErrorClassifier     ErrorClassifier
	QuotaSourceID       string
	QuotaSourceRef      extensions.Ref
	QuotaSource         QuotaSource
	QuotaWindowName     string
	QuotaEndpointRef    extensions.Ref
	QuotaTransportRef   extensions.Ref
}

// DefinitionMetadata is the safe, frontend-facing projection of a provider
// definition. It deliberately excludes credentials, auth options, endpoint
// options and arbitrary defaults while exposing the schemas needed to build
// generic setup forms.
type DefinitionMetadata struct {
	ContractVersion       uint64              `json:"contractVersion"`
	ID                    string              `json:"id"`
	Version               string              `json:"version"`
	DisplayName           string              `json:"displayName"`
	Aliases               []string            `json:"aliases,omitempty"`
	Capabilities          CapabilitySet       `json:"capabilities"`
	DefaultCredentialType string              `json:"defaultCredentialType"`
	AuthModes             []AuthModeMetadata  `json:"authModes"`
	Operations            []OperationMetadata `json:"operations"`
}

type AuthModeMetadata struct {
	CredentialType      string      `json:"credentialType"`
	ID                  string      `json:"id"`
	SetupSchema         SetupSchema `json:"setupSchema"`
	AuthorizationCode   bool        `json:"authorizationCode,omitempty"`
	DeviceAuthorization bool        `json:"deviceAuthorization,omitempty"`
}

type BoundAuthMode struct {
	FlowID       string
	Flow         AuthFlow
	Presentation kernel.CredentialPresentation
}

type OperationMetadata struct {
	ID             Operation               `json:"id"`
	Protocol       kernel.Protocol         `json:"protocol,omitempty"`
	TaskRef        *extensions.Ref         `json:"task,omitempty"`
	ProviderFormat normalize.Format        `json:"providerFormat,omitempty"`
	Primitives     map[string]PrimitiveRef `json:"primitives"`
}

// DefinitionCatalog returns deterministic, secret-free metadata for generic
// control clients. Provider-specific behavior remains described by primitive
// IDs rather than frontend branches.
func (r *RuntimeRegistry) DefinitionCatalog() ([]DefinitionMetadata, error) {
	if r == nil || r.Primitives == nil {
		return nil, fmt.Errorf("provider runtime registry is not initialized")
	}
	definitions := r.Primitives.Definitions()
	sort.Slice(definitions, func(i, j int) bool {
		left, right := ProviderDefinitionRef(definitions[i].ID, definitions[i].ContractVersion), ProviderDefinitionRef(definitions[j].ID, definitions[j].ContractVersion)
		if left.ID == right.ID {
			return left.ContractVersion < right.ContractVersion
		}
		return left.ID < right.ID
	})
	result := make([]DefinitionMetadata, 0, len(definitions))
	for _, definition := range definitions {
		item := DefinitionMetadata{
			ContractVersion: definition.ContractVersion,
			ID:              definition.ID, Version: definition.Version,
			DisplayName:           definition.DisplayName,
			Aliases:               append([]string(nil), definition.Aliases...),
			Capabilities:          definition.Capabilities,
			DefaultCredentialType: definition.DefaultCredentialType,
		}
		for _, mode := range definition.AuthModes {
			flow, err := r.Auth.Build(extensionRef(mode.Auth), mode.AuthOptions)
			if err != nil {
				return nil, fmt.Errorf("provider %q auth mode %q metadata: %w", definition.ID, mode.CredentialType, err)
			}
			metadata := AuthModeMetadata{CredentialType: mode.CredentialType, ID: flow.ID(), SetupSchema: flow.SetupSchema()}
			metadata.AuthorizationCode, metadata.DeviceAuthorization = InteractiveAuthModes(flow)
			if metadata.AuthorizationCode || metadata.DeviceAuthorization {
				for i := range metadata.SetupSchema.Fields {
					if metadata.SetupSchema.Fields[i].Name == "access_token" || metadata.SetupSchema.Fields[i].Name == "secret" {
						metadata.SetupSchema.Fields[i].Required = false
					}
				}
			}
			metadata.SetupSchema.Fields = append([]SetupField(nil), metadata.SetupSchema.Fields...)
			item.AuthModes = append(item.AuthModes, metadata)
		}
		operations := make([]string, 0, len(definition.Operations))
		for operation := range definition.Operations {
			operations = append(operations, string(operation))
		}
		sort.Strings(operations)
		for _, rawOperation := range operations {
			operation := Operation(rawOperation)
			binding := definition.Operations[operation]
			primitives := map[string]PrimitiveRef{
				"endpoint":        binding.Endpoint,
				"transport":       binding.Transport,
				"requestCodec":    binding.RequestCodec,
				"responseDecoder": binding.ResponseDecoder,
				"modelSource":     binding.ModelSource,
				"usageSource":     binding.UsageSource,
				"quotaSource":     binding.QuotaSource,
				"errorClassifier": binding.ErrorClassifier,
			}
			for primitive, ref := range primitives {
				if ref.ID == "" {
					delete(primitives, primitive)
				}
			}
			item.Operations = append(item.Operations, OperationMetadata{
				ID: operation, Protocol: binding.Protocol, TaskRef: binding.TaskRef,
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

// RequestArtifactRoleMapper declares the exact operation artifact ports a
// request codec can encode so manifest bindings can fail closed at startup.
type RequestArtifactRoleMapper interface {
	ArtifactRoleMappings() map[string]string
}

func NewRuntimeBindingBuilder(registry *RuntimeRegistry) *RuntimeBindingBuilder {
	return &RuntimeBindingBuilder{registry: registry}
}

func (b *RuntimeBindingBuilder) bindPrimitive(ref PrimitiveRef) (any, error) {
	return b.bindPrimitiveOptions(ref, json.RawMessage(`{}`))
}

func (b *RuntimeBindingBuilder) bindPrimitiveOptions(ref PrimitiveRef, options json.RawMessage) (any, error) {
	snapshot, err := b.registry.FreezeCatalog()
	if err != nil {
		return nil, err
	}
	_, implementation, err := snapshot.Bind(extensionRef(ref), options)
	return implementation, err
}

func bindPrimitiveAs[T any](builder *RuntimeBindingBuilder, ref PrimitiveRef) (T, error) {
	return bindPrimitiveAsOptions[T](builder, ref, json.RawMessage(`{}`))
}

func bindPrimitiveAsOptions[T any](builder *RuntimeBindingBuilder, ref PrimitiveRef, options json.RawMessage) (T, error) {
	var zero T
	implementation, err := builder.bindPrimitiveOptions(ref, options)
	if err != nil {
		return zero, err
	}
	resolved, ok := implementation.(T)
	if !ok {
		return zero, fmt.Errorf("primitive %s/%q implementation has type %T", ref.Kind, ref.ID, implementation)
	}
	return resolved, nil
}

func (b *RuntimeBindingBuilder) Build(definitionRef extensions.Ref, operation Operation) (RuntimeBinding, error) {
	if b == nil || b.registry == nil || b.registry.Primitives == nil {
		return RuntimeBinding{}, fmt.Errorf("runtime binding registry is not initialized")
	}
	if _, err := b.registry.FreezeCatalog(); err != nil {
		return RuntimeBinding{}, fmt.Errorf("freeze extension catalog: %w", err)
	}
	if err := definitionRef.Validate(); err != nil || definitionRef.Kind != "provider-definition" {
		return RuntimeBinding{}, fmt.Errorf("runtime binding requires an exact provider-definition reference")
	}
	definition, ok := b.registry.Primitives.Definition(definitionRef)
	if !ok {
		return RuntimeBinding{}, fmt.Errorf("provider definition %q is not registered", definitionRef.Key())
	}
	definitionID := definition.ID
	binding, ok := definition.Operations[operation]
	if !ok {
		return RuntimeBinding{}, fmt.Errorf("provider %q has no %q operation", definitionID, operation)
	}
	authModes := make(map[string]BoundAuthMode, len(definition.AuthModes))
	for _, mode := range definition.AuthModes {
		authOptions, err := json.Marshal(mode.AuthOptions)
		if err != nil {
			return RuntimeBinding{}, fmt.Errorf("encode auth options for %q: %w", mode.Auth.ID, err)
		}
		flow, err := bindPrimitiveAsOptions[AuthFlow](b, mode.Auth, authOptions)
		if err != nil {
			return RuntimeBinding{}, fmt.Errorf("bind auth flow %q: %w", mode.Auth.ID, err)
		}
		flowID := AuthBindingKey(definitionRef, mode.CredentialType, mode.Auth)
		authModes[mode.CredentialType] = BoundAuthMode{FlowID: flowID, Flow: flow, Presentation: mode.Presentation}
	}
	var err error
	var modelSource ModelSource
	if binding.ModelSource.ID != "" {
		modelSource, err = bindPrimitiveAsOptions[ModelSource](b, binding.ModelSource, binding.ModelSourceOptions)
		if err != nil {
			return RuntimeBinding{}, fmt.Errorf("resolve model source %q: %w", binding.ModelSource.ID, err)
		}
		if operation == OperationModels && binding.Endpoint.ID == "" {
			if _, static := modelSource.(StaticModelCatalogSource); !static {
				return RuntimeBinding{}, fmt.Errorf("model source %q does not provide a static catalog and requires an endpoint", binding.ModelSource.ID)
			}
		}
	}
	var sessionStore kernel.SessionStore
	sessionStoreID := ""
	sessionStoreRef := extensions.Ref{}
	if definition.Session.ID != "" {
		sessionStore, err = bindPrimitiveAs[kernel.SessionStore](b, definition.Session)
		if err != nil {
			return RuntimeBinding{}, fmt.Errorf("resolve session store %q: %w", definition.Session.ID, err)
		}
		sessionStoreID = definition.Session.ID
		sessionStoreRef = extensionRef(definition.Session)
	}
	usageSourceRef := extensions.Ref{}
	if binding.UsageSource.ID != "" {
		options, marshalErr := json.Marshal(binding.UsageOptions)
		if marshalErr != nil {
			return RuntimeBinding{}, fmt.Errorf("encode usage options: %w", marshalErr)
		}
		if _, err := bindPrimitiveAsOptions[UsageSource](b, binding.UsageSource, options); err != nil {
			return RuntimeBinding{}, fmt.Errorf("resolve usage source %q: %w", binding.UsageSource.ID, err)
		}
		usageSourceRef = extensionRef(binding.UsageSource)
	}
	var classifier ErrorClassifier
	errorClassifierID := binding.ErrorClassifier.ID
	errorClassifierRef := extensions.Ref{}
	if binding.ErrorClassifier.ID != "" {
		options, marshalErr := json.Marshal(binding.ErrorClassifierOptions)
		if marshalErr != nil {
			return RuntimeBinding{}, fmt.Errorf("encode error classifier options: %w", marshalErr)
		}
		classifier, err = bindPrimitiveAsOptions[ErrorClassifier](b, binding.ErrorClassifier, options)
		if err != nil {
			return RuntimeBinding{}, fmt.Errorf("resolve error classifier %q: %w", binding.ErrorClassifier.ID, err)
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
			errorClassifierRef = RuntimeErrorClassifierRef(definitionRef, operation, binding.ErrorClassifier)
		} else {
			errorClassifierRef = extensionRef(binding.ErrorClassifier)
		}
	}
	var quotaSource QuotaSource
	quotaSourceRef := extensions.Ref{}
	if binding.QuotaSource.ID != "" {
		options := binding.QuotaSourceOptions
		if len(options) == 0 {
			options = json.RawMessage(`{}`)
		}
		quotaSource, err = bindPrimitiveAsOptions[QuotaSource](b, binding.QuotaSource, options)
		if err != nil {
			return RuntimeBinding{}, fmt.Errorf("resolve quota source %q: %w", binding.QuotaSource.ID, err)
		}
		quotaSourceRef = extensionRef(binding.QuotaSource)
		if len(binding.QuotaSourceOptions) > 0 && !bytes.Equal(bytes.TrimSpace(options), []byte(`{}`)) {
			quotaSourceRef = runtimeQuotaSourceRef(definitionRef, operation, binding.QuotaSource)
		}
	}
	endpointOptions := binding.EndpointOptions
	query := make(map[string]string, len(binding.EndpointOptions.Query))
	for key, value := range binding.EndpointOptions.Query {
		query[key] = value
	}
	endpointOptions.Query = query
	endpointOptionsJSON, err := json.Marshal(endpointOptions)
	if err != nil {
		return RuntimeBinding{}, fmt.Errorf("encode endpoint options: %w", err)
	}
	var endpoint kernel.Endpoint
	var transport kernel.Transport
	if binding.Endpoint.ID != "" {
		endpoint, err = bindPrimitiveAsOptions[kernel.Endpoint](b, binding.Endpoint, endpointOptionsJSON)
		if err != nil {
			return RuntimeBinding{}, fmt.Errorf("resolve endpoint %q: %w", binding.Endpoint.ID, err)
		}
		transport, err = bindPrimitiveAs[kernel.Transport](b, binding.Transport)
		if err != nil {
			return RuntimeBinding{}, fmt.Errorf("resolve transport %q: %w", binding.Transport.ID, err)
		}
	}
	taskRef := extensions.Ref{}
	if binding.TaskRef != nil {
		taskRef = *binding.TaskRef
	} else if binding.RequestCodec.ID != "" {
		return RuntimeBinding{}, fmt.Errorf("provider %q %q inference binding requires a semantic task ref", definitionID, operation)
	}
	result := RuntimeBinding{DefinitionID: definitionID, DefinitionRef: ProviderDefinitionRef(definition.ID, definition.ContractVersion), Operation: operation, TaskRef: taskRef, IdempotencyHeader: binding.IdempotencyHeader, Protocol: binding.Protocol, ProviderFormat: binding.ProviderFormat, EndpointID: binding.Endpoint.ID, EndpointRef: extensionRef(binding.Endpoint), Endpoint: endpoint, EndpointOptions: endpointOptions, TransportID: binding.Transport.ID, TransportRef: extensionRef(binding.Transport), Transport: transport, RequestCodecID: binding.RequestCodec.ID, RequestCodecOptions: append(json.RawMessage(nil), binding.RequestCodecOptions...), ResponseDecoderID: binding.ResponseDecoder.ID, AuthModes: authModes, ModelSourceID: binding.ModelSource.ID, ModelSource: modelSource, UsageSourceID: binding.UsageSource.ID, UsageSourceRef: usageSourceRef, UsageOptions: binding.UsageOptions, SessionStoreID: sessionStoreID, SessionStoreRef: sessionStoreRef, SessionStore: sessionStore, ErrorClassifierID: errorClassifierID, ErrorClassifierRef: errorClassifierRef, ErrorClassifier: classifier, QuotaSourceID: binding.QuotaSource.ID, QuotaSourceRef: quotaSourceRef, QuotaEndpointRef: extensionRef(binding.Endpoint), QuotaTransportRef: extensionRef(binding.Transport), QuotaSource: quotaSource, QuotaWindowName: binding.QuotaWindowName}
	if binding.RequestCodec.ID == "" && binding.ResponseDecoder.ID == "" {
		return result, nil
	}
	requestCodec, err := bindPrimitiveAsOptions[kernel.RequestCodec](b, binding.RequestCodec, binding.RequestCodecOptions)
	if err != nil {
		return RuntimeBinding{}, fmt.Errorf("resolve request codec %q: %w", binding.RequestCodec.ID, err)
	}
	if mapper, ok := requestCodec.(RequestArtifactRoleMapper); ok {
		operationDefinition, exists := b.registry.operationSnapshot.Resolve(taskRef)
		if !exists {
			return RuntimeBinding{}, fmt.Errorf("request codec %q task %q is not registered", binding.RequestCodec.ID, taskRef.Key())
		}
		mappings := mapper.ArtifactRoleMappings()
		ports := make(map[string]bool, len(operationDefinition.ArtifactInputs))
		for _, port := range operationDefinition.ArtifactInputs {
			ports[port.Role] = true
			if mappings[port.Role] == "" {
				return RuntimeBinding{}, fmt.Errorf("request codec %q does not map declared artifact role %q for operation %q", binding.RequestCodec.ID, port.Role, taskRef.Key())
			}
		}
		for role := range mappings {
			if !ports[role] {
				return RuntimeBinding{}, fmt.Errorf("request codec %q maps undeclared artifact role %q for operation %q", binding.RequestCodec.ID, role, taskRef.Key())
			}
		}
	}
	responseDecoder, err := bindPrimitiveAs[kernel.ResponseDecoder](b, binding.ResponseDecoder)
	if err != nil {
		return RuntimeBinding{}, fmt.Errorf("resolve response decoder %q: %w", binding.ResponseDecoder.ID, err)
	}
	result.AdapterID = RuntimeBindingKey(definitionRef, operation)
	if result.Protocol == "" {
		result.Protocol = kernel.Protocol(operation)
	}
	result.Adapter = kernel.ComposedAdapter{AdapterID: result.AdapterID, Endpoint: endpoint, EndpointOptions: endpointOptions, Request: requestCodec, Transport: transport, Response: responseDecoder, Renderers: b.registry.ResponseRenderers(), ProviderFormat: result.ProviderFormat}
	return result, nil
}

func RuntimeBindingKey(definitionRef extensions.Ref, operation Operation) string {
	return definitionRef.Key() + ":operation:" + string(operation)
}

func RuntimeBindingsForProtocol(bindings map[string]RuntimeBinding, definitionRef extensions.Ref, protocol kernel.Protocol) []RuntimeBinding {
	result := make([]RuntimeBinding, 0)
	for _, binding := range bindings {
		if binding.DefinitionRef == definitionRef && binding.Adapter != nil && binding.Protocol == protocol {
			result = append(result, binding)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Operation < result[j].Operation })
	return result
}

func RuntimeErrorClassifierRef(definitionRef extensions.Ref, operation Operation, primitive PrimitiveRef) extensions.Ref {
	return extensions.Ref{Kind: string(PrimitiveErrorClassifier), ID: RuntimeBindingKey(definitionRef, operation) + ":error:" + primitive.ID, ContractVersion: primitive.ContractVersion}
}

func runtimeQuotaSourceRef(definitionRef extensions.Ref, operation Operation, primitive PrimitiveRef) extensions.Ref {
	return extensions.Ref{Kind: string(PrimitiveQuotaSource), ID: RuntimeBindingKey(definitionRef, operation) + ":quota:" + primitive.ID, ContractVersion: primitive.ContractVersion}
}

func AuthBindingKey(definitionRef extensions.Ref, credentialType string, auth PrimitiveRef) string {
	if definitionRef.Validate() != nil || definitionRef.Kind != "provider-definition" || credentialType == "" || auth.ID == "" || auth.ContractVersion == 0 {
		return ""
	}
	return definitionRef.Key() + ":" + credentialType + ":" + extensionRef(auth).Key()
}

func (r *RuntimeRegistry) BuildBindings() (map[string]RuntimeBinding, error) {
	if _, err := r.FreezeCatalog(); err != nil {
		return nil, err
	}
	builder := NewRuntimeBindingBuilder(r)
	result := map[string]RuntimeBinding{}
	for _, definition := range r.Primitives.Definitions() {
		for operation := range definition.Operations {
			definitionRef := ProviderDefinitionRef(definition.ID, definition.ContractVersion)
			binding, err := builder.Build(definitionRef, operation)
			if err != nil {
				return nil, err
			}
			result[RuntimeBindingKey(definitionRef, operation)] = binding
		}
	}
	return result, nil
}
