package kernel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/normalize"
)

var ErrTransformSafetyViolation = errors.New("transform violated a protected invariant")

type TransformApplicationReport struct {
	Failures []TransformFailure `json:"failures,omitempty"`
}

func effectiveTransformFailureMode(mode TransformFailureMode) TransformFailureMode {
	if mode == "" {
		return TransformFailClosed
	}
	return mode
}

func validateAllowedTransformFailureModes(modes []TransformFailureMode) error {
	seen := make(map[TransformFailureMode]bool, len(modes))
	for _, mode := range modes {
		if mode != TransformSafeFailOpen || seen[mode] {
			return fmt.Errorf("unsupported or duplicate transform failure mode %q", mode)
		}
		seen[mode] = true
	}
	return nil
}

func transformFailureModeStrings(modes []TransformFailureMode) []string {
	result := make([]string, len(modes))
	for index, mode := range modes {
		result[index] = string(mode)
	}
	return result
}

func validateBindingFailureMode(binding TransformBinding, allowed []TransformFailureMode) error {
	mode := effectiveTransformFailureMode(binding.FailureMode)
	if mode != TransformFailClosed && mode != TransformSafeFailOpen {
		return fmt.Errorf("transform binding %q has unsupported failure mode %q", binding.ID, mode)
	}
	if mode == TransformSafeFailOpen && !slices.Contains(allowed, TransformSafeFailOpen) {
		return fmt.Errorf("transform binding %q requests safe_fail_open but its module does not allow it", binding.ID)
	}
	return nil
}

type responseOutputStartedKey struct{}

func withResponseOutputStarted(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, responseOutputStartedKey{}, true)
}

func responseOutputStarted(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	started, _ := ctx.Value(responseOutputStartedKey{}).(bool)
	return started
}

func failOpenTransform(ctx context.Context, binding TransformBinding, allowed []TransformFailureMode, response bool) bool {
	if ctx == nil || effectiveTransformFailureMode(binding.FailureMode) != TransformSafeFailOpen || !slices.Contains(allowed, TransformSafeFailOpen) || ctx.Err() != nil {
		return false
	}
	return !response || !responseOutputStarted(ctx)
}

type TransformStage string

const TransformBeforeRequirements TransformStage = "request.before_requirements"

type TransformScopeKind string

const (
	TransformScopeDaemon     TransformScopeKind = "daemon"
	TransformScopeModel      TransformScopeKind = "model"
	TransformScopeRoute      TransformScopeKind = "route"
	TransformScopeProvider   TransformScopeKind = "provider"
	TransformScopeConnection TransformScopeKind = "connection"
)

const (
	RequestTransformKind  = "request_transform"
	ResponseTransformKind = "response_transform"
)

type TransformFailureMode string

const (
	TransformFailClosed   TransformFailureMode = "fail_closed"
	TransformSafeFailOpen TransformFailureMode = "safe_fail_open"
)

type TransformFailureReason string

const (
	TransformFailureApply  TransformFailureReason = "apply_failed"
	TransformFailureEffect TransformFailureReason = "effect_violation"
)

type TransformFailure struct {
	BindingID    string                 `json:"bindingId"`
	TransformRef extensions.Ref         `json:"transformRef"`
	Scope        TransformScope         `json:"scope"`
	Mode         TransformFailureMode   `json:"mode"`
	Reason       TransformFailureReason `json:"reason"`
}

type TransformScope struct {
	Kind TransformScopeKind `json:"kind"`
	ID   string             `json:"id,omitempty"`
}

type TransformBinding struct {
	ID           string               `json:"id"`
	TransformRef extensions.Ref       `json:"transformRef"`
	Enabled      bool                 `json:"enabled"`
	Scope        TransformScope       `json:"scope"`
	Order        int                  `json:"order,omitempty"`
	FailureMode  TransformFailureMode `json:"failureMode,omitempty"`
	Options      json.RawMessage      `json:"options,omitempty"`
}

type RequestTransformPlanStep struct {
	BindingID    string               `json:"bindingId"`
	TransformRef extensions.Ref       `json:"transformRef"`
	Scope        TransformScope       `json:"scope"`
	Order        int                  `json:"order,omitempty"`
	Stage        TransformStage       `json:"stage"`
	FailureMode  TransformFailureMode `json:"failureMode"`
	Effects      []TransformEffect    `json:"effects"`
}

type TransformEffect string

const (
	TransformPrompt     TransformEffect = "prompt"
	TransformInput      TransformEffect = "input"
	TransformTools      TransformEffect = "tools"
	TransformThinking   TransformEffect = "thinking"
	TransformOptions    TransformEffect = "options"
	TransformContinuity TransformEffect = "continuity"
)

type TransformDefinition struct {
	Ref                   extensions.Ref            `json:"ref"`
	ImplementationVersion string                    `json:"implementationVersion"`
	Label                 string                    `json:"label"`
	Description           string                    `json:"description"`
	OptionsSchemaRef      *extensions.Ref           `json:"optionsSchemaRef,omitempty"`
	ResourceBounds        extensions.ResourceBounds `json:"resourceBounds,omitempty"`
	Stage                 TransformStage            `json:"stage"`
	FailureModes          []TransformFailureMode    `json:"failureModes,omitempty"`
	Effects               []TransformEffect         `json:"effects"`
}

type RequestTransform interface {
	Definition() TransformDefinition
	Apply(context.Context, *NormalizedRequest, json.RawMessage) error
}

type RequestTransformRegistry struct {
	mu         sync.RWMutex
	transforms map[extensions.Ref]RequestTransform
	catalog    *extensions.Snapshot
}

type ResponseTransform interface {
	Definition() ResponseTransformDefinition
	ApplyResponse(context.Context, ResponseEvent, json.RawMessage) (ResponseEvent, error)
}

type ResponseTransformEffect string

const (
	ResponseEffectText          ResponseTransformEffect = "text"
	ResponseEffectThinking      ResponseTransformEffect = "thinking"
	ResponseEffectToolArguments ResponseTransformEffect = "tool_arguments"
	ResponseEffectUsage         ResponseTransformEffect = "usage"
)

type ResponseTransformDefinition struct {
	Ref                   extensions.Ref            `json:"ref"`
	ImplementationVersion string                    `json:"implementationVersion"`
	Label                 string                    `json:"label"`
	Description           string                    `json:"description"`
	OptionsSchemaRef      *extensions.Ref           `json:"optionsSchemaRef,omitempty"`
	ResourceBounds        extensions.ResourceBounds `json:"resourceBounds,omitempty"`
	FailureModes          []TransformFailureMode    `json:"failureModes,omitempty"`
	Effects               []ResponseTransformEffect `json:"effects"`
}

type ResponseTransformPlanStep struct {
	BindingID    string                    `json:"bindingId"`
	TransformRef extensions.Ref            `json:"transformRef"`
	Scope        TransformScope            `json:"scope"`
	Order        int                       `json:"order,omitempty"`
	FailureMode  TransformFailureMode      `json:"failureMode"`
	Effects      []ResponseTransformEffect `json:"effects"`
}

type ResponseTransformRegistry struct {
	mu         sync.RWMutex
	transforms map[extensions.Ref]ResponseTransform
	catalog    *extensions.Snapshot
}

func NewResponseTransformRegistry() *ResponseTransformRegistry {
	return &ResponseTransformRegistry{transforms: map[extensions.Ref]ResponseTransform{}}
}

// NewResponseTransformRegistryFromCatalog binds every response transform from
// the frozen extension catalog. The catalog remains the authority for exact
// contract resolution and binding-option schema validation.
func NewResponseTransformRegistryFromCatalog(catalog *extensions.Snapshot) (*ResponseTransformRegistry, error) {
	if catalog == nil {
		return nil, fmt.Errorf("response transform catalog is nil")
	}
	registry := NewResponseTransformRegistry()
	registry.catalog = catalog
	for _, descriptor := range catalog.Descriptors() {
		if descriptor.Ref.Kind != ResponseTransformKind {
			continue
		}
		_, implementation, err := catalog.Implementation(descriptor.Ref)
		if err != nil {
			return nil, err
		}
		transform, ok := implementation.(ResponseTransform)
		if !ok {
			return nil, fmt.Errorf("response transform %q factory returned %T", descriptor.Ref.Key(), implementation)
		}
		if !slices.Equal(descriptor.FailureModes, transformFailureModeStrings(transform.Definition().FailureModes)) {
			return nil, fmt.Errorf("response transform %q failure modes do not match its pinned descriptor", descriptor.Ref.Key())
		}
		if err := registry.Register(transform); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

func (r *ResponseTransformRegistry) Register(transform ResponseTransform) error {
	if r == nil || transform == nil {
		return fmt.Errorf("response transform registry and transform are required")
	}
	definition := transform.Definition()
	if err := validateTransformRef(definition.Ref, ResponseTransformKind); err != nil {
		return err
	}
	if err := validateTransformMetadata(definition.ImplementationVersion, definition.Label, definition.Description, definition.OptionsSchemaRef); err != nil {
		return fmt.Errorf("response transform %q: %w", definition.Ref.Key(), err)
	}
	if len(definition.Effects) == 0 {
		return fmt.Errorf("response transform %q requires declared effects", definition.Ref.Key())
	}
	if err := validateAllowedTransformFailureModes(definition.FailureModes); err != nil {
		return fmt.Errorf("response transform %q: %w", definition.Ref.Key(), err)
	}
	allowed := map[ResponseTransformEffect]bool{ResponseEffectText: true, ResponseEffectThinking: true, ResponseEffectToolArguments: true, ResponseEffectUsage: true}
	seen := map[ResponseTransformEffect]bool{}
	for _, effect := range definition.Effects {
		if !allowed[effect] || seen[effect] {
			return fmt.Errorf("response transform %q has invalid or duplicate effect %q", definition.Ref.Key(), effect)
		}
		seen[effect] = true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.transforms[definition.Ref]; exists {
		return fmt.Errorf("response transform %q is already registered", definition.Ref.Key())
	}
	r.transforms[definition.Ref] = transform
	return nil
}

func (r *ResponseTransformRegistry) ValidateBindings(bindings []TransformBinding) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if err := validateRegistryBindings(r.transforms, bindings, ResponseTransformKind); err != nil {
		return err
	}
	for _, binding := range bindings {
		if transform := r.transforms[binding.TransformRef]; transform != nil {
			if err := validateBindingFailureMode(binding, transform.Definition().FailureModes); err != nil {
				return err
			}
		}
	}
	return validateTransformOptions(r.catalog, bindings, ResponseTransformKind)
}

func (r *ResponseTransformRegistry) Active(bindings []TransformBinding, scopes ...TransformScope) bool {
	return len(orderedBindings(bindings, scopes, ResponseTransformKind)) > 0
}

func (r *ResponseTransformRegistry) Plan(bindings []TransformBinding, scopes ...TransformScope) []ResponseTransformPlanStep {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	selected := orderedBindings(bindings, scopes, ResponseTransformKind)
	result := make([]ResponseTransformPlanStep, 0, len(selected))
	for _, binding := range selected {
		transform := r.transforms[binding.TransformRef]
		if transform == nil {
			continue
		}
		definition := transform.Definition()
		result = append(result, ResponseTransformPlanStep{
			BindingID: binding.ID, TransformRef: definition.Ref, Scope: binding.Scope,
			Order: binding.Order, FailureMode: effectiveTransformFailureMode(binding.FailureMode), Effects: append([]ResponseTransformEffect(nil), definition.Effects...),
		})
	}
	return result
}

func (r *ResponseTransformRegistry) ApplyScopes(ctx context.Context, event ResponseEvent, bindings []TransformBinding, scopes ...TransformScope) (ResponseEvent, error) {
	updated, _, err := r.ApplyScopesWithReport(ctx, event, bindings, scopes...)
	return updated, err
}

func (r *ResponseTransformRegistry) ApplyScopesWithReport(ctx context.Context, event ResponseEvent, bindings []TransformBinding, scopes ...TransformScope) (ResponseEvent, TransformApplicationReport, error) {
	var report TransformApplicationReport
	if r == nil {
		return event, report, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, binding := range orderedBindings(bindings, scopes, ResponseTransformKind) {
		transform := r.transforms[binding.TransformRef]
		if transform == nil {
			return ResponseEvent{}, report, fmt.Errorf("response transform binding %q references missing transform %q", binding.ID, binding.TransformRef.Key())
		}
		definition := transform.Definition()
		before := cloneResponseEvent(event)
		updated, err := transform.ApplyResponse(ctx, cloneResponseEvent(event), append(json.RawMessage(nil), binding.Options...))
		if err != nil {
			if failOpenTransform(ctx, binding, definition.FailureModes, true) && !errors.Is(err, ErrTransformSafetyViolation) {
				report.Failures = append(report.Failures, transformFailure(binding, definition.Ref, TransformFailureApply))
				continue
			}
			return ResponseEvent{}, report, fmt.Errorf("response transform %q: %w", definition.Ref.Key(), err)
		}
		if updated.Kind != before.Kind || updated.At != before.At || updated.Index != before.Index || updated.ResponseID != before.ResponseID || updated.ItemID != before.ItemID || updated.ContentType != before.ContentType || updated.BlockType != before.BlockType || updated.StopReason != before.StopReason || updated.ToolCallID != before.ToolCallID || updated.ToolName != before.ToolName || updated.Signature != before.Signature || updated.Error != before.Error || updated.WireFormat != before.WireFormat || !bytes.Equal(updated.Raw, before.Raw) || !bytes.Equal(updated.Opaque, before.Opaque) {
			return ResponseEvent{}, report, fmt.Errorf("response transform %q: %w: changed immutable event identity or opaque data", definition.Ref.Key(), ErrTransformSafetyViolation)
		}
		if !reflect.DeepEqual(updated.Artifacts, before.Artifacts) {
			return ResponseEvent{}, report, fmt.Errorf("response transform %q: %w: changed immutable artifact references", definition.Ref.Key(), ErrTransformSafetyViolation)
		}
		changedText := updated.Text != before.Text
		changedArguments := updated.ToolArguments != before.ToolArguments
		changedUsage := !reflect.DeepEqual(updated.Usage, before.Usage)
		textEffect := ResponseEffectText
		if before.Kind == EventThinkingDelta {
			textEffect = ResponseEffectThinking
		}
		if changedText && (before.Kind != EventTextDelta && before.Kind != EventThinkingDelta || !responseTransformHasEffect(definition, textEffect)) || changedArguments && (before.Kind != EventToolCallDelta || !responseTransformHasEffect(definition, ResponseEffectToolArguments)) || changedUsage && (before.Kind != EventUsage && before.Kind != EventResponseComplete || !responseTransformHasEffect(definition, ResponseEffectUsage)) {
			if failOpenTransform(ctx, binding, definition.FailureModes, true) {
				report.Failures = append(report.Failures, transformFailure(binding, definition.Ref, TransformFailureEffect))
				continue
			}
			return ResponseEvent{}, report, fmt.Errorf("response transform %q changed undeclared event content", definition.Ref.Key())
		}
		event = updated
	}
	return event, report, nil
}

func responseTransformHasEffect(definition ResponseTransformDefinition, effects ...ResponseTransformEffect) bool {
	for _, declared := range definition.Effects {
		for _, effect := range effects {
			if declared == effect {
				return true
			}
		}
	}
	return false
}

func NewRequestTransformRegistry() *RequestTransformRegistry {
	return &RequestTransformRegistry{transforms: map[extensions.Ref]RequestTransform{}}
}

// NewRequestTransformRegistryFromCatalog binds every request transform from
// the frozen extension catalog. The catalog remains the authority for exact
// contract resolution and binding-option schema validation.
func NewRequestTransformRegistryFromCatalog(catalog *extensions.Snapshot) (*RequestTransformRegistry, error) {
	if catalog == nil {
		return nil, fmt.Errorf("request transform catalog is nil")
	}
	registry := NewRequestTransformRegistry()
	registry.catalog = catalog
	for _, descriptor := range catalog.Descriptors() {
		if descriptor.Ref.Kind != RequestTransformKind {
			continue
		}
		_, implementation, err := catalog.Implementation(descriptor.Ref)
		if err != nil {
			return nil, err
		}
		transform, ok := implementation.(RequestTransform)
		if !ok {
			return nil, fmt.Errorf("request transform %q factory returned %T", descriptor.Ref.Key(), implementation)
		}
		if !slices.Equal(descriptor.FailureModes, transformFailureModeStrings(transform.Definition().FailureModes)) {
			return nil, fmt.Errorf("request transform %q failure modes do not match its pinned descriptor", descriptor.Ref.Key())
		}
		if err := registry.Register(transform); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

func (r *RequestTransformRegistry) Register(transform RequestTransform) error {
	if r == nil || transform == nil {
		return fmt.Errorf("request transform registry and transform are required")
	}
	definition := transform.Definition()
	if err := validateTransformRef(definition.Ref, RequestTransformKind); err != nil {
		return err
	}
	if err := validateTransformMetadata(definition.ImplementationVersion, definition.Label, definition.Description, definition.OptionsSchemaRef); err != nil {
		return fmt.Errorf("request transform %q: %w", definition.Ref.Key(), err)
	}
	if definition.Stage != TransformBeforeRequirements || len(definition.Effects) == 0 {
		return fmt.Errorf("request transform %q requires a supported stage and declared effects", definition.Ref.Key())
	}
	if err := validateAllowedTransformFailureModes(definition.FailureModes); err != nil {
		return fmt.Errorf("request transform %q: %w", definition.Ref.Key(), err)
	}
	allowed := map[TransformEffect]bool{TransformPrompt: true, TransformInput: true, TransformTools: true, TransformThinking: true, TransformOptions: true, TransformContinuity: true}
	seen := map[TransformEffect]bool{}
	for _, effect := range definition.Effects {
		if !allowed[effect] || seen[effect] {
			return fmt.Errorf("request transform %q has invalid or duplicate effect %q", definition.Ref.Key(), effect)
		}
		seen[effect] = true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.transforms[definition.Ref]; exists {
		return fmt.Errorf("request transform %q is already registered", definition.Ref.Key())
	}
	r.transforms[definition.Ref] = transform
	return nil
}

func (r *RequestTransformRegistry) ValidateBindings(bindings []TransformBinding) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if err := validateRegistryBindings(r.transforms, bindings, RequestTransformKind); err != nil {
		return err
	}
	for _, binding := range bindings {
		if transform := r.transforms[binding.TransformRef]; transform != nil {
			if err := validateBindingFailureMode(binding, transform.Definition().FailureModes); err != nil {
				return err
			}
		}
	}
	return validateTransformOptions(r.catalog, bindings, RequestTransformKind)
}

func validateTransformOptions(catalog *extensions.Snapshot, bindings []TransformBinding, expectedKind string) error {
	if catalog == nil {
		return nil
	}
	for _, binding := range bindings {
		if !binding.Enabled || binding.TransformRef.Kind != expectedKind {
			continue
		}
		if err := catalog.ValidateOptions(binding.TransformRef, binding.Options); err != nil {
			return fmt.Errorf("transform binding %q: %w", binding.ID, err)
		}
	}
	return nil
}

func (r *RequestTransformRegistry) Active(bindings []TransformBinding, scopes ...TransformScope) bool {
	return len(orderedBindings(bindings, scopes, RequestTransformKind)) > 0
}

func (r *RequestTransformRegistry) Plan(bindings []TransformBinding, scopes ...TransformScope) []RequestTransformPlanStep {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	selected := orderedBindings(bindings, scopes, RequestTransformKind)
	result := make([]RequestTransformPlanStep, 0, len(selected))
	for _, binding := range selected {
		transform := r.transforms[binding.TransformRef]
		if transform == nil {
			continue
		}
		definition := transform.Definition()
		result = append(result, RequestTransformPlanStep{
			BindingID: binding.ID, TransformRef: definition.Ref, Scope: binding.Scope,
			Order: binding.Order, Stage: definition.Stage, FailureMode: effectiveTransformFailureMode(binding.FailureMode), Effects: append([]TransformEffect(nil), definition.Effects...),
		})
	}
	return result
}

func (r *RequestTransformRegistry) ApplyScopes(ctx context.Context, request *NormalizedRequest, bindings []TransformBinding, scopes ...TransformScope) error {
	_, err := r.ApplyScopesWithReport(ctx, request, bindings, scopes...)
	return err
}

func (r *RequestTransformRegistry) ApplyScopesWithReport(ctx context.Context, request *NormalizedRequest, bindings []TransformBinding, scopes ...TransformScope) (TransformApplicationReport, error) {
	var report TransformApplicationReport
	if r == nil || request == nil {
		return report, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.RLock()
	transforms := make([]struct {
		binding TransformBinding
		impl    RequestTransform
	}, 0, len(bindings))
	for _, binding := range orderedBindings(bindings, scopes, RequestTransformKind) {
		transform := r.transforms[binding.TransformRef]
		if transform == nil {
			r.mu.RUnlock()
			return report, fmt.Errorf("request transform binding %q references missing transform %q", binding.ID, binding.TransformRef.Key())
		}
		transforms = append(transforms, struct {
			binding TransformBinding
			impl    RequestTransform
		}{binding, transform})
	}
	r.mu.RUnlock()
	for _, item := range transforms {
		transform := item.impl
		definition := transform.Definition()
		before := normalize.CloneRequest(*request)
		updated := normalize.CloneRequest(*request)
		if err := transform.Apply(ctx, &updated, append(json.RawMessage(nil), item.binding.Options...)); err != nil {
			if failOpenTransform(ctx, item.binding, definition.FailureModes, false) && !errors.Is(err, ErrTransformSafetyViolation) {
				report.Failures = append(report.Failures, transformFailure(item.binding, definition.Ref, TransformFailureApply))
				continue
			}
			return report, fmt.Errorf("request transform %q: %w", definition.Ref.Key(), err)
		}
		if updated.Model != before.Model || updated.Operation != before.Operation || updated.SourceFormat != before.SourceFormat || updated.Stream != before.Stream || updated.Session.ID != before.Session.ID || updated.Session.Client != before.Session.Client || updated.Session.Conversation != before.Session.Conversation || !reflect.DeepEqual(updated.Transport, before.Transport) {
			return report, fmt.Errorf("request transform %q: %w: changed immutable request identity or client contract", definition.Ref.Key(), ErrTransformSafetyViolation)
		}
		if err := validateRequestTransformEffects(definition, before, updated); err != nil {
			if failOpenTransform(ctx, item.binding, definition.FailureModes, false) && !errors.Is(err, ErrTransformSafetyViolation) {
				report.Failures = append(report.Failures, transformFailure(item.binding, definition.Ref, TransformFailureEffect))
				continue
			}
			return report, fmt.Errorf("request transform %q: %w", definition.Ref.Key(), err)
		}
		*request = updated
	}
	return report, nil
}

func transformFailure(binding TransformBinding, ref extensions.Ref, reason TransformFailureReason) TransformFailure {
	return TransformFailure{BindingID: binding.ID, TransformRef: ref, Scope: binding.Scope, Mode: effectiveTransformFailureMode(binding.FailureMode), Reason: reason}
}

func cloneResponseEvent(event ResponseEvent) ResponseEvent {
	event.Raw = append([]byte(nil), event.Raw...)
	event.Opaque = append(json.RawMessage(nil), event.Opaque...)
	event.Artifacts = make([]extensions.ArtifactRef, len(event.Artifacts))
	for index := range event.Artifacts {
		event.Artifacts[index] = event.Artifacts[index].Clone()
	}
	if event.Usage != nil {
		usage := *event.Usage
		event.Usage = &usage
	}
	return event
}

func validateRequestTransformEffects(definition TransformDefinition, before, after NormalizedRequest) error {
	require := func(changed bool, effect TransformEffect, label string) error {
		if changed && !requestTransformHasEffect(definition, effect) {
			return fmt.Errorf("changed %s without declaring effect %q", label, effect)
		}
		return nil
	}
	if err := require(!reflect.DeepEqual(before.Prompt, after.Prompt), TransformPrompt, "prompt layers"); err != nil {
		return err
	}
	if !promptLayerIdentityEqual(before.Prompt, after.Prompt) {
		return fmt.Errorf("%w: changed prompt provenance, role or layer ordering", ErrTransformSafetyViolation)
	}
	if !reflect.DeepEqual(before.Artifacts, after.Artifacts) {
		return fmt.Errorf("%w: changed artifact references or ownership", ErrTransformSafetyViolation)
	}
	if err := require(!reflect.DeepEqual(before.Tools, after.Tools), TransformTools, "tool definitions"); err != nil {
		return err
	}
	if len(before.Messages) != len(after.Messages) {
		if err := require(true, TransformInput, "message count"); err != nil {
			return err
		}
	}
	for i := 0; i < len(before.Messages) && i < len(after.Messages); i++ {
		left, right := before.Messages[i], after.Messages[i]
		if left.Role != right.Role || left.Name != right.Name || left.ToolCallID != right.ToolCallID || !reflect.DeepEqual(left.Metadata, right.Metadata) {
			return fmt.Errorf("%w: changed immutable message role, correlation or metadata", ErrTransformSafetyViolation)
		}
		if !reflect.DeepEqual(left.Content, right.Content) {
			if err := require(true, TransformInput, "message content"); err != nil {
				return err
			}
		}
		if len(left.ToolCalls) != len(right.ToolCalls) {
			return fmt.Errorf("%w: changed tool-call count or correlation", ErrTransformSafetyViolation)
		}
		for j := range left.ToolCalls {
			beforeCall, afterCall := left.ToolCalls[j], right.ToolCalls[j]
			if beforeCall.ID != afterCall.ID || beforeCall.Type != afterCall.Type || beforeCall.Name != afterCall.Name || beforeCall.State != afterCall.State || !bytes.Equal(beforeCall.ProviderData, afterCall.ProviderData) || !reflect.DeepEqual(beforeCall.Metadata, afterCall.Metadata) {
				return fmt.Errorf("%w: changed tool-call identity, correlation or opaque data", ErrTransformSafetyViolation)
			}
			if !reflect.DeepEqual(beforeCall.Arguments, afterCall.Arguments) {
				if err := require(true, TransformTools, "tool-call arguments"); err != nil {
					return err
				}
			}
		}
	}
	if err := require(!reflect.DeepEqual(before.Modalities, after.Modalities) || !bytes.Equal(before.OperationPayload, after.OperationPayload), TransformInput, "operation input or modalities"); err != nil {
		return err
	}
	if err := require(!reflect.DeepEqual(before.Thinking, after.Thinking), TransformThinking, "thinking intent"); err != nil {
		return err
	}
	if err := require(!reflect.DeepEqual(before.Continuity, after.Continuity) || !bytes.Equal(before.Session.ProviderState, after.Session.ProviderState), TransformContinuity, "continuity state"); err != nil {
		return err
	}
	if err := require(!reflect.DeepEqual(before.Requirements, after.Requirements), TransformInput, "compiled request requirements"); err != nil {
		return err
	}
	if !reflect.DeepEqual(before.Extensions, after.Extensions) {
		if err := require(true, TransformOptions, "namespaced request extensions"); err != nil {
			return err
		}
	}
	if !reflect.DeepEqual(before.Raw, after.Raw) {
		beforeRaw, afterRaw := before.Raw, after.Raw
		for _, key := range []string{"model", "stream"} {
			if !reflect.DeepEqual(beforeRaw[key], afterRaw[key]) {
				return fmt.Errorf("%w: changed immutable raw request field %q", ErrTransformSafetyViolation, key)
			}
		}
		for key := range beforeRaw {
			if protectedRequestField(key) && !reflect.DeepEqual(beforeRaw[key], afterRaw[key]) {
				return fmt.Errorf("%w: changed protected raw request field %q", ErrTransformSafetyViolation, key)
			}
		}
		for key := range afterRaw {
			if protectedRequestField(key) && !reflect.DeepEqual(beforeRaw[key], afterRaw[key]) {
				return fmt.Errorf("%w: changed protected raw request field %q", ErrTransformSafetyViolation, key)
			}
		}
		if !rawChangesHaveDeclaredEffect(definition, beforeRaw, afterRaw) {
			return fmt.Errorf("changed raw request options without declaring a corresponding effect")
		}
	}
	return nil
}

func requestTransformHasEffect(definition TransformDefinition, effect TransformEffect) bool {
	for _, declared := range definition.Effects {
		if declared == effect {
			return true
		}
	}
	return false
}

func promptLayerIdentityEqual(before, after normalize.PromptPlan) bool {
	if len(before.Layers) != len(after.Layers) {
		return false
	}
	for i := range before.Layers {
		if before.Layers[i].Origin != after.Layers[i].Origin || before.Layers[i].Role != after.Layers[i].Role {
			return false
		}
	}
	return true
}

func rawChangesHaveDeclaredEffect(definition TransformDefinition, before, after map[string]any) bool {
	keys := make(map[string]bool, len(before)+len(after))
	for key := range before {
		keys[key] = true
	}
	for key := range after {
		keys[key] = true
	}
	for key := range keys {
		if reflect.DeepEqual(before[key], after[key]) {
			continue
		}
		var effect TransformEffect
		switch key {
		case "model", "stream":
			return false
		case "messages", "input", "contents", "prompt", "operationPayload":
			effect = TransformInput
		case "tools":
			effect = TransformTools
		case "thinking", "reasoning", "reasoning_effort":
			effect = TransformThinking
		case "previous_response_id", "response_id", "encrypted_content":
			effect = TransformContinuity
		default:
			effect = TransformOptions
		}
		if !requestTransformHasEffect(definition, effect) {
			return false
		}
	}
	return true
}

func protectedRequestField(key string) bool {
	key = strings.NewReplacer("-", "_", ".", "_").Replace(strings.ToLower(key))
	switch key {
	case "authorization", "api_key", "x_api_key", "api_token", "x_auth_token", "x_access_token", "access_token", "refresh_token", "token", "secret", "password", "client_secret":
		return true
	default:
		return false
	}
}

func ValidateTransformBinding(binding TransformBinding) error {
	if binding.ID == "" {
		return fmt.Errorf("transform binding ID is required")
	}
	if err := binding.TransformRef.Validate(); err != nil {
		return fmt.Errorf("transform binding %q: %w", binding.ID, err)
	}
	if binding.TransformRef.Kind != RequestTransformKind && binding.TransformRef.Kind != ResponseTransformKind {
		return fmt.Errorf("transform binding %q has invalid transform kind %q", binding.ID, binding.TransformRef.Kind)
	}
	if len(binding.Options) > 0 && !json.Valid(binding.Options) {
		return fmt.Errorf("transform binding %q options are not valid JSON", binding.ID)
	}
	if mode := effectiveTransformFailureMode(binding.FailureMode); mode != TransformFailClosed && mode != TransformSafeFailOpen {
		return fmt.Errorf("transform binding %q has unsupported failure mode %q", binding.ID, mode)
	}
	switch binding.Scope.Kind {
	case TransformScopeDaemon:
		if binding.Scope.ID != "" {
			return fmt.Errorf("daemon transform scope must not have an ID")
		}
	case TransformScopeModel, TransformScopeRoute, TransformScopeProvider, TransformScopeConnection:
		if binding.Scope.ID == "" {
			return fmt.Errorf("transform scope %q requires an ID", binding.Scope.Kind)
		}
	default:
		return fmt.Errorf("unsupported transform scope %q", binding.Scope.Kind)
	}
	return nil
}

func ValidateTransformBindingFailureMode(binding TransformBinding, allowed []string) error {
	allowedModes := make([]TransformFailureMode, len(allowed))
	for index, mode := range allowed {
		allowedModes[index] = TransformFailureMode(mode)
	}
	return validateBindingFailureMode(binding, allowedModes)
}

func validateTransformRef(ref extensions.Ref, expectedKind string) error {
	if err := ref.Validate(); err != nil {
		return fmt.Errorf("transform reference: %w", err)
	}
	if ref.Kind != expectedKind {
		return fmt.Errorf("transform reference kind must be %q, got %q", expectedKind, ref.Kind)
	}
	return nil
}

func validateTransformMetadata(version, label, description string, optionsSchema *extensions.Ref) error {
	if strings.TrimSpace(version) == "" || strings.TrimSpace(label) == "" || strings.TrimSpace(description) == "" {
		return fmt.Errorf("implementation version, label and description are required")
	}
	if optionsSchema != nil {
		if err := optionsSchema.Validate(); err != nil {
			return fmt.Errorf("options schema: %w", err)
		}
		if optionsSchema.Kind != extensions.SchemaKind {
			return fmt.Errorf("options schema reference must use kind %q", extensions.SchemaKind)
		}
	}
	return nil
}

func orderedBindings(bindings []TransformBinding, scopes []TransformScope, expectedKind string) []TransformBinding {
	ranks := make(map[TransformScope]int, len(scopes))
	for rank, scope := range scopes {
		ranks[scope] = rank
	}
	type rankedBinding struct {
		binding TransformBinding
		rank    int
	}
	selected := make([]rankedBinding, 0, len(bindings))
	seenIDs := make(map[string]bool, len(bindings))
	for _, binding := range bindings {
		if !binding.Enabled || binding.TransformRef.Kind != expectedKind {
			continue
		}
		if seenIDs[binding.ID] {
			continue
		}
		seenIDs[binding.ID] = true
		if rank, ok := ranks[binding.Scope]; ok {
			selected = append(selected, rankedBinding{binding: binding, rank: rank})
		}
	}
	sort.Slice(selected, func(i, j int) bool {
		if selected[i].rank != selected[j].rank {
			return selected[i].rank < selected[j].rank
		}
		if selected[i].binding.Order != selected[j].binding.Order {
			return selected[i].binding.Order < selected[j].binding.Order
		}
		return selected[i].binding.ID < selected[j].binding.ID
	})
	result := make([]TransformBinding, len(selected))
	for i, item := range selected {
		result[i] = item.binding
	}
	return result
}

func validateRegistryBindings[T any](registry map[extensions.Ref]T, bindings []TransformBinding, expectedKind string) error {
	seen := make(map[string]bool, len(bindings))
	for _, binding := range bindings {
		if err := ValidateTransformBinding(binding); err != nil {
			return err
		}
		if seen[binding.ID] {
			return fmt.Errorf("duplicate transform binding ID %q", binding.ID)
		}
		seen[binding.ID] = true
		if binding.TransformRef.Kind != expectedKind {
			return fmt.Errorf("transform binding %q expects kind %q, got %q", binding.ID, expectedKind, binding.TransformRef.Kind)
		}
		if _, ok := registry[binding.TransformRef]; !ok {
			return fmt.Errorf("transform binding %q references missing implementation %q", binding.ID, binding.TransformRef.Key())
		}
	}
	return nil
}

func cloneTransformBindings(bindings []TransformBinding) []TransformBinding {
	cloned := make([]TransformBinding, len(bindings))
	for i, binding := range bindings {
		binding.Options = append(json.RawMessage(nil), binding.Options...)
		cloned[i] = binding
	}
	return cloned
}

// CompileRequestRequirements is a shared helper for extensions that derive
// namespaced capability requirements from normalized input.
func AppendFeatureRequirement(request *normalize.Request, requirement normalize.FeatureRequirement) error {
	if request == nil || requirement.Ref.Validate() != nil || requirement.Ref.Kind != FeatureEvaluatorKind {
		return fmt.Errorf("request and exact versioned feature evaluator reference are required")
	}
	request.Requirements = append(request.Requirements, requirement)
	return nil
}
