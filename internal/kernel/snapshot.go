package kernel

import (
	"fmt"
	"sort"
	"sync/atomic"
)

type SnapshotStore struct{ current atomic.Pointer[Snapshot] }

func NewSnapshotStore(initial Snapshot) (*SnapshotStore, error) {
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
	if err := ValidateSnapshot(next); err != nil {
		return err
	}
	s.current.Store(&next)
	return nil
}

func ValidateSnapshot(s Snapshot) error {
	if s.PublicModels == nil || s.Combos == nil || s.Routes == nil || s.LogicalModels == nil {
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
	return nil
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
	strategy := StrategyFallback
	if combo, ok := s.Combos[public.TargetRef]; ok && combo.Strategy != "" {
		strategy = combo.Strategy
	}
	return ResolvedModel{PublicName: name, TargetRef: public.TargetRef, Strategy: strategy, Candidates: items}, nil
}

func resolveRef(s Snapshot, ref string, stack map[string]bool) ([]Route, error) {
	if route, ok := s.Routes[ref]; ok {
		if !route.Enabled {
			return nil, nil
		}
		return []Route{route}, nil
	}
	if logical, ok := s.LogicalModels[ref]; ok {
		return resolveRef(s, logical, stack)
	}
	combo, ok := s.Combos[ref]
	if !ok {
		return nil, fmt.Errorf("unknown route reference %q", ref)
	}
	if stack[ref] {
		return nil, fmt.Errorf("combo cycle at %q", ref)
	}
	stack[ref] = true
	defer delete(stack, ref)
	seen := map[string]bool{}
	result := make([]Route, 0)
	for _, member := range combo.Members {
		items, err := resolveRef(s, member, stack)
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

func CandidatesByID(items []Route) []string {
	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, item.ID)
	}
	sort.Strings(result)
	return result
}
