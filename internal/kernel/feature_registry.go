package kernel

import (
	"encoding/json"
	"fmt"
	"sync"
)

// FeatureEvaluator evaluates one namespaced hard requirement against the
// matching route evidence. Feature-specific semantics stay in a registered
// extension; routing only sees the generic result.
type FeatureEvaluator interface {
	Evaluate(Capability, json.RawMessage) (bool, string)
}

type FeatureRegistry struct {
	mu         sync.RWMutex
	evaluators map[string]FeatureEvaluator
}

func NewFeatureRegistry() *FeatureRegistry {
	return &FeatureRegistry{evaluators: map[string]FeatureEvaluator{}}
}

func (r *FeatureRegistry) Register(id string, evaluator FeatureEvaluator) error {
	if r == nil {
		return fmt.Errorf("feature registry is nil")
	}
	if id == "" || evaluator == nil {
		return fmt.Errorf("feature ID and evaluator are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.evaluators[id]; exists {
		return fmt.Errorf("feature evaluator %q is already registered", id)
	}
	r.evaluators[id] = evaluator
	return nil
}

func (r *FeatureRegistry) Evaluate(route Route, requirements []FeatureRequirement) (bool, string) {
	for _, requirement := range requirements {
		if requirement.ID == "" {
			return false, "feature requirement is missing an ID"
		}
		capability, ok := route.Profile[requirement.ID]
		if !ok {
			return false, requirement.ID + " has no route evidence"
		}
		var evaluator FeatureEvaluator
		if r != nil {
			r.mu.RLock()
			evaluator = r.evaluators[requirement.ID]
			r.mu.RUnlock()
		}
		if evaluator == nil {
			return false, requirement.ID + " has no registered evaluator"
		}
		if eligible, reason := evaluator.Evaluate(capability, requirement.Constraints); !eligible {
			if reason == "" {
				reason = "requirement is not satisfied"
			}
			return false, requirement.ID + ": " + reason
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
