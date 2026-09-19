package controlplane

import (
	"fmt"
	"strings"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/store"
)

type Loader struct{ Store *store.Store }

func (l Loader) LoadSnapshot(version uint64) (kernel.Snapshot, error) {
	if l.Store == nil {
		return kernel.Snapshot{}, fmt.Errorf("store is required")
	}
	publicRows, err := l.Store.PublicModels()
	if err != nil {
		return kernel.Snapshot{}, err
	}
	comboRows, err := l.Store.ComboDetails()
	if err != nil {
		return kernel.Snapshot{}, err
	}
	routeRows, err := l.Store.Routes()
	if err != nil {
		return kernel.Snapshot{}, err
	}
	logicalRows, err := l.Store.LogicalModels()
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

	input := kernel.SnapshotInput{LogicalModels: map[string]string{}, RouteGroups: map[string][]string{}}
	publicNames := make(map[string]bool, len(publicRows)+len(physicalRows)+len(typedComboRows))
	for _, row := range publicRows {
		input.PublicModels = append(input.PublicModels, kernel.PublicModel{Name: row.Name, TargetRef: row.TargetRef, OwnedBy: row.OwnedBy})
		publicNames[row.Name] = true
	}
	for _, row := range comboRows {
		strategy := kernel.Strategy(row.Strategy)
		if strategy == "" {
			strategy = kernel.StrategyFallback
		}
		input.Combos = append(input.Combos, kernel.Combo{Name: row.Name, Strategy: strategy, StickyLimit: row.StickyLimit, Members: row.Members})
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
		input.Routes = append(input.Routes, kernel.Route{ID: row.ID, NodeID: row.NodeID, DefinitionID: row.DefinitionID, DisplayPrefix: row.Prefix, ExternalModel: row.ExternalModel, Protocol: protocol, AdapterID: provider.RuntimeAdapterIDForProtocol(protocol), ErrorClassifierID: "http-json", Capabilities: row.Capabilities, Enabled: row.Enabled, BaseURL: row.BaseURL, CredentialID: row.CredentialID, CredentialType: row.CredentialType})
		baseID := row.ID
		if at := strings.IndexByte(baseID, '@'); at >= 0 {
			baseID = baseID[:at]
		}
		input.RouteGroups[baseID] = append(input.RouteGroups[baseID], row.ID)
	}
	for _, row := range logicalRows {
		input.LogicalModels[row.Name] = row.TargetRef
	}
	for _, row := range physicalRows {
		node := kernel.ModelNode{ID: row.Name, Kind: kernel.ModelPhysical, Strategy: kernelStrategy(row.Policy.ID), StickyLimit: strategyInt(row.Policy.Config, "stickyLimit", 1)}
		for _, source := range row.Sources {
			node.Members = append(node.Members, kernel.MemberRef{Kind: kernel.MemberRouteGroup, ID: source.RouteID})
		}
		input.Nodes = append(input.Nodes, node)
		if row.Discoverable && !publicNames[row.Name] {
			input.PublicModels = append(input.PublicModels, kernel.PublicModel{Name: row.Name, TargetRef: row.Name, OwnedBy: "gobroom"})
			publicNames[row.Name] = true
		}
	}
	for _, row := range typedComboRows {
		node := kernel.ModelNode{ID: row.Name, Kind: kernel.ModelCombo, Strategy: kernelStrategy(row.Strategy.ID), StickyLimit: strategyInt(row.Strategy.Config, "stickyLimit", 1)}
		for _, member := range row.Members {
			node.Members = append(node.Members, kernel.MemberRef{Kind: kernel.MemberModel, ID: member.ID, Weight: strategyInt(row.Strategy.Config, "weight:"+member.ID, 0)})
		}
		input.Nodes = append(input.Nodes, node)
		if row.Discoverable && !publicNames[row.Name] {
			input.PublicModels = append(input.PublicModels, kernel.PublicModel{Name: row.Name, TargetRef: row.Name, OwnedBy: "gobroom"})
			publicNames[row.Name] = true
		}
	}
	return kernel.BuildSnapshot(input, version)
}

func kernelStrategy(id string) kernel.Strategy {
	switch id {
	case "", "ordered-fallback", "fallback":
		return kernel.StrategyFallback
	case "rotating-fallback", "rotating_fallback":
		return kernel.StrategyRotatingFallback
	case "round-robin", "round_robin":
		return kernel.StrategyRoundRobin
	case "round-robin-fallback", "round_robin_fallback":
		return kernel.StrategyRoundRobinFallback
	case "weighted-fallback", "weighted":
		return kernel.StrategyWeighted
	default:
		return kernel.Strategy(id)
	}
}

func strategyInt(config map[string]any, key string, fallback int) int {
	if value, ok := config[key]; ok {
		switch number := value.(type) {
		case float64:
			return int(number)
		case int:
			return number
		}
	}
	return fallback
}
