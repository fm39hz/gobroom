package kernel

import "fmt"

// ensureModelNodes validates the typed Physical/Combo graph.
func ensureModelNodes(snapshot *Snapshot) error {
	if snapshot.Nodes == nil {
		snapshot.Nodes = make(map[string]ModelNode)
	}
	for _, public := range snapshot.PublicModels {
		if _, exists := snapshot.Nodes[public.TargetRef]; exists {
			continue
		}
		member := classifyMember(*snapshot, public.TargetRef)
		if member.Kind == MemberRoute || member.Kind == MemberRouteGroup {
			snapshot.Nodes[public.TargetRef] = ModelNode{ID: public.TargetRef, Kind: ModelPhysical, Strategy: StrategyFallback, Members: []MemberRef{member}}
		}
	}
	for id, node := range snapshot.Nodes {
		if node.ID == "" {
			node.ID = id
		}
		if node.Strategy == "" {
			node.Strategy = StrategyFallback
		}
		if node.Kind == "" {
			node.Kind = ModelCombo
			for _, member := range node.Members {
				if member.Kind == MemberRoute || member.Kind == MemberRouteGroup {
					node.Kind = ModelPhysical
					break
				}
			}
		}
		for i := range node.Members {
			if node.Members[i].Kind == "" {
				node.Members[i] = classifyMember(*snapshot, node.Members[i].ID)
			}
		}
		snapshot.Nodes[id] = cloneNode(node)
	}
	return nil
}

func classifyMember(snapshot Snapshot, ref string) MemberRef {
	if _, ok := snapshot.Nodes[ref]; ok {
		return MemberRef{Kind: MemberModel, ID: ref}
	}
	if _, ok := snapshot.Routes[ref]; ok {
		return MemberRef{Kind: MemberRoute, ID: ref}
	}
	if _, ok := snapshot.RouteGroups[ref]; ok {
		return MemberRef{Kind: MemberRouteGroup, ID: ref}
	}
	if _, ok := snapshot.WireRoutes[ref]; ok {
		return MemberRef{Kind: MemberRouteGroup, ID: ref}
	}
	return MemberRef{ID: ref}
}

func cloneNode(node ModelNode) ModelNode {
	node.Members = append([]MemberRef(nil), node.Members...)
	return node
}

func ResolveModelNode(s Snapshot, name string) (ModelNode, error) {
	s.Nodes = cloneNodes(s.Nodes)
	public, ok := s.PublicModels[name]
	if !ok {
		return ModelNode{}, ErrModelNotPublished
	}
	if err := ensureModelNodes(&s); err != nil {
		return ModelNode{}, err
	}
	node, ok := s.Nodes[public.TargetRef]
	if !ok {
		return ModelNode{}, fmt.Errorf("public model %q targets non-model reference %q", name, public.TargetRef)
	}
	return cloneNode(node), nil
}

func cloneNodes(nodes map[string]ModelNode) map[string]ModelNode {
	result := make(map[string]ModelNode, len(nodes))
	for id, node := range nodes {
		result[id] = cloneNode(node)
	}
	return result
}

func resolveRouteMember(s Snapshot, member MemberRef) []Route {
	if member.Kind == MemberRoute {
		if route, ok := s.Routes[member.ID]; ok {
			return []Route{route}
		}
		return nil
	}
	var ids []string
	switch member.Kind {
	case MemberRouteGroup:
		ids = s.RouteGroups[member.ID]
		if len(ids) == 0 {
			ids = s.WireRoutes[member.ID]
		}
	default:
		return nil
	}
	result := make([]Route, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		if route, ok := s.Routes[id]; ok {
			result = append(result, route)
		}
	}
	return result
}

func validateModelGraph(s Snapshot) error {
	if err := ensureModelNodes(&s); err != nil {
		return err
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var walk func(string) error
	walk = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("model cycle at %q", id)
		}
		if visited[id] {
			return nil
		}
		node, ok := s.Nodes[id]
		if !ok {
			return fmt.Errorf("unknown model node %q", id)
		}
		visiting[id] = true
		for _, member := range node.Members {
			switch member.Kind {
			case MemberModel:
				if err := walk(member.ID); err != nil {
					return err
				}
			case MemberRoute:
				if _, ok := s.Routes[member.ID]; !ok {
					return fmt.Errorf("unknown route %q", member.ID)
				}
			case MemberRouteGroup:
				if _, ok := s.RouteGroups[member.ID]; !ok {
					if _, ok := s.WireRoutes[member.ID]; !ok {
						return fmt.Errorf("unknown route group %q", member.ID)
					}
				}
			default:
				return fmt.Errorf("model %q has untyped member %q", id, member.ID)
			}
		}
		delete(visiting, id)
		visited[id] = true
		return nil
	}
	for id := range s.Nodes {
		if err := walk(id); err != nil {
			return err
		}
	}
	return nil
}
