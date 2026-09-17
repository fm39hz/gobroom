package controlplane

import (
	"fmt"
	"strconv"
	"sync"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/store"
)

type Manager struct {
	store     *store.Store
	snapshots *kernel.SnapshotStore
	mu        sync.Mutex
	version   uint64
}

func NewManager(s *store.Store) (*Manager, error) {
	initial := kernel.Snapshot{PublicModels: map[string]kernel.PublicModel{}, Combos: map[string]kernel.Combo{}, Routes: map[string]kernel.Route{}, RouteGroups: map[string][]string{}, WireRoutes: map[string][]string{}, LogicalModels: map[string]string{}}
	snapshots, err := kernel.NewSnapshotStore(initial)
	if err != nil {
		return nil, err
	}
	m := &Manager{store: s, snapshots: snapshots}
	if raw, ok, err := s.Setting("snapshot_version"); err == nil && ok {
		m.version, _ = strconv.ParseUint(raw, 10, 64)
	}
	if err := m.Reload(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) Reload() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	nextVersion := m.version + 1
	snapshot, err := (Loader{Store: m.store}).LoadSnapshot(nextVersion)
	if err != nil {
		return err
	}
	if err := m.snapshots.Publish(snapshot); err != nil {
		return err
	}
	m.version = nextVersion
	if err := m.store.SetSetting("snapshot_version", strconv.FormatUint(m.version, 10)); err != nil {
		return err
	}
	return nil
}

func (m *Manager) Snapshot() kernel.Snapshot { return m.snapshots.Load() }
func (m *Manager) Version() uint64           { m.mu.Lock(); defer m.mu.Unlock(); return m.version }
func (m *Manager) Resolve(name string) (kernel.ResolvedModel, error) {
	return kernel.ResolvePublic(m.snapshots.Load(), name)
}

func (m *Manager) ValidateCombo(record store.ComboRecord) error {
	s := m.snapshots.Load()
	input := snapshotInput(s)
	replaced := false
	for i := range input.Combos {
		if input.Combos[i].Name == record.Name {
			input.Combos[i] = kernel.Combo{Name: record.Name, Strategy: kernel.Strategy(record.Strategy), StickyLimit: record.StickyLimit, Members: record.Members}
			replaced = true
			break
		}
	}
	if !replaced {
		input.Combos = append(input.Combos, kernel.Combo{Name: record.Name, Strategy: kernel.Strategy(record.Strategy), StickyLimit: record.StickyLimit, Members: record.Members})
	}
	_, err := kernel.BuildSnapshot(input, s.Version+1)
	return err
}

func (m *Manager) ValidateLogicalModel(record store.LogicalModelRecord) error {
	s := m.snapshots.Load()
	input := snapshotInput(s)
	input.LogicalModels[record.Name] = record.TargetRef
	_, err := kernel.BuildSnapshot(input, s.Version+1)
	return err
}

func (m *Manager) ValidateComboDelete(name string) error {
	s := m.snapshots.Load()
	input := snapshotInput(s)
	filtered := input.Combos[:0]
	for _, item := range input.Combos {
		if item.Name != name {
			filtered = append(filtered, item)
		}
	}
	input.Combos = filtered
	_, err := kernel.BuildSnapshot(input, s.Version+1)
	return err
}

func (m *Manager) ValidateLogicalModelDelete(name string) error {
	s := m.snapshots.Load()
	input := snapshotInput(s)
	delete(input.LogicalModels, name)
	_, err := kernel.BuildSnapshot(input, s.Version+1)
	return err
}

func (m *Manager) ValidatePublicModel(item store.PublicModel) error {
	s := m.snapshots.Load()
	input := snapshotInput(s)
	replaced := false
	for i := range input.PublicModels {
		if input.PublicModels[i].Name == item.Name {
			input.PublicModels[i] = kernel.PublicModel{Name: item.Name, TargetRef: item.TargetRef, OwnedBy: item.OwnedBy}
			replaced = true
			break
		}
	}
	if !replaced {
		input.PublicModels = append(input.PublicModels, kernel.PublicModel{Name: item.Name, TargetRef: item.TargetRef, OwnedBy: item.OwnedBy})
	}
	_, err := kernel.BuildSnapshot(input, s.Version+1)
	return err
}

func (m *Manager) ValidateProviderDelete(id string) error {
	if id == "" {
		return fmt.Errorf("provider node ID is required")
	}
	for _, route := range m.snapshots.Load().Routes {
		if route.NodeID == id {
			return fmt.Errorf("provider node %q still has catalog routes", id)
		}
	}
	return nil
}

func snapshotInput(s kernel.Snapshot) kernel.SnapshotInput {
	input := kernel.SnapshotInput{LogicalModels: map[string]string{}, RouteGroups: map[string][]string{}, WireRoutes: map[string][]string{}}
	for _, item := range s.PublicModels {
		input.PublicModels = append(input.PublicModels, item)
	}
	for _, item := range s.Combos {
		input.Combos = append(input.Combos, item)
	}
	for _, item := range s.Routes {
		input.Routes = append(input.Routes, item)
	}
	for name, target := range s.LogicalModels {
		input.LogicalModels[name] = target
	}
	for name, variants := range s.RouteGroups {
		input.RouteGroups[name] = append([]string(nil), variants...)
	}
	for name, variants := range s.WireRoutes {
		input.WireRoutes[name] = append([]string(nil), variants...)
	}
	return input
}
