package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/fm39hz/gobroom/internal/daemon"
	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/provider"
)

// Only public session state survives a command. Values live in the submitted
// request, never in the inspector, status, catalog cache or retained draft.
type setupResultMsg struct {
	epoch             uint64
	sessionID, method string
	revision          uint64
	actionID          string
	schemaRef         extensions.Ref
	result            json.RawMessage
	err               error
}

type setupFormMsg struct {
	epoch, nonce        uint64
	sessionID, actionID string
	revision            uint64
	schemaRef           extensions.Ref
	pane                int
	form                *formState
	err                 error
}

func (m *app) invokeSetup(method string, params map[string]any) tea.Cmd {
	epoch, sessionID, path := m.setupEpoch, m.setupSessionID, m.ipcPath
	revision := m.setupView.Revision
	actionID, _ := params["actionID"].(string)
	var schemaRef extensions.Ref
	if method == "auth.setup.advance" {
		if submitted, ok := params["revision"].(uint64); ok {
			revision = submitted
		}
		for _, action := range m.setupInputs() {
			if action.ID == actionID && action.InputSchemaRef != nil {
				schemaRef = *action.InputSchemaRef
			}
		}
	}
	requestContext, requestCancel := context.WithCancel(context.Background())
	if method == "auth.setup.start" {
		m.setupStartCancel = requestCancel
	}
	return func() tea.Msg {
		defer requestCancel()
		defer delete(params, "value")
		ctx, cancel := context.WithTimeout(requestContext, 20*time.Second)
		defer cancel()
		response, err := daemon.CallIPC(ctx, path, daemon.IPCRequest{Method: method, Params: params})
		message := setupResultMsg{epoch: epoch, sessionID: sessionID, method: method, revision: revision, actionID: actionID, schemaRef: schemaRef, err: err}
		if err == nil {
			message.result, message.err = json.Marshal(response.Result)
		}
		return message
	}
}

func (m *app) setupPoll() tea.Cmd {
	if m.setupSessionID == "" || m.setupSubmitting || m.setupCancelPending {
		return nil
	}
	m.setupPollID++
	epoch, id, pollID := m.setupEpoch, m.setupSessionID, m.setupPollID
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return setupPollTick{epoch: epoch, sessionID: id, pollID: pollID} })
}

func (m *app) setupBindingCurrent(binding *setupFormBinding) bool {
	if binding == nil || binding.SessionID != m.setupSessionID || binding.Revision != m.setupView.Revision || m.setupView.Status != "awaiting_user" {
		return false
	}
	for _, action := range m.setupInputs() {
		if action.ID == binding.ActionID && action.InputSchemaRef != nil && *action.InputSchemaRef == binding.SchemaRef {
			return true
		}
	}
	return false
}

func (m *app) discardSetupInput() {
	m.setupInputNonce++
	m.setupFormPending, m.pendingSetupInput, m.setupAutoOpen = false, false, false
	if m.form != nil && m.form.setup != nil {
		m.form.wipeSetupInput()
		m.form = nil
		m.mode = modeBrowse
	}
}

func (m *app) setupInputs() []provider.AuthorizationAction {
	var inputs []provider.AuthorizationAction
	for _, action := range m.setupView.Actions {
		if action.Kind == provider.AuthorizationInput {
			inputs = append(inputs, action)
		}
	}
	return inputs
}

func (m *app) openSetupInput() tea.Cmd {
	if m.setupFormPending || m.pendingSetupInput {
		return nil
	}
	if m.setupSessionID == "" || m.setupView.Status != "awaiting_user" || m.setupSubmitting || m.setupCancelPending {
		return nil
	}
	inputs := m.setupInputs()
	if len(inputs) == 0 {
		m.status = "setup is waiting for the provider; no input action is offered"
		return nil
	}
	if m.mode != modeBrowse && (m.form == nil || m.form.setup == nil) {
		return nil
	}
	m.setupActionIndex %= len(inputs)
	action := inputs[m.setupActionIndex]
	if action.InputSchemaRef == nil {
		m.status = "setup input has no registered schema"
		return nil
	}
	m.setupAutoOpen = true
	m.setupInputPane = m.activeContextIndex()
	if len(m.raw[sectionExtensionCatalog]) == 0 {
		m.pendingSetupInput = true
		m.status = fmt.Sprintf("connection setup · revision %d · loading registered setup schemas…", m.setupView.Revision)
		return m.fetch(sectionExtensionCatalog)
	}
	m.setupInputNonce++
	m.setupFormPending = true
	m.status = "loading setup input…"
	view := m.setupView
	epoch, nonce, pane := m.setupEpoch, m.setupInputNonce, m.setupInputPane
	catalog := append(json.RawMessage(nil), m.raw[sectionExtensionCatalog]...)
	ref := *action.InputSchemaRef
	return func() tea.Msg {
		form, err := newSetupActionForm(view, action, catalog)
		return setupFormMsg{epoch: epoch, nonce: nonce, sessionID: view.SessionID, actionID: action.ID, revision: view.Revision, schemaRef: ref, pane: pane, form: form, err: err}
	}
}

func (m *app) handleSetupForm(message setupFormMsg) tea.Cmd {
	valid := message.epoch == m.setupEpoch && message.nonce == m.setupInputNonce && message.sessionID == m.setupSessionID && message.revision == m.setupView.Revision && m.setupView.Status == "awaiting_user" && m.mode == modeBrowse && message.pane == m.activeContextIndex() && !m.setupSubmitting && !m.setupCancelPending
	found := false
	for _, action := range m.setupInputs() {
		if action.ID == message.actionID && action.InputSchemaRef != nil && *action.InputSchemaRef == message.schemaRef {
			found = true
		}
	}
	if !valid || !found {
		if message.form != nil {
			message.form.wipeSetupInput()
		}
		if message.nonce == m.setupInputNonce {
			m.setupFormPending = false
			return m.setupPoll()
		}
		return nil
	}
	m.setupFormPending, m.setupAutoOpen = false, false
	if message.err != nil || message.form == nil {
		m.status = "registered setup schema cannot be edited; D retries"
		return m.setupPoll()
	}
	m.form, m.mode = message.form, modeForm
	m.status = fmt.Sprintf("connection setup input · revision %d · values hidden", message.revision)
	m.resize()
	return m.setupPoll()
}

func (m *app) cancelSetup() tea.Cmd {
	if m.setupStartPending {
		m.setupEpoch++
		if m.setupStartCancel != nil {
			m.setupStartCancel()
			m.setupStartCancel = nil
		}
		m.setupStartPending, m.loading = false, false
		m.discardSetupInput()
		m.status = "connection setup request cancelled"
		return nil
	}
	if m.setupSessionID == "" {
		return nil
	}
	if m.setupCancelPending {
		return nil
	}
	m.setupEpoch++ // fence pending polls, compilers and submission replies
	m.discardSetupInput()
	m.setupSubmitting, m.setupStartPending = false, false
	m.setupCancelPending = true
	m.loading = true
	m.status = "cancelling connection setup…"
	return m.invokeSetup("auth.setup.cancel", map[string]any{"sessionID": m.setupSessionID})
}

func (m *app) handleSetupResult(message setupResultMsg) tea.Cmd {
	if message.epoch != m.setupEpoch {
		return nil
	}
	if message.method == "auth.setup.start" {
		if !m.setupStartPending || message.sessionID != "" {
			return nil
		}
		m.setupStartCancel = nil
	} else if message.sessionID == "" || message.sessionID != m.setupSessionID {
		return nil
	}
	if m.setupCancelPending && message.method != "auth.setup.cancel" {
		return nil
	}
	if (message.method == "auth.setup.get" || message.method == "auth.setup.advance") && message.revision < m.setupView.Revision {
		return nil
	}
	if message.method == "auth.setup.advance" && message.schemaRef != (extensions.Ref{}) && !m.setupBindingCurrent(&setupFormBinding{SessionID: message.sessionID, ActionID: message.actionID, Revision: message.revision, SchemaRef: message.schemaRef}) {
		return nil
	}
	if message.err != nil {
		m.setupSubmitting, m.setupStartPending, m.loading = false, false, false
		m.setupCancelPending = false
		m.discardSetupInput()
		m.lastError = "" // remote errors and validators can quote submitted values
		m.setupNeedsRefresh = m.setupSessionID != ""
		m.status = "connection setup unavailable or input rejected; values cleared; D retries"
		m.syncDetail()
		if message.method == "auth.setup.advance" || message.method == "auth.setup.cancel" {
			return m.invokeSetup("auth.setup.get", map[string]any{"sessionID": m.setupSessionID})
		}
		return nil
	}
	if message.method == "auth.setup.cancel" {
		m.setupCancelPending = false
		m.discardSetupInput()
		m.setupSessionID = ""
		m.setupView = daemon.AuthorizationSetupView{}
		m.setupSubmitting, m.setupNeedsRefresh, m.loading = false, false, false
		m.status = "connection setup cancelled"
		m.previewExtra, m.previewTitle = "", ""
		m.usageDetailPreview = nil
		m.syncDetail()
		return nil
	}
	var view daemon.AuthorizationSetupView
	if json.Unmarshal(message.result, &view) != nil || view.SessionID == "" || view.ConnectionID == "" || view.Revision == 0 {
		m.setupSubmitting, m.setupStartPending, m.loading = false, false, false
		m.discardSetupInput()
		m.setupNeedsRefresh = m.setupSessionID != ""
		m.status = "invalid public setup state; D retries"
		return nil
	}
	if message.method != "auth.setup.start" && view.SessionID != m.setupSessionID {
		return nil
	}
	if m.setupView.SessionID == view.SessionID && (view.Revision < m.setupView.Revision || view.ConnectionID != m.setupView.ConnectionID) {
		return nil
	}
	if m.setupSubmitting && message.method == "auth.setup.get" && view.Revision <= m.setupView.Revision {
		return nil
	}
	editing := m.form != nil && m.form.setup != nil
	changed := m.setupView.SessionID != view.SessionID || m.setupView.Revision != view.Revision || !reflect.DeepEqual(m.setupView.Actions, view.Actions)
	autoOpen := m.setupAutoOpen || (message.method == "auth.setup.advance") || (editing && changed)
	if changed || view.Status != "awaiting_user" {
		m.discardSetupInput()
	}
	m.setupView, m.setupSessionID = view, view.SessionID
	m.setupStartPending, m.setupSubmitting, m.setupNeedsRefresh, m.loading = false, false, false, false
	m.lastError = ""
	var actions strings.Builder
	for _, action := range view.Actions {
		fmt.Fprintf(&actions, "%s · %s\n", action.Kind, action.Label)
		if action.Code != "" {
			fmt.Fprintf(&actions, "  Code: %s\n", action.Code)
		}
		if action.URL != "" {
			fmt.Fprintf(&actions, "  URL: %s\n", action.URL)
		}
		if action.InputSchemaRef != nil {
			fmt.Fprintf(&actions, "  Input schema: %s\n", action.InputSchemaRef.Key())
		}
	}
	for _, callback := range view.CallbackModes {
		fmt.Fprintf(&actions, "Callback: %s\n", callback.Mode)
	}
	m.previewExtra, m.previewTitle, m.previewKey = strings.TrimSpace(actions.String()), "Connection setup actions · "+view.ConnectionID, ""
	m.usageDetailPreview = nil
	if view.Status == "awaiting_user" || view.Status == "starting" {
		m.status = fmt.Sprintf("connection setup · %d public action(s) · revision %d · D input/resume · O cancel", len(view.Actions), view.Revision)
		m.syncDetail()
		if autoOpen && !editing && m.mode == modeBrowse && m.activeContextIndex() == m.setupInputPane && !m.setupFormPending && len(m.setupInputs()) > 0 {
			return m.openSetupInput()
		}
		if autoOpen && editing && changed && len(m.setupInputs()) > 0 {
			return m.openSetupInput()
		}
		return m.setupPoll()
	}
	m.status = "connection setup " + view.Status
	if view.Status == "completed" {
		var poll bool
		var hint string
		m.status, poll, hint = authActivationCompletion("Connection setup", view.ConnectionID, view.Activation)
		if hint != "" {
			m.previewExtra += "\n\n" + hint
		}
		m.syncDetail()
		if poll {
			return m.setupPoll()
		}
	}
	m.setupSessionID = ""
	m.syncDetail()
	return nil
}
