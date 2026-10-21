package kernel

import (
	"context"
	"fmt"
	"sync"
)

// Provider continuation is a private decoder side channel, never a client
// event or a response transform. Collection closes with Decode's lifetime.
type sessionUpdates struct {
	mu                     sync.Mutex
	ctx                    context.Context
	closed, changed, reset bool
	state                  SessionState
	err                    error
}

func emptySessionState(s SessionState) bool {
	return s.ResponseID == "" && len(s.ProviderData) == 0 && len(s.Signatures) == 0 && s.ExpiresAt.IsZero()
}

func mergeSessionState(target *SessionState, update SessionState) {
	if update.ResponseID != "" {
		target.ResponseID = update.ResponseID
	}
	if len(update.ProviderData) > 0 {
		target.ProviderData = append([]byte(nil), update.ProviderData...)
	}
	if len(update.Signatures) > 0 {
		target.Signatures = append([]SignatureProof(nil), update.Signatures...)
	}
	if !update.ExpiresAt.IsZero() {
		target.ExpiresAt = update.ExpiresAt
	}
}

func (u *sessionUpdates) capture(state SessionState) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed || u.err != nil {
		return
	}
	if err := u.ctx.Err(); err != nil {
		u.err = err
		return
	}
	if ValidateSessionState(state) != nil {
		u.err = fmt.Errorf("provider emitted invalid private session state")
		return
	}
	u.changed = true
	if emptySessionState(state) {
		u.state = SessionState{}
		u.reset = true
		return
	}
	mergeSessionState(&u.state, state)
}

func (u *sessionUpdates) finish(publish func(SessionState), accept bool) error {
	u.mu.Lock()
	u.closed = true
	state, changed, reset, err := cloneSessionState(u.state), u.changed, u.reset, u.err
	u.mu.Unlock()
	if err != nil {
		return err
	}
	if accept && changed && publish != nil {
		if reset {
			publish(SessionState{})
		}
		if !emptySessionState(state) {
			publish(state)
		}
	}
	return nil
}
