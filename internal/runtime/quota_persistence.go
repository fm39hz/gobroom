package runtime

import (
	"sync"

	"github.com/fm39hz/gobroom/internal/quota"
)

type QuotaSnapshotSink interface {
	SaveQuotaSnapshot(quota.Snapshot) error
}

type quotaSnapshotKey struct {
	provider, connection, model, window string
}

// QuotaSnapshotQueue coalesces pending writes by typed quota identity so
// persistence never performs SQLite I/O on the inference response path.
type QuotaSnapshotQueue struct {
	sink      QuotaSnapshotSink
	capacity  int
	pending   map[quotaSnapshotKey]quota.Snapshot
	wake      chan struct{}
	done      chan struct{}
	mu        sync.Mutex
	closeOnce sync.Once
	closed    bool
	onError   func(quota.Snapshot, error)
}

func NewQuotaSnapshotQueue(sink QuotaSnapshotSink, capacity int, onError func(quota.Snapshot, error)) *QuotaSnapshotQueue {
	if capacity < 1 {
		capacity = 256
	}
	return &QuotaSnapshotQueue{sink: sink, capacity: capacity, pending: map[quotaSnapshotKey]quota.Snapshot{}, wake: make(chan struct{}, 1), done: make(chan struct{}), onError: onError}
}

// Enqueue is non-blocking. New observations replace stale pending evidence for
// the same provider/connection/model/window; false means the bounded queue is
// full of distinct keys and the caller should log the persistence loss.
func (q *QuotaSnapshotQueue) Enqueue(snapshot quota.Snapshot) bool {
	if q == nil || q.sink == nil {
		return false
	}
	key := quotaSnapshotKey{provider: snapshot.ProviderNodeID, connection: snapshot.ConnectionID, model: snapshot.ModelRef, window: snapshot.WindowName}
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return false
	}
	if _, exists := q.pending[key]; !exists && len(q.pending) >= q.capacity {
		q.mu.Unlock()
		return false
	}
	q.pending[key] = snapshot
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return true
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
		q.pending = make(map[quotaSnapshotKey]quota.Snapshot)
		q.mu.Unlock()
		if len(batch) == 0 {
			return
		}
		for _, snapshot := range batch {
			if err := q.sink.SaveQuotaSnapshot(snapshot); err != nil && q.onError != nil {
				q.onError(snapshot, err)
			}
		}
	}
}
