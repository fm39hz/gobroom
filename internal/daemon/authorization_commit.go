package daemon

import (
	"context"
	"fmt"
	"time"

	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/store"
)

// Commit and cancellation share a linearization point. A driver returning a
// token after cancellation does not grant permission to persist that token.
func (d *Daemon) commitAuthorizationCredential(ctx context.Context, sessions *authorizationSessions, sessionID string, state provider.CredentialState) (CredentialActivationView, error) {
	sessions.mu.Lock()
	session, exists := sessions.byID[sessionID]
	sessions.mu.Unlock()
	if !exists {
		return CredentialActivationView{}, fmt.Errorf("authorization session is no longer active: %w", context.Canceled)
	}
	flow := session.authFlow
	if flow == nil {
		return CredentialActivationView{}, fmt.Errorf("authorization auth primitive is unavailable")
	}
	if _, err := d.resolveReplacementCredential(ctx, flow, session.expectedCredential, state); err != nil {
		return CredentialActivationView{}, err
	}
	unlock, err := d.lockCredentialRefresh(ctx, session.connectionID)
	if err != nil {
		return CredentialActivationView{}, err
	}
	defer unlock()
	sessions.mu.Lock()
	current, exists := sessions.byID[sessionID]
	if sessions.closed || !exists || current.status != "awaiting_user" {
		sessions.mu.Unlock()
		return CredentialActivationView{}, fmt.Errorf("authorization session is no longer active: %w", context.Canceled)
	}
	if !current.expiresAt.After(time.Now()) {
		sessions.mu.Unlock()
		return CredentialActivationView{}, context.DeadlineExceeded
	}
	if err := ctx.Err(); err != nil {
		sessions.mu.Unlock()
		return CredentialActivationView{}, err
	}
	// AfterFunc cancellation propagation may still be scheduled; check the
	// daemon root directly at the commit boundary as well.
	if d.runCtx != nil && d.runCtx.Err() != nil {
		sessions.mu.Unlock()
		return CredentialActivationView{}, d.runCtx.Err()
	}
	commit, err := d.store.CompareAndSwapConnectionCredential(ctx, current.expectedCredential, state.Encoded(), store.PrincipalReplacement)
	if err != nil {
		sessions.mu.Unlock()
		return CredentialActivationView{}, err
	}
	current.activationCommit = commit
	callbackKey := current.callbackKey
	current.callbackKey = ""
	if current.timer != nil {
		current.timer.Stop()
		current.timer = nil
	}
	current.status = "completed"
	current.expiresAt = time.Now().Add(authorizationSessionRetention)
	current.expectedCredential = store.Credential{}
	current.authFlow = nil
	clearSetupSession(&current)
	sessions.byID[sessionID] = current
	if sessions.byConnection[current.connectionID] == sessionID {
		delete(sessions.byConnection, current.connectionID)
	}
	sessions.mu.Unlock()
	d.releaseAuthorizationCallbacks(callbackKey)
	view, activationErr := d.activateCredentialLocked(d.activationContext(), commit)
	if activationErr != nil {
		d.appendLog("warn", "credential committed; activation progress requires reconciliation")
		view = d.credentialActivationView(d.activationContext(), commit)
	}
	d.signalCredentialActivation()
	return view, nil
}
