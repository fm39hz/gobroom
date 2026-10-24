package daemon

import (
	"errors"
	"sync"
	"sync/atomic"

	"github.com/fm39hz/gobroom/internal/sessionpersistence"
)

// ErrSessionPersistenceFailureObserved reports fault history, not a claim that
// newer session writes have not repaired an earlier failed version.
var ErrSessionPersistenceFailureObserved = errors.New("session persistence observed failures")

// bindSessionPersistence runs before serving. Neither callback receives an error
// string, session key, response ID, opaque payload or signature.
func (d *Daemon) bindSessionPersistence() {
	cache := d.sessionCache
	var saveErrors atomic.Uint64
	notify := sync.OnceFunc(func() {
		d.appendLog("warn", "session persistence observed failures; inspect sessionPersistence counters")
	})
	cache.OnFailure = notify
	d.kernel.OnSessionSaveFailure = func() {
		saveErrors.Add(1)
		notify()
	}
	snapshot := func() sessionpersistence.Status {
		status := cache.PersistenceStatus()
		status.SaveErrors = saveErrors.Load()
		return status
	}
	d.server.SetSessionPersistenceObserver(snapshot)
	d.sessionPersistenceFailure = func() error {
		if snapshot().Faulted() {
			return ErrSessionPersistenceFailureObserved
		}
		return nil
	}
}
