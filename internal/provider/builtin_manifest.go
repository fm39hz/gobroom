package provider

import (
	_ "embed"
	"fmt"
)

//go:embed builtin_manifests/generic.json
var embeddedBuiltinManifest string

func (r *RuntimeRegistry) loadEmbeddedBuiltinDefinitions() error {
	if r == nil || r.Primitives == nil {
		return fmt.Errorf("runtime registry is not initialized")
	}
	if err := r.LoadDefinitionJSON([]byte(embeddedBuiltinManifest)); err != nil {
		return fmt.Errorf("load embedded built-in provider definitions: %w", err)
	}
	return nil
}
