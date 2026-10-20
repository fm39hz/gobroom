package daemon

import (
	"context"
	"fmt"
	"time"

	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/store"
)

type AuthorizationSetupStartMode string

const (
	AuthorizationSetupNew    AuthorizationSetupStartMode = "new"
	AuthorizationSetupResume AuthorizationSetupStartMode = "resume"
)

type AuthorizationSetupStartInput struct {
	ConnectionID string                      `json:"connectionID"`
	Mode         AuthorizationSetupStartMode `json:"mode,omitempty"`
}

// Reattachment observes the existing owner, never transfers ownership of the
// running provider call to the resuming frontend. Background poll/await work
// leaves its published instructions immediately observable.
func (d *Daemon) resumeSetupAuthorization(ctx context.Context, id string, expected store.Credential) (AuthorizationSetupView, error) {
	sessions := d.authorizationSessions()
	for {
		if err := d.credentialContextError(ctx); err != nil {
			return AuthorizationSetupView{}, err
		}
		d.pruneAuthorizationSessions(sessions, time.Now())
		sessions.mu.Lock()
		session, exists := sessions.byID[id]
		if sessions.closed || !exists {
			sessions.mu.Unlock()
			return AuthorizationSetupView{}, fmt.Errorf("authorization setup session is unavailable")
		}
		active := session.status == "starting" || session.status == "awaiting_user"
		committed := session.activationCommit.ConnectionID != ""
		if active && !committed && (session.expectedCredential.Owner != expected.Owner || session.expectedCredential.Revision != expected.Revision || session.expectedCredential.PrincipalGeneration != expected.PrincipalGeneration || session.expectedCredential.Type != expected.Type) {
			sessions.mu.Unlock()
			d.finishSetupAuthorization(id, "failed", "connection changed")
			return AuthorizationSetupView{}, fmt.Errorf("authorization setup connection binding changed")
		}
		wait := active && !committed && sessions.inFlight[id] && (session.setupRevision == 0 || session.setupCallKind == provider.AuthorizationSubmit)
		done := session.setupCallDone
		sessions.mu.Unlock()
		if wait {
			if done == nil {
				return AuthorizationSetupView{}, fmt.Errorf("authorization setup step is unavailable")
			}
			select {
			case <-ctx.Done():
				return AuthorizationSetupView{}, ctx.Err()
			case <-done:
				continue
			}
		}
		if active && !committed {
			live, err := d.store.ConnectionCredentialBinding(ctx, session.connectionID)
			if err != nil || live.Owner != expected.Owner || live.Revision != expected.Revision || live.PrincipalGeneration != expected.PrincipalGeneration || live.Type != expected.Type {
				d.finishSetupAuthorization(id, "failed", "connection changed")
				return AuthorizationSetupView{}, fmt.Errorf("authorization setup connection binding changed")
			}
		}
		if err := d.credentialContextError(ctx); err != nil {
			return AuthorizationSetupView{}, err
		}
		return d.setupAuthorizationStatus(id)
	}
}
