package controlplane

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/store"
)

type Loader struct {
	Store      store.SnapshotRepository
	Bindings   map[string]provider.RuntimeBinding
	Strategies *kernel.StrategyCatalog
}

func (l Loader) LoadSnapshot(version uint64) (kernel.Snapshot, error) {
	if l.Store == nil {
		return kernel.Snapshot{}, fmt.Errorf("store is required")
	}
	strategies := l.Strategies
	if strategies == nil {
		var err error
		strategies, err = kernel.NewBuiltinStrategyCatalog()
		if err != nil {
			return kernel.Snapshot{}, fmt.Errorf("build builtin strategy catalog: %w", err)
		}
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
	transformBindings, err := l.Store.TransformBindings()
	if err != nil {
		return kernel.Snapshot{}, err
	}
	lossCeiling, _, err := l.Store.CompatibilityLossCeiling()
	if err != nil {
		return kernel.Snapshot{}, err
	}

	input := kernel.SnapshotInput{RouteGroups: map[string][]string{}, TransformBindings: transformBindings, LossCeiling: lossCeiling}
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
		routeBindings := provider.RuntimeBindingsForProtocol(l.Bindings, row.DefinitionID, protocol)
		if l.Bindings != nil && len(routeBindings) == 0 {
			return kernel.Snapshot{}, fmt.Errorf("provider definition %q has no inference operation for protocol %q", row.DefinitionID, protocol)
		}
		var binding provider.RuntimeBinding
		operationBindings := make(map[normalize.Operation]kernel.RouteOperationBinding, len(routeBindings))
		if len(routeBindings) > 0 {
			binding = routeBindings[0]
			for _, routeBinding := range routeBindings {
				if routeBinding.TaskRef.ID != "" {
					operation := normalize.Operation(routeBinding.TaskRef.ID)
					operationBinding := operationBindings[operation]
					if operationBinding.ContractVersion != 0 && operationBinding.ContractVersion != routeBinding.TaskRef.ContractVersion {
						return kernel.Snapshot{}, fmt.Errorf("provider %q binds operation %q at mixed contract versions", row.DefinitionID, routeBinding.TaskRef.ID)
					}
					operationBinding.ContractVersion = routeBinding.TaskRef.ContractVersion
					operationBinding.AdapterIDs = append(operationBinding.AdapterIDs, routeBinding.AdapterID)
					if routeBinding.IdempotencyHeader != "" {
						if operationBinding.IdempotencyHeaders == nil {
							operationBinding.IdempotencyHeaders = make(map[string]string)
						}
						operationBinding.IdempotencyHeaders[routeBinding.AdapterID] = routeBinding.IdempotencyHeader
					}
					if operationBinding.ErrorClassifierRef.ID == "" {
						operationBinding.ErrorClassifierRef = routeBinding.ErrorClassifierRef
						operationBinding.UsageSourceRef = routeBinding.UsageSourceRef
						operationBinding.UsageOptions = routeBinding.UsageOptions
						operationBinding.SessionStoreRef = routeBinding.SessionStoreRef
					}
					operationBindings[operation] = operationBinding
				}
			}
		}
		quotaBinding := l.Bindings[provider.RuntimeBindingKey(row.DefinitionID, provider.OperationQuota)]
		input.Routes = append(input.Routes, kernel.Route{ID: row.ID, NodeID: row.NodeID, DefinitionID: row.DefinitionID, DefinitionRef: binding.DefinitionRef, AuthFlowID: binding.AuthFlowID, DisplayPrefix: row.Prefix, ExternalModel: row.ExternalModel, Protocol: protocol, OperationBindings: operationBindings, ErrorClassifierRef: binding.ErrorClassifierRef, QuotaSourceRef: quotaBinding.QuotaSourceRef, QuotaEndpointRef: quotaBinding.EndpointRef, QuotaTransportRef: quotaBinding.TransportRef, QuotaEndpointOptions: quotaBinding.EndpointOptions, QuotaWindowName: quotaBinding.QuotaWindowName, UsageSourceRef: binding.UsageSourceRef, UsageOptions: binding.UsageOptions, SessionStoreRef: binding.SessionStoreRef, Profile: row.Profile, Limits: row.Limits, Enabled: row.Enabled, BaseURL: row.BaseURL, CredentialID: row.CredentialID, CredentialType: row.CredentialType})
		baseID := row.ID
		if at := strings.IndexByte(baseID, '@'); at >= 0 {
			baseID = baseID[:at]
		}
		input.RouteGroups[baseID] = append(input.RouteGroups[baseID], row.ID)
	}
	for _, row := range physicalRows {
		definition, stickyLimit, err := strategies.Resolve(row.Policy.Ref, row.Policy.Config)
		if err != nil {
			return kernel.Snapshot{}, fmt.Errorf("physical model %q: %w", row.Name, err)
		}
		strategyConfig, err := json.Marshal(row.Policy.Config)
		if err != nil {
			return kernel.Snapshot{}, fmt.Errorf("physical model %q strategy config: %w", row.Name, err)
		}
		if row.Policy.Config == nil {
			strategyConfig = json.RawMessage(`{}`)
		}
		node := kernel.ModelNode{ID: row.Name, Kind: kernel.ModelPhysical, Strategy: definition.Runtime, StrategyRef: row.Policy.Ref, StrategyConfig: strategyConfig, StickyLimit: stickyLimit, Identity: row.Identity, Reasoning: row.Reasoning, LossPolicy: row.LossPolicy, AllowCompatibleSources: row.AllowCompatibleSources, AllowDynamicSources: row.AllowDynamicSources}
		for _, source := range row.Sources {
			node.Members = append(node.Members, kernel.MemberRef{Kind: kernel.MemberRouteGroup, ID: source.RouteID, Fidelity: source.Fidelity, Evidence: source.Evidence})
		}
		input.Nodes = append(input.Nodes, node)
		if row.Discoverable {
			input.PublicModels = append(input.PublicModels, kernel.PublicModel{Name: row.Name, TargetRef: row.Name, OwnedBy: "gobroom"})
		}
	}
	for _, row := range typedComboRows {
		definition, stickyLimit, err := strategies.Resolve(row.Strategy.Ref, row.Strategy.Config)
		if err != nil {
			return kernel.Snapshot{}, fmt.Errorf("combo model %q: %w", row.Name, err)
		}
		strategyConfig, err := json.Marshal(row.Strategy.Config)
		if err != nil {
			return kernel.Snapshot{}, fmt.Errorf("combo model %q strategy config: %w", row.Name, err)
		}
		if row.Strategy.Config == nil {
			strategyConfig = json.RawMessage(`{}`)
		}
		node := kernel.ModelNode{ID: row.Name, Kind: kernel.ModelCombo, Strategy: definition.Runtime, StrategyRef: row.Strategy.Ref, StrategyConfig: strategyConfig, StickyLimit: stickyLimit, Reasoning: row.Reasoning, LossPolicy: row.LossPolicy}
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
