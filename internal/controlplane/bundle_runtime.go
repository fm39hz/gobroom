package controlplane

import (
	"crypto/rand"
	"fmt"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/store"
)

// resolveRouteRuntime is shared by candidate validation and serving compilation.
func resolveRouteRuntime(bindings map[string]provider.RuntimeBinding, definition extensions.Ref, protocol kernel.Protocol, credentialType string) ([]provider.RuntimeBinding, provider.BoundAuthMode, error) {
	routes := provider.RuntimeBindingsForProtocol(bindings, definition, protocol)
	if len(routes) == 0 {
		return nil, provider.BoundAuthMode{}, fmt.Errorf("provider definition %q has no inference operation for protocol %q", definition.ID, protocol)
	}
	mode, exists := routes[0].AuthModes[credentialType]
	if !exists || mode.Flow == nil || mode.FlowID == "" {
		return nil, provider.BoundAuthMode{}, fmt.Errorf("provider definition %q does not bind credential type %q", definition.ID, credentialType)
	}
	return routes, mode, nil
}

// ValidateBundleRuntime compiles the candidate with the real store/loader in an
// isolated memory database. No live configuration or credentials are copied.
// This is a cold configuration path, never a request-path database operation.
func ValidateBundleRuntime(bundle ConfigBundle, strategies *kernel.StrategyCatalog, bindings map[string]provider.RuntimeBinding) error {
	if err := ValidateBundleWithStrategies(bundle, strategies); err != nil {
		return err
	}
	nodes := make(map[string]store.ProviderNode, len(bundle.Providers))
	for _, node := range bundle.Providers {
		nodes[node.ID] = node
	}
	for _, connection := range bundle.Connections {
		if !bundleProviderHasModels(bundle, connection.ProviderNodeID) {
			continue
		}
		node := nodes[connection.ProviderNodeID]
		if _, _, err := resolveRouteRuntime(bindings, node.DefinitionRef(), kernel.Protocol(node.Protocol), connection.CredentialType); err != nil {
			return err
		}
	}
	// Store's unnamed :memory: DSN shares a cache process-wide. A distinct
	// named memory database isolates simultaneous previews and live embedders,
	// while still sharing connections within this one database/sql pool.
	candidate, err := store.Open("file:gobroom-preview-" + rand.Text() + "?mode=memory&cache=shared")
	if err != nil {
		return fmt.Errorf("open candidate configuration: %w", err)
	}
	defer candidate.Close()
	if err := ApplyBundle(candidate, bundle, strategies); err != nil {
		return err
	}
	_, err = (Loader{Store: candidate, Bindings: bindings, Strategies: strategies}).LoadSnapshot(1)
	return err
}
