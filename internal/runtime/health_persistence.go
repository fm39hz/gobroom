package runtime

import (
	"sync"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
)

type HealthObservation struct {
	Route kernel.Route
	State RouteHealth
}

type HealthObservationSink func(HealthObservation) error
type HealthObservationErrorHandler func(HealthObservation, error)

type healthObservationKey struct {
	scope, provider, connection, model, route string
	operation                                 extensions.Ref
}

// HealthObservationQueue keeps inference independent of storage latency while
// coalescing pending observations at their actual breaker scope.
type HealthObservationQueue struct {
	capacity int
	pending  map[healthObservationKey]HealthObservation
	sink     HealthObservationSink
	onError  HealthObservationErrorHandler
	wake     chan struct{}
	done     chan struct{}
	mu       sync.Mutex
	close    sync.Once
	closed   bool
}

func NewHealthObservationQueue(capacity int, sink HealthObservationSink, onError HealthObservationErrorHandler) *HealthObservationQueue {
	if capacity < 1 {
		capacity = 8192
	}
	return &HealthObservationQueue{
		capacity: capacity,
		pending:  make(map[healthObservationKey]HealthObservation),
		sink:     sink,
		onError:  onError,
		wake:     make(chan struct{}, 1),
		done:     make(chan struct{}),
	}
}

// Enqueue is non-blocking. false means closed, unavailable, or full of
// distinct breaker identities; callers should surface the persistence loss.
func (q *HealthObservationQueue) Enqueue(observation HealthObservation) bool {
	if q == nil || q.sink == nil {
		return false
	}
	key := healthObservationIdentity(observation)
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return false
	}
	if _, exists := q.pending[key]; !exists && len(q.pending) >= q.capacity {
		q.mu.Unlock()
		return false
	}
	q.pending[key] = observation
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return true
}

func (q *HealthObservationQueue) Close() {
	if q == nil {
		return
	}
	q.close.Do(func() {
		q.mu.Lock()
		q.closed = true
		q.mu.Unlock()
		close(q.done)
	})
}

func (q *HealthObservationQueue) Run() {
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

func (q *HealthObservationQueue) flush() {
	for {
		q.mu.Lock()
		batch := q.pending
		q.pending = make(map[healthObservationKey]HealthObservation)
		q.mu.Unlock()
		if len(batch) == 0 {
			return
		}
		for _, observation := range batch {
			if err := q.sink(observation); err != nil && q.onError != nil {
				q.onError(observation, err)
			}
		}
	}
}

func healthObservationIdentity(observation HealthObservation) healthObservationKey {
	route := observation.Route
	key := healthObservationKey{scope: string(observation.State.LastScope)}
	switch observation.State.LastScope {
	case kernel.ScopeOperation:
		key.connection, key.model, key.route, key.operation = route.CredentialID, route.ExternalModel, route.ID, route.SessionOperationRef
	case kernel.ScopeProvider:
		key.provider = route.NodeID
	case kernel.ScopeConnection:
		key.connection = route.CredentialID
	case kernel.ScopeRouteConnection:
		key.connection, key.model, key.route = route.CredentialID, route.ExternalModel, route.ID
	default:
		key.connection, key.model, key.route = route.CredentialID, route.ExternalModel, route.ID
	}
	return key
}
