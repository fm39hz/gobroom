package daemon

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"

	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/store"
)

const (
	authorizationStartTimeout     = 20 * time.Second
	authorizationSessionRetention = 5 * time.Minute
	authorizationStepTimeout      = 30 * time.Second
	maxAuthorizationSessions      = 64
)

type authorizationSession struct {
	connectionID                  string
	authFlow                      provider.AuthFlow
	expectedCredential            store.Credential
	activationCommit              store.CredentialCommit
	expiresAt                     time.Time
	status, statusError           string
	cancel                        context.CancelFunc
	timer                         *time.Timer
	setupFlow                     provider.AuthorizationFlow
	setupState                    provider.AuthorizationState
	setupActions                  []provider.AuthorizationAction
	setupRevision                 uint64
	setupPollTimer                *time.Timer
	setupPollAfter                time.Duration
	setupAwait, setupAwaitStarted bool
	setupCallbackModes            []AuthorizationCallbackView
	setupCallDone                 chan struct{}
	setupCallKind                 provider.AuthorizationEventKind
	callbackKey                   string
}

type authorizationSessions struct {
	mu           sync.Mutex
	closed       bool
	byID         map[string]authorizationSession
	byConnection map[string]string
	inFlight     map[string]bool
}

func (d *Daemon) authorizationSessions() *authorizationSessions {
	d.authorizationMu.Lock()
	defer d.authorizationMu.Unlock()
	if d.authorizations == nil {
		d.authorizations = &authorizationSessions{byID: map[string]authorizationSession{}, byConnection: map[string]string{}, inFlight: map[string]bool{}}
	}
	return d.authorizations
}

func (s *authorizationSessions) pruneLocked(now time.Time) []string {
	var released []string
	for id, session := range s.byID {
		if session.expiresAt.After(now) || s.inFlight[id] {
			continue
		}
		if session.timer != nil {
			session.timer.Stop()
		}
		if session.cancel != nil {
			session.cancel()
		}
		if session.callbackKey != "" {
			released = append(released, session.callbackKey)
			session.callbackKey = ""
		}
		if session.status == "starting" || session.status == "awaiting_user" {
			clearSetupSession(&session)
			session.status, session.statusError = "expired", "authorization expired"
			session.expiresAt = now.Add(authorizationSessionRetention)
			s.byID[id] = session
		} else {
			delete(s.byID, id)
		}
		if s.byConnection[session.connectionID] == id {
			delete(s.byConnection, session.connectionID)
		}
	}
	return released
}

func (d *Daemon) pruneAuthorizationSessions(sessions *authorizationSessions, now time.Time) {
	sessions.mu.Lock()
	owners := sessions.pruneLocked(now)
	sessions.mu.Unlock()
	for _, owner := range owners {
		d.releaseAuthorizationCallbacks(owner)
	}
}

func randomURLToken(size int) (string, error) {
	bytes := make([]byte, size)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
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
