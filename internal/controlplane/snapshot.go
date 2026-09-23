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
	routeRows, err := l.Store.Routes()
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
		input.Routes = append(input.Routes, kernel.Route{ID: row.ID, NodeID: row.NodeID, DefinitionID: row.DefinitionID, DisplayPrefix: row.Prefix, ExternalModel: row.ExternalModel, Protocol: protocol, AdapterID: provider.RuntimeAdapterIDForProtocol(protocol), ErrorClassifierID: "http-json", Profile: row.Profile, Limits: row.Limits, Enabled: row.Enabled, BaseURL: row.BaseURL, CredentialID: row.CredentialID, CredentialType: row.CredentialType})
		baseID := row.ID
		if at := strings.IndexByte(baseID, '@'); at >= 0 {
			baseID = baseID[:at]
		}
		input.RouteGroups[baseID] = append(input.RouteGroups[baseID], row.ID)
	}
	for _, row := range physicalRows {
		node := kernel.ModelNode{ID: row.Name, Kind: kernel.ModelPhysical, Strategy: kernelStrategy(row.Policy.ID), StickyLimit: strategyInt(row.Policy.Config, "stickyLimit", 1), Identity: row.Identity}
		for _, source := range row.Sources {
			node.Members = append(node.Members, kernel.MemberRef{Kind: kernel.MemberRouteGroup, ID: source.RouteID, Fidelity: source.Fidelity, Evidence: source.Evidence})
		}
		input.Nodes = append(input.Nodes, node)
		if row.Discoverable {
			input.PublicModels = append(input.PublicModels, kernel.PublicModel{Name: row.Name, TargetRef: row.Name, OwnedBy: "gobroom"})
		}
	}
	for _, row := range typedComboRows {
		node := kernel.ModelNode{ID: row.Name, Kind: kernel.ModelCombo, Strategy: kernelStrategy(row.Strategy.ID), StickyLimit: strategyInt(row.Strategy.Config, "stickyLimit", 1)}
		for _, member := range row.Members {
			node.Members = append(node.Members, kernel.MemberRef{Kind: kernel.MemberModel, ID: member.ID, Weight: strategyInt(row.Strategy.Config, "weight:"+member.ID, 0)})
		}
		input.Nodes = append(input.Nodes, node)
		if row.Discoverable {
			input.PublicModels = append(input.PublicModels, kernel.PublicModel{Name: row.Name, TargetRef: row.Name, OwnedBy: "gobroom"})
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
