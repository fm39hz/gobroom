package store

import "github.com/fm39hz/gobroom/internal/kernel"

// SnapshotRepository is the read-only boundary needed to build an immutable
// data-plane snapshot. Store is the default SQLite implementation; alternate
// backends must satisfy this interface and pass their own transaction/recovery
// tests before being supported.
type SnapshotRepository interface {
	Routes() ([]RouteRecord, error)
	DiscoveredRoutes(providerNodeID string) ([]DiscoveredRoute, error)
	PhysicalModels() ([]PhysicalModel, error)
	ComboModels() ([]ComboModel, error)
	TransformBindings() ([]kernel.TransformBinding, error)
}
