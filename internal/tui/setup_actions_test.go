package tui

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/fm39hz/gobroom/internal/daemon"
	"github.com/fm39hz/gobroom/internal/extensions"
)

var setupActionSchemaRef = extensions.Ref{Kind: extensions.SchemaKind, ID: "test.authorization.input", ContractVersion: 1}

func setupActionCatalog(t *testing.T) json.RawMessage {
	t.Helper()
	document, err := extensions.BindSchemaDocument(setupActionSchemaRef, json.RawMessage(`{"type":"object","properties":{"secret":{"type":"string","writeOnly":true,"minLength":1}},"required":["secret"],"additionalProperties":false}`))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := json.Marshal(extensions.CatalogView{Schemas: []extensions.Schema{{Ref: setupActionSchemaRef, Document: document}}})
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func setupActionView(sessionID string, revision uint64, actions ...map[string]any) json.RawMessage {
	view := daemon.AuthorizationSetupView{SessionID: sessionID, ConnectionID: "connection-setup", Status: "awaiting_user", Revision: revision}
	encodedActions, _ := json.Marshal(actions)
	_ = json.Unmarshal(encodedActions, &view.Actions)
	result, _ := json.Marshal(view)
	return result
}

func setupActionResult(model *app, sessionID, method string, result json.RawMessage) (tea.Model, tea.Cmd) {
	return model.Update(setupResultMsg{epoch: model.setupEpoch, sessionID: sessionID, method: method, revision: model.setupView.Revision, result: result})
}

func TestSetupActionFormBindsSessionActionRevisionAndSchema(t *testing.T) {
	model := newApp("/tmp/gobroom.sock")
	model.raw[sectionExtensionCatalog] = setupActionCatalog(t)
	model.setupEpoch = 7
	model.setupSessionID = "setup-session"
	model.mode = modeBrowse
	view := setupActionView("setup-session", 12, map[string]any{
		"id": "submit-code", "kind": "input", "label": "Enter verification code", "inputSchemaRef": setupActionSchemaRef,
	})
	updated, _ := setupActionResult(model, "setup-session", "auth.setup.get", view)
	model = updated.(*app)
	open := model.openSetupInput()
	if open == nil {
		t.Fatal("setup view did not schedule schema compilation")
	}
	formResult := open()
	compiled, ok := formResult.(setupFormMsg)
	if !ok {
		t.Fatalf("setup form command returned %T, want setupFormMsg", formResult)
	}
	if compiled.epoch != model.setupEpoch || compiled.nonce != model.setupInputNonce || compiled.sessionID != "setup-session" || compiled.actionID != "submit-code" || compiled.revision != 12 || compiled.schemaRef != setupActionSchemaRef || compiled.pane != model.activeContextIndex() {
		t.Fatalf("compiled form completion lost its stale-result binding: %#v", compiled)
	}
	updated, _ = model.Update(compiled)
	model = updated.(*app)
	if model.form == nil || model.form.setup == nil {
		t.Fatalf("compiled setup input form was not installed: mode=%v form=%#v status=%q", model.mode, model.form, model.status)
	}
	if model.form.setup.SessionID != "setup-session" || model.form.setup.ActionID != "submit-code" || model.form.setup.Revision != 12 || model.form.setup.SchemaRef != setupActionSchemaRef {
		t.Fatalf("setup form lost immutable action binding: %#v", model.form.setup)
	}
}

func TestSetupResultRejectsStaleEpochSessionAndRevision(t *testing.T) {
	model := newApp("/tmp/gobroom.sock")
	model.setupEpoch = 4
	model.setupSessionID = "current-session"
	current := setupActionView("current-session", 9, map[string]any{"id": "current", "kind": "display_code", "label": "Current", "code": "CURRENT-CODE"})
	updated, _ := setupActionResult(model, "current-session", "auth.setup.get", current)
	model = updated.(*app)
	for _, stale := range []setupResultMsg{
		{epoch: 3, sessionID: "current-session", method: "auth.setup.get", result: setupActionView("current-session", 10, map[string]any{"id": "new", "kind": "display_code", "label": "Stale epoch", "code": "STALE-EPOCH"})},
		{epoch: 4, sessionID: "old-session", method: "auth.setup.get", result: setupActionView("old-session", 10, map[string]any{"id": "new", "kind": "display_code", "label": "Stale session", "code": "STALE-SESSION"})},
		{epoch: 4, sessionID: "current-session", method: "auth.setup.get", result: setupActionView("current-session", 8, map[string]any{"id": "old", "kind": "display_code", "label": "Stale revision", "code": "STALE-REVISION"})},
	} {
		updated, _ := model.Update(stale)
		model = updated.(*app)
		if strings.Contains(model.previewExtra, "STALE-") || !strings.Contains(model.previewExtra, "CURRENT-CODE") {
			t.Fatalf("stale setup result changed current public view: %q", model.previewExtra)
		}
	}
}

func TestSetupSameRevisionPreservesDraftAndChangedPublicActionsInvalidateIt(t *testing.T) {
	model := newApp("/tmp/gobroom.sock")
	model.raw[sectionExtensionCatalog] = setupActionCatalog(t)
	model.setupEpoch, model.setupSessionID = 5, "stable-session"
	first := setupActionView("stable-session", 6, map[string]any{"id": "submit", "kind": "input", "label": "Enter code", "inputSchemaRef": setupActionSchemaRef})
	updated, _ := setupActionResult(model, "stable-session", "auth.setup.get", first)
	model = updated.(*app)
	compile := model.openSetupInput()
	updated, _ = model.Update(compile())
	model = updated.(*app)
	if model.form == nil {
		t.Fatal("input form did not compile")
	}
	model.form.inputs[0].SetValue("draft-value")
	updated, _ = setupActionResult(model, "stable-session", "auth.setup.get", first)
	model = updated.(*app)
	if model.form == nil || model.form.inputs[0].Value() != "draft-value" {
		t.Fatalf("same revision replaced the active draft: form=%#v", model.form)
	}
	changed := setupActionView("stable-session", 6, map[string]any{"id": "submit", "kind": "input", "label": "Different action", "inputSchemaRef": setupActionSchemaRef})
	updated, _ = setupActionResult(model, "stable-session", "auth.setup.get", changed)
	model = updated.(*app)
	if model.form != nil && strings.Contains(model.form.inputs[0].Value(), "draft-value") {
		t.Fatal("changed public action digest retained input from the old view")
	}
}

func TestSetupCompletionDuringResourceEditDoesNotStealEditor(t *testing.T) {
	model := newApp("/tmp/gobroom.sock")
	model.setupEpoch, model.setupSessionID = 8, "callback-session"
	form := newConnectionForm(providerNode{ID: "provider", AuthMode: "api_key"})
	model.form, model.mode = form, modeForm
	completed := json.RawMessage(`{"sessionId":"callback-session","connectionId":"connection-setup","status":"completed","revision":2,"activation":{"status":"active","credentialCommitted":true,"progress":{"servingPublished":true}}}`)
	updated, _ := setupActionResult(model, "callback-session", "auth.setup.get", completed)
	model = updated.(*app)
	if model.form != form || model.mode != modeForm {
		t.Fatalf("late setup completion replaced unrelated resource editor: mode=%v form=%p want=%p", model.mode, model.form, form)
	}
	if model.setupSessionID != "" || !strings.Contains(model.status, "credential saved") {
		t.Fatalf("completion did not update public setup state: status=%q session=%q", model.status, model.setupSessionID)
	}
}

func TestSetupInputSubmitWipesDraftAndNeverRetainsSecretOnError(t *testing.T) {
	model := newApp("/tmp/gobroom.sock")
	model.raw[sectionExtensionCatalog] = setupActionCatalog(t)
	model.setupSessionID = "secret-session"
	model.setupEpoch = 2
	model.mode = modeBrowse
	result := setupActionView("secret-session", 3, map[string]any{"id": "submit-code", "kind": "input", "label": "Enter code", "inputSchemaRef": setupActionSchemaRef})
	updated, _ := setupActionResult(model, "secret-session", "auth.setup.get", result)
	model = updated.(*app)
	compile := model.openSetupInput()
	if compile == nil {
		t.Fatal("setup input did not schedule schema compilation")
	}
	updated, _ = model.Update(compile())
	model = updated.(*app)
	if model.form == nil || model.form.setup == nil {
		t.Fatalf("setup editor did not open: mode=%v status=%q", model.mode, model.status)
	}
	const submittedSecret = "one-time-secret-4815"
	for i := range model.form.inputs {
		model.form.inputs[i].SetValue(submittedSecret)
	}
	for i := range model.form.inputs {
		if strings.Contains(model.form.inputs[i].View(), submittedSecret) {
			t.Fatal("setup input rendered secret in clear text")
		}
	}
	_, command := model.updateKey(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if command == nil || model.form != nil {
		t.Fatalf("submit did not wipe editor before async advance: form=%#v command=%v", model.form, command)
	}
	if strings.Contains(model.status+model.previewExtra, submittedSecret) {
		t.Fatal("submitted secret was retained in status or public preview")
	}
	updated, _ = model.Update(setupResultMsg{epoch: model.setupEpoch, sessionID: "secret-session", method: "auth.setup.advance", revision: model.setupView.Revision, actionID: "submit-code", schemaRef: setupActionSchemaRef, err: errors.New("upstream rejected " + submittedSecret)})
	model = updated.(*app)
	if strings.Contains(model.status+model.previewExtra, submittedSecret) || strings.Contains(model.status, "upstream rejected") {
		t.Fatalf("remote setup failure exposed secret or raw error: status=%q preview=%q", model.status, model.previewExtra)
	}
}

func TestSetupCancelPendingBlocksEditorAndDropsOldReplies(t *testing.T) {
	model := newApp("/tmp/gobroom.sock")
	model.setupEpoch, model.setupSessionID = 4, "cancel-session"
	view := setupActionView("cancel-session", 3, map[string]any{"id": "input", "kind": "input", "inputSchemaRef": setupActionSchemaRef})
	_, _ = setupActionResult(model, "cancel-session", "auth.setup.get", view)
	oldEpoch := model.setupEpoch
	if model.cancelSetup() == nil || !model.setupCancelPending {
		t.Fatal("cancel was not admitted")
	}
	if model.openSetupInput() != nil {
		t.Fatal("editor reopened while cancellation was pending")
	}
	_, _ = model.Update(setupResultMsg{epoch: oldEpoch, sessionID: "cancel-session", method: "auth.setup.get", result: view})
	if !model.setupCancelPending || model.form != nil {
		t.Fatal("old poll changed a cancellation")
	}
	_, _ = model.Update(setupResultMsg{epoch: model.setupEpoch, sessionID: "cancel-session", method: "auth.setup.cancel", result: json.RawMessage(`{}`)})
	if model.setupCancelPending || model.setupSessionID != "" {
		t.Fatal("cancel did not clear active state")
	}
}

func TestSetupCancelBeforeStartReplyCancelsCallerAndFencesLateResult(t *testing.T) {
	model := newApp("/tmp/gobroom.sock")
	model.setupEpoch, model.setupStartPending = 9, true
	cancelled := false
	model.setupStartCancel = func() { cancelled = true }
	model.cancelSetup()
	if !cancelled || model.setupStartPending {
		t.Fatal("pending start was not canceled")
	}
	view := setupActionView("late-start", 1)
	_, _ = model.Update(setupResultMsg{epoch: 9, method: "auth.setup.start", result: view})
	if model.setupSessionID != "" {
		t.Fatal("late start result restored a canceled view")
	}
}

func TestSetupPendingCompilerDoesNotSpawnDuplicateWork(t *testing.T) {
	model := newApp("/tmp/gobroom.sock")
	model.raw[sectionExtensionCatalog] = setupActionCatalog(t)
	model.setupSessionID = "compile-session"
	_, _ = setupActionResult(model, "compile-session", "auth.setup.get", setupActionView("compile-session", 2, map[string]any{"id": "input", "kind": "input", "inputSchemaRef": setupActionSchemaRef}))
	if model.openSetupInput() == nil {
		t.Fatal("first editor compilation was not queued")
	}
	if model.openSetupInput() != nil {
		t.Fatal("repeated resume queued another compiler")
	}
}

func TestSetupLateOldStepFailureDoesNotDestroyNewDraft(t *testing.T) {
	model := newApp("/tmp/gobroom.sock")
	model.raw[sectionExtensionCatalog] = setupActionCatalog(t)
	model.setupEpoch, model.setupSessionID = 3, "callback-step"
	_, _ = setupActionResult(model, "callback-step", "auth.setup.get", setupActionView("callback-step", 2, map[string]any{"id": "new-input", "kind": "input", "inputSchemaRef": setupActionSchemaRef}))
	compile := model.openSetupInput()
	_, _ = model.Update(compile())
	model.form.inputs[0].SetValue("new-step-draft")
	form := model.form
	for _, old := range []setupResultMsg{
		{epoch: 3, sessionID: "callback-step", method: "auth.setup.advance", revision: 1, actionID: "old-input", schemaRef: setupActionSchemaRef, err: errors.New("late old step")},
		{epoch: 3, sessionID: "callback-step", method: "auth.setup.get", revision: 1, err: errors.New("late old poll")},
		{epoch: 3, sessionID: "callback-step", method: "auth.setup.advance", revision: 2, actionID: "old-input", schemaRef: setupActionSchemaRef, err: errors.New("changed same-revision action")},
	} {
		_, _ = model.Update(old)
	}
	if model.form != form || model.form.inputs[0].Value() != "new-step-draft" {
		t.Fatal("obsolete RPC failure replaced the current editor")
	}
}
