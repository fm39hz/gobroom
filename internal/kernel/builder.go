package kernel

import "fmt"

type SnapshotInput struct {
	PublicModels  []PublicModel
	Combos        []Combo
	Routes        []Route
	LogicalModels map[string]string
}

// BuildSnapshot converts durable control-plane records into the immutable
// data-plane representation. Storage adapters stay outside the kernel.
func BuildSnapshot(input SnapshotInput, version uint64) (Snapshot, error) {
	snapshot := Snapshot{
		Version: version, PublicModels: map[string]PublicModel{}, Combos: map[string]Combo{}, Routes: map[string]Route{}, LogicalModels: map[string]string{},
	}
	for _, item := range input.PublicModels {
		if item.Name == "" {
			return Snapshot{}, fmt.Errorf("public model has empty name")
		}
		if _, exists := snapshot.PublicModels[item.Name]; exists {
			return Snapshot{}, fmt.Errorf("duplicate public model %q", item.Name)
		}
		snapshot.PublicModels[item.Name] = item
	}
	for _, item := range input.Combos {
		if item.Name == "" {
			return Snapshot{}, fmt.Errorf("combo has empty name")
		}
		if item.Strategy == "" {
			item.Strategy = StrategyFallback
		}
		if _, exists := snapshot.Combos[item.Name]; exists {
			return Snapshot{}, fmt.Errorf("duplicate combo %q", item.Name)
		}
		snapshot.Combos[item.Name] = item
	}
	for _, item := range input.Routes {
		if item.ID == "" {
			return Snapshot{}, fmt.Errorf("route has empty ID")
		}
		if _, exists := snapshot.Routes[item.ID]; exists {
			return Snapshot{}, fmt.Errorf("duplicate route %q", item.ID)
		}
		snapshot.Routes[item.ID] = item
	}
	for name, target := range input.LogicalModels {
		if name == "" || target == "" {
			return Snapshot{}, fmt.Errorf("invalid logical model %q", name)
		}
		snapshot.LogicalModels[name] = target
	}
	if err := ValidateSnapshot(snapshot); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}
