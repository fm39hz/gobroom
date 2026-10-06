package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

type sessionStoreKey struct {
	connectionID string
	namespace    string
	modelID      string
	sessionID    string
}

// MemorySessionStore is the default ephemeral implementation. Daemons can
// replace this primitive with a bounded durable cache before building routes.
type MemorySessionStore struct {
	mu      sync.Mutex
	entries map[sessionStoreKey]SessionState
	TTL     time.Duration
}

func NewMemorySessionStore() *MemorySessionStore {
	return &MemorySessionStore{entries: map[sessionStoreKey]SessionState{}, TTL: 24 * time.Hour}
}

func (*MemorySessionStore) ID() string { return "session" }

func (s *MemorySessionStore) Load(ctx context.Context, route Route, sessionID string) (SessionState, bool, error) {
	if err := ctx.Err(); err != nil {
		return SessionState{}, false, err
	}
	key, err := makeSessionStoreKey(route, sessionID)
	if err != nil {
		return SessionState{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.entries[key]
	if ok && !state.ExpiresAt.IsZero() && !state.ExpiresAt.After(time.Now()) {
		delete(s.entries, key)
		return SessionState{}, false, nil
	}
	return cloneSessionState(state), ok, nil
}

func (s *MemorySessionStore) Save(ctx context.Context, route Route, sessionID string, state SessionState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := makeSessionStoreKey(route, sessionID)
	if err != nil {
		return err
	}
	if len(state.ResponseID) > 4096 {
		return fmt.Errorf("provider response ID exceeds 4 KiB")
	}
	if len(state.ProviderData) > 64*1024 || len(state.ProviderData) > 0 && !json.Valid(state.ProviderData) {
		return fmt.Errorf("provider session state must be valid JSON no larger than 64 KiB")
	}
	if state.ExpiresAt.IsZero() {
		ttl := s.TTL
		if ttl <= 0 {
			ttl = 24 * time.Hour
		}
		state.ExpiresAt = time.Now().Add(ttl)
	}
	s.mu.Lock()
	s.entries[key] = cloneSessionState(state)
	s.mu.Unlock()
	return nil
}

func makeSessionStoreKey(route Route, sessionID string) (sessionStoreKey, error) {
	if route.CredentialID == "" || route.DefinitionID == "" || route.ExternalModel == "" || sessionID == "" {
		return sessionStoreKey{}, fmt.Errorf("connection, provider definition, physical source model and session IDs are required")
	}
	return sessionStoreKey{connectionID: route.CredentialID, namespace: route.DefinitionID, modelID: route.ExternalModel, sessionID: sessionID}, nil
}

func cloneSessionState(state SessionState) SessionState {
	state.ProviderData = append(json.RawMessage(nil), state.ProviderData...)
	return state
}
