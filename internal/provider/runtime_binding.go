package provider

import (
	"fmt"

	"github.com/fm39hz/gobroom/internal/kernel"
)

type RuntimeBinding struct {
	DefinitionID    string
	Operation       Operation
	AdapterID       string
	Adapter         kernel.ProviderAdapter
	Auth            AuthFlow
	ModelSource     ModelSource
	ErrorClassifier ErrorClassifier
	QuotaSource     QuotaSource
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
	if definition.Auth.ID != "" {
		var found bool
		auth, found = b.registry.Auth.Resolve(definition.Auth.ID)
		if !found {
			return RuntimeBinding{}, fmt.Errorf("auth flow %q is not registered", definition.Auth.ID)
		}
	}
	var modelSource ModelSource
	if binding.ModelSource.ID != "" {
		var found bool
		modelSource, found = b.registry.modelSources[binding.ModelSource.ID]
		if !found {
			return RuntimeBinding{}, fmt.Errorf("model source %q is not registered", binding.ModelSource.ID)
		}
	}
	var classifier ErrorClassifier
	if binding.ErrorClassifier.ID != "" {
		var found bool
		classifier, found = b.registry.errorClassifiers[binding.ErrorClassifier.ID]
		if !found {
			return RuntimeBinding{}, fmt.Errorf("error classifier %q is not registered", binding.ErrorClassifier.ID)
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
	if binding.RuntimeAdapterID == "" {
		return RuntimeBinding{DefinitionID: definitionID, Operation: operation, Auth: auth, ModelSource: modelSource, ErrorClassifier: classifier, QuotaSource: quotaSource}, nil
	}
	adapter, ok := b.registry.adapters[binding.RuntimeAdapterID]
	if !ok {
		return RuntimeBinding{}, fmt.Errorf("runtime adapter %q is not registered", binding.RuntimeAdapterID)
	}
	return RuntimeBinding{DefinitionID: definitionID, Operation: operation, AdapterID: binding.RuntimeAdapterID, Adapter: adapter, Auth: auth, ModelSource: modelSource, ErrorClassifier: classifier, QuotaSource: quotaSource}, nil
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
			result[definition.ID+":"+string(operation)] = binding
		}
	}
	return result, nil
}
