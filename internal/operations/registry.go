// Package operations owns semantic task contracts independently from client
// and provider wire formats.
package operations

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

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
	ArtifactInputs      []ArtifactInput
	ArtifactOutputs     []ArtifactOutput
	NewResultProjector  ResultProjectorFactory
	ResourceBounds      extensions.ResourceBounds
	ReplaySafety        ReplaySafety
	InputPayload        func(normalize.Request) (json.RawMessage, error)
	ValidateRequest     func(normalize.Request) error
	CompileRequirements func(normalize.Request) ([]normalize.FeatureRequirement, error)
}

// ResultProjector incrementally derives one bounded, operation-typed result
// from canonical response events. Implementations must enforce the supplied
// ResourceBounds while accumulating state; they must not retain raw event
// payloads when a smaller semantic projection is sufficient.
type ResultProjector interface {
	ConsumeEvent(json.RawMessage) error
	Finalize() (json.RawMessage, error)
}

type ResultProjectorFactory func(extensions.ResourceBounds) (ResultProjector, error)

// ArtifactInput is a named semantic input port shared with the frozen
// extension descriptor contract.
type ArtifactInput = extensions.ArtifactInput
type ArtifactOutput = extensions.ArtifactOutput

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
	Ref            extensions.Ref
	InputPayload   json.RawMessage
	Requirements   []normalize.FeatureRequirement
	ReplaySafety   ReplaySafety
	ResourceBounds extensions.ResourceBounds
	Artifacts      []extensions.ArtifactRef
}

func DefaultResourceBounds() extensions.ResourceBounds {
	return extensions.ResourceBounds{MaxInputBytes: 16 << 20, MaxOutputBytes: 32 << 20, MaxBufferedBytes: 1 << 20, DeadlineMillis: 300_000}
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
	eventRef := extensions.Ref{Kind: extensions.SchemaKind, ID: "gobroom.operation.chat-generate.event", ContractVersion: 1}
	for _, item := range []struct {
		ref   extensions.Ref
		shape json.RawMessage
	}{
		{inputRef, json.RawMessage(`{"type":"object","properties":{"model":{"type":"string","minLength":1},"messages":{"type":"array","items":{"type":"object","required":["role"],"properties":{"role":{"type":"string"},"content":true},"additionalProperties":true}},"prompt":{"type":"object"},"tools":{"type":"array"},"thinking":{"type":"object"},"modalities":{"type":"object"}},"required":["model"],"additionalProperties":false}`)},
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
		Description:     "Generate a chat response from semantic messages, tools, prompt layers and reasoning intent.",
		InputSchemaRef:  inputRef,
		EventSchemaRefs: []extensions.Ref{eventRef}, ResourceBounds: DefaultResourceBounds(), ReplaySafety: ReplayOnRejection,
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
	if definition.ResourceBounds.MaxInputBytes == 0 || definition.ResourceBounds.MaxOutputBytes == 0 || definition.ResourceBounds.MaxBufferedBytes == 0 || definition.ResourceBounds.DeadlineMillis == 0 {
		return fmt.Errorf("operation %q requires positive input/output/buffer/deadline bounds", definition.Ref.Key())
	}
	if definition.ResultSchemaRef != nil && (definition.ResultSchemaRef.Kind != extensions.SchemaKind || definition.ResultSchemaRef.ContractVersion == 0) {
		return fmt.Errorf("operation %q has invalid result schema reference", definition.Ref.Key())
	}
	if definition.ResultSchemaRef != nil && definition.NewResultProjector == nil {
		return fmt.Errorf("operation %q declares a result schema without a bounded result projector", definition.Ref.Key())
	}
	if definition.ResultSchemaRef == nil && definition.NewResultProjector != nil {
		return fmt.Errorf("operation %q has a result projector without a result schema", definition.Ref.Key())
	}
	for _, eventSchema := range definition.EventSchemaRefs {
		if eventSchema.Kind != extensions.SchemaKind || eventSchema.ContractVersion == 0 {
			return fmt.Errorf("operation %q has invalid event schema reference", definition.Ref.Key())
		}
	}
	seenInputRoles := map[string]bool{}
	for _, input := range definition.ArtifactInputs {
		if strings.TrimSpace(input.Role) == "" || seenInputRoles[input.Role] || input.MinCount < 0 || input.MaxCount <= 0 || input.MinCount > input.MaxCount {
			return fmt.Errorf("operation %q has an invalid or duplicate artifact input role", definition.Ref.Key())
		}
		if err := input.TypeRef.Validate(); err != nil || input.TypeRef.Kind != extensions.ArtifactKind {
			return fmt.Errorf("operation %q artifact input %q has an invalid artifact type ref", definition.Ref.Key(), input.Role)
		}
		seenInputRoles[input.Role] = true
	}
	seenOutputRoles := map[string]bool{}
	for _, output := range definition.ArtifactOutputs {
		if strings.TrimSpace(output.Role) == "" || seenOutputRoles[output.Role] || output.MinCount < 0 || output.MaxCount <= 0 || output.MinCount > output.MaxCount {
			return fmt.Errorf("operation %q has an invalid or duplicate artifact output role", definition.Ref.Key())
		}
		if err := output.TypeRef.Validate(); err != nil || output.TypeRef.Kind != extensions.ArtifactKind {
			return fmt.Errorf("operation %q artifact output %q has an invalid artifact type ref", definition.Ref.Key(), output.Role)
		}
		seenOutputRoles[output.Role] = true
	}
	definition.EventSchemaRefs = append([]extensions.Ref(nil), definition.EventSchemaRefs...)
	definition.ArtifactInputs = append([]ArtifactInput(nil), definition.ArtifactInputs...)
	definition.ArtifactOutputs = append([]ArtifactOutput(nil), definition.ArtifactOutputs...)
	if definition.ResultSchemaRef != nil {
		copy := *definition.ResultSchemaRef
		definition.ResultSchemaRef = &copy
	}
	descriptor := extensions.Descriptor{
		Ref: definition.Ref, ImplementationVersion: fmt.Sprint(definition.Ref.ContractVersion),
		DisplayName: definition.DisplayName, Description: definition.Description,
		InputSchemaRef: &definition.InputSchemaRef, ResultSchemaRef: definition.ResultSchemaRef,
		EventSchemaRefs: definition.EventSchemaRefs, SemanticContracts: artifactPortTypeRefs(definition.ArtifactInputs, definition.ArtifactOutputs), ArtifactInputs: definition.ArtifactInputs, ArtifactOutputs: definition.ArtifactOutputs, ReplaySafety: string(definition.ReplaySafety), ResourceBounds: definition.ResourceBounds,
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
	return r.RegisterRawPayloadWithBounds(ref, displayName, description, schemaBody, replaySafety, DefaultResourceBounds())
}

func (r *Registry) RegisterRawPayloadWithBounds(ref extensions.Ref, displayName, description string, schemaBody json.RawMessage, replaySafety ReplaySafety, bounds extensions.ResourceBounds) error {
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
		InputSchemaRef: inputRef, ResourceBounds: bounds, ReplaySafety: replaySafety,
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
		for _, artifactInput := range definition.ArtifactInputs {
			descriptor, ok := catalog.Descriptor(artifactInput.TypeRef)
			if !ok || descriptor.ArtifactPolicy == nil {
				return nil, fmt.Errorf("operation %q artifact type %q is missing or has no artifact policy", ref.Key(), artifactInput.TypeRef.Key())
			}
		}
		for _, artifactOutput := range definition.ArtifactOutputs {
			descriptor, ok := catalog.Descriptor(artifactOutput.TypeRef)
			if !ok || descriptor.ArtifactPolicy == nil {
				return nil, fmt.Errorf("operation %q artifact output type %q is missing or has no artifact policy", ref.Key(), artifactOutput.TypeRef.Key())
			}
		}
		definitions[ref] = cloneDefinition(definition)
	}
	r.sealed = &Snapshot{definitions: definitions, catalog: catalog}
	return r.sealed, nil
}

func (s *Snapshot) Resolve(ref extensions.Ref) (Definition, bool) {
	if s == nil {
		return Definition{}, false
	}
	definition, ok := s.definitions[ref]
	return cloneDefinition(definition), ok
}

func cloneDefinition(definition Definition) Definition {
	definition.EventSchemaRefs = append([]extensions.Ref(nil), definition.EventSchemaRefs...)
	definition.ArtifactInputs = append([]ArtifactInput(nil), definition.ArtifactInputs...)
	definition.ArtifactOutputs = append([]ArtifactOutput(nil), definition.ArtifactOutputs...)
	if definition.ResultSchemaRef != nil {
		result := *definition.ResultSchemaRef
		definition.ResultSchemaRef = &result
	}
	return definition
}

func (s *Snapshot) ExtensionCatalog() *extensions.Snapshot {
	if s == nil {
		return nil
	}
	return s.catalog
}

// ValidateArtifactsForConsumer checks the operation allowlist and the exact
// artifact policy again at the selected route boundary, where the receiver
// and recipient are known. This prevents a valid reference from being reused
// by a different provider/session than the owner allowed.
func (s *Snapshot) ValidateArtifactsForConsumer(operationRef extensions.Ref, artifacts []extensions.ArtifactRef, receiver extensions.ArtifactOwner, recipient extensions.Ref) error {
	definition, ok := s.Resolve(operationRef)
	if !ok {
		return fmt.Errorf("operation %q is not registered", operationRef.Key())
	}
	allowed := make(map[string]ArtifactInput, len(definition.ArtifactInputs))
	for _, input := range definition.ArtifactInputs {
		allowed[input.Role] = input
	}
	counts := map[string]int{}
	for _, artifact := range artifacts {
		input, exists := allowed[artifact.Role]
		if !exists || input.TypeRef != artifact.TypeRef {
			return fmt.Errorf("artifact role/type %q/%q is not allowed by operation %q", artifact.Role, artifact.TypeRef.Key(), operationRef.Key())
		}
		counts[artifact.Role]++
		if counts[artifact.Role] > input.MaxCount {
			return fmt.Errorf("artifact role %q exceeds maximum count %d", artifact.Role, input.MaxCount)
		}
		if err := s.catalog.ValidateArtifact(artifact, receiver, recipient, time.Now()); err != nil {
			return fmt.Errorf("artifact %q for %s: %w", artifact.TypeRef.Key(), recipient.Key(), err)
		}
	}
	for _, input := range definition.ArtifactInputs {
		if counts[input.Role] < input.MinCount {
			return fmt.Errorf("artifact role %q requires at least %d item(s)", input.Role, input.MinCount)
		}
	}
	return nil
}

func (s *Snapshot) ArtifactInputs(operationRef extensions.Ref) ([]ArtifactInput, bool) {
	definition, ok := s.Resolve(operationRef)
	if !ok {
		return nil, false
	}
	return append([]ArtifactInput(nil), definition.ArtifactInputs...), true
}

func (s *Snapshot) ArtifactOutputs(operationRef extensions.Ref) ([]ArtifactOutput, bool) {
	definition, ok := s.Resolve(operationRef)
	if !ok {
		return nil, false
	}
	return append([]ArtifactOutput(nil), definition.ArtifactOutputs...), true
}

// ValidateArtifactOutputs validates one event's artifact references against
// the operation's declared response ports and the recipient's exact contract.
// Counts are returned for aggregation across the response stream.
func (s *Snapshot) ValidateArtifactOutputs(operationRef extensions.Ref, artifacts []extensions.ArtifactRef, receiver extensions.ArtifactOwner, recipient extensions.Ref, counts map[string]int) error {
	definition, ok := s.Resolve(operationRef)
	if !ok {
		return fmt.Errorf("operation %q is not registered", operationRef.Key())
	}
	if counts == nil && len(artifacts) > 0 {
		return fmt.Errorf("artifact output count accumulator is required")
	}
	allowed := make(map[string]ArtifactOutput, len(definition.ArtifactOutputs))
	for _, output := range definition.ArtifactOutputs {
		allowed[output.Role] = output
	}
	for _, artifact := range artifacts {
		output, exists := allowed[artifact.Role]
		if !exists || output.TypeRef != artifact.TypeRef {
			return fmt.Errorf("artifact role/type %q/%q is not allowed by operation %q output", artifact.Role, artifact.TypeRef.Key(), operationRef.Key())
		}
		counts[artifact.Role]++
		if counts[artifact.Role] > output.MaxCount {
			return fmt.Errorf("artifact output role %q exceeds maximum count %d", artifact.Role, output.MaxCount)
		}
		if err := s.catalog.ValidateArtifact(artifact, receiver, recipient, time.Now()); err != nil {
			return fmt.Errorf("artifact %q for %s: %w", artifact.TypeRef.Key(), recipient.Key(), err)
		}
	}
	return nil
}

func (s *Snapshot) ValidateArtifactOutputCounts(operationRef extensions.Ref, counts map[string]int) error {
	definition, ok := s.Resolve(operationRef)
	if !ok {
		return fmt.Errorf("operation %q is not registered", operationRef.Key())
	}
	for _, output := range definition.ArtifactOutputs {
		if counts[output.Role] < output.MinCount {
			return fmt.Errorf("artifact output role %q requires at least %d item(s)", output.Role, output.MinCount)
		}
	}
	return nil
}

// ValidateEvent validates one canonical operation event against its exact
// registered event schemas. A schema list is a union of supported event
// envelopes; the event is rejected only when none of the pinned schemas match.
func (s *Snapshot) ValidateEvent(operationRef extensions.Ref, event json.RawMessage) error {
	definition, ok := s.Resolve(operationRef)
	if !ok {
		return fmt.Errorf("operation %q is not registered", operationRef.Key())
	}
	if len(definition.EventSchemaRefs) == 0 {
		return nil
	}
	var validationErrors []error
	for _, schemaRef := range definition.EventSchemaRefs {
		if err := s.catalog.ValidateSchema(schemaRef, event); err == nil {
			return nil
		} else {
			validationErrors = append(validationErrors, fmt.Errorf("schema %q: %w", schemaRef.Key(), err))
		}
	}
	return fmt.Errorf("event for operation %q violates every declared event schema: %w", operationRef.Key(), errors.Join(validationErrors...))
}

// NewResultProjector creates one request-local result accumulator. The
// operation's resource bounds are passed into the module so its internal
// state remains bounded before final schema validation.
func (s *Snapshot) NewResultProjector(operationRef extensions.Ref) (ResultProjector, error) {
	definition, ok := s.Resolve(operationRef)
	if !ok {
		return nil, fmt.Errorf("operation %q is not registered", operationRef.Key())
	}
	if definition.ResultSchemaRef == nil || definition.NewResultProjector == nil {
		return nil, fmt.Errorf("operation %q has no typed result projector", operationRef.Key())
	}
	projector, err := definition.NewResultProjector(definition.ResourceBounds)
	if err != nil {
		return nil, fmt.Errorf("create result projector for %q: %w", operationRef.Key(), err)
	}
	if projector == nil {
		return nil, fmt.Errorf("operation %q result projector factory returned nil", operationRef.Key())
	}
	return projector, nil
}

func (s *Snapshot) ValidateResult(operationRef extensions.Ref, result json.RawMessage) error {
	definition, ok := s.Resolve(operationRef)
	if !ok || definition.ResultSchemaRef == nil {
		return fmt.Errorf("operation %q has no registered result schema", operationRef.Key())
	}
	if int64(len(result)) > definition.ResourceBounds.MaxBufferedBytes {
		return fmt.Errorf("operation %q result exceeds %d-byte projector bound", operationRef.Key(), definition.ResourceBounds.MaxBufferedBytes)
	}
	if err := s.catalog.ValidateSchema(*definition.ResultSchemaRef, result); err != nil {
		return fmt.Errorf("operation %q result violates schema %q: %w", operationRef.Key(), definition.ResultSchemaRef.Key(), err)
	}
	return nil
}

func (s *Snapshot) Prepare(request normalize.Request) (Prepared, error) {
	ref := extensions.Ref{Kind: "operation", ID: string(request.Operation), ContractVersion: request.OperationContractVersion}
	definition, ok := s.Resolve(ref)
	if !ok {
		return Prepared{}, &InputError{Ref: ref, Err: fmt.Errorf("contract is not registered")}
	}
	allowedArtifacts := make(map[string]ArtifactInput, len(definition.ArtifactInputs))
	for _, input := range definition.ArtifactInputs {
		allowedArtifacts[input.Role] = input
	}
	var artifactBytes int64
	counts := map[string]int{}
	for _, artifact := range request.Artifacts {
		input, exists := allowedArtifacts[artifact.Role]
		if !exists || input.TypeRef != artifact.TypeRef {
			return Prepared{}, &InputError{Ref: ref, Err: fmt.Errorf("artifact role/type %q/%q is not allowed by this operation", artifact.Role, artifact.TypeRef.Key())}
		}
		counts[artifact.Role]++
		if counts[artifact.Role] > input.MaxCount {
			return Prepared{}, &InputError{Ref: ref, Err: fmt.Errorf("artifact role %q exceeds maximum count %d", artifact.Role, input.MaxCount)}
		}
		if err := s.catalog.ValidateArtifact(artifact, artifact.Owner, extensions.Ref{}, time.Now()); err != nil {
			return Prepared{}, &InputError{Ref: ref, Err: fmt.Errorf("artifact %q: %w", artifact.TypeRef.Key(), err)}
		}
		if artifact.SizeBytes > definition.ResourceBounds.MaxInputBytes-artifactBytes {
			return Prepared{}, &InputError{Ref: ref, Err: fmt.Errorf("artifacts exceed %d-byte operation input limit", definition.ResourceBounds.MaxInputBytes)}
		}
		artifactBytes += artifact.SizeBytes
	}
	for _, input := range definition.ArtifactInputs {
		if counts[input.Role] < input.MinCount {
			return Prepared{}, &InputError{Ref: ref, Err: fmt.Errorf("artifact role %q requires at least %d item(s)", input.Role, input.MinCount)}
		}
	}
	if limit := definition.ResourceBounds.MaxInputBytes; limit > 0 && int64(len(request.OperationPayload)) > limit-artifactBytes {
		return Prepared{}, &InputError{Ref: ref, Err: fmt.Errorf("input payload exceeds %d-byte operation limit", limit)}
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
	inputBytes := len(payload)
	if inputBytes == 0 {
		inputView := any(request.Raw)
		if len(request.Raw) == 0 {
			inputRequest := request
			inputRequest.Artifacts = nil
			inputView = inputRequest
		}
		encoded, err := json.Marshal(inputView)
		if err != nil {
			return Prepared{}, &InputError{Ref: ref, Err: fmt.Errorf("measure normalized input: %w", err)}
		}
		inputBytes = len(encoded)
	}
	if limit := definition.ResourceBounds.MaxInputBytes; limit > 0 && int64(inputBytes) > limit-artifactBytes {
		return Prepared{}, &InputError{Ref: ref, Err: fmt.Errorf("input payload exceeds %d-byte operation limit", limit)}
	}
	requirements, err := definition.CompileRequirements(request)
	if err != nil {
		return Prepared{}, &InputError{Ref: ref, Err: fmt.Errorf("compile requirements: %w", err)}
	}
	artifacts := make([]extensions.ArtifactRef, len(request.Artifacts))
	for i, artifact := range request.Artifacts {
		artifacts[i] = artifact.Clone()
	}
	return Prepared{Ref: ref, InputPayload: append(json.RawMessage(nil), payload...), Requirements: requirements, ReplaySafety: definition.ReplaySafety, ResourceBounds: definition.ResourceBounds, Artifacts: artifacts}, nil
}

func artifactPortTypeRefs(inputs []ArtifactInput, outputs []ArtifactOutput) []extensions.Ref {
	refs := make([]extensions.Ref, 0, len(inputs)+len(outputs))
	seen := map[extensions.Ref]bool{}
	for _, input := range inputs {
		if !seen[input.TypeRef] {
			refs = append(refs, input.TypeRef)
			seen[input.TypeRef] = true
		}
	}
	for _, output := range outputs {
		if !seen[output.TypeRef] {
			refs = append(refs, output.TypeRef)
			seen[output.TypeRef] = true
		}
	}
	return refs
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
