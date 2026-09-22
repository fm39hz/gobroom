package kernel

import "fmt"

type SnapshotInput struct {
	PublicModels []PublicModel
	Routes       []Route
	RouteGroups  map[string][]string
	WireRoutes   map[string][]string
	Nodes        []ModelNode
}

// BuildSnapshot converts durable control-plane records into the immutable
// data-plane representation. Storage adapters stay outside the kernel.
func BuildSnapshot(input SnapshotInput, version uint64) (Snapshot, error) {
	snapshot := Snapshot{
		Version: version, PublicModels: map[string]PublicModel{}, Routes: map[string]Route{}, RouteGroups: map[string][]string{}, WireRoutes: map[string][]string{}, Nodes: map[string]ModelNode{},
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
	for _, item := range input.Routes {
		if item.ID == "" {
			return Snapshot{}, fmt.Errorf("route has empty ID")
		}
		if _, exists := snapshot.Routes[item.ID]; exists {
			return Snapshot{}, fmt.Errorf("duplicate route %q", item.ID)
		}
		snapshot.Routes[item.ID] = item
		snapshot.RouteGroups[item.ID] = []string{item.ID}
		if item.DisplayPrefix != "" && item.ExternalModel != "" {
			wireName := item.DisplayPrefix + "/" + item.ExternalModel
			snapshot.WireRoutes[wireName] = append(snapshot.WireRoutes[wireName], item.ID)
		}
	}
	for group, variants := range input.RouteGroups {
		if group == "" {
			return Snapshot{}, fmt.Errorf("route group has empty name")
		}
		snapshot.RouteGroups[group] = append([]string(nil), variants...)
	}
	for wireName, variants := range input.WireRoutes {
		if wireName == "" {
			return Snapshot{}, fmt.Errorf("wire route has empty name")
		}
		snapshot.WireRoutes[wireName] = append([]string(nil), variants...)
	}
	for _, node := range input.Nodes {
		if node.ID == "" {
			return Snapshot{}, fmt.Errorf("model node has empty ID")
		}
		if _, exists := snapshot.Nodes[node.ID]; exists {
			return Snapshot{}, fmt.Errorf("duplicate model node %q", node.ID)
		}
		snapshot.Nodes[node.ID] = cloneNode(node)
	}
	if err := ensureModelNodes(&snapshot); err != nil {
		return Snapshot{}, err
	}
	if err := ValidateSnapshot(snapshot); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}
