package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/store"
)

// AuthorizationSetupView contains only provider-declared public instructions.
// Private driver state and submitted values never enter a retained public view.
type AuthorizationSetupView struct {
	SessionID     string                         `json:"sessionId"`
	ConnectionID  string                         `json:"connectionId"`
	Status        string                         `json:"status"`
	Revision      uint64                         `json:"revision"`
	Actions       []provider.AuthorizationAction `json:"actions,omitempty"`
	ExpiresAt     time.Time                      `json:"expiresAt"`
	Error         string                         `json:"error,omitempty"`
	Activation    *CredentialActivationView      `json:"activation,omitempty"`
	CallbackModes []AuthorizationCallbackView    `json:"callbackModes,omitempty"`
}

func (d *Daemon) startSetupAuthorization(ctx context.Context, connectionID string) (AuthorizationSetupView, error) {
	credential, ok := d.store.ConnectionCredentialByID(connectionID)
	if !ok {
		return AuthorizationSetupView{}, fmt.Errorf("enabled connection not found")
	}
	node, err := d.store.ProviderNodeByConnection(connectionID)
	if err != nil || !credential.Owner.Matches(node.ID, node.DefinitionRef()) {
		return AuthorizationSetupView{}, fmt.Errorf("connection auth owner changed")
	}
	flow := d.authFlows[d.authFlowID(node.DefinitionRef(), credential.Type)]
	driver, ok := flow.(provider.AuthorizationFlow)
	if !ok {
		return AuthorizationSetupView{}, fmt.Errorf("connection auth mode does not support action-based setup")
	}
	id, err := randomURLToken(24)
	if err != nil {
		return AuthorizationSetupView{}, err
	}
	callCtx, cancel := d.setupCallContext(ctx, time.Now().Add(authorizationStartTimeout))
	defer cancel()
	sessions := d.authorizationSessions()
	d.pruneAuthorizationSessions(sessions, time.Now())
	sessions.mu.Lock()
	if sessions.closed || len(sessions.byID) >= maxAuthorizationSessions || sessions.byConnection[connectionID] != "" {
		sessions.mu.Unlock()
		return AuthorizationSetupView{}, fmt.Errorf("authorization coordinator is stopped, full or connection is busy")
	}
	sessions.byID[id] = authorizationSession{kind: "actions", connectionID: connectionID, authFlow: flow, setupFlow: driver, expectedCredential: credential, expiresAt: time.Now().Add(provider.MaxAuthorizationDuration), status: "starting", cancel: cancel, setupCallDone: make(chan struct{})}
	sessions.byConnection[connectionID] = id
	sessions.inFlight[id] = true
	d.authWG.Add(1)
	sessions.mu.Unlock()
	defer d.releaseSetupCall(id)
	// Before Start returns a session ID, a failed reservation has no public
	// handle. Do not let repeated bind/driver failures fill the retention cap.
	delivered := false
	defer func() {
		if delivered {
			return
		}
		sessions.mu.Lock()
		current, exists := sessions.byID[id]
		if exists && current.activationCommit.ConnectionID == "" && current.status != "starting" && current.status != "awaiting_user" {
			delete(sessions.byID, id)
		}
		sessions.mu.Unlock()
	}()
	if err := callCtx.Err(); err != nil {
		return AuthorizationSetupView{}, d.failSetupCall(id, err)
	}
	transition, err := driver.BeginAuthorization(callCtx, provider.AuthInput{ConnectionID: connectionID, Type: credential.Type, Secret: credential.Secret})
	if callCtx.Err() != nil {
		err = callCtx.Err()
	}
	if err == nil {
		err = d.applySetupTransition(callCtx, id, transition)
	}
	if err != nil {
		return AuthorizationSetupView{}, d.failSetupCall(id, err)
	}
	view, err := d.setupAuthorizationStatus(id)
	delivered = err == nil
	return view, err
}

// Each step has its own bounded context; no frontend lifetime controls later
// polls, and shutdown cancellation reaches both start and advance callbacks.
func (d *Daemon) setupCallContext(parent context.Context, expiry time.Time) (context.Context, context.CancelFunc) {
	return d.authorizationContext(parent, expiry, authorizationStepTimeout)
}

func (d *Daemon) authorizationContext(parent context.Context, expiry time.Time, timeout time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithDeadline(parent, expiry)
	cancelTimeout := func() {}
	if timeout > 0 {
		ctx, cancelTimeout = context.WithTimeout(ctx, timeout)
	}
	stop := func() bool { return false }
	if d.runCtx != nil {
		stop = context.AfterFunc(d.runCtx, cancel)
		if d.runCtx.Err() != nil {
			cancel()
		}
	}
	return ctx, func() { stop(); cancelTimeout(); cancel() }
}

func (d *Daemon) advanceSetupAuthorization(ctx context.Context, id string, revision uint64, event provider.AuthorizationEvent) (AuthorizationSetupView, error) {
	sessions := d.authorizationSessions()
	sessions.mu.Lock()
	session, exists := sessions.byID[id]
	if sessions.closed || !exists || session.kind != "actions" || session.status != "awaiting_user" || sessions.inFlight[id] || session.setupRevision != revision || !session.expiresAt.After(time.Now()) {
		sessions.mu.Unlock()
		return AuthorizationSetupView{}, fmt.Errorf("authorization action is stale, busy, expired or unavailable")
	}
	isAwait := event.Kind == provider.AuthorizationAwait && session.setupAwait && session.setupAwaitStarted
	if event.Kind == provider.AuthorizationAwait && !isAwait {
		sessions.mu.Unlock()
		return AuthorizationSetupView{}, fmt.Errorf("authorization await is not scheduled")
	}
	timeout := authorizationStepTimeout
	if isAwait {
		timeout = 0
		session.setupAwait = false
		session.setupAwaitStarted = false
	}
	callCtx, cancel := d.authorizationContext(ctx, session.expiresAt, timeout)
	defer cancel()
	session.cancel = cancel
	session.setupCallDone = make(chan struct{})
	if session.setupPollTimer != nil {
		session.setupPollTimer.Stop()
		session.setupPollTimer = nil
	}
	sessions.byID[id] = session
	sessions.inFlight[id] = true
	d.authWG.Add(1)
	sessions.mu.Unlock()
	defer d.releaseSetupCall(id)
	if event.Kind == provider.AuthorizationSubmit {
		if err := d.validateSetupInput(session, event); err != nil {
			return AuthorizationSetupView{}, err
		}
	} else if (event.Kind != provider.AuthorizationPoll && !isAwait) || len(event.Value) != 0 || event.ActionID != "" || len(event.Callbacks) != 0 {
		return AuthorizationSetupView{}, fmt.Errorf("invalid authorization event")
	}
	current, ok := d.store.ConnectionCredentialByID(session.connectionID)
	if !ok || current.Owner != session.expectedCredential.Owner || current.Revision != session.expectedCredential.Revision || current.PrincipalGeneration != session.expectedCredential.PrincipalGeneration {
		return AuthorizationSetupView{}, d.failSetupCall(id, fmt.Errorf("connection changed"))
	}
	if err := callCtx.Err(); err != nil {
		return AuthorizationSetupView{}, d.failSetupCall(id, err)
	}
	transition, err := session.setupFlow.AdvanceAuthorization(callCtx, session.setupState, event)
	if callCtx.Err() != nil {
		err = callCtx.Err()
	}
	if err == nil {
		err = d.applySetupTransition(callCtx, id, transition)
	}
	if err != nil {
		return AuthorizationSetupView{}, d.failSetupCall(id, err)
	}
	return d.setupAuthorizationStatus(id)
}

func (d *Daemon) validateSetupInput(session authorizationSession, event provider.AuthorizationEvent) error {
	if len(event.Value) == 0 || len(event.Value) > provider.MaxAuthorizationStateBytes {
		return fmt.Errorf("authorization action input must be a bounded JSON object")
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(event.Value, &object) != nil || object == nil {
		return fmt.Errorf("authorization action input must be a JSON object")
	}
	for _, action := range session.setupActions {
		if action.ID != event.ActionID || action.Kind != provider.AuthorizationInput {
			continue
		}
		if action.InputSchemaRef == nil || d.setupSchemas == nil || d.setupSchemas.ValidateSchema(*action.InputSchemaRef, event.Value) != nil {
			// Schema errors may quote submitted secrets. Return only this safe summary.
			return fmt.Errorf("authorization input does not satisfy its registered schema")
		}
		return nil
	}
	return fmt.Errorf("authorization input action is not offered by this transition")
}

func (d *Daemon) applySetupTransition(ctx context.Context, id string, transition provider.AuthorizationTransition) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var err error
	transition, err = d.prepareSetupCallbackResources(ctx, id, transition)
	if err != nil {
		return err
	}
	sessions := d.authorizationSessions()
	sessions.mu.Lock()
	session, exists := sessions.byID[id]
	if ctx.Err() != nil {
		sessions.mu.Unlock()
		return ctx.Err()
	}
	if sessions.closed || !exists || session.kind != "actions" || (session.status != "starting" && session.status != "awaiting_user") || !session.expiresAt.After(time.Now()) {
		sessions.mu.Unlock()
		return context.Canceled
	}
	if transition.Credential == nil && transition.ExpiresAt.After(session.expiresAt) {
		sessions.mu.Unlock()
		return fmt.Errorf("authorization transition cannot extend its session deadline")
	}
	if transition.Failure != "" {
		session.setupRevision++
		sessions.byID[id] = session
		sessions.mu.Unlock()
		status, message := "failed", "authorization provider is unavailable"
		if transition.Failure == provider.AuthorizationDenied {
			status, message = "denied", "authorization was denied"
		}
		if transition.Failure == provider.AuthorizationExpired {
			status, message = "expired", "authorization expired"
		}
		if !d.finishSetupAuthorization(id, status, message) {
			return context.Canceled
		}
		return nil
	}
	if session.timer != nil {
		session.timer.Stop()
		session.timer = nil
	}
	if session.setupPollTimer != nil {
		session.setupPollTimer.Stop()
		session.setupPollTimer = nil
	}
	nextRevision := session.setupRevision + 1
	bindings := make([]authorizationCallbackBinding, 0, len(transition.Callbacks))
	for _, callback := range transition.Callbacks {
		actionID := callback.ActionID
		bindings = append(bindings, authorizationCallbackBinding{spec: callback, invoke: func(callbackCtx context.Context, rawURL string) error {
			value, err := json.Marshal(provider.AuthorizationCallbackInput{URL: rawURL})
			if err != nil {
				return fmt.Errorf("invalid callback input")
			}
			return d.deliverSetupCallback(callbackCtx, id, nextRevision, provider.AuthorizationEvent{Kind: provider.AuthorizationSubmit, ActionID: actionID, Value: value})
		}})
	}
	if transition.Credential == nil {
		modes, err := d.replaceAuthorizationCallbacks(id, bindings)
		if err != nil {
			sessions.mu.Unlock()
			return err
		}
		session.setupCallbackModes = modes
		session.callbackKey = ""
		if len(bindings) != 0 {
			session.callbackKey = id
		}
	}
	session.status = "awaiting_user"
	session.setupRevision++
	if transition.Credential == nil {
		session.setupState = transition.State
		session.setupActions = cloneSetupActions(transition.Actions)
		session.setupPollAfter = transition.PollAfter
		session.setupAwait = transition.Await
		session.setupAwaitStarted = false
		session.expiresAt = transition.ExpiresAt
		session.timer = time.AfterFunc(time.Until(session.expiresAt), func() { d.finishSetupAuthorization(id, "expired", "authorization expired") })
	}
	sessions.byID[id] = session
	sessions.mu.Unlock()
	if transition.Credential != nil {
		_, err := d.commitAuthorizationCredential(ctx, sessions, id, *transition.Credential)
		return err
	}
	return nil
}

func (d *Daemon) pollSetupAuthorization(id string, revision uint64) {
	// Stale callbacks are rejected by revision admission. Admitted callbacks
	// retain failures and rearm pending polls through releaseSetupCall.
	_, _ = d.advanceSetupAuthorization(context.Background(), id, revision, provider.AuthorizationEvent{Kind: provider.AuthorizationPoll})
}

// A browser can arrive after grants are published but before the preceding
// step returns. Wait for that admitted step, never poll or bypass its revision.
func (d *Daemon) deliverSetupCallback(ctx context.Context, id string, revision uint64, event provider.AuthorizationEvent) error {
	for {
		sessions := d.authorizationSessions()
		sessions.mu.Lock()
		session, exists := sessions.byID[id]
		if sessions.closed || !exists || session.kind != "actions" || session.status != "awaiting_user" || session.setupRevision != revision {
			sessions.mu.Unlock()
			return fmt.Errorf("callback grant is no longer active")
		}
		busy := sessions.inFlight[id]
		done := session.setupCallDone
		sessions.mu.Unlock()
		if !busy {
			_, err := d.advanceSetupAuthorization(ctx, id, revision, event)
			return err
		}
		if done == nil {
			return fmt.Errorf("callback step is unavailable")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
		}
	}
}

func (d *Daemon) releaseSetupCall(id string) {
	sessions := d.authorizationSessions()
	sessions.mu.Lock()
	delete(sessions.inFlight, id)
	if current, exists := sessions.byID[id]; exists {
		if current.setupCallDone != nil {
			close(current.setupCallDone)
			current.setupCallDone = nil
		}
		current.cancel = nil
		if !sessions.closed && current.status == "awaiting_user" && current.setupPollAfter != 0 {
			if current.setupPollTimer != nil {
				current.setupPollTimer.Stop()
			}
			revision := current.setupRevision
			current.setupPollTimer = time.AfterFunc(current.setupPollAfter, func() { d.pollSetupAuthorization(id, revision) })
		}
		if !sessions.closed && current.status == "awaiting_user" && current.setupAwait && !current.setupAwaitStarted {
			current.setupAwaitStarted = true
			revision := current.setupRevision
			root := d.runCtx
			if root == nil {
				root = context.Background()
			}
			d.authWG.Add(1)
			go func() {
				defer d.authWG.Done()
				_, _ = d.advanceSetupAuthorization(root, id, revision, provider.AuthorizationEvent{Kind: provider.AuthorizationAwait})
			}()
		}
		sessions.byID[id] = current
	}
	sessions.mu.Unlock()
	d.authWG.Done()
}

func (d *Daemon) failSetupCall(id string, err error) error {
	status, message := "failed", "authorization driver failed or connection changed"
	if errors.Is(err, context.Canceled) {
		status, message = "cancelled", "authorization was cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		status, message = "expired", "authorization expired"
	}
	d.finishSetupAuthorization(id, status, message)
	return fmt.Errorf("%s", message)
}

func clearSetupSession(session *authorizationSession) {
	if session.setupPollTimer != nil {
		session.setupPollTimer.Stop()
		session.setupPollTimer = nil
	}
	session.setupState = provider.AuthorizationState{}
	session.setupActions = nil
	session.setupFlow = nil
	session.setupPollAfter = 0
	session.setupAwait = false
	session.setupAwaitStarted = false
	session.expectedCredential = store.Credential{}
	session.authFlow = nil
	session.setupCallbackModes = nil
}

func (d *Daemon) finishSetupAuthorization(id, status, message string) bool {
	sessions := d.authorizationSessions()
	sessions.mu.Lock()
	session, exists := sessions.byID[id]
	if !exists || session.kind != "actions" || (session.status != "starting" && session.status != "awaiting_user") {
		sessions.mu.Unlock()
		return false
	}
	if session.cancel != nil {
		session.cancel()
		session.cancel = nil
	}
	if session.timer != nil {
		session.timer.Stop()
		session.timer = nil
	}
	if session.setupCallDone != nil {
		close(session.setupCallDone)
		session.setupCallDone = nil
	}
	callbackOwner := session.callbackKey
	session.callbackKey = ""
	clearSetupSession(&session)
	session.expectedCredential, session.authFlow = store.Credential{}, nil
	session.status, session.statusError = status, message
	session.expiresAt = time.Now().Add(authorizationSessionRetention)
	sessions.byID[id] = session
	if sessions.byConnection[session.connectionID] == id {
		delete(sessions.byConnection, session.connectionID)
	}
	sessions.mu.Unlock()
	d.releaseAuthorizationCallbacks(callbackOwner)
	return true
}

func cloneSetupActions(actions []provider.AuthorizationAction) []provider.AuthorizationAction {
	result := append([]provider.AuthorizationAction(nil), actions...)
	for i := range result {
		if result[i].InputSchemaRef != nil {
			ref := *result[i].InputSchemaRef
			result[i].InputSchemaRef = &ref
		}
	}
	return result
}

func (d *Daemon) setupAuthorizationStatus(id string) (AuthorizationSetupView, error) {
	sessions := d.authorizationSessions()
	d.pruneAuthorizationSessions(sessions, time.Now())
	sessions.mu.Lock()
	session, exists := sessions.byID[id]
	sessions.mu.Unlock()
	if !exists || session.kind != "actions" {
		return AuthorizationSetupView{}, fmt.Errorf("authorization setup session not found")
	}
	view := AuthorizationSetupView{SessionID: id, ConnectionID: session.connectionID, Status: session.status, Revision: session.setupRevision, Actions: cloneSetupActions(session.setupActions), ExpiresAt: session.expiresAt, Error: session.statusError, CallbackModes: append([]AuthorizationCallbackView(nil), session.setupCallbackModes...)}
	if session.activationCommit.ConnectionID != "" {
		activation := d.credentialActivationView(d.activationContext(), session.activationCommit)
		view.Activation = &activation
	}
	return view, nil
}
