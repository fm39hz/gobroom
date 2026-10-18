package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/fm39hz/gobroom/internal/provider"
	"golang.org/x/oauth2"
)

const (
	deviceAuthorizationStartTimeout = 20 * time.Second
	authorizationSessionRetention   = 5 * time.Minute
)

type deviceAuthorizationView struct {
	SessionID               string                    `json:"sessionId"`
	ConnectionID            string                    `json:"connectionId"`
	Status                  string                    `json:"status"`
	UserCode                string                    `json:"userCode"`
	VerificationURL         string                    `json:"verificationUrl"`
	VerificationURLComplete string                    `json:"verificationUrlComplete,omitempty"`
	ExpiresAt               time.Time                 `json:"expiresAt"`
	PollIntervalSeconds     int64                     `json:"pollIntervalSeconds"`
	Error                   string                    `json:"error,omitempty"`
	Activation              *CredentialActivationView `json:"activation,omitempty"`
}

func (d *Daemon) startDeviceAuthorization(ctx context.Context, connectionID string) (deviceAuthorizationView, error) {
	connection, ok := d.store.ConnectionCredentialByID(connectionID)
	if !ok {
		return deviceAuthorizationView{}, fmt.Errorf("enabled connection %q not found", connectionID)
	}
	node, err := d.store.ProviderNodeByConnection(connectionID)
	if err != nil {
		return deviceAuthorizationView{}, err
	}
	if !connection.Owner.Matches(node.ID, node.DefinitionRef()) {
		return deviceAuthorizationView{}, fmt.Errorf("provider binding changed while starting authorization")
	}
	flowID := d.authFlowID(node.DefinitionRef(), connection.Type)
	flow, ok := d.authFlows[flowID]
	if flowID == "" || !ok {
		return deviceAuthorizationView{}, fmt.Errorf("auth flow for provider definition %q is unavailable", node.DefinitionID)
	}
	deviceFlow, ok := flow.(provider.DeviceAuthorizationFlow)
	if !ok {
		return deviceAuthorizationView{}, fmt.Errorf("auth flow %q does not support device authorization", flowID)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	sessionID, err := randomURLToken(24)
	if err != nil {
		return deviceAuthorizationView{}, err
	}
	startCtx, cancelStart := context.WithTimeout(ctx, deviceAuthorizationStartTimeout)
	defer cancelStart()
	if d.runCtx != nil {
		stopCancel := context.AfterFunc(d.runCtx, cancelStart)
		defer stopCancel()
		if d.runCtx.Err() != nil {
			cancelStart()
		}
	}
	expiresAt := time.Now().Add(deviceAuthorizationStartTimeout)
	sessions := d.authorizationSessions()
	d.pruneAuthorizationSessions(sessions, time.Now())
	sessions.mu.Lock()
	if sessions.closed {
		sessions.mu.Unlock()
		cancelStart()
		return deviceAuthorizationView{}, fmt.Errorf("authorization coordinator is stopped")
	}
	if active := sessions.byConnection[connection.ID]; active != "" {
		sessions.mu.Unlock()
		cancelStart()
		return deviceAuthorizationView{}, fmt.Errorf("connection %q already has pending authorization session %q", connection.ID, active)
	}
	if len(sessions.byID) >= maxAuthorizationSessions {
		sessions.mu.Unlock()
		cancelStart()
		return deviceAuthorizationView{}, fmt.Errorf("maximum pending authorization sessions (%d) reached", maxAuthorizationSessions)
	}
	sessions.byID[sessionID] = authorizationSession{kind: "device_code", connectionID: connection.ID, expectedCredential: connection, authFlow: flow, expiresAt: expiresAt, status: "starting", cancel: cancelStart}
	sessions.byConnection[connection.ID] = sessionID
	d.authWG.Add(1)
	sessions.mu.Unlock()
	defer d.authWG.Done()

	challenge, err := deviceFlow.BeginDeviceAuthorization(startCtx)
	if startCtx.Err() != nil {
		err = startCtx.Err()
	}
	cancelStart()
	if err != nil {
		d.removeDeviceAuthorizationReservation(sessionID)
		return deviceAuthorizationView{}, err
	}
	if err := validateDeviceAuthorization(challenge); err != nil {
		d.removeDeviceAuthorizationReservation(sessionID)
		return deviceAuthorizationView{}, err
	}
	sessions.mu.Lock()
	session, exists := sessions.byID[sessionID]
	if !exists || session.status != "starting" {
		sessions.mu.Unlock()
		return deviceAuthorizationView{}, fmt.Errorf("device authorization start was cancelled before completion")
	}
	root := d.runCtx
	if root == nil {
		root = context.Background()
	}
	sessionCtx, cancelSession := context.WithCancel(root)
	deviceCtx, cancelExpiry := context.WithDeadline(sessionCtx, challenge.ExpiresAt)
	cancel := func() { cancelExpiry(); cancelSession() }
	session.deviceFlow = deviceFlow
	session.device = challenge
	session.expiresAt = challenge.ExpiresAt
	session.status = "awaiting_user"
	session.cancel = cancel
	sessions.byID[sessionID] = session
	d.authWG.Add(1)
	sessions.mu.Unlock()
	go func() {
		defer d.authWG.Done()
		d.pollDeviceAuthorization(deviceCtx, sessionID, session)
	}()
	return publicDeviceAuthorization(sessionID, session), nil
}

func (d *Daemon) removeDeviceAuthorizationReservation(sessionID string) {
	sessions := d.authorizationSessions()
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	session, exists := sessions.byID[sessionID]
	if !exists || session.status != "starting" {
		return
	}
	if session.cancel != nil {
		session.cancel()
	}
	delete(sessions.byID, sessionID)
	if sessions.byConnection[session.connectionID] == sessionID {
		delete(sessions.byConnection, session.connectionID)
	}
}

func validateDeviceAuthorization(device provider.DeviceAuthorization) error {
	if device.DeviceCode == "" || len(device.DeviceCode) > 4096 || device.UserCode == "" || len(device.UserCode) > 256 || device.ExpiresAt.IsZero() || !device.ExpiresAt.After(time.Now()) {
		return fmt.Errorf("OAuth device authorization response has invalid code or expiry")
	}
	for _, raw := range []string{device.VerificationURL, device.VerificationURLComplete} {
		if raw == "" {
			continue
		}
		parsed, err := url.Parse(raw)
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil {
			return fmt.Errorf("OAuth device authorization response has an invalid verification URL")
		}
	}
	if device.VerificationURL == "" {
		return fmt.Errorf("OAuth device authorization response has no verification URL")
	}
	if device.PollInterval < 0 || device.PollInterval > time.Minute {
		return fmt.Errorf("OAuth device polling interval is out of bounds")
	}
	return nil
}

func (d *Daemon) pollDeviceAuthorization(ctx context.Context, sessionID string, session authorizationSession) {
	credential, err := session.deviceFlow.WaitForDeviceAuthorization(ctx, session.device)
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		status, message := classifyAuthorizationError(ctx, err)
		d.finishDeviceAuthorization(sessionID, status, message)
		return
	}
	_, err = d.commitAuthorizationCredential(ctx, d.authorizationSessions(), sessionID, credential)
	if err != nil {
		status, message := authorizationCommitFailure(ctx, err)
		d.finishDeviceAuthorization(sessionID, status, message)
		return
	}
	d.finishDeviceAuthorization(sessionID, "completed", "")
}

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

func (d *Daemon) finishDeviceAuthorization(sessionID, status, message string) {
	sessions := d.authorizationSessions()
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	session, exists := sessions.byID[sessionID]
	if !exists || session.kind != "device_code" || session.status == "expired" || session.status == "cancelled" {
		return
	}
	if session.cancel != nil {
		session.cancel()
	}
	session.cancel = nil
	session.device.DeviceCode = ""
	session.deviceFlow = nil
	session.status = status
	session.statusError = message
	session.expiresAt = time.Now().Add(authorizationSessionRetention)
	sessions.byID[sessionID] = session
	if sessions.byConnection[session.connectionID] == sessionID {
		delete(sessions.byConnection, session.connectionID)
	}
}

func (d *Daemon) deviceAuthorizationStatus(sessionID string) (deviceAuthorizationView, error) {
	sessions := d.authorizationSessions()
	d.pruneAuthorizationSessions(sessions, time.Now())
	sessions.mu.Lock()
	session, exists := sessions.byID[sessionID]
	sessions.mu.Unlock()
	if !exists || session.kind != "device_code" {
		return deviceAuthorizationView{}, fmt.Errorf("device authorization session %q not found", sessionID)
	}
	view := publicDeviceAuthorization(sessionID, session)
	if session.activationCommit.ConnectionID != "" {
		activation := d.credentialActivationView(d.activationContext(), session.activationCommit)
		view.Activation = &activation
	}
	return view, nil
}

func (d *Daemon) cancelDeviceAuthorization(sessionID string) error {
	sessions := d.authorizationSessions()
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	session, exists := sessions.byID[sessionID]
	if !exists || session.kind != "device_code" {
		return fmt.Errorf("device authorization session %q not found", sessionID)
	}
	if session.cancel != nil {
		session.cancel()
	}
	delete(sessions.byID, sessionID)
	if sessions.byConnection[session.connectionID] == sessionID {
		delete(sessions.byConnection, session.connectionID)
	}
	return nil
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

func publicDeviceAuthorization(sessionID string, session authorizationSession) deviceAuthorizationView {
	return deviceAuthorizationView{
		SessionID: sessionID, ConnectionID: session.connectionID, Status: session.status,
		UserCode: session.device.UserCode, VerificationURL: session.device.VerificationURL,
		VerificationURLComplete: session.device.VerificationURLComplete,
		ExpiresAt:               session.device.ExpiresAt, PollIntervalSeconds: int64(session.device.PollInterval / time.Second),
		Error: session.statusError,
	}
}
