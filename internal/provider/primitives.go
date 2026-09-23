package provider

import (
	"encoding/json"
	"fmt"

	"github.com/fm39hz/gobroom/internal/adapter/anthropic"
	"github.com/fm39hz/gobroom/internal/adapter/gemini"
	openai "github.com/fm39hz/gobroom/internal/adapter/openai"
	"github.com/fm39hz/gobroom/internal/kernel"
)

type Operation string

const (
	OperationChat       Operation = "chat"
	OperationResponses  Operation = "responses"
	OperationMessages   Operation = "messages"
	OperationModels     Operation = "models"
	OperationEmbeddings Operation = "embeddings"
	OperationImage      Operation = "image"
	OperationAudio      Operation = "audio"
	OperationVideo      Operation = "video"
	OperationUsage      Operation = "usage"
	OperationQuota      Operation = "quota"
)

type PrimitiveKind string

const (
	PrimitiveEndpoint        PrimitiveKind = "endpoint"
	PrimitiveAuth            PrimitiveKind = "auth"
	PrimitiveRequestCodec    PrimitiveKind = "request_codec"
	PrimitiveResponseCodec   PrimitiveKind = "response_codec"
	PrimitiveModelSource     PrimitiveKind = "model_source"
	PrimitiveUsageSource     PrimitiveKind = "usage_source"
	PrimitiveQuotaSource     PrimitiveKind = "quota_source"
	PrimitiveSessionStore    PrimitiveKind = "session_store"
	PrimitiveErrorClassifier PrimitiveKind = "error_classifier"
	PrimitiveExtension       PrimitiveKind = "extension"
)

type PrimitiveRef struct {
	Kind PrimitiveKind `json:"kind"`
	ID   string        `json:"id"`
}

type OperationBinding struct {
	Endpoint         PrimitiveRef `json:"endpoint"`
	RuntimeAdapterID string       `json:"runtimeAdapterId,omitempty"`
	RequestCodec     PrimitiveRef `json:"requestCodec,omitempty"`
	ResponseCodec    PrimitiveRef `json:"responseCodec,omitempty"`
	ModelSource      PrimitiveRef `json:"modelSource,omitempty"`
	UsageSource      PrimitiveRef `json:"usageSource,omitempty"`
	QuotaSource      PrimitiveRef `json:"quotaSource,omitempty"`
	ErrorClassifier  PrimitiveRef `json:"errorClassifier,omitempty"`
}

type CapabilitySet struct {
	Chat        bool `json:"chat,omitempty"`
	Responses   bool `json:"responses,omitempty"`
	Messages    bool `json:"messages,omitempty"`
	Embeddings  bool `json:"embeddings,omitempty"`
	Image       bool `json:"image,omitempty"`
	AudioInput  bool `json:"audioInput,omitempty"`
	AudioOutput bool `json:"audioOutput,omitempty"`
	Video       bool `json:"video,omitempty"`
	Tools       bool `json:"tools,omitempty"`
	Thinking    bool `json:"thinking,omitempty"`
	Streaming   bool `json:"streaming,omitempty"`
	Usage       bool `json:"usage,omitempty"`
	Quota       bool `json:"quota,omitempty"`
}

type ProviderDefinition struct {
	ID           string                         `json:"id"`
	Version      string                         `json:"version"`
	DisplayName  string                         `json:"displayName"`
	Aliases      []string                       `json:"aliases,omitempty"`
	Operations   map[Operation]OperationBinding `json:"operations"`
	Auth         PrimitiveRef                   `json:"auth,omitempty"`
	Session      PrimitiveRef                   `json:"session,omitempty"`
	Extensions   []PrimitiveRef                 `json:"extensions,omitempty"`
	Capabilities CapabilitySet                  `json:"capabilities"`
	Defaults     map[string]any                 `json:"defaults,omitempty"`
}

func DecodeProviderDefinitionJSON(data []byte) (ProviderDefinition, error) {
	var definition ProviderDefinition
	if err := json.Unmarshal(data, &definition); err != nil {
		return ProviderDefinition{}, fmt.Errorf("decode provider definition: %w", err)
	}
	return definition, nil
}

func EncodeProviderDefinitionJSON(definition ProviderDefinition) ([]byte, error) {
	return json.MarshalIndent(definition, "", "  ")
}

type PrimitivePlugin interface {
	Definition() ProviderDefinition
}

type PrimitiveRegistry struct {
	definitions map[string]ProviderDefinition
	primitives  map[PrimitiveKind]map[string]struct{}
}

type RuntimeRegistry struct {
	Primitives       *PrimitiveRegistry
	Auth             *AuthRegistry
	adapters         map[string]kernel.ProviderAdapter
	modelSources     map[string]ModelSource
	errorClassifiers map[string]ErrorClassifier
	quotaSources     map[string]QuotaSource
}

func NewRuntimeRegistry() (*RuntimeRegistry, error) {
	primitives, err := NewBuiltinPrimitiveRegistry()
	if err != nil {
		return nil, err
	}
	runtime := &RuntimeRegistry{Primitives: primitives, Auth: NewAuthRegistry(), adapters: map[string]kernel.ProviderAdapter{}, modelSources: map[string]ModelSource{}, errorClassifiers: map[string]ErrorClassifier{}, quotaSources: map[string]QuotaSource{}}
	runtime.modelSources["openai-models"] = OpenAIModelSource{}
	runtime.modelSources["static-models"] = StaticModelSource{}
	runtime.errorClassifiers["http-json"] = HTTPJSONErrorClassifier{}
	runtime.quotaSources["none"] = NoopQuotaSource{}
	runtime.quotaSources["http-json-quota"] = HTTPJSONQuotaSource{}
	if err := runtime.RegisterAdapter("openai-chat", openai.NewAdapter()); err != nil {
		return nil, err
	}
	if err := runtime.RegisterAdapter("openai-responses", openai.NewResponsesAdapter()); err != nil {
		return nil, err
	}
	if err := runtime.RegisterAdapter("anthropic-messages", anthropic.NewAdapter()); err != nil {
		return nil, err
	}
	if err := runtime.RegisterAdapter("gemini", gemini.NewAdapter()); err != nil {
		return nil, err
	}
	return runtime, nil
}

func (r *RuntimeRegistry) RegisterAdapter(id string, adapter kernel.ProviderAdapter) error {
	if r == nil || r.Primitives == nil {
		return fmt.Errorf("runtime registry is not initialized")
	}
	if adapter == nil {
		return fmt.Errorf("adapter for %q is nil", id)
	}
	r.adapters[id] = adapter
	return nil
}

func (r *RuntimeRegistry) Adapters() map[string]kernel.ProviderAdapter {
	result := make(map[string]kernel.ProviderAdapter, len(r.adapters))
	for id, adapter := range r.adapters {
		result[id] = adapter
	}
	return result
}

func (r *RuntimeRegistry) ErrorClassifiers() map[string]kernel.ErrorClassifier {
	result := make(map[string]kernel.ErrorClassifier, len(r.errorClassifiers))
	for id, classifier := range r.errorClassifiers {
		result[id] = classifierAdapter{classifier}
	}
	return result
}

func (r *RuntimeRegistry) QuotaSources() map[string]QuotaSource {
	result := make(map[string]QuotaSource, len(r.quotaSources))
	for id, source := range r.quotaSources {
		result[id] = source
	}
	return result
}

type classifierAdapter struct{ classifier ErrorClassifier }

func (c classifierAdapter) ClassifyError(status int, body []byte) kernel.ErrorClass {
	return c.classifier.Classify(status, body)
}

func RuntimeAdapterIDForProtocol(protocol kernel.Protocol) string {
	switch protocol {
	case kernel.ProtocolOpenAIChat:
		return "openai-chat"
	case kernel.ProtocolOpenAIResponses:
		return "openai-responses"
	case kernel.ProtocolAnthropic:
		return "anthropic-messages"
	case kernel.ProtocolGemini:
		return "gemini"
	default:
		return ""
	}
}

func NewPrimitiveRegistry() *PrimitiveRegistry {
	return &PrimitiveRegistry{
		definitions: map[string]ProviderDefinition{},
		primitives: map[PrimitiveKind]map[string]struct{}{
			PrimitiveEndpoint: {}, PrimitiveAuth: {}, PrimitiveRequestCodec: {},
			PrimitiveResponseCodec: {}, PrimitiveModelSource: {}, PrimitiveUsageSource: {},
			PrimitiveQuotaSource: {}, PrimitiveSessionStore: {},
			PrimitiveErrorClassifier: {}, PrimitiveExtension: {},
		},
	}
}

func (r *PrimitiveRegistry) RegisterPrimitive(kind PrimitiveKind, id string) error {
	if r == nil {
		return fmt.Errorf("primitive registry is nil")
	}
	if id == "" {
		return fmt.Errorf("primitive ID is required")
	}
	set, ok := r.primitives[kind]
	if !ok {
		return fmt.Errorf("unknown primitive kind %q", kind)
	}
	if _, exists := set[id]; exists {
		return fmt.Errorf("primitive %s/%q already registered", kind, id)
	}
	set[id] = struct{}{}
	return nil
}

func (r *PrimitiveRegistry) RegisterDefinition(def ProviderDefinition) error {
	if r == nil {
		return fmt.Errorf("primitive registry is nil")
	}
	if def.ID == "" || def.Version == "" || def.DisplayName == "" {
		return fmt.Errorf("provider definition requires ID, version and display name")
	}
	if _, exists := r.definitions[def.ID]; exists {
		return fmt.Errorf("provider definition %q already registered", def.ID)
	}
	if len(def.Operations) == 0 {
		return fmt.Errorf("provider %q has no operations", def.ID)
	}
	if def.Auth.ID != "" && !r.hasPrimitive(def.Auth) {
		return fmt.Errorf("provider %q references unknown auth primitive %q", def.ID, def.Auth.ID)
	}
	for operation, binding := range def.Operations {
		if operation == "" || binding.Endpoint.ID == "" {
			return fmt.Errorf("provider %q has incomplete %q operation binding", def.ID, operation)
		}
		if operation != OperationModels && (binding.RequestCodec.ID == "" || binding.ResponseCodec.ID == "") {
			return fmt.Errorf("provider %q has incomplete %q codec binding", def.ID, operation)
		}
		for _, ref := range []PrimitiveRef{binding.Endpoint, binding.RequestCodec, binding.ResponseCodec, binding.ModelSource, binding.UsageSource, binding.QuotaSource, binding.ErrorClassifier} {
			if ref.ID != "" && !r.hasPrimitive(ref) {
				return fmt.Errorf("provider %q references unknown %s primitive %q", def.ID, ref.Kind, ref.ID)
			}
		}
	}
	for _, extension := range def.Extensions {
		if extension.ID == "" || !r.hasPrimitive(extension) {
			return fmt.Errorf("provider %q references unknown extension %q", def.ID, extension.ID)
		}
	}
	r.definitions[def.ID] = def
	return nil
}

func (r *PrimitiveRegistry) RegisterPlugin(plugin PrimitivePlugin) error {
	if plugin == nil {
		return fmt.Errorf("provider plugin is nil")
	}
	return r.RegisterDefinition(plugin.Definition())
}

func (r *PrimitiveRegistry) Definition(id string) (ProviderDefinition, bool) {
	definition, ok := r.definitions[id]
	return definition, ok
}

func (r *PrimitiveRegistry) Definitions() []ProviderDefinition {
	result := make([]ProviderDefinition, 0, len(r.definitions))
	for _, definition := range r.definitions {
		result = append(result, definition)
	}
	return result
}

func (r *PrimitiveRegistry) hasPrimitive(ref PrimitiveRef) bool {
	if ref.ID == "" {
		return false
	}
	set, ok := r.primitives[ref.Kind]
	if !ok {
		return false
	}
	_, exists := set[ref.ID]
	return exists
}
