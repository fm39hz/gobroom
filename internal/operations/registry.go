// Package operations owns semantic task contracts independently from client
// and provider wire formats.
package operations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	Ref                         extensions.Ref
	DisplayName                 string
	Description                 string
	InputSchemaRef              extensions.Ref
	ResultSchemaRef             *extensions.Ref
	EventSchemaRefs             []extensions.Ref
	ArtifactInputs              []ArtifactInput
	ArtifactOutputs             []ArtifactOutput
	ArtifactInterpreters        []extensions.ArtifactInterpreterBinding
	NewResultProjector          ResultProjectorFactory
	ResourceBounds              extensions.ResourceBounds
	ReplaySafety                ReplaySafety
	InputPayload                func(normalize.Request) (json.RawMessage, error)
	ValidateRequest             func(normalize.Request) error
	CompileRequirements         func(normalize.Request) ([]normalize.FeatureRequirement, error)
	CompileArtifactRequirements func(normalize.Request, []normalize.ArtifactFacts) ([]normalize.FeatureRequirement, error)
}

type ArtifactBodyOpener func(context.Context, extensions.ArtifactRef, extensions.Ref) (io.ReadCloser, error)

var ErrArtifactInspectionLimit = errors.New("artifact interpreter input exceeds its declared bound")

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
	ArtifactFacts  []normalize.ArtifactFacts
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
	inputRoles := make(map[string]bool, len(definition.ArtifactInputs))
	for _, input := range definition.ArtifactInputs {
		inputRoles[input.Role] = true
	}
	seenInterpreterRoles := map[string]bool{}
	for index, binding := range definition.ArtifactInterpreters {
		if !inputRoles[binding.Role] || seenInterpreterRoles[binding.Role] {
			return fmt.Errorf("operation %q has an invalid or duplicate interpreter binding for artifact role %q", definition.Ref.Key(), binding.Role)
		}
		if err := binding.InterpreterRef.Validate(); err != nil || binding.InterpreterRef.Kind != extensions.ArtifactInterpreterKind {
			return fmt.Errorf("operation %q artifact role %q requires an exact artifact-interpreter ref", definition.Ref.Key(), binding.Role)
		}
		if definition.CompileArtifactRequirements == nil {
			return fmt.Errorf("operation %q binds artifact interpreters without an artifact requirement compiler", definition.Ref.Key())
		}
		options := binding.Options
		if len(options) == 0 {
			options = json.RawMessage(`{}`)
		}
		if !json.Valid(options) {
			return fmt.Errorf("operation %q artifact interpreter options are invalid JSON", definition.Ref.Key())
		}
		binding.Options = append(json.RawMessage(nil), options...)
		definition.ArtifactInterpreters[index] = binding
		seenInterpreterRoles[binding.Role] = true
	}
	definition.EventSchemaRefs = append([]extensions.Ref(nil), definition.EventSchemaRefs...)
	definition.ArtifactInputs = append([]ArtifactInput(nil), definition.ArtifactInputs...)
	definition.ArtifactOutputs = append([]ArtifactOutput(nil), definition.ArtifactOutputs...)
	definition.ArtifactInterpreters = append([]extensions.ArtifactInterpreterBinding(nil), definition.ArtifactInterpreters...)
	if definition.ResultSchemaRef != nil {
		copy := *definition.ResultSchemaRef
		definition.ResultSchemaRef = &copy
	}
	descriptor := extensions.Descriptor{
		Ref: definition.Ref, ImplementationVersion: fmt.Sprint(definition.Ref.ContractVersion),
		DisplayName: definition.DisplayName, Description: definition.Description,
		InputSchemaRef: &definition.InputSchemaRef, ResultSchemaRef: definition.ResultSchemaRef,
		EventSchemaRefs: definition.EventSchemaRefs, SemanticContracts: artifactPortTypeRefs(definition.ArtifactInputs, definition.ArtifactOutputs), ArtifactInputs: definition.ArtifactInputs, ArtifactOutputs: definition.ArtifactOutputs, ArtifactInterpreters: definition.ArtifactInterpreters, ReplaySafety: string(definition.ReplaySafety), ResourceBounds: definition.ResourceBounds,
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

func (r *Registry) ReplayContract(ref extensions.Ref) (ReplaySafety, bool) {
	if r == nil {
		return "", false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	definition, ok := r.items[ref]
	if !ok {
		return "", false
	}
	return definition.ReplaySafety, true
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
		for _, interpreterBinding := range definition.ArtifactInterpreters {
			interpreter, ok := catalog.Descriptor(interpreterBinding.InterpreterRef)
			if !ok || interpreter.Ref.Kind != extensions.ArtifactInterpreterKind || interpreter.ResultSchemaRef == nil {
				return nil, fmt.Errorf("operation %q artifact interpreter %q is missing its facts schema contract", ref.Key(), interpreterBinding.InterpreterRef.Key())
			}
			if _, ok := catalog.Schema(*interpreter.ResultSchemaRef); !ok {
				return nil, fmt.Errorf("operation %q artifact interpreter facts schema %q is missing", ref.Key(), interpreter.ResultSchemaRef.Key())
			}
		}
		for _, interpreterBinding := range definition.ArtifactInterpreters {
			interpreterDescriptor, implementation, err := catalog.Bind(interpreterBinding.InterpreterRef, interpreterBinding.Options)
			if err != nil {
				return nil, fmt.Errorf("operation %q artifact interpreter %q: %w", ref.Key(), interpreterBinding.InterpreterRef.Key(), err)
			}
			if interpreterDescriptor.ResultSchemaRef == nil {
				return nil, fmt.Errorf("operation %q artifact interpreter %q has no facts schema", ref.Key(), interpreterBinding.InterpreterRef.Key())
			}
			if _, ok := implementation.(extensions.ArtifactInterpreter); !ok {
				return nil, fmt.Errorf("operation %q artifact interpreter %q factory returned %T", ref.Key(), interpreterBinding.InterpreterRef.Key(), implementation)
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
	definition.ArtifactInterpreters = append([]extensions.ArtifactInterpreterBinding(nil), definition.ArtifactInterpreters...)
	for index := range definition.ArtifactInterpreters {
		definition.ArtifactInterpreters[index].Options = append(json.RawMessage(nil), definition.ArtifactInterpreters[index].Options...)
	}
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
	return s.PrepareWithArtifactBodies(context.Background(), request, nil)
}

// PrepareWithArtifactBodies validates an invocation, runs only the exact
// operation-bound artifact interpreters, and compiles requirements from their
// schema-validated facts. The opener grants one owner-scoped read lease at a
// time; body bytes never enter the normalized request or fact cache.
func (s *Snapshot) PrepareWithArtifactBodies(ctx context.Context, request normalize.Request, openBody ArtifactBodyOpener) (Prepared, error) {
	if ctx == nil {
		return Prepared{}, fmt.Errorf("operation preparation requires a context")
	}
	ref := extensions.Ref{Kind: "operation", ID: string(request.Operation), ContractVersion: request.OperationContractVersion}
	definition, ok := s.Resolve(ref)
	if !ok {
		return Prepared{}, &InputError{Ref: ref, Err: fmt.Errorf("contract is not registered")}
	}
	if definition.ResourceBounds.DeadlineMillis > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(definition.ResourceBounds.DeadlineMillis)*time.Millisecond)
		defer cancel()
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
	facts, err := s.prepareArtifactFacts(ctx, request, definition, openBody)
	if err != nil {
		return Prepared{}, &InputError{Ref: ref, Err: fmt.Errorf("inspect operation artifacts: %w", err)}
	}
	var factsBytes int64
	for _, fact := range facts {
		factsBytes += int64(len(fact.Value))
		if factsBytes > definition.ResourceBounds.MaxBufferedBytes {
			return Prepared{}, &InputError{Ref: ref, Err: fmt.Errorf("artifact facts exceed %d-byte operation buffer bound", definition.ResourceBounds.MaxBufferedBytes)}
		}
	}
	requirements, err := definition.CompileRequirements(request)
	if err != nil {
		return Prepared{}, &InputError{Ref: ref, Err: fmt.Errorf("compile requirements: %w", err)}
	}
	if definition.CompileArtifactRequirements != nil {
		artifactRequirements, compileErr := definition.CompileArtifactRequirements(request, cloneArtifactFacts(facts))
		if compileErr != nil {
			return Prepared{}, &InputError{Ref: ref, Err: fmt.Errorf("compile artifact requirements: %w", compileErr)}
		}
		requirements = append(requirements, artifactRequirements...)
	}
	artifacts := make([]extensions.ArtifactRef, len(request.Artifacts))
	for i, artifact := range request.Artifacts {
		artifacts[i] = artifact.Clone()
	}
	return Prepared{Ref: ref, InputPayload: append(json.RawMessage(nil), payload...), Requirements: requirements, ReplaySafety: definition.ReplaySafety, ResourceBounds: definition.ResourceBounds, Artifacts: artifacts, ArtifactFacts: cloneArtifactFacts(facts)}, nil
}

func (s *Snapshot) prepareArtifactFacts(ctx context.Context, request normalize.Request, definition Definition, openBody ArtifactBodyOpener) ([]normalize.ArtifactFacts, error) {
	if len(definition.ArtifactInterpreters) == 0 {
		if len(request.ArtifactFacts) != 0 {
			return nil, fmt.Errorf("request contains facts but operation declares no artifact interpreters")
		}
		return nil, nil
	}
	bindings := make(map[string]extensions.ArtifactInterpreterBinding, len(definition.ArtifactInterpreters))
	for _, binding := range definition.ArtifactInterpreters {
		bindings[binding.Role] = binding
	}
	expected := make([]struct {
		artifact extensions.ArtifactRef
		binding  extensions.ArtifactInterpreterBinding
	}, 0, len(request.Artifacts))
	for _, artifact := range request.Artifacts {
		if binding, exists := bindings[artifact.Role]; exists {
			expected = append(expected, struct {
				artifact extensions.ArtifactRef
				binding  extensions.ArtifactInterpreterBinding
			}{artifact: artifact, binding: binding})
		}
	}
	if len(expected) == 0 {
		if len(request.ArtifactFacts) != 0 {
			return nil, fmt.Errorf("request contains stale artifact facts")
		}
		return nil, nil
	}
	facts := make([]normalize.ArtifactFacts, 0, len(expected))
	if len(request.ArtifactFacts) > 0 {
		used := make([]bool, len(request.ArtifactFacts))
		for _, item := range expected {
			found := false
			sourceDigest, err := artifactSourceDigest(item.artifact)
			if err != nil {
				return nil, err
			}
			for index, fact := range request.ArtifactFacts {
				if used[index] || fact.Role != item.binding.Role || fact.InterpreterRef != item.binding.InterpreterRef || fact.SourceDigest != sourceDigest {
					continue
				}
				if err := s.validateArtifactFacts(item.binding.InterpreterRef, fact.Value); err != nil {
					return nil, err
				}
				used[index] = true
				facts = append(facts, cloneArtifactFact(fact))
				found = true
				break
			}
			if !found {
				return nil, fmt.Errorf("cached facts for artifact role %q do not match the current artifact and interpreter", item.binding.Role)
			}
		}
		for _, matched := range used {
			if !matched {
				return nil, fmt.Errorf("request contains an unbound artifact facts entry")
			}
		}
		return facts, nil
	}
	for _, item := range expected {
		fact, err := s.inspectArtifact(ctx, item.artifact, item.binding, definition.ResourceBounds, openBody)
		if err != nil {
			return nil, err
		}
		facts = append(facts, fact)
	}
	return facts, nil
}

func (s *Snapshot) inspectArtifact(ctx context.Context, artifact extensions.ArtifactRef, binding extensions.ArtifactInterpreterBinding, operationBounds extensions.ResourceBounds, openBody ArtifactBodyOpener) (normalize.ArtifactFacts, error) {
	descriptor, implementation, err := s.catalog.Bind(binding.InterpreterRef, binding.Options)
	if err != nil {
		return normalize.ArtifactFacts{}, err
	}
	interpreter, ok := implementation.(extensions.ArtifactInterpreter)
	if !ok {
		return normalize.ArtifactFacts{}, fmt.Errorf("artifact interpreter %q factory returned %T", binding.InterpreterRef.Key(), implementation)
	}
	if descriptor.ResultSchemaRef == nil {
		return normalize.ArtifactFacts{}, fmt.Errorf("artifact interpreter %q has no facts schema", binding.InterpreterRef.Key())
	}
	if artifact.SizeBytes > descriptor.ResourceBounds.MaxInputBytes || artifact.SizeBytes > operationBounds.MaxInputBytes {
		return normalize.ArtifactFacts{}, ErrArtifactInspectionLimit
	}
	deadline := descriptor.ResourceBounds.DeadlineMillis
	if operationBounds.DeadlineMillis > 0 && operationBounds.DeadlineMillis < deadline {
		deadline = operationBounds.DeadlineMillis
	}
	inspectCtx := ctx
	if deadline > 0 {
		var cancel context.CancelFunc
		inspectCtx, cancel = context.WithTimeout(ctx, time.Duration(deadline)*time.Millisecond)
		defer cancel()
	}
	var source io.ReadCloser
	if len(artifact.Value) > 0 {
		source = io.NopCloser(bytes.NewReader(artifact.Value))
	} else {
		if openBody == nil {
			return normalize.ArtifactFacts{}, fmt.Errorf("artifact body opener is unavailable")
		}
		source, err = openBody(inspectCtx, artifact, binding.InterpreterRef)
		if err != nil {
			return normalize.ArtifactFacts{}, err
		}
	}
	if source == nil {
		return normalize.ArtifactFacts{}, fmt.Errorf("artifact body opener returned a nil reader")
	}
	defer source.Close()
	reader := contextReader{ctx: inspectCtx, reader: io.LimitReader(source, artifact.SizeBytes)}
	value, err := interpreter.InspectArtifact(inspectCtx, artifact.Clone(), reader)
	if err != nil {
		return normalize.ArtifactFacts{}, fmt.Errorf("interpreter %q: %w", binding.InterpreterRef.Key(), err)
	}
	if err := inspectCtx.Err(); err != nil {
		return normalize.ArtifactFacts{}, err
	}
	if int64(len(value)) > descriptor.ResourceBounds.MaxOutputBytes || int64(len(value)) > descriptor.ResourceBounds.MaxBufferedBytes {
		return normalize.ArtifactFacts{}, fmt.Errorf("artifact interpreter %q facts exceed declared output bounds", binding.InterpreterRef.Key())
	}
	if err := s.catalog.ValidateSchema(*descriptor.ResultSchemaRef, value); err != nil {
		return normalize.ArtifactFacts{}, fmt.Errorf("artifact interpreter %q facts violate schema: %w", binding.InterpreterRef.Key(), err)
	}
	sourceDigest, err := artifactSourceDigest(artifact)
	if err != nil {
		return normalize.ArtifactFacts{}, err
	}
	return normalize.ArtifactFacts{Role: binding.Role, SourceDigest: sourceDigest, InterpreterRef: binding.InterpreterRef, Value: append(json.RawMessage(nil), value...)}, nil
}

func artifactSourceDigest(artifact extensions.ArtifactRef) (string, error) {
	metadata := artifact
	metadata.Value = nil
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return "", fmt.Errorf("fingerprint artifact metadata: %w", err)
	}
	digest := sha256.New()
	_, _ = digest.Write(encoded)
	if len(artifact.Value) > 0 {
		_, _ = digest.Write([]byte{0})
		_, _ = digest.Write(artifact.Value)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func (s *Snapshot) validateArtifactFacts(interpreterRef extensions.Ref, value json.RawMessage) error {
	descriptor, ok := s.catalog.Descriptor(interpreterRef)
	if !ok || descriptor.ResultSchemaRef == nil {
		return fmt.Errorf("artifact interpreter %q facts schema is unavailable", interpreterRef.Key())
	}
	return s.catalog.ValidateSchema(*descriptor.ResultSchemaRef, value)
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

func cloneArtifactFact(fact normalize.ArtifactFacts) normalize.ArtifactFacts {
	fact.Value = append(json.RawMessage(nil), fact.Value...)
	return fact
}

func cloneArtifactFacts(facts []normalize.ArtifactFacts) []normalize.ArtifactFacts {
	result := make([]normalize.ArtifactFacts, len(facts))
	for index, fact := range facts {
		result[index] = cloneArtifactFact(fact)
	}
	return result
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
