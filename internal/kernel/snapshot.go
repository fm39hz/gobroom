package kernel

import (
	"fmt"
	"sort"
	"sync/atomic"
)

type SnapshotStore struct{ current atomic.Pointer[Snapshot] }

func NewSnapshotStore(initial Snapshot) (*SnapshotStore, error) {
	if err := ensureModelNodes(&initial); err != nil {
		return nil, err
	}
	if err := ValidateSnapshot(initial); err != nil {
		return nil, err
	}
	s := &SnapshotStore{}
	s.current.Store(&initial)
	return s, nil
}

func (s *SnapshotStore) Load() Snapshot {
	if value := s.current.Load(); value != nil {
		return *value
	}
	return Snapshot{}
}

func (s *SnapshotStore) Publish(next Snapshot) error {
	if err := ensureModelNodes(&next); err != nil {
		return err
	}
	if err := ValidateSnapshot(next); err != nil {
		return err
	}
	s.current.Store(&next)
	return nil
}

func ValidateSnapshot(s Snapshot) error {
	if s.PublicModels == nil || s.Routes == nil || s.RouteGroups == nil || s.WireRoutes == nil || s.Nodes == nil {
		return ErrSnapshotInvalid
	}
	for name, public := range s.PublicModels {
		if name == "" || public.TargetRef == "" {
			return fmt.Errorf("public model %q has empty target", name)
		}
		if _, err := resolveRef(s, public.TargetRef, map[string]bool{}); err != nil {
			return fmt.Errorf("public model %q: %w", name, err)
		}
	}
	return validateModelGraph(s)
}

func ResolvePublicNode(s Snapshot, name string) (ModelNode, error) {
	return ResolveModelNode(s, name)
}

func ResolvePublic(s Snapshot, name string) (ResolvedModel, error) {
	public, ok := s.PublicModels[name]
	if !ok {
		return ResolvedModel{}, ErrModelNotPublished
	}
	items, err := resolveRef(s, public.TargetRef, map[string]bool{})
	if err != nil {
		return ResolvedModel{}, err
	}
	node, ok := s.Nodes[public.TargetRef]
	if !ok {
		return ResolvedModel{}, fmt.Errorf("public model %q targets unknown typed model %q", name, public.TargetRef)
	}
	strategy, stickyLimit := node.Strategy, node.StickyLimit
	if strategy == "" {
		strategy = StrategyFallback
	}
	if stickyLimit < 1 {
		stickyLimit = 1
	}
	return ResolvedModel{PublicName: name, TargetRef: public.TargetRef, Strategy: strategy, StickyLimit: stickyLimit, Candidates: items}, nil
}

func resolveRef(s Snapshot, ref string, stack map[string]bool) ([]Route, error) {
	if route, ok := s.Routes[ref]; ok {
		if !route.Enabled {
			return nil, nil
		}
		return []Route{route}, nil
	}
	if variants, ok := s.RouteGroups[ref]; ok {
		result := make([]Route, 0, len(variants))
		for _, variant := range variants {
			route, exists := s.Routes[variant]
			if !exists || !route.Enabled {
				continue
			}
			result = append(result, route)
		}
		return result, nil
	}
	if variants, ok := s.WireRoutes[ref]; ok {
		result := make([]Route, 0, len(variants))
		for _, variant := range variants {
			route, exists := s.Routes[variant]
			if !exists || !route.Enabled {
				continue
			}
			result = append(result, route)
		}
		return result, nil
	}
	if node, ok := s.Nodes[ref]; ok {
		if stack[ref] {
			return nil, fmt.Errorf("model cycle at %q", ref)
		}
		stack[ref] = true
		defer delete(stack, ref)
		seen := map[string]bool{}
		result := make([]Route, 0)
		for _, member := range node.Members {
			var items []Route
			var err error
			if member.Kind == MemberRouteGroup {
				items = resolveRouteMember(s, member)
			} else {
				items, err = resolveRef(s, member.ID, stack)
			}
			if err != nil {
				return nil, err
			}
			for _, item := range items {
				if !seen[item.ID] {
					seen[item.ID] = true
					result = append(result, item)
				}
			}
		}
		return result, nil
	}
	return nil, fmt.Errorf("unknown typed model or route reference %q", ref)
}

func CandidatesByID(items []Route) []string {
	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, item.ID)
	}
	sort.Strings(result)
	return result
}
