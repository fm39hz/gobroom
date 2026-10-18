package daemon

import (
	"context"
	"errors"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

const (
	authorizationStartTimeout     = 20 * time.Second
	authorizationSessionRetention = 5 * time.Minute
)

func classifyAuthorizationError(ctx context.Context, err error) (string, string) {
	if errors.Is(err, context.Canceled) || (ctx != nil && errors.Is(ctx.Err(), context.Canceled)) {
		return "cancelled", "authorization was cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) || (ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded)) {
		return "expired", "authorization expired before approval"
	}
	var retrieveError *oauth2.RetrieveError
	if errors.As(err, &retrieveError) && retrieveError.ErrorCode != "" {
		return "failed", "provider rejected authorization: " + strings.ToLower(retrieveError.ErrorCode)
	}
	return "failed", "authorization failed; inspect provider availability and configuration"
}

func (d *Daemon) cancelAuthorizationSessions() {
	sessions := d.authorizationSessions()
	sessions.mu.Lock()
	sessions.closed = true
	var callbackOwners []string
	for _, session := range sessions.byID {
		if session.callbackKey != "" {
			callbackOwners = append(callbackOwners, session.callbackKey)
		}
		if session.setupCallDone != nil {
			close(session.setupCallDone)
		}
		if session.setupPollTimer != nil {
			session.setupPollTimer.Stop()
		}
		if session.timer != nil {
			session.timer.Stop()
		}
		if session.cancel != nil {
			session.cancel()
		}
	}
	sessions.byID = map[string]authorizationSession{}
	sessions.byConnection = map[string]string{}
	sessions.inFlight = map[string]bool{}
	sessions.mu.Unlock()
	for _, owner := range callbackOwners {
		d.releaseAuthorizationCallbacks(owner)
	}
}
