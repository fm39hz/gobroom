package tui

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAuthCompletionWithoutActivationIsNotReady(t *testing.T) {
	message, poll, hint := authActivationCompletion("OAuth", "connection", nil)
	if poll || !strings.Contains(message, "unavailable") || strings.Contains(message, "credential saved") || !strings.Contains(hint, "activation-get") {
		t.Fatalf("missing activation was accepted as ready: %q %v %q", message, poll, hint)
	}
}

func TestAuthCompletionWaitsForCredentialActivation(t *testing.T) {
	flows := []struct {
		name      string
		method    string
		sessionID func(*app) string
	}{
		{name: "generic setup", method: "auth.setup.get", sessionID: func(model *app) string { return model.setupSessionID }},
	}
	for _, flow := range flows {
		for _, state := range []struct {
			name       string
			activation string
			wantStatus string
			poll       bool
			hint       bool
		}{
			{name: "pending", activation: `"status":"pending","credentialCommitted":true,"progress":{"servingPublished":false}`, wantStatus: "credential committed; activation pending", poll: true},
			{name: "active", activation: `"status":"active","credentialCommitted":true,"progress":{"servingPublished":true}`, wantStatus: "connection credential saved"},
			{name: "exhausted", activation: `"status":"exhausted","credentialCommitted":true,"progress":{"servingPublished":true}`, wantStatus: "credential committed; activation exhausted", hint: true},
		} {
			t.Run(flow.name+"/"+state.name, func(t *testing.T) {
				model := newApp("/tmp/gobroom.sock")
				sessionID := "activation-session"
				model.setupSessionID = sessionID
				result := json.RawMessage(`{"sessionId":"` + sessionID + `","connectionId":"connection-42","status":"completed","revision":1,"activation":{` + state.activation + `}}`)
				updated, next := model.Update(setupResultMsg{epoch: model.setupEpoch, sessionID: sessionID, method: flow.method, result: result})
				model = updated.(*app)
				if !strings.Contains(model.status, state.wantStatus) {
					t.Fatalf("status=%q, want substring %q", model.status, state.wantStatus)
				}
				if state.poll {
					if next == nil || flow.sessionID(model) != sessionID {
						t.Fatalf("pending activation stopped polling: command=%v session=%q", next, flow.sessionID(model))
					}
				} else if next != nil || flow.sessionID(model) != "" {
					t.Fatalf("terminal activation kept polling: command=%v session=%q", next, flow.sessionID(model))
				}
				if state.hint && !strings.Contains(model.previewExtra, "gobroom connections activation-retry --connection-id connection-42") {
					t.Fatalf("exhausted activation inspector lacks retry command: %q", model.previewExtra)
				}
			})
		}
	}
}
