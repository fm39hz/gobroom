package kernel

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"sort"
	"sync"

	"github.com/fm39hz/gobroom/internal/normalize"
)

type TransformStage string

const TransformBeforeRequirements TransformStage = "request.before_requirements"

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
	ID          string            `json:"id"`
	Label       string            `json:"label"`
	Description string            `json:"description"`
	Stage       TransformStage    `json:"stage"`
	Order       int               `json:"order,omitempty"`
	Effects     []TransformEffect `json:"effects"`
}

type RequestTransform interface {
	Definition() TransformDefinition
	Apply(context.Context, *NormalizedRequest) error
}

type RequestTransformRegistry struct {
	mu         sync.RWMutex
	transforms map[string]RequestTransform
}

type ResponseTransform interface {
	Definition() ResponseTransformDefinition
	ApplyResponse(context.Context, ResponseEvent) (ResponseEvent, error)
}

type ResponseTransformEffect string

const (
	ResponseEffectText          ResponseTransformEffect = "text"
	ResponseEffectThinking      ResponseTransformEffect = "thinking"
	ResponseEffectToolArguments ResponseTransformEffect = "tool_arguments"
	ResponseEffectUsage         ResponseTransformEffect = "usage"
)

type ResponseTransformDefinition struct {
	ID          string                    `json:"id"`
	Label       string                    `json:"label"`
	Description string                    `json:"description"`
	Order       int                       `json:"order,omitempty"`
	Effects     []ResponseTransformEffect `json:"effects"`
}

type ResponseTransformRegistry struct {
	mu         sync.RWMutex
	transforms map[string]ResponseTransform
	ordered    []ResponseTransform
}

func NewResponseTransformRegistry() *ResponseTransformRegistry {
	return &ResponseTransformRegistry{transforms: map[string]ResponseTransform{}}
}

func (r *ResponseTransformRegistry) Register(transform ResponseTransform) error {
	if r == nil || transform == nil {
		return fmt.Errorf("response transform registry and transform are required")
	}
	definition := transform.Definition()
	if definition.ID == "" || definition.Label == "" || definition.Description == "" || len(definition.Effects) == 0 {
		return fmt.Errorf("response transform requires ID, label, description and declared effects")
	}
	allowed := map[ResponseTransformEffect]bool{ResponseEffectText: true, ResponseEffectThinking: true, ResponseEffectToolArguments: true, ResponseEffectUsage: true}
	seen := map[ResponseTransformEffect]bool{}
	for _, effect := range definition.Effects {
		if !allowed[effect] || seen[effect] {
			return fmt.Errorf("response transform %q has invalid or duplicate effect %q", definition.ID, effect)
		}
		seen[effect] = true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.transforms[definition.ID]; exists {
		return fmt.Errorf("response transform %q is already registered", definition.ID)
	}
	definition.Effects = append([]ResponseTransformEffect(nil), definition.Effects...)
	r.transforms[definition.ID] = transform
	r.ordered = append(r.ordered, transform)
	sort.Slice(r.ordered, func(i, j int) bool {
		left, right := r.ordered[i].Definition(), r.ordered[j].Definition()
		if left.Order != right.Order {
			return left.Order < right.Order
		}
		return left.ID < right.ID
	})
	return nil
}

func (r *ResponseTransformRegistry) Apply(ctx context.Context, event ResponseEvent) (ResponseEvent, error) {
	if r == nil {
		return event, nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, transform := range r.ordered {
		definition := transform.Definition()
		before := event
		updated, err := transform.ApplyResponse(ctx, event)
		if err != nil {
			return ResponseEvent{}, fmt.Errorf("response transform %q: %w", definition.ID, err)
		}
		if updated.Kind != before.Kind || updated.At != before.At || updated.Index != before.Index || updated.ResponseID != before.ResponseID || updated.ItemID != before.ItemID || updated.ContentType != before.ContentType || updated.BlockType != before.BlockType || updated.StopReason != before.StopReason || updated.ToolCallID != before.ToolCallID || updated.ToolName != before.ToolName || updated.Error != before.Error || updated.WireFormat != before.WireFormat || !bytes.Equal(updated.Raw, before.Raw) || !bytes.Equal(updated.Opaque, before.Opaque) {
			return ResponseEvent{}, fmt.Errorf("response transform %q changed immutable event identity or opaque data", definition.ID)
		}
		changedText := updated.Text != before.Text
		changedArguments := updated.ToolArguments != before.ToolArguments
		changedUsage := !reflect.DeepEqual(updated.Usage, before.Usage)
		textEffect := ResponseEffectText
		if before.Kind == EventThinkingDelta {
			textEffect = ResponseEffectThinking
		}
		if changedText && (before.Kind != EventTextDelta && before.Kind != EventThinkingDelta || !responseTransformHasEffect(definition, textEffect)) || changedArguments && (before.Kind != EventToolCallDelta || !responseTransformHasEffect(definition, ResponseEffectToolArguments)) || changedUsage && (before.Kind != EventUsage && before.Kind != EventResponseComplete || !responseTransformHasEffect(definition, ResponseEffectUsage)) {
			return ResponseEvent{}, fmt.Errorf("response transform %q changed undeclared event content", definition.ID)
		}
		event = updated
	}
	return event, nil
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
	return &RequestTransformRegistry{transforms: map[string]RequestTransform{}}
}

func (r *RequestTransformRegistry) Register(transform RequestTransform) error {
	if r == nil || transform == nil {
		return fmt.Errorf("request transform registry and transform are required")
	}
	definition := transform.Definition()
	if definition.ID == "" || definition.Label == "" || definition.Description == "" || definition.Stage != TransformBeforeRequirements || len(definition.Effects) == 0 {
		return fmt.Errorf("request transform requires ID, description, supported stage and declared effects")
	}
	allowed := map[TransformEffect]bool{TransformPrompt: true, TransformInput: true, TransformTools: true, TransformThinking: true, TransformOptions: true, TransformContinuity: true}
	seen := map[TransformEffect]bool{}
	for _, effect := range definition.Effects {
		if !allowed[effect] || seen[effect] {
			return fmt.Errorf("request transform %q has invalid or duplicate effect %q", definition.ID, effect)
		}
		seen[effect] = true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.transforms[definition.ID]; exists {
		return fmt.Errorf("request transform %q is already registered", definition.ID)
	}
	definition.Effects = append([]TransformEffect(nil), definition.Effects...)
	r.transforms[definition.ID] = transform
	return nil
}

func (r *RequestTransformRegistry) Apply(ctx context.Context, request *NormalizedRequest) error {
	if r == nil || request == nil {
		return nil
	}
	r.mu.RLock()
	transforms := make([]RequestTransform, 0, len(r.transforms))
	for _, transform := range r.transforms {
		transforms = append(transforms, transform)
	}
	r.mu.RUnlock()
	sort.Slice(transforms, func(i, j int) bool {
		left, right := transforms[i].Definition(), transforms[j].Definition()
		if left.Order != right.Order {
			return left.Order < right.Order
		}
		return left.ID < right.ID
	})
	for _, transform := range transforms {
		definition := transform.Definition()
		model, operation, clientFormat, streaming, sessionID := request.Model, request.Operation, request.SourceFormat, request.Stream, request.Session.ID
		if err := transform.Apply(ctx, request); err != nil {
			return fmt.Errorf("request transform %q: %w", definition.ID, err)
		}
		if request.Model != model || request.Operation != operation || request.SourceFormat != clientFormat || request.Stream != streaming || request.Session.ID != sessionID {
			return fmt.Errorf("request transform %q changed immutable request identity or client contract", definition.ID)
		}
	}
	return nil
}

// CompileRequestRequirements is a shared helper for extensions that derive
// namespaced capability requirements from normalized input.
func AppendFeatureRequirement(request *normalize.Request, requirement normalize.FeatureRequirement) error {
	if request == nil || requirement.ID == "" {
		return fmt.Errorf("request and feature requirement ID are required")
	}
	request.Requirements = append(request.Requirements, requirement)
	return nil
}
