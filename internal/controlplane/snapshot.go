package controlplane

import (
	"fmt"
	"strings"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/store"
)

type Loader struct {
	Store    store.SnapshotRepository
	Bindings map[string]provider.RuntimeBinding
}

func (l Loader) LoadSnapshot(version uint64) (kernel.Snapshot, error) {
	if l.Store == nil {
		return kernel.Snapshot{}, fmt.Errorf("store is required")
	}
	routeRows, err := l.Store.Routes()
	if err != nil {
		return kernel.Snapshot{}, err
	}
	catalogRows, err := l.Store.DiscoveredRoutes("")
	if err != nil {
		return kernel.Snapshot{}, err
	}
	physicalRows, err := l.Store.PhysicalModels()
	if err != nil {
		return kernel.Snapshot{}, err
	}
	typedComboRows, err := l.Store.ComboModels()
	if err != nil {
		return kernel.Snapshot{}, err
	}

	input := kernel.SnapshotInput{RouteGroups: map[string][]string{}}
	// Catalog identity must outlive current connection entitlement. A Physical
	// model may legitimately reference a discovered route before its account is
	// tested; represent that route group as empty until positive evidence adds
	// executable connection variants. Otherwise a failed entitlement state
	// makes snapshot publication fail and can block the very refresh that would
	// establish availability.
	for _, row := range catalogRows {
		if row.ID != "" {
			input.RouteGroups[row.ID] = []string{}
		}
	}
	for _, row := range routeRows {
		protocol := kernel.Protocol(row.Protocol)
		if protocol == "chat" {
			protocol = kernel.ProtocolOpenAIChat
		}
		if protocol == "responses" {
			protocol = kernel.ProtocolOpenAIResponses
		}
		if protocol == "" {
			protocol = kernel.ProtocolOpenAIChat
		}
		operation, ok := provider.OperationForProtocol(protocol)
		if !ok {
			return kernel.Snapshot{}, fmt.Errorf("route %q has unsupported protocol %q", row.ID, protocol)
		}
		binding, ok := l.Bindings[provider.RuntimeBindingKey(row.DefinitionID, operation)]
		if l.Bindings != nil && !ok {
			return kernel.Snapshot{}, fmt.Errorf("provider definition %q has no runtime binding for %q", row.DefinitionID, operation)
		}
		quotaBinding := l.Bindings[provider.RuntimeBindingKey(row.DefinitionID, provider.OperationQuota)]
		input.Routes = append(input.Routes, kernel.Route{ID: row.ID, NodeID: row.NodeID, DefinitionID: row.DefinitionID, AuthFlowID: binding.AuthFlowID, DisplayPrefix: row.Prefix, ExternalModel: row.ExternalModel, Protocol: protocol, AdapterID: binding.AdapterID, ErrorClassifierID: binding.ErrorClassifierID, QuotaSourceID: quotaBinding.QuotaSourceID, QuotaEndpointID: quotaBinding.EndpointID, QuotaTransportID: quotaBinding.TransportID, QuotaEndpointOptions: quotaBinding.EndpointOptions, QuotaWindowName: quotaBinding.QuotaWindowName, UsageSourceID: binding.UsageSourceID, UsageOptions: binding.UsageOptions, SessionStoreID: binding.SessionStoreID, Profile: row.Profile, Limits: row.Limits, Enabled: row.Enabled, BaseURL: row.BaseURL, CredentialID: row.CredentialID, CredentialType: row.CredentialType})
		baseID := row.ID
		if at := strings.IndexByte(baseID, '@'); at >= 0 {
			baseID = baseID[:at]
		}
		input.RouteGroups[baseID] = append(input.RouteGroups[baseID], row.ID)
	}
	for _, row := range physicalRows {
		strategy, stickyLimit, err := resolveStrategy(row.Policy.ID, row.Policy.Config)
		if err != nil {
			return kernel.Snapshot{}, fmt.Errorf("physical model %q: %w", row.Name, err)
		}
		node := kernel.ModelNode{ID: row.Name, Kind: kernel.ModelPhysical, Strategy: strategy, StickyLimit: stickyLimit, Identity: row.Identity, Reasoning: row.Reasoning, AllowCompatibleSources: row.AllowCompatibleSources, AllowDynamicSources: row.AllowDynamicSources}
		for _, source := range row.Sources {
			node.Members = append(node.Members, kernel.MemberRef{Kind: kernel.MemberRouteGroup, ID: source.RouteID, Fidelity: source.Fidelity, Evidence: source.Evidence})
		}
		input.Nodes = append(input.Nodes, node)
		if row.Discoverable {
			input.PublicModels = append(input.PublicModels, kernel.PublicModel{Name: row.Name, TargetRef: row.Name, OwnedBy: "gobroom"})
		}
	}
	for _, row := range typedComboRows {
		strategy, stickyLimit, err := resolveStrategy(row.Strategy.ID, row.Strategy.Config)
		if err != nil {
			return kernel.Snapshot{}, fmt.Errorf("combo model %q: %w", row.Name, err)
		}
		node := kernel.ModelNode{ID: row.Name, Kind: kernel.ModelCombo, Strategy: strategy, StickyLimit: stickyLimit, Reasoning: row.Reasoning}
		for _, member := range row.Members {
			node.Members = append(node.Members, kernel.MemberRef{Kind: kernel.MemberModel, ID: member.ID, Weight: member.Weight})
		}
		input.Nodes = append(input.Nodes, node)
		if row.Discoverable {
			input.PublicModels = append(input.PublicModels, kernel.PublicModel{Name: row.Name, TargetRef: row.Name, OwnedBy: "gobroom"})
		}
	}
	return kernel.BuildSnapshot(input, version)
}

func resolveStrategy(id string, config map[string]any) (kernel.Strategy, int, error) {
	definition, ok := kernel.StrategyDefinitionByID(id)
	if !ok {
		return "", 0, fmt.Errorf("unknown strategy primitive %q", id)
	}
	if err := kernel.ValidateStrategyConfig(id, config); err != nil {
		return "", 0, err
	}
	stickyLimit := 1
	for _, option := range definition.Options {
		if option.Key == "stickyLimit" {
			value, err := kernel.StrategyIntegerOption(id, config, option.Key)
			if err != nil {
				return "", 0, err
			}
			stickyLimit = value
		}
	}
	return definition.Runtime, stickyLimit, nil
}
