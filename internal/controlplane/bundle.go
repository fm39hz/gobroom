package controlplane

import (
	"fmt"
	"github.com/fm39hz/gobroom/internal/store"
)

type ConfigBundle struct {
	Version     int                      `json:"version"`
	Providers   []store.ProviderNode     `json:"providers"`
	Connections []store.ConnectionRecord `json:"connections"`
	Models      []store.Model            `json:"models"`
	Physical    []store.PhysicalModel    `json:"physicalModels"`
	Combos      []store.ComboModel       `json:"comboModels"`
}

type BundleDiff struct {
	ProvidersAdded, ProvidersRemoved, ConnectionsAdded, ConnectionsRemoved int      `json:"providersAdded"`
	ModelsAdded, ModelsRemoved, PhysicalAdded, PhysicalRemoved             int      `json:"modelsAdded"`
	CombosAdded, CombosRemoved                                             int      `json:"combosAdded"`
	Changes                                                                []string `json:"changes"`
}

func ExportBundle(s *store.Store) (ConfigBundle, error) {
	providers, err := s.ProviderNodes()
	if err != nil {
		return ConfigBundle{}, err
	}
	connections, err := s.Connections("")
	if err != nil {
		return ConfigBundle{}, err
	}
	models, err := s.Models()
	if err != nil {
		return ConfigBundle{}, err
	}
	physical, err := s.PhysicalModels()
	if err != nil {
		return ConfigBundle{}, err
	}
	combos, err := s.ComboModels()
	if err != nil {
		return ConfigBundle{}, err
	}
	return ConfigBundle{Version: 1, Providers: providers, Connections: connections, Models: models, Physical: physical, Combos: combos}, nil
}

func ValidateBundle(bundle ConfigBundle) error {
	if bundle.Version != 1 {
		return fmt.Errorf("unsupported config bundle version %d", bundle.Version)
	}
	providers := map[string]bool{}
	for _, item := range bundle.Providers {
		if item.ID == "" {
			return fmt.Errorf("provider ID is required")
		}
		if providers[item.ID] {
			return fmt.Errorf("duplicate provider %q", item.ID)
		}
		providers[item.ID] = true
	}
	for _, item := range bundle.Connections {
		if item.ID == "" || !providers[item.ProviderNodeID] {
			return fmt.Errorf("connection %q references unknown provider", item.ID)
		}
	}
	models := map[string]bool{}
	for _, item := range bundle.Models {
		if item.ID == "" {
			return fmt.Errorf("model ID is required")
		}
		models[item.ID] = true
	}
	physical := map[string]bool{}
	for _, item := range bundle.Physical {
		if item.Name == "" {
			return fmt.Errorf("physical model name is required")
		}
		if physical[item.Name] {
			return fmt.Errorf("duplicate physical model %q", item.Name)
		}
		physical[item.Name] = true
		for _, source := range item.Sources {
			if !models[source.RouteID] {
				return fmt.Errorf("physical %q references unknown route %q", item.Name, source.RouteID)
			}
		}
	}
	for _, item := range bundle.Combos {
		if item.Name == "" {
			return fmt.Errorf("combo model name is required")
		}
		for _, member := range item.Members {
			if member.Kind == store.PhysicalReference && !physical[member.ID] {
				return fmt.Errorf("combo %q references unknown physical %q", item.Name, member.ID)
			}
		}
	}
	return nil
}

func DiffBundle(current, desired ConfigBundle) (BundleDiff, error) {
	if err := ValidateBundle(desired); err != nil {
		return BundleDiff{}, err
	}
	result := BundleDiff{}
	result.ProvidersAdded, result.ProvidersRemoved = countDelta(providerIDs(current.Providers), providerIDs(desired.Providers), "provider", &result.Changes)
	result.ConnectionsAdded, result.ConnectionsRemoved = countDelta(connectionIDs(current.Connections), connectionIDs(desired.Connections), "connection", &result.Changes)
	result.ModelsAdded, result.ModelsRemoved = countDelta(modelIDs(current.Models), modelIDs(desired.Models), "discovered model", &result.Changes)
	result.PhysicalAdded, result.PhysicalRemoved = countDelta(physicalIDs(current.Physical), physicalIDs(desired.Physical), "physical model", &result.Changes)
	result.CombosAdded, result.CombosRemoved = countDelta(comboIDs(current.Combos), comboIDs(desired.Combos), "combo model", &result.Changes)
	return result, nil
}

func countDelta(current, desired map[string]bool, kind string, changes *[]string) (int, int) {
	added, removed := 0, 0
	for id := range desired {
		if !current[id] {
			added++
			*changes = append(*changes, "+ "+kind+" "+id)
		}
	}
	for id := range current {
		if !desired[id] {
			removed++
			*changes = append(*changes, "- "+kind+" "+id)
		}
	}
	return added, removed
}
func providerIDs(items []store.ProviderNode) map[string]bool {
	result := map[string]bool{}
	for _, item := range items {
		result[item.ID] = true
	}
	return result
}
func connectionIDs(items []store.ConnectionRecord) map[string]bool {
	result := map[string]bool{}
	for _, item := range items {
		result[item.ID] = true
	}
	return result
}
func modelIDs(items []store.Model) map[string]bool {
	result := map[string]bool{}
	for _, item := range items {
		result[item.ID] = true
	}
	return result
}
func physicalIDs(items []store.PhysicalModel) map[string]bool {
	result := map[string]bool{}
	for _, item := range items {
		result[item.Name] = true
	}
	return result
}
func comboIDs(items []store.ComboModel) map[string]bool {
	result := map[string]bool{}
	for _, item := range items {
		result[item.Name] = true
	}
	return result
}
