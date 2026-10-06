package sessionstore

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/store"
)

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

type sessionNamespace struct {
	DefinitionID string `json:"definitionId"`
	ModelID      string `json:"modelId"`
}

type entry struct {
	state     kernel.SessionState
	updatedAt time.Time
}

// Cache provides synchronous in-memory session reads/writes and bounded
// asynchronous persistence. SQLite is only touched during preload and from
// the worker, never while the data plane is choosing or preparing a route.
type Cache struct {
	repository Repository
	mu         sync.Mutex
	entries    map[key]entry
	writes     chan store.ProviderSession
	ttl        time.Duration
	maxEntries int
	dropped    atomic.Uint64
}

func New(repository Repository) (*Cache, error) {
	if repository == nil {
		return nil, fmt.Errorf("session repository is required")
	}
	if _, err := repository.PruneExpiredProviderSessions(time.Now()); err != nil {
		return nil, fmt.Errorf("prune expired provider sessions: %w", err)
	}
	c := &Cache{repository: repository, entries: map[key]entry{}, writes: make(chan store.ProviderSession, 1024), ttl: 24 * time.Hour, maxEntries: 16384}
	rows, err := repository.ProviderSessions()
	if err != nil {
		return nil, fmt.Errorf("load provider sessions: %w", err)
	}
	now := time.Now()
	for _, row := range rows {
		if row.ConnectionID == "" || row.Namespace == "" || row.SessionKey == "" {
			continue
		}
		if !row.State.ExpiresAt.IsZero() && !row.State.ExpiresAt.After(now) {
			continue
		}
		c.entries[key{connectionID: row.ConnectionID, namespace: row.Namespace, sessionID: row.SessionKey}] = entry{state: clone(row.State), updatedAt: now}
	}
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
	if len(state.ResponseID) > 4096 {
		return fmt.Errorf("provider response ID exceeds 4 KiB")
	}
	if len(state.ProviderData) > 64*1024 || len(state.ProviderData) > 0 && !json.Valid(state.ProviderData) {
		return fmt.Errorf("provider session data must be valid JSON no larger than 64 KiB")
	}
	now := time.Now()
	state.ExpiresAt = now.Add(c.ttl)
	state = clone(state)
	c.mu.Lock()
	if _, exists := c.entries[k]; !exists && len(c.entries) >= c.maxEntries {
		c.evictLocked(now)
	}
	c.entries[k] = entry{state: state, updatedAt: now}
	c.mu.Unlock()
	item := store.ProviderSession{ConnectionID: k.connectionID, Namespace: k.namespace, SessionKey: k.sessionID, State: state, ExpiresAt: state.ExpiresAt.UTC().Format(time.RFC3339Nano), LastUsedAt: now.UTC().Format(time.RFC3339Nano)}
	select {
	case c.writes <- item:
	default:
		c.dropped.Add(1)
	}
	return nil
}

func (c *Cache) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			for {
				select {
				case item := <-c.writes:
					_ = c.repository.SaveProviderSession(item)
				default:
					return
				}
			}
		case item := <-c.writes:
			_ = c.repository.SaveProviderSession(item)
		}
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
	if route.CredentialID == "" || route.DefinitionID == "" || route.ExternalModel == "" || sessionID == "" {
		return key{}, fmt.Errorf("connection, provider definition, physical source model and session IDs are required")
	}
	namespace, err := json.Marshal(sessionNamespace{DefinitionID: route.DefinitionID, ModelID: route.ExternalModel})
	if err != nil {
		return key{}, fmt.Errorf("encode provider session namespace: %w", err)
	}
	return key{connectionID: route.CredentialID, namespace: string(namespace), sessionID: sessionID}, nil
}

func clone(state kernel.SessionState) kernel.SessionState {
	state.ProviderData = append(json.RawMessage(nil), state.ProviderData...)
	return state
}
