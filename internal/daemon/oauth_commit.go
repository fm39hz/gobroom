package daemon

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/store"
)

// Commit and cancellation share a linearization point. A driver returning a
// token after cancellation does not grant permission to persist that token.
func (d *Daemon) commitAuthorizationCredential(ctx context.Context, sessions *authorizationSessions, sessionID string, state provider.CredentialState) error {
	sessions.mu.Lock()
	session, exists := sessions.byID[sessionID]
	sessions.mu.Unlock()
	if !exists {
		return fmt.Errorf("authorization session is no longer active: %w", context.Canceled)
	}
	flow := session.authFlow
	if flow == nil {
		return fmt.Errorf("authorization auth primitive is unavailable")
	}
	if _, err := d.resolveReplacementCredential(ctx, flow, session.expectedCredential, state); err != nil {
		return err
	}
	unlock, err := d.lockCredentialRefresh(ctx, session.connectionID)
	if err != nil {
		return err
	}
	defer unlock()
	sessions.mu.Lock()
	current, exists := sessions.byID[sessionID]
	if sessions.closed || !exists || current.status != "awaiting_user" {
		sessions.mu.Unlock()
		return fmt.Errorf("authorization session is no longer active: %w", context.Canceled)
	}
	if !current.expiresAt.After(time.Now()) {
		sessions.mu.Unlock()
		return context.DeadlineExceeded
	}
	if err := ctx.Err(); err != nil {
		sessions.mu.Unlock()
		return err
	}
	// AfterFunc cancellation propagation may still be scheduled; check the
	// daemon root directly at the commit boundary as well.
	if d.runCtx != nil && d.runCtx.Err() != nil {
		sessions.mu.Unlock()
		return d.runCtx.Err()
	}
	if err := d.store.CompareAndSwapConnectionCredential(ctx, current.expectedCredential, state.Encoded()); err != nil {
		sessions.mu.Unlock()
		return err
	}
	callbackKey := current.callbackKey
	current.callbackKey = ""
	if current.timer != nil {
		current.timer.Stop()
		current.timer = nil
	}
	current.status = "completed"
	current.expiresAt = time.Now().Add(authorizationSessionRetention)
	current.expectedCredential = store.Credential{}
	current.verifier = ""
	current.device.DeviceCode = ""
	current.deviceFlow = nil
	current.authFlow = nil
	sessions.byID[sessionID] = current
	if sessions.byConnection[current.connectionID] == sessionID {
		delete(sessions.byConnection, current.connectionID)
	}
	sessions.mu.Unlock()
	d.releaseOAuthCallbackListener(callbackKey)
	return nil
}

func authorizationCommitFailure(ctx context.Context, err error) (string, string) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return classifyAuthorizationError(ctx, err)
	}
	return "failed", "credential could not be applied; the connection may have changed or been removed"
}
