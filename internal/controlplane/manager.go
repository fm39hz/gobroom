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
	initial := kernel.Snapshot{PublicModels: map[string]kernel.PublicModel{}, Routes: map[string]kernel.Route{}, RouteGroups: map[string][]string{}, WireRoutes: map[string][]string{}, Nodes: map[string]kernel.ModelNode{}}
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
	input := kernel.SnapshotInput{RouteGroups: map[string][]string{}, WireRoutes: map[string][]string{}}
	for _, item := range s.PublicModels {
		input.PublicModels = append(input.PublicModels, item)
	}
	for _, item := range s.Routes {
		input.Routes = append(input.Routes, item)
	}
	for _, item := range s.Nodes {
		input.Nodes = append(input.Nodes, item)
	}
	for name, variants := range s.RouteGroups {
		input.RouteGroups[name] = append([]string(nil), variants...)
	}
	for name, variants := range s.WireRoutes {
		input.WireRoutes[name] = append([]string(nil), variants...)
	}
	return input
}
