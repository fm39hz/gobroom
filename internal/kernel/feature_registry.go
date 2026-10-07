package kernel

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/fm39hz/gobroom/internal/extensions"
)

// FeatureEvaluator evaluates one namespaced hard requirement against the
// matching route evidence. Feature-specific semantics stay in a registered
// extension; routing only sees the generic result.
type FeatureEvaluator interface {
	Evaluate(Capability, json.RawMessage) (bool, string)
}

type FeatureRegistry struct {
	mu         sync.RWMutex
	evaluators map[extensions.Ref]featureRegistration
}

type featureRegistration struct {
	evaluator FeatureEvaluator
	input     *extensions.Ref
	catalog   *extensions.Snapshot
}

const FeatureEvaluatorKind = "feature-evaluator"

func NewFeatureRegistry() *FeatureRegistry {
	return &FeatureRegistry{evaluators: map[extensions.Ref]featureRegistration{}}
}

func (r *FeatureRegistry) Register(ref extensions.Ref, evaluator FeatureEvaluator) error {
	if r == nil {
		return fmt.Errorf("feature registry is nil")
	}
	if err := ref.Validate(); err != nil || ref.Kind != FeatureEvaluatorKind || evaluator == nil {
		return fmt.Errorf("feature evaluator requires an exact %q reference and implementation", FeatureEvaluatorKind)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.evaluators[ref]; exists {
		return fmt.Errorf("feature evaluator %q is already registered", ref.Key())
	}
	r.evaluators[ref] = featureRegistration{evaluator: evaluator}
	return nil
}

// NewFeatureRegistryFromCatalog binds evaluators from the frozen shared
// extension catalog. The exact evaluator version travels with each request
// requirement; route profiles remain keyed by semantic feature ID.
func NewFeatureRegistryFromCatalog(catalog *extensions.Snapshot) (*FeatureRegistry, error) {
	if catalog == nil {
		return nil, fmt.Errorf("feature evaluator catalog is nil")
	}
	registry := NewFeatureRegistry()
	for _, descriptor := range catalog.Descriptors() {
		if descriptor.Ref.Kind != FeatureEvaluatorKind {
			continue
		}
		_, implementation, err := catalog.Bind(descriptor.Ref, json.RawMessage(`{}`))
		if err != nil {
			return nil, err
		}
		evaluator, ok := implementation.(FeatureEvaluator)
		if !ok {
			return nil, fmt.Errorf("feature evaluator %q factory returned %T", descriptor.Ref.Key(), implementation)
		}
		registry.evaluators[descriptor.Ref] = featureRegistration{evaluator: evaluator, input: descriptor.InputSchemaRef, catalog: catalog}
	}
	return registry, nil
}

func (r *FeatureRegistry) Evaluate(route Route, requirements []FeatureRequirement) (bool, string) {
	for _, requirement := range requirements {
		if err := requirement.Ref.Validate(); err != nil || requirement.Ref.Kind != FeatureEvaluatorKind {
			return false, "feature requirement is missing a valid versioned evaluator reference"
		}
		capability, ok := route.Profile[requirement.Ref.ID]
		if !ok {
			return false, requirement.Ref.ID + " has no route evidence"
		}
		var registered featureRegistration
		if r != nil {
			r.mu.RLock()
			registered = r.evaluators[requirement.Ref]
			r.mu.RUnlock()
		}
		if registered.evaluator == nil {
			return false, requirement.Ref.Key() + " has no registered evaluator"
		}
		if registered.input != nil {
			if err := registered.catalog.ValidateSchema(*registered.input, requirement.Constraints); err != nil {
				return false, requirement.Ref.ID + ": invalid feature constraints: " + err.Error()
			}
		}
		if eligible, reason := registered.evaluator.Evaluate(capability, requirement.Constraints); !eligible {
			if reason == "" {
				reason = "requirement is not satisfied"
			}
			return false, requirement.Ref.ID + ": " + reason
		}
	}
	return true, ""
}

// NativeFeature is the conservative evaluator for features whose contract is
// exactly the typed SupportState. Requirements with additional constraints
// need a feature-owned evaluator.
type NativeFeature struct{}

func (NativeFeature) Evaluate(capability Capability, constraints json.RawMessage) (bool, string) {
	if len(constraints) != 0 && string(constraints) != "null" {
		return false, "feature constraints require a specialized evaluator"
	}
	if capability.State != SupportNative {
		return false, "support is not native and unconditional"
	}
	return true, ""
}
