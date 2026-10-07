package daemon

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/fm39hz/gobroom/internal/provider"
)

const (
	oauthSessionTTL          = 10 * time.Minute
	oauthExchangeTimeout     = 30 * time.Second
	maxAuthorizationSessions = 64
	maxOAuthCallbackURLBytes = 8192
	maxOAuthCodeBytes        = 4096
)

type authorizationSession struct {
	kind           string
	connectionID   string
	flow           provider.AuthorizationCodeFlow
	state          string
	verifier       string
	redirectURL    string
	expectedSecret string
	expiresAt      time.Time
	deviceFlow     provider.DeviceAuthorizationFlow
	device         provider.DeviceAuthorization
	status         string
	statusError    string
	cancel         context.CancelFunc
}

type authorizationChallenge struct {
	SessionID        string    `json:"sessionId"`
	AuthorizationURL string    `json:"authorizationUrl"`
	ExpiresAt        time.Time `json:"expiresAt"`
}

type authorizationSessions struct {
	mu           sync.Mutex
	byID         map[string]authorizationSession
	byConnection map[string]string
	inFlight     map[string]bool
}

func (d *Daemon) startAuthorization(connectionID string) (authorizationChallenge, error) {
	connection, ok := d.store.ConnectionCredentialByID(connectionID)
	if !ok {
		return authorizationChallenge{}, fmt.Errorf("enabled connection %q not found", connectionID)
	}
	node, err := d.store.ProviderNodeByConnection(connectionID)
	if err != nil {
		return authorizationChallenge{}, err
	}
	flowID := d.providerAuthFlowIDs[node.DefinitionID]
	flow, ok := d.authFlows[flowID]
	if flowID == "" || !ok {
		return authorizationChallenge{}, fmt.Errorf("auth flow for provider definition %q is unavailable", node.DefinitionID)
	}
	interactive, ok := flow.(provider.AuthorizationCodeFlow)
	if !ok {
		return authorizationChallenge{}, fmt.Errorf("auth flow %q does not support authorization-code setup", flowID)
	}
	state, err := randomURLToken(32)
	if err != nil {
		return authorizationChallenge{}, err
	}
	verifier, err := randomURLToken(32)
	if err != nil {
		return authorizationChallenge{}, err
	}
	authorizationURL, err := interactive.AuthorizationURL(state, verifier)
	if err != nil {
		return authorizationChallenge{}, err
	}
	parsed, err := url.Parse(authorizationURL)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return authorizationChallenge{}, fmt.Errorf("auth flow returned an invalid authorization URL")
	}
	redirectURL := parsed.Query().Get("redirect_uri")
	if redirectURL == "" {
		return authorizationChallenge{}, fmt.Errorf("auth flow authorization URL has no redirect_uri")
	}
	expiresAt := time.Now().Add(oauthSessionTTL)
	sessions := d.authorizationSessions()
	sessions.mu.Lock()
	sessions.pruneLocked(time.Now())
	if active := sessions.byConnection[connection.ID]; active != "" {
		sessions.mu.Unlock()
		return authorizationChallenge{}, fmt.Errorf("connection %q already has pending authorization session %q", connection.ID, active)
	}
	if len(sessions.byID) >= maxAuthorizationSessions {
		sessions.mu.Unlock()
		return authorizationChallenge{}, fmt.Errorf("maximum pending authorization sessions (%d) reached", maxAuthorizationSessions)
	}
	sessions.byID[state] = authorizationSession{kind: "authorization_code", connectionID: connection.ID, flow: interactive, state: state, verifier: verifier, redirectURL: redirectURL, expectedSecret: connection.Secret, expiresAt: expiresAt, status: "awaiting_user"}
	sessions.byConnection[connection.ID] = state
	sessions.mu.Unlock()
	return authorizationChallenge{SessionID: state, AuthorizationURL: authorizationURL, ExpiresAt: expiresAt}, nil
}

func (d *Daemon) completeAuthorization(ctx context.Context, sessionID, state, code, callbackURL string) (map[string]string, error) {
	if len(code) == 0 || len(code) > maxOAuthCodeBytes {
		return nil, fmt.Errorf("OAuth authorization code must be between 1 and %d bytes", maxOAuthCodeBytes)
	}
	if len(state) == 0 || len(state) > 256 || len(sessionID) == 0 || len(sessionID) > 256 {
		return nil, fmt.Errorf("OAuth session ID and state must be between 1 and 256 bytes")
	}
	sessions := d.authorizationSessions()
	sessions.mu.Lock()
	sessions.pruneLocked(time.Now())
	session, exists := sessions.byID[sessionID]
	if !exists || session.kind != "authorization_code" || session.state != state {
		sessions.mu.Unlock()
		return nil, fmt.Errorf("authorization session is missing, expired or has invalid state")
	}
	if callbackURL != "" && !sameOAuthCallbackTarget(session.redirectURL, callbackURL) {
		sessions.mu.Unlock()
		return nil, fmt.Errorf("OAuth callback URL does not match the redirect URI bound to this session")
	}
	if sessions.inFlight[sessionID] {
		sessions.mu.Unlock()
		return nil, fmt.Errorf("authorization session is already completing")
	}
	sessions.inFlight[sessionID] = true
	sessions.mu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}
	exchangeCtx, cancel := context.WithTimeout(ctx, oauthExchangeTimeout)
	credential, err := session.flow.ExchangeAuthorizationCode(exchangeCtx, code, session.verifier)
	cancel()
	if err != nil {
		sessions.mu.Lock()
		delete(sessions.inFlight, sessionID)
		sessions.mu.Unlock()
		return nil, err
	}
	credential.ConnectionID = session.connectionID
	encoded, err := provider.EncodeOAuthCredential(credential)
	if err != nil {
		sessions.mu.Lock()
		delete(sessions.byID, sessionID)
		delete(sessions.inFlight, sessionID)
		sessions.mu.Unlock()
		return nil, err
	}
	unlockCredential := d.lockCredentialRefresh(session.connectionID)
	if err := d.store.UpdateConnectionSecretIfUnchanged(session.connectionID, session.expectedSecret, encoded); err != nil {
		unlockCredential()
		sessions.mu.Lock()
		delete(sessions.inFlight, sessionID)
		sessions.mu.Unlock()
		return nil, fmt.Errorf("persist OAuth credential: %w", err)
	}
	unlockCredential()
	sessions.mu.Lock()
	delete(sessions.byID, sessionID)
	delete(sessions.byConnection, session.connectionID)
	delete(sessions.inFlight, sessionID)
	sessions.mu.Unlock()
	if err := d.server.Reload(); err != nil {
		return nil, fmt.Errorf("credential stored but serving snapshot reload failed: %w", err)
	}
	return map[string]string{"connectionId": session.connectionID, "status": "authorized"}, nil
}

func sameOAuthCallbackTarget(expected, actual string) bool {
	want, errWant := url.Parse(expected)
	got, errGot := url.Parse(actual)
	if errWant != nil || errGot != nil || want.Scheme != got.Scheme || want.Host != got.Host || want.EscapedPath() != got.EscapedPath() || got.User != nil {
		return false
	}
	expectedQuery, actualQuery := want.Query(), got.Query()
	for key, values := range expectedQuery {
		if key == "code" || key == "state" || !equalStringSlices(values, actualQuery[key]) {
			return false
		}
	}
	return true
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (d *Daemon) cancelAuthorization(sessionID string) error {
	sessions := d.authorizationSessions()
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	if sessions.inFlight[sessionID] {
		return fmt.Errorf("authorization session is completing")
	}
	session, exists := sessions.byID[sessionID]
	if !exists {
		return fmt.Errorf("authorization session %q not found", sessionID)
	}
	delete(sessions.byID, sessionID)
	delete(sessions.byConnection, session.connectionID)
	return nil
}

func (d *Daemon) authorizationSessions() *authorizationSessions {
	d.oauthMu.Lock()
	defer d.oauthMu.Unlock()
	if d.oauth == nil {
		d.oauth = &authorizationSessions{byID: map[string]authorizationSession{}, byConnection: map[string]string{}, inFlight: map[string]bool{}}
	}
	return d.oauth
}

func (s *authorizationSessions) pruneLocked(now time.Time) {
	for id, session := range s.byID {
		if !session.expiresAt.After(now) && !s.inFlight[id] {
			if session.cancel != nil {
				session.cancel()
			}
			if session.kind == "device_code" && (session.status == "awaiting_user" || session.status == "polling") {
				session.cancel = nil
				session.device.DeviceCode = ""
				session.deviceFlow = nil
				session.status = "expired"
				session.statusError = "device authorization expired before approval"
				session.expiresAt = now.Add(deviceAuthorizationRetention)
				s.byID[id] = session
				if s.byConnection[session.connectionID] == id {
					delete(s.byConnection, session.connectionID)
				}
				continue
			}
			delete(s.byID, id)
			if s.byConnection[session.connectionID] == id {
				delete(s.byConnection, session.connectionID)
			}
		}
	}
}

func randomURLToken(size int) (string, error) {
	bytes := make([]byte, size)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func authorizationCallbackCode(raw string) (string, string, error) {
	if len(raw) > maxOAuthCallbackURLBytes {
		return "", "", fmt.Errorf("OAuth callback URL exceeds %d bytes", maxOAuthCallbackURLBytes)
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", "", fmt.Errorf("parse OAuth callback URL: %w", err)
	}
	if (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil {
		return "", "", fmt.Errorf("OAuth callback must be an absolute HTTP(S) URL without user information")
	}
	query := parsed.Query()
	if message := query.Get("error"); message != "" {
		return "", query.Get("state"), fmt.Errorf("OAuth authorization rejected: %s", message)
	}
	code, state := query.Get("code"), query.Get("state")
	if code == "" || state == "" {
		return "", "", fmt.Errorf("OAuth callback must contain code and state")
	}
	return code, state, nil
}
