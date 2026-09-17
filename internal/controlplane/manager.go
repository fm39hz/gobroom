package controlplane

import (
	"sync"

	"github.com/gorouter/gorouter/internal/kernel"
	"github.com/gorouter/gorouter/internal/store"
)

type Manager struct {
	store     *store.Store
	snapshots *kernel.SnapshotStore
	mu        sync.Mutex
	version   uint64
}

func NewManager(s *store.Store) (*Manager, error) {
	initial := kernel.Snapshot{PublicModels: map[string]kernel.PublicModel{}, Combos: map[string]kernel.Combo{}, Routes: map[string]kernel.Route{}, LogicalModels: map[string]string{}}
	snapshots, err := kernel.NewSnapshotStore(initial)
	if err != nil {
		return nil, err
	}
	m := &Manager{store: s, snapshots: snapshots}
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
	return nil
}

func (m *Manager) Snapshot() kernel.Snapshot { return m.snapshots.Load() }
func (m *Manager) Version() uint64           { m.mu.Lock(); defer m.mu.Unlock(); return m.version }
func (m *Manager) Resolve(name string) (kernel.ResolvedModel, error) {
	return kernel.ResolvePublic(m.snapshots.Load(), name)
}
