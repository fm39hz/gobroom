package daemon

import (
	"github.com/fm39hz/gobroom/internal/discovery"
	"github.com/fm39hz/gobroom/internal/operations"
)

// Every control transport uses the same issuer as serving and quota work.
func (d *Daemon) discoveryService(snapshot *operations.Snapshot) discovery.Service {
	return discovery.Service{Store: d.store, Bindings: d.providerBindings,
		Operations: snapshot, Artifacts: d.artifactStore,
		AcquireCredential: d.acquireScopedCredentialLease}
}
