package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
)

const MaxCredentialLeaseDuration = time.Hour

var (
	errInvalidCredentialLease = errors.New("invalid credential lease")
	errCredentialLeaseScope   = errors.New("credential lease scope mismatch")
	errCredentialLeaseClosed  = errors.New("credential lease is released or expired")
)

// CredentialLeaseScope binds runtime credential material to one exact route
// and operation. Generation here identifies the stable principal; credential
// revision is intentionally kept private in CredentialLease.
type CredentialLeaseScope struct {
	RouteID             string
	ProviderNodeID      string
	ConnectionID        string
	CredentialType      string
	AuthFlowID          string
	BaseURL             string
	ModelRef            string
	PrincipalGeneration string
	DefinitionRef       extensions.Ref
	Protocol            Protocol
	OperationRef        extensions.Ref
}

func CredentialLeaseScopeForRoute(route Route) CredentialLeaseScope {
	return CredentialLeaseScope{
		RouteID: route.ID, ProviderNodeID: route.NodeID, ConnectionID: route.CredentialID,
		CredentialType: route.CredentialType, AuthFlowID: route.AuthFlowID, BaseURL: route.BaseURL,
		ModelRef: route.ExternalModel, PrincipalGeneration: route.HealthGeneration.Connection,
		DefinitionRef: route.DefinitionRef, Protocol: route.Protocol, OperationRef: route.SessionOperationRef,
	}
}

func (s CredentialLeaseScope) validate() error {
	if s.RouteID == "" || s.OperationRef.Kind != "operation" || s.OperationRef.Validate() != nil {
		return fmt.Errorf("%w: route ID and valid operation reference are required", errInvalidCredentialLease)
	}
	return nil
}

func (CredentialLeaseScope) String() string     { return "CredentialLeaseScope{<redacted>}" }
func (s CredentialLeaseScope) GoString() string { return s.String() }
func (CredentialLeaseScope) MarshalJSON() ([]byte, error) {
	return []byte(`"<redacted credential lease scope>"`), nil
}

// CredentialLease deliberately has no exported fields. A borrower receives a
// copy of the credential and cannot extend this lease by retaining that copy.
type CredentialLease struct {
	mu         sync.Mutex
	scope      CredentialLeaseScope
	credential Credential
	expiresAt  time.Time
	check      func(context.Context) error
	release    func()
	released   bool
	generation string
}

func NewCredentialLease(scope CredentialLeaseScope, credential Credential, expiresAt time.Time, check func(context.Context) error, release func()) (*CredentialLease, error) {
	if err := scope.validate(); err != nil {
		return nil, err
	}
	if credential.ConnectionID != "" && scope.ConnectionID != "" && credential.ConnectionID != scope.ConnectionID {
		return nil, fmt.Errorf("%w: credential connection does not match scope", errInvalidCredentialLease)
	}
	if credential.Type != "" && scope.CredentialType != "" && credential.Type != scope.CredentialType {
		return nil, fmt.Errorf("%w: credential type does not match scope", errInvalidCredentialLease)
	}
	if err := credential.ValidateRuntimeContext(); err != nil {
		return nil, fmt.Errorf("%w: %v", errInvalidCredentialLease, err)
	}
	now := time.Now()
	if !expiresAt.After(now) || expiresAt.After(now.Add(MaxCredentialLeaseDuration)) {
		return nil, fmt.Errorf("%w: expiry must be in the future and within %s", errInvalidCredentialLease, MaxCredentialLeaseDuration)
	}
	if !credential.ExpiresAt.IsZero() {
		if !credential.ExpiresAt.After(now) {
			return nil, fmt.Errorf("%w: credential is expired", errInvalidCredentialLease)
		}
		if credential.ExpiresAt.Before(expiresAt) {
			expiresAt = credential.ExpiresAt
		}
	}
	return &CredentialLease{scope: scope, credential: cloneLeaseCredential(credential), expiresAt: expiresAt, check: check, release: release, generation: credential.Generation}, nil
}

func cloneLeaseCredential(c Credential) Credential {
	c.RequestHeaders = c.RequestHeaders.Clone()
	if c.AuthContext != nil {
		copyContext := make(AuthContext, len(c.AuthContext))
		for k, v := range c.AuthContext {
			copyContext[k] = v
		}
		c.AuthContext = copyContext
	}
	return c
}

func (l *CredentialLease) local(ctx context.Context, scope CredentialLeaseScope, allowExpired bool) (Credential, error) {
	if l == nil {
		return Credential{}, errCredentialLeaseClosed
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.checkLocked(ctx, scope, allowExpired); err != nil {
		return Credential{}, err
	}
	return cloneLeaseCredential(l.credential), nil
}

// Caller owns mu. Dispatch validation must not allocate borrowed copies of
// headers/context just to check authority.
func (l *CredentialLease) checkLocked(ctx context.Context, scope CredentialLeaseScope, allowExpired bool) error {
	if ctx == nil {
		return errors.New("credential lease requires a context")
	}
	if err := scope.validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if l.released {
		return errCredentialLeaseClosed
	}
	if l.scope != scope {
		return errCredentialLeaseScope
	}
	if !allowExpired && !time.Now().Before(l.expiresAt) {
		return errCredentialLeaseClosed
	}
	return nil
}

func (l *CredentialLease) validateLocal(ctx context.Context, scope CredentialLeaseScope) error {
	if l == nil {
		return errCredentialLeaseClosed
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.checkLocked(ctx, scope, false)
}

func (l *CredentialLease) Borrow(ctx context.Context, scope CredentialLeaseScope) (Credential, error) {
	return l.local(ctx, scope, false)
}
func (l *CredentialLease) MaterialForRefresh(ctx context.Context, scope CredentialLeaseScope) (Credential, error) {
	return l.local(ctx, scope, true)
}
func (l *CredentialLease) ValidateForDispatch(ctx context.Context, scope CredentialLeaseScope) error {
	if err := l.validateLocal(ctx, scope); err != nil {
		return err
	}
	if l.check != nil {
		if err := l.check(ctx); err != nil {
			return err
		}
	}
	return l.validateLocal(ctx, scope)
}
func (l *CredentialLease) Release() {
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.released {
		l.mu.Unlock()
		return
	}
	l.released = true
	// Strings are immutable in Go: drop owned references, do not claim to wipe
	// the backing memory (which may still be held by a borrowed copy).
	l.credential = Credential{}
	release := l.release
	l.release = nil
	l.mu.Unlock()
	if release != nil {
		release()
	}
}
func (l *CredentialLease) Generation() string {
	if l == nil {
		return ""
	}
	return l.generation
}
func (l *CredentialLease) ExpiresAt() time.Time {
	if l == nil {
		return time.Time{}
	}
	return l.expiresAt
}
func (l *CredentialLease) Scope() CredentialLeaseScope {
	if l == nil {
		return CredentialLeaseScope{}
	}
	return l.scope
}
func (*CredentialLease) String() string     { return "CredentialLease{<redacted>}" }
func (l *CredentialLease) GoString() string { return l.String() }
func (*CredentialLease) MarshalJSON() ([]byte, error) {
	return json.Marshal("<redacted credential lease>")
}
