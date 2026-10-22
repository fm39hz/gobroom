package sessionstore

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/store"
)

// Repository owns persisted principal/endpoint authority validation. Cache
// independently enforces state validity, TTL, entry bounds and copy ownership;
// it does not rely on a backend's row ordering or limit for those invariants.
type Repository interface {
	ProviderSessions() ([]store.ProviderSession, error)
	SaveProviderSession(store.ProviderSession) error
	PruneExpiredProviderSessions(time.Time) (int64, error)
}

type key struct {
	connectionID string
	namespace    string
	sessionID    string
}

type entry struct {
	state     kernel.SessionState
	updatedAt time.Time
}

type write struct {
	record    store.ProviderSession
	done      chan error
	completed func(error)
}

// Cache provides synchronous in-memory session reads/writes and bounded
// asynchronous persistence. SQLite is only touched during preload and from
// the worker, never while the data plane is choosing or preparing a route.
type Cache struct {
	repository Repository
	mu         sync.Mutex
	entries    map[key]entry
	writes     chan write
	ttl        time.Duration
	maxEntries int
	dropped    atomic.Uint64
	version    uint64 // protected by mu; preload runs before publication
}

func New(repository Repository) (*Cache, error) {
	if repository == nil {
		return nil, fmt.Errorf("session repository is required")
	}
	if _, err := repository.PruneExpiredProviderSessions(time.Now()); err != nil {
		return nil, fmt.Errorf("prune expired provider sessions: %w", err)
	}
	c := &Cache{repository: repository, entries: map[key]entry{}, writes: make(chan write, 1024), ttl: 24 * time.Hour, maxEntries: 16384}
	rows, err := repository.ProviderSessions()
	if err != nil {
		return nil, fmt.Errorf("load provider sessions: %w", err)
	}
	c.preload(rows, time.Now())
	return c, nil
}

func (*Cache) ID() string { return "session" }

func (c *Cache) Load(ctx context.Context, route kernel.Route, sessionID string) (kernel.SessionState, bool, error) {
	if err := ctx.Err(); err != nil {
		return kernel.SessionState{}, false, err
	}
	k, err := makeKey(route, sessionID)
	if err != nil {
		return kernel.SessionState{}, false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	item, ok := c.entries[k]
	if ok && !item.state.ExpiresAt.IsZero() && !item.state.ExpiresAt.After(time.Now()) {
		delete(c.entries, k)
		return kernel.SessionState{}, false, nil
	}
	if ok && item.state.ResponseID == "" && len(item.state.ProviderData) == 0 && len(item.state.Signatures) == 0 {
		// Keep an in-flight invalidation marker for its persistence callback,
		// but never expose a clear marker as usable provider continuity.
		return kernel.SessionState{}, false, nil
	}
	return clone(item.state), ok, nil
}

func (c *Cache) Save(ctx context.Context, route kernel.Route, sessionID string, state kernel.SessionState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	k, err := makeKey(route, sessionID)
	if err != nil {
		return err
	}
	if err := kernel.ValidateSessionState(state); err != nil {
		return err
	}
	now := time.Now()
	cacheExpiry := now.Add(c.ttl)
	if !state.ExpiresAt.IsZero() && !state.ExpiresAt.After(now) {
		// Persist a valid clear marker through the same ordered writer. An
		// expired row would be rejected by the repository, leaving an older,
		// still-unexpired parent on disk to resurrect at the next startup.
		state = kernel.SessionState{ExpiresAt: cacheExpiry}
	}
	if state.ExpiresAt.IsZero() || state.ExpiresAt.After(cacheExpiry) {
		state.ExpiresAt = cacheExpiry
	}
	state = clone(state)
	c.mu.Lock()
	c.version++
	state.StoreVersion = c.version
	if _, exists := c.entries[k]; !exists && len(c.entries) >= c.maxEntries {
		c.evictLocked(now)
	}
	c.entries[k] = entry{state: state, updatedAt: now}
	item := store.ProviderSession{ConnectionID: k.connectionID, Namespace: k.namespace, SessionKey: k.sessionID, State: state, ExpiresAt: state.ExpiresAt.UTC().Format(time.RFC3339Nano), LastUsedAt: now.UTC().Format(time.RFC3339Nano)}
	select {
	case c.writes <- write{record: item}:
	default:
		c.dropped.Add(1)
		c.mu.Unlock()
		// The live update remains useful, but failed queue admission is not
		// an acknowledgement of asynchronous persistence.
		return fmt.Errorf("session write queue is full")
	}
	c.mu.Unlock()
	return nil
}

// Invalidate removes a session only when its current value still matches expected,
// and waits until the tombstone has been persisted before acknowledging success.
func (c *Cache) Invalidate(ctx context.Context, route kernel.Route, sessionID string, expected kernel.SessionState) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	k, err := makeKey(route, sessionID)
	if err != nil {
		return false, err
	}
	now := time.Now()
	tombstone := kernel.SessionState{ExpiresAt: now.Add(c.ttl)}
	record := store.ProviderSession{ConnectionID: k.connectionID, Namespace: k.namespace, SessionKey: k.sessionID, State: tombstone, ExpiresAt: tombstone.ExpiresAt.UTC().Format(time.RFC3339Nano), LastUsedAt: now.UTC().Format(time.RFC3339Nano)}
	done := make(chan error, 1)
	c.mu.Lock()
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return false, err
	}
	current, ok := c.entries[k]
	if !ok || !reflect.DeepEqual(current.state, expected) {
		c.mu.Unlock()
		return false, nil
	}
	c.version++
	tombstone.StoreVersion = c.version
	record.State = tombstone
	marker := entry{state: tombstone, updatedAt: now}
	completed := func(err error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		if latest, exists := c.entries[k]; exists && reflect.DeepEqual(latest, marker) {
			if err != nil {
				c.entries[k] = current
			} else {
				delete(c.entries, k)
			}
		}
	}
	select {
	case c.writes <- write{record: record, done: done, completed: completed}:
		c.entries[k] = marker
	default:
		c.mu.Unlock()
		return false, fmt.Errorf("session write queue is full")
	}
	c.mu.Unlock()
	select {
	case err := <-done:
		return err == nil, err
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

func (c *Cache) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			for {
				select {
				case item := <-c.writes:
					item.ack(c.repository.SaveProviderSession(item.record))
				default:
					return
				}
			}
		case item := <-c.writes:
			item.ack(c.repository.SaveProviderSession(item.record))
		}
	}
}

func (item write) ack(err error) {
	if item.completed != nil {
		item.completed(err)
	}
	if item.done != nil {
		item.done <- err
	}
}

func (c *Cache) DroppedWrites() uint64 { return c.dropped.Load() }

func (c *Cache) evictLocked(now time.Time) {
	var oldestKey key
	var oldestTime time.Time
	for candidate, item := range c.entries {
		if !item.state.ExpiresAt.IsZero() && !item.state.ExpiresAt.After(now) {
			delete(c.entries, candidate)
			continue
		}
		if oldestTime.IsZero() || item.updatedAt.Before(oldestTime) {
			oldestKey, oldestTime = candidate, item.updatedAt
		}
	}
	if len(c.entries) >= c.maxEntries && !oldestTime.IsZero() {
		delete(c.entries, oldestKey)
	}
}

func makeKey(route kernel.Route, sessionID string) (key, error) {
	if route.CredentialID == "" || route.ExternalModel == "" || sessionID == "" {
		return key{}, fmt.Errorf("connection, physical source model and session IDs are required")
	}
	storeKey, err := kernel.SessionStoreKeyFor(route, sessionID)
	if err != nil {
		return key{}, err
	}
	namespace, err := kernel.EncodeSessionStoreNamespace(route)
	if err != nil {
		return key{}, fmt.Errorf("encode provider session namespace: %w", err)
	}
	return key{connectionID: storeKey.ConnectionID, namespace: namespace, sessionID: storeKey.ClientSessionID}, nil
}

func clone(state kernel.SessionState) kernel.SessionState {
	state.ProviderData = append(json.RawMessage(nil), state.ProviderData...)
	state.Signatures = append([]kernel.SignatureProof(nil), state.Signatures...)
	return state
}
