package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// SessionStoreKey is the complete isolation boundary for opaque provider state.
// Custom session stores should use this scope rather than keying by client
// session ID alone.
type SessionStoreKey struct {
	ConnectionID          string
	ProviderDefinitionRef string
	OperationRef          string
	PhysicalModelID       string
	ClientSessionID       string
}

// MemorySessionStore is the default ephemeral implementation. Daemons can
// replace this primitive with a bounded durable cache before building routes.
type MemorySessionStore struct {
	mu      sync.Mutex
	entries map[SessionStoreKey]SessionState
	TTL     time.Duration
}

func NewMemorySessionStore() *MemorySessionStore {
	return &MemorySessionStore{entries: map[SessionStoreKey]SessionState{}, TTL: 24 * time.Hour}
}

func (*MemorySessionStore) ID() string { return "session" }

func (s *MemorySessionStore) Load(ctx context.Context, route Route, sessionID string) (SessionState, bool, error) {
	if err := ctx.Err(); err != nil {
		return SessionState{}, false, err
	}
	key, err := SessionStoreKeyFor(route, sessionID)
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
	key, err := SessionStoreKeyFor(route, sessionID)
	if err != nil {
		return err
	}
	if len(state.ResponseID) > 4096 {
		return fmt.Errorf("provider response ID exceeds 4 KiB")
	}
	if len(state.ProviderData) > 64*1024 || len(state.ProviderData) > 0 && !json.Valid(state.ProviderData) {
		return fmt.Errorf("provider session state must be valid JSON no larger than 64 KiB")
	}
	if len(state.Signatures) > MaxProviderSignatureProofs {
		return ErrSignatureProofLimit
	}
	for _, proof := range state.Signatures {
		if err := validateSignatureProof(proof); err != nil {
			return err
		}
	}
	ttl := s.TTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	cacheExpiry := time.Now().Add(ttl)
	if state.ExpiresAt.IsZero() || state.ExpiresAt.After(cacheExpiry) {
		state.ExpiresAt = cacheExpiry
	}
	s.mu.Lock()
	s.entries[key] = cloneSessionState(state)
	s.mu.Unlock()
	return nil
}

func SessionStoreKeyFor(route Route, sessionID string) (SessionStoreKey, error) {
	if route.CredentialID == "" || route.ExternalModel == "" || sessionID == "" {
		return SessionStoreKey{}, fmt.Errorf("connection, physical source model and session IDs are required")
	}
	if err := route.DefinitionRef.Validate(); err != nil {
		return SessionStoreKey{}, fmt.Errorf("provider session requires an exact provider definition reference: %w", err)
	}
	if route.DefinitionRef.Kind != "provider-definition" {
		return SessionStoreKey{}, fmt.Errorf("provider session requires a provider-definition reference")
	}
	if err := route.SessionOperationRef.Validate(); err != nil || route.SessionOperationRef.Kind != "operation" {
		return SessionStoreKey{}, fmt.Errorf("provider session requires an exact operation reference")
	}
	return SessionStoreKey{ConnectionID: route.CredentialID, ProviderDefinitionRef: route.DefinitionRef.Key(), OperationRef: route.SessionOperationRef.Key(), PhysicalModelID: route.ExternalModel, ClientSessionID: sessionID}, nil
}

func cloneSessionState(state SessionState) SessionState {
	state.ProviderData = append(json.RawMessage(nil), state.ProviderData...)
	state.Signatures = append([]SignatureProof(nil), state.Signatures...)
	return state
}
