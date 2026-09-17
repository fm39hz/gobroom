package controlplane

import (
	"fmt"

	"github.com/gorouter/gorouter/internal/kernel"
	"github.com/gorouter/gorouter/internal/store"
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

	input := kernel.SnapshotInput{LogicalModels: map[string]string{}}
	for _, row := range publicRows {
		input.PublicModels = append(input.PublicModels, kernel.PublicModel{Name: row.Name, TargetRef: row.TargetRef, OwnedBy: row.OwnedBy})
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
		input.Routes = append(input.Routes, kernel.Route{ID: row.ID, NodeID: row.NodeID, DisplayPrefix: row.Prefix, ExternalModel: row.ExternalModel, Protocol: protocol, Enabled: row.Enabled})
	}
	for _, row := range logicalRows {
		input.LogicalModels[row.Name] = row.TargetRef
	}
	return kernel.BuildSnapshot(input, version)
}
