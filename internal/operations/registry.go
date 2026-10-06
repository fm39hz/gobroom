// Package operations owns semantic task contracts independently from client
// and provider wire formats.
package operations

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/normalize"
)

type ReplaySafety string

const (
	ReplayBeforeDispatch ReplaySafety = "safe_before_dispatch"
	ReplayOnRejection    ReplaySafety = "confirmed_rejection_only"
	ReplayWithKey        ReplaySafety = "scoped_idempotency_key"
	ReplayNever          ReplaySafety = "unsafe_after_dispatch"
)

type Definition struct {
	Ref                 extensions.Ref
	DisplayName         string
	Description         string
	InputSchemaRef      extensions.Ref
	ResultSchemaRef     *extensions.Ref
	EventSchemaRefs     []extensions.Ref
	ReplaySafety        ReplaySafety
	InputPayload        func(normalize.Request) (json.RawMessage, error)
	ValidateRequest     func(normalize.Request) error
	CompileRequirements func(normalize.Request) ([]normalize.FeatureRequirement, error)
}

type Registry struct {
	mu      sync.RWMutex
	catalog *extensions.Catalog
	items   map[extensions.Ref]Definition
	sealed  *Snapshot
}

type Snapshot struct {
	definitions map[extensions.Ref]Definition
	catalog     *extensions.Snapshot
}

type Prepared struct {
	Ref          extensions.Ref
	InputPayload json.RawMessage
	Requirements []normalize.FeatureRequirement
	ReplaySafety ReplaySafety
}

type InputError struct {
	Ref extensions.Ref
	Err error
}

func (e *InputError) Error() string { return fmt.Sprintf("operation %q input: %v", e.Ref.Key(), e.Err) }
func (e *InputError) Unwrap() error { return e.Err }

func NewRegistry(catalog *extensions.Catalog) (*Registry, error) {
	if catalog == nil {
		return nil, fmt.Errorf("operation registry requires the shared extension catalog")
	}
	return &Registry{catalog: catalog, items: map[extensions.Ref]Definition{}}, nil
}

func BuiltinSnapshot() (*Snapshot, error) {
	catalog := extensions.NewCatalog()
	registry, err := NewRegistry(catalog)
	if err != nil {
		return nil, err
	}
	if err := RegisterChatGenerate(catalog, registry); err != nil {
		return nil, err
	}
	extensionsSnapshot, err := catalog.Freeze()
	if err != nil {
		return nil, err
	}
	return registry.Seal(extensionsSnapshot)
}

func ChatGenerateRef() extensions.Ref {
	return extensions.Ref{Kind: "operation", ID: string(normalize.OperationChatGenerate), ContractVersion: 1}
}

func RegisterChatGenerate(catalog *extensions.Catalog, registry *Registry) error {
	if catalog == nil || registry == nil {
		return fmt.Errorf("catalog and operation registry are required")
	}
	inputRef := extensions.Ref{Kind: extensions.SchemaKind, ID: "gobroom.operation.chat-generate.input", ContractVersion: 1}
	resultRef := extensions.Ref{Kind: extensions.SchemaKind, ID: "gobroom.operation.chat-generate.result", ContractVersion: 1}
	eventRef := extensions.Ref{Kind: extensions.SchemaKind, ID: "gobroom.operation.chat-generate.event", ContractVersion: 1}
	for _, item := range []struct {
		ref   extensions.Ref
		shape json.RawMessage
	}{
		{inputRef, json.RawMessage(`{"type":"object","properties":{"model":{"type":"string","minLength":1},"messages":{"type":"array","items":{"type":"object","required":["role"],"properties":{"role":{"type":"string"},"content":true},"additionalProperties":true}},"prompt":{"type":"object"},"tools":{"type":"array"},"thinking":{"type":"object"},"modalities":{"type":"object"}},"required":["model"],"additionalProperties":false}`)},
		{resultRef, json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"},"model":{"type":"string"},"content":{"type":"array"},"stop_reason":{"type":"string"},"usage":{"type":"object"},"opaque":true},"additionalProperties":true}`)},
		{eventRef, json.RawMessage(`{"type":"object","properties":{"kind":{"type":"string","minLength":1},"sequence":{"type":"integer","minimum":0},"itemId":{"type":"string"},"content":{"type":"object"}},"required":["kind"],"additionalProperties":true}`)},
	} {
		document, err := extensions.BindSchemaDocument(item.ref, item.shape)
		if err != nil {
			return err
		}
		if err := catalog.RegisterSchema(item.ref, document); err != nil {
			return err
		}
	}
	return registry.Register(Definition{
		Ref: ChatGenerateRef(), DisplayName: "Chat generation",
		Description:    "Generate a chat response from semantic messages, tools, prompt layers and reasoning intent.",
		InputSchemaRef: inputRef, ResultSchemaRef: &resultRef,
		EventSchemaRefs: []extensions.Ref{eventRef}, ReplaySafety: ReplayOnRejection,
		ValidateRequest: validateChatRequest,
		CompileRequirements: func(request normalize.Request) ([]normalize.FeatureRequirement, error) {
			return append([]normalize.FeatureRequirement(nil), request.Requirements...), nil
		},
	})
}

func (r *Registry) Register(definition Definition) error {
	if r == nil || r.catalog == nil {
		return fmt.Errorf("operation registry is not initialized")
	}
	if err := definition.Ref.Validate(); err != nil {
		return err
	}
	if definition.Ref.Kind != "operation" || definition.InputSchemaRef.Kind != extensions.SchemaKind || definition.InputSchemaRef.ContractVersion == 0 || (definition.InputPayload == nil && definition.ValidateRequest == nil) || definition.CompileRequirements == nil {
		return fmt.Errorf("operation %q requires exact input schema, payload validation and requirement compiler", definition.Ref.Key())
	}
	if definition.DisplayName == "" || definition.Description == "" || !validReplaySafety(definition.ReplaySafety) {
		return fmt.Errorf("operation %q requires display metadata and replay safety", definition.Ref.Key())
	}
	if definition.ResultSchemaRef != nil && (definition.ResultSchemaRef.Kind != extensions.SchemaKind || definition.ResultSchemaRef.ContractVersion == 0) {
		return fmt.Errorf("operation %q has invalid result schema reference", definition.Ref.Key())
	}
	for _, eventSchema := range definition.EventSchemaRefs {
		if eventSchema.Kind != extensions.SchemaKind || eventSchema.ContractVersion == 0 {
			return fmt.Errorf("operation %q has invalid event schema reference", definition.Ref.Key())
		}
	}
	definition.EventSchemaRefs = append([]extensions.Ref(nil), definition.EventSchemaRefs...)
	if definition.ResultSchemaRef != nil {
		copy := *definition.ResultSchemaRef
		definition.ResultSchemaRef = &copy
	}
	descriptor := extensions.Descriptor{
		Ref: definition.Ref, ImplementationVersion: fmt.Sprint(definition.Ref.ContractVersion),
		DisplayName: definition.DisplayName, Description: definition.Description,
		InputSchemaRef: &definition.InputSchemaRef, ResultSchemaRef: definition.ResultSchemaRef,
		EventSchemaRefs: definition.EventSchemaRefs, ReplaySafety: string(definition.ReplaySafety),
	}
	if err := r.catalog.Register(descriptor, func(json.RawMessage) (any, error) { return definition, nil }); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed != nil {
		return fmt.Errorf("operation registry is frozen")
	}
	if _, exists := r.items[definition.Ref]; exists {
		return fmt.Errorf("operation %q is already registered", definition.Ref.Key())
	}
	r.items[definition.Ref] = definition
	return nil
}

func (r *Registry) RegisterRawPayload(ref extensions.Ref, displayName, description string, schemaBody json.RawMessage, replaySafety ReplaySafety) error {
	if r == nil || r.catalog == nil {
		return fmt.Errorf("operation registry is not initialized")
	}
	inputRef := extensions.Ref{Kind: extensions.SchemaKind, ID: ref.ID + ".input", ContractVersion: ref.ContractVersion}
	document, err := extensions.BindSchemaDocument(inputRef, schemaBody)
	if err != nil {
		return err
	}
	if err := r.catalog.RegisterSchema(inputRef, document); err != nil {
		return err
	}
	return r.Register(Definition{
		Ref: ref, DisplayName: displayName, Description: description,
		InputSchemaRef: inputRef, ReplaySafety: replaySafety,
		InputPayload: func(request normalize.Request) (json.RawMessage, error) {
			if len(request.OperationPayload) == 0 {
				return nil, fmt.Errorf("operation payload is required")
			}
			return append(json.RawMessage(nil), request.OperationPayload...), nil
		},
		CompileRequirements: func(request normalize.Request) ([]normalize.FeatureRequirement, error) {
			return append([]normalize.FeatureRequirement(nil), request.Requirements...), nil
		},
	})
}

func (r *Registry) Contains(ref extensions.Ref) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, exists := r.items[ref]
	return exists
}

func (r *Registry) Seal(catalog *extensions.Snapshot) (*Snapshot, error) {
	if r == nil || catalog == nil {
		return nil, fmt.Errorf("operation registry and frozen extension catalog are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed != nil {
		return r.sealed, nil
	}
	definitions := make(map[extensions.Ref]Definition, len(r.items))
	for ref, definition := range r.items {
		if _, ok := catalog.Descriptor(ref); !ok {
			return nil, fmt.Errorf("operation %q is missing from extension catalog", ref.Key())
		}
		if _, ok := catalog.Schema(definition.InputSchemaRef); !ok {
			return nil, fmt.Errorf("operation %q input schema is missing", ref.Key())
		}
		if definition.ResultSchemaRef != nil {
			if _, ok := catalog.Schema(*definition.ResultSchemaRef); !ok {
				return nil, fmt.Errorf("operation %q result schema is missing", ref.Key())
			}
		}
		for _, eventSchema := range definition.EventSchemaRefs {
			if _, ok := catalog.Schema(eventSchema); !ok {
				return nil, fmt.Errorf("operation %q event schema %q is missing", ref.Key(), eventSchema.Key())
			}
		}
		definitions[ref] = definition
	}
	r.sealed = &Snapshot{definitions: definitions, catalog: catalog}
	return r.sealed, nil
}

func (s *Snapshot) Resolve(ref extensions.Ref) (Definition, bool) {
	if s == nil {
		return Definition{}, false
	}
	definition, ok := s.definitions[ref]
	return definition, ok
}

func (s *Snapshot) Prepare(request normalize.Request) (Prepared, error) {
	ref := extensions.Ref{Kind: "operation", ID: string(request.Operation), ContractVersion: request.OperationContractVersion}
	definition, ok := s.Resolve(ref)
	if !ok {
		return Prepared{}, &InputError{Ref: ref, Err: fmt.Errorf("contract is not registered")}
	}
	var payload json.RawMessage
	if len(request.OperationPayload) > 0 {
		payload = append(json.RawMessage(nil), request.OperationPayload...)
		if err := s.catalog.ValidateSchema(definition.InputSchemaRef, payload); err != nil {
			return Prepared{}, &InputError{Ref: ref, Err: err}
		}
	} else if definition.ValidateRequest != nil {
		if err := definition.ValidateRequest(request); err != nil {
			return Prepared{}, &InputError{Ref: ref, Err: err}
		}
	} else {
		var err error
		payload, err = definition.InputPayload(request)
		if err != nil {
			return Prepared{}, &InputError{Ref: ref, Err: fmt.Errorf("encode payload: %w", err)}
		}
		if err := s.catalog.ValidateSchema(definition.InputSchemaRef, payload); err != nil {
			return Prepared{}, &InputError{Ref: ref, Err: err}
		}
	}
	requirements, err := definition.CompileRequirements(request)
	if err != nil {
		return Prepared{}, &InputError{Ref: ref, Err: fmt.Errorf("compile requirements: %w", err)}
	}
	return Prepared{Ref: ref, InputPayload: append(json.RawMessage(nil), payload...), Requirements: requirements, ReplaySafety: definition.ReplaySafety}, nil
}

func validateChatRequest(request normalize.Request) error {
	if request.Model == "" {
		return fmt.Errorf("model is required")
	}
	if len(request.OperationPayload) != 0 {
		return fmt.Errorf("chat.generate does not accept an opaque operation payload; decode it into chat semantics")
	}
	for index, message := range request.Messages {
		if message.Role == "" {
			return fmt.Errorf("message %d has no role", index)
		}
	}
	for index, layer := range request.Prompt.Layers {
		if layer.Origin == "" {
			return fmt.Errorf("prompt layer %d has no origin", index)
		}
	}
	return nil
}

func validReplaySafety(value ReplaySafety) bool {
	switch value {
	case ReplayBeforeDispatch, ReplayOnRejection, ReplayWithKey, ReplayNever:
		return true
	default:
		return false
	}
}
