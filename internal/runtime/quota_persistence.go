package runtime

import (
	"errors"
	"sync"

	"github.com/fm39hz/gobroom/internal/quota"
)

type QuotaSnapshotSink interface {
	SaveQuotaSnapshot(quota.Snapshot) error
}

type quotaSnapshotKey = quota.Identity

type queuedQuotaSnapshot struct {
	snapshot          quota.Snapshot
	connectionVersion uint64
	providerVersion   uint64
}

// QuotaSnapshotQueue coalesces pending writes by typed quota identity so
// persistence never performs SQLite I/O on the inference response path.
type QuotaSnapshotQueue struct {
	sink                 QuotaSnapshotSink
	capacity             int
	pending              map[quotaSnapshotKey]queuedQuotaSnapshot
	wake                 chan struct{}
	done                 chan struct{}
	mu                   sync.Mutex
	writeMu              sync.RWMutex
	connectionVersion    map[string]uint64
	providerVersion      map[string]uint64
	resettingConnections map[string]bool
	resettingProviders   map[string]bool
	closeOnce            sync.Once
	closed               bool
	onError              func(quota.Snapshot, error)
}

func NewQuotaSnapshotQueue(sink QuotaSnapshotSink, capacity int, onError func(quota.Snapshot, error)) *QuotaSnapshotQueue {
	if capacity < 1 {
		capacity = 256
	}
	return &QuotaSnapshotQueue{
		sink: sink, capacity: capacity, pending: map[quotaSnapshotKey]queuedQuotaSnapshot{},
		wake: make(chan struct{}, 1), done: make(chan struct{}), onError: onError,
		connectionVersion: map[string]uint64{}, providerVersion: map[string]uint64{},
		resettingConnections: map[string]bool{}, resettingProviders: map[string]bool{},
	}
}

// Enqueue is non-blocking. New observations replace stale pending evidence for
// the same provider/connection/model/window; false means the bounded queue is
// full of distinct keys and the caller should log the persistence loss.
func (q *QuotaSnapshotQueue) Enqueue(snapshot quota.Snapshot) bool {
	if q == nil || q.sink == nil {
		return false
	}
	if snapshot.ValidateOperation() != nil {
		return false
	}
	key := snapshot.Identity()
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return false
	}
	if q.resettingConnections[snapshot.ConnectionID] || q.resettingProviders[snapshot.ProviderNodeID] {
		q.mu.Unlock()
		return false
	}
	if _, exists := q.pending[key]; !exists && len(q.pending) >= q.capacity {
		q.mu.Unlock()
		return false
	}
	q.pending[key] = queuedQuotaSnapshot{snapshot: snapshot, connectionVersion: q.connectionVersion[snapshot.ConnectionID], providerVersion: q.providerVersion[snapshot.ProviderNodeID]}
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return true
}

// ResetConnection drains any queued writes for a replaced/reconfigured
// connection and serializes the durable reset against an active SQLite write.
func (q *QuotaSnapshotQueue) ResetConnection(connectionID string, reset func() error) error {
	if connectionID == "" || reset == nil {
		return errors.New("connection ID and reset callback are required")
	}
	if q == nil {
		return reset()
	}
	q.writeMu.Lock()
	defer q.writeMu.Unlock()
	q.mu.Lock()
	q.connectionVersion[connectionID]++
	q.resettingConnections[connectionID] = true
	for key := range q.pending {
		if key.Connection == connectionID {
			delete(q.pending, key)
		}
	}
	q.mu.Unlock()
	err := reset()
	q.mu.Lock()
	delete(q.resettingConnections, connectionID)
	q.mu.Unlock()
	return err
}

// ResetProvider is the provider-wide counterpart to ResetConnection.
func (q *QuotaSnapshotQueue) ResetProvider(providerNodeID string, reset func() error) error {
	if providerNodeID == "" || reset == nil {
		return errors.New("provider node ID and reset callback are required")
	}
	if q == nil {
		return reset()
	}
	q.writeMu.Lock()
	defer q.writeMu.Unlock()
	q.mu.Lock()
	q.providerVersion[providerNodeID]++
	q.resettingProviders[providerNodeID] = true
	for key := range q.pending {
		if key.Provider == providerNodeID {
			delete(q.pending, key)
		}
	}
	q.mu.Unlock()
	err := reset()
	q.mu.Lock()
	delete(q.resettingProviders, providerNodeID)
	q.mu.Unlock()
	return err
}

func (q *QuotaSnapshotQueue) Close() {
	if q == nil {
		return
	}
	q.closeOnce.Do(func() {
		q.mu.Lock()
		q.closed = true
		q.mu.Unlock()
		close(q.done)
	})
}

func (q *QuotaSnapshotQueue) Run() {
	if q == nil || q.sink == nil {
		return
	}
	for {
		select {
		case <-q.done:
			q.flush()
			return
		case <-q.wake:
			q.flush()
		}
	}
}

func (q *QuotaSnapshotQueue) flush() {
	for {
		q.mu.Lock()
		batch := q.pending
		q.pending = make(map[quotaSnapshotKey]queuedQuotaSnapshot)
		q.mu.Unlock()
		if len(batch) == 0 {
			return
		}
		for _, queued := range batch {
			q.writeMu.RLock()
			q.mu.Lock()
			stale := queued.connectionVersion != q.connectionVersion[queued.snapshot.ConnectionID] || queued.providerVersion != q.providerVersion[queued.snapshot.ProviderNodeID] || q.resettingConnections[queued.snapshot.ConnectionID] || q.resettingProviders[queued.snapshot.ProviderNodeID]
			q.mu.Unlock()
			if stale {
				q.writeMu.RUnlock()
				continue
			}
			if err := q.sink.SaveQuotaSnapshot(queued.snapshot); err != nil && q.onError != nil {
				q.onError(queued.snapshot, err)
			}
			q.writeMu.RUnlock()
		}
	}
}
