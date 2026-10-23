package tui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/fm39hz/gobroom/internal/daemon"
)

func TestUsageDetailOutputRejectsAllTerminalControls(t *testing.T) {
	socket, arrived := newUsageCodecIPC(t)
	model := newUsageDetailTestApp(socket)
	results := startUsageCodecCommand(beginUsageCodecRequest(t, model, "17"))
	pending := awaitUsageCodecRequest(t, arrived)
	assertUsageCodecRequest(t, pending.request, "17")
	const marker = "AUTHORIZED_TUI_DETAIL_MARKER"
	var hostile strings.Builder
	hostile.WriteString(marker)
	for value := rune(0); value <= 0x9f; value++ {
		if unicode.IsControl(value) {
			hostile.WriteString("L")
			hostile.WriteRune(value)
			hostile.WriteString("R")
		}
	}
	pending.reply <- daemon.IPCResponse{ID: pending.request.ID, OK: true, Result: map[string]any{
		"id": 17, "private": marker, "rawUsage": map[string]any{"value": hostile.String()},
	}}
	_, _ = model.Update(awaitUsageCodecResult(t, results))
	if !strings.Contains(model.previewExtra, marker) || !strings.Contains(model.detail.View(), marker) {
		t.Fatalf("authorized detail was lost: preview=%q view=%q", model.previewExtra, model.detail.View())
	}
	assertUsageCodecSafeText(t, "detail content", model.previewExtra)
	assertUsageCodecSafeText(t, "visible detail", ansi.Strip(model.detail.View()))
}

func TestUsageDetailRenderedTextIsBoundedAfterEscaping(t *testing.T) {
	for _, fixture := range []string{strings.Repeat("x", 256<<10), strings.Repeat("\u007f", 20<<10)} {
		raw, err := json.Marshal(map[string]any{"rawUsage": map[string]any{"value": fixture}})
		if err != nil {
			t.Fatal(err)
		}
		text := formatUsageDetail(raw)
		if len(text) == 0 || len(text) > 64<<10 {
			t.Fatalf("detail display exceeded its 64 KiB bound after escaping: bytes=%d", len(text))
		}
		assertUsageCodecSafeText(t, "bounded detail", text)
	}
}

func TestUsageDetailOverlappingRepliesStayWithRequestRowAndEpoch(t *testing.T) {
	tests := []struct {
		name          string
		currentID     string
		staleDenied   bool
		currentDenied bool
		staleFirst    bool
	}{
		{name: "row16 then stale row17 success", currentID: "16"},
		{name: "row16 denial then stale row17 success", currentID: "16", currentDenied: true},
		{name: "row16 success then stale row17 denial", currentID: "16", staleDenied: true},
		{name: "stale row17 success while row16 pending", currentID: "16", staleFirst: true},
		{name: "stale row17 denial while row16 pending", currentID: "16", staleDenied: true, staleFirst: true},
		{name: "same row newer success beats older success", currentID: "17"},
		{name: "same row newer denial beats older success", currentID: "17", currentDenied: true},
		{name: "same row newer success survives older denial", currentID: "17", staleDenied: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			socket, arrived := newUsageCodecIPC(t)
			model := newUsageDetailTestApp(socket)
			staleResults := startUsageCodecCommand(beginUsageCodecRequest(t, model, "17"))
			stale := awaitUsageCodecRequest(t, arrived)
			assertUsageCodecRequest(t, stale.request, "17")
			currentResults := startUsageCodecCommand(beginUsageCodecRequest(t, model, tt.currentID))
			current := awaitUsageCodecRequest(t, arrived)
			assertUsageCodecRequest(t, current.request, tt.currentID)
			const staleMarker = "STALE_PRIVATE_ROW17_MARKER"
			const currentMarker = "CURRENT_PRIVATE_DETAIL_MARKER"

			applyStale := func() {
				beforeView, beforePreview := model.detail.View(), model.previewExtra
				beforeStatus, beforeError := model.status, model.lastError
				beforeLoading, beforeMode := model.loading, model.mode
				replyUsageCodec(t, stale, staleResults, model, tt.staleDenied, staleMarker)
				if model.detail.View() != beforeView || model.previewExtra != beforePreview || model.status != beforeStatus || model.lastError != beforeError || model.loading != beforeLoading || model.mode != beforeMode {
					t.Fatalf("stale detail reply mutated the current row: selected=%#v preview=%q status=%q loading=%v", model.selectedEntry(), model.previewExtra, model.status, model.loading)
				}
				if strings.Contains(model.previewExtra, staleMarker) || strings.Contains(model.detail.View(), staleMarker) {
					t.Fatal("stale row17 private detail attached to the current row")
				}
			}
			applyCurrent := func() {
				replyUsageCodec(t, current, currentResults, model, tt.currentDenied, currentMarker)
				if model.loading || model.mode != modeBrowse {
					t.Fatalf("current detail reply was not consumed: loading=%v mode=%v", model.loading, model.mode)
				}
				view := model.detail.View()
				if strings.Contains(view, staleMarker) || strings.Contains(model.previewExtra, staleMarker) {
					t.Fatal("current detail view contains the old row17 private marker")
				}
				if tt.currentDenied {
					if strings.Contains(view, currentMarker) || strings.Contains(model.previewExtra, currentMarker) {
						t.Fatal("denied current detail exposed private data")
					}
				} else if !strings.Contains(view, currentMarker) {
					t.Fatalf("current authorized detail was not shown for row %s: %q", tt.currentID, view)
				}
			}
			if tt.staleFirst {
				applyStale()
				applyCurrent()
			} else {
				applyCurrent()
				applyStale()
			}
		})
	}
}

func TestConnectionSetupPreviewRemainsVisibleAfterAcceptedUsageDetail(t *testing.T) {
	socket, arrived := newUsageCodecIPC(t)
	model := newUsageDetailTestApp(socket)
	usageResults := startUsageCodecCommand(beginUsageCodecRequest(t, model, "17"))
	usagePending := awaitUsageCodecRequest(t, arrived)
	assertUsageCodecRequest(t, usagePending.request, "17")
	const privateMarker = "PREVIOUS_USAGE17_PRIVATE_MARKER"
	replyUsageCodec(t, usagePending, usageResults, model, false, privateMarker)
	if !strings.Contains(model.previewExtra, privateMarker) || !strings.Contains(model.detail.View(), privateMarker) {
		t.Fatalf("test did not accept and display Usage17 detail: preview=%q view=%q", model.previewExtra, model.detail.View())
	}
	if model.focus == focusInspector {
		_, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	}
	_, _ = model.Update(tea.KeyPressMsg{Code: '2', Text: "2"})
	if model.active().definition.view != "connections" {
		t.Fatalf("keyboard did not navigate to Connections: %s", model.active().definition.view)
	}
	const connectionID = "connection-setup-target"
	_ = model.setPaneItems(model.activeContextIndex(), []entry{{key: connectionID, title: "Setup target", detail: "Connection setup target", payload: connection{ID: connectionID, Enabled: true}}})
	if strings.Contains(model.detail.View(), privateMarker) {
		t.Fatal("accepted Usage detail leaked into Connections before setup")
	}
	_, command := model.Update(tea.KeyPressMsg{Code: 'D', Text: "D"})
	if command == nil || !model.setupStartPending {
		t.Fatalf("D did not start connection setup: pending=%v command=%v", model.setupStartPending, command)
	}
	setupResults := startUsageCodecCommand(command)
	setupPending := awaitUsageCodecRequest(t, arrived)
	if setupPending.request.Method != "auth.setup.start" || setupPending.request.Params["connectionID"] != connectionID {
		t.Fatalf("setup request targeted the wrong operation or connection: %#v", setupPending.request)
	}
	const publicCode = "PUBLIC-SETUP-CODE-3815"
	const publicURL = "https://identity.test/activate-3815"
	setupPending.reply <- daemon.IPCResponse{ID: setupPending.request.ID, OK: true, Result: map[string]any{
		"sessionId": "setup-after-usage", "connectionId": connectionID, "status": "awaiting_user", "revision": 1,
		"actions": []map[string]any{
			{"id": "show-code", "kind": "display_code", "label": "Enter this code", "code": publicCode},
			{"id": "open-login", "kind": "open_url", "label": "Open sign-in", "url": publicURL},
		},
	}}
	_, _ = model.Update(awaitUsageCodecResult(t, setupResults))
	if model.setupSessionID != "setup-after-usage" || model.setupStartPending {
		t.Fatalf("setup reply was not accepted: session=%q pending=%v", model.setupSessionID, model.setupStartPending)
	}
	selected := model.selectedEntry()
	if selected == nil || selected.key != connectionID {
		t.Fatalf("setup target selection changed: %#v", selected)
	}
	preview, visible := model.mainPreview(*selected), model.detail.View()
	for _, expected := range []string{publicCode, publicURL} {
		if !strings.Contains(model.previewExtra, expected) || !strings.Contains(preview, expected) || !strings.Contains(visible, expected) {
			t.Fatalf("new setup action %q hidden after accepted Usage detail: stored=%q preview=%q view=%q", expected, model.previewExtra, preview, visible)
		}
	}
	if strings.Contains(model.previewExtra, privateMarker) || strings.Contains(preview, privateMarker) || strings.Contains(visible, privateMarker) {
		t.Fatal("new connection setup preview retained the old private Usage detail")
	}
}

func TestAcceptedUsageDetailDoesNotRenderInAnotherResourceWithSameKey(t *testing.T) {
	tests := []struct {
		view    string
		keys    []rune
		payload any
	}{
		{view: "quota", keys: []rune{']'}, payload: quotaSnapshot{}},
		{view: "logs", keys: []rune{'5', ']'}, payload: logRecord{Message: "Public log entry"}},
	}
	for _, tt := range tests {
		t.Run(tt.view, func(t *testing.T) {
			socket, arrived := newUsageCodecIPC(t)
			model := newUsageDetailTestApp(socket)
			results := startUsageCodecCommand(beginUsageCodecRequest(t, model, "17"))
			pending := awaitUsageCodecRequest(t, arrived)
			assertUsageCodecRequest(t, pending.request, "17")
			const privateMarker = "ACCEPTED_USAGE17_PRIVATE_MARKER"
			replyUsageCodec(t, pending, results, model, false, privateMarker)
			if !strings.Contains(model.previewExtra, privateMarker) || !strings.Contains(model.detail.View(), privateMarker) {
				t.Fatalf("test did not first accept and display Usage detail17: preview=%q view=%q", model.previewExtra, model.detail.View())
			}
			if model.focus == focusInspector {
				_, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
			}
			for _, code := range tt.keys {
				_, _ = model.Update(tea.KeyPressMsg{Code: code, Text: string(code)})
			}
			if model.active().definition.view != tt.view {
				t.Fatalf("keyboard did not navigate to %s: got %s", tt.view, model.active().definition.view)
			}
			const publicMarker = "CURRENT_RESOURCE_PUBLIC_DETAIL"
			_ = model.setPaneItems(model.activeContextIndex(), []entry{{key: "17", title: tt.view, detail: publicMarker, payload: tt.payload}})
			selected := model.selectedEntry()
			if selected == nil || selected.key != "17" {
				t.Fatalf("test did not select %s row17: %#v", tt.view, selected)
			}
			preview, visible := model.mainPreview(*selected), model.detail.View()
			if !strings.Contains(preview, publicMarker) || !strings.Contains(visible, publicMarker) {
				t.Fatalf("current %s resource is not visible: preview=%q view=%q", tt.view, preview, visible)
			}
			if strings.Contains(preview, privateMarker) || strings.Contains(visible, privateMarker) {
				t.Fatalf("accepted Usage17 private detail rendered under %s row17: preview=%q view=%q", tt.view, preview, visible)
			}
		})
	}
}

func TestUsageDetailReplyIsRejectedAfterSelectionOrPaneChange(t *testing.T) {
	for _, target := range []string{"other usage row", "quota pane with matching row key"} {
		t.Run(target, func(t *testing.T) {
			socket, arrived := newUsageCodecIPC(t)
			model := newUsageDetailTestApp(socket)
			results := startUsageCodecCommand(beginUsageCodecRequest(t, model, "17"))
			pending := awaitUsageCodecRequest(t, arrived)
			assertUsageCodecRequest(t, pending.request, "17")
			if model.focus == focusInspector {
				_, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
			}
			if target == "other usage row" {
				_, _ = model.Update(tea.KeyPressMsg{Code: 'k', Text: "k"})
				if model.selectedEntry() == nil || model.selectedEntry().key != "16" {
					t.Fatal("test did not move to usage row16")
				}
			} else {
				_, _ = model.Update(tea.KeyPressMsg{Code: ']', Text: "]"})
				if model.active().definition.view != "quota" {
					t.Fatal("test did not leave the usage pane")
				}
				_ = model.setPaneItems(model.activeContextIndex(), []entry{{key: "17", title: "quota", detail: "Quota detail", payload: quotaSnapshot{}}})
			}
			beforeView, beforePreview, beforeStatus := model.detail.View(), model.previewExtra, model.status
			replyUsageCodec(t, pending, results, model, false, "WRONG_CONTEXT_PRIVATE_MARKER")
			if model.detail.View() != beforeView || model.previewExtra != beforePreview || model.status != beforeStatus {
				t.Fatalf("detail reply mutated a different selection or pane: preview=%q view=%q status=%q", model.previewExtra, model.detail.View(), model.status)
			}
		})
	}
}

type usageCodecPending struct {
	request daemon.IPCRequest
	reply   chan daemon.IPCResponse
}

func newUsageCodecIPC(t *testing.T) (string, <-chan usageCodecPending) {
	t.Helper()
	directory, err := os.MkdirTemp("", "gb-codec-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socket := filepath.Join(directory, "daemon.sock")
	arrived := make(chan usageCodecPending, 4)
	ctx, cancel := context.WithCancel(context.Background())
	server := daemon.NewIPCServer(socket, func(requestContext context.Context, request daemon.IPCRequest) daemon.IPCResponse {
		pending := usageCodecPending{request: request, reply: make(chan daemon.IPCResponse, 1)}
		select {
		case arrived <- pending:
		case <-requestContext.Done():
			return daemon.IPCResponse{ID: request.ID, Error: "test request canceled"}
		}
		select {
		case response := <-pending.reply:
			return response
		case <-requestContext.Done():
			return daemon.IPCResponse{ID: request.ID, Error: "test request canceled"}
		}
	})
	if err := server.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = server.Close() })
	return socket, arrived
}

func beginUsageCodecRequest(t *testing.T, model *app, id string) tea.Cmd {
	t.Helper()
	if model.focus == focusInspector {
		_, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	}
	if selected := model.selectedEntry(); selected == nil || selected.key != id {
		code := 'j'
		if id == "16" {
			code = 'k'
		}
		_, _ = model.Update(tea.KeyPressMsg{Code: code, Text: string(code)})
	}
	if selected := model.selectedEntry(); selected == nil || selected.key != id {
		t.Fatalf("keyboard did not select usage row %s: %#v", id, selected)
	}
	_, command := model.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	if command != nil || model.mode != modeForm || model.form == nil {
		t.Fatalf("d did not open usage detail options: mode=%v form=%#v", model.mode, model.form)
	}
	setFormValue(model.form, "includePrivate", "true")
	setFormValue(model.form, "includeRaw", "true")
	_, command = model.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if command == nil {
		t.Fatal("detail submission did not return an async command")
	}
	return command
}

func startUsageCodecCommand(command tea.Cmd) <-chan tea.Msg {
	results := make(chan tea.Msg, 1)
	go func() { results <- command() }()
	return results
}

func awaitUsageCodecRequest(t *testing.T, arrived <-chan usageCodecPending) usageCodecPending {
	t.Helper()
	select {
	case pending := <-arrived:
		return pending
	case <-time.After(5 * time.Second):
		t.Fatal("detail command did not reach the fake IPC server")
		return usageCodecPending{}
	}
}

func awaitUsageCodecResult(t *testing.T, results <-chan tea.Msg) tea.Msg {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("detail command did not return after its IPC reply")
		return nil
	}
}

func replyUsageCodec(t *testing.T, pending usageCodecPending, results <-chan tea.Msg, model *app, denied bool, marker string) {
	t.Helper()
	response := daemon.IPCResponse{ID: pending.request.ID, OK: true, Result: map[string]any{"id": pending.request.Params["id"], "private": marker}}
	if denied {
		response = daemon.IPCResponse{ID: pending.request.ID, Error: "usage detail disclosure denied"}
	}
	pending.reply <- response
	_, _ = model.Update(awaitUsageCodecResult(t, results))
}

func assertUsageCodecRequest(t *testing.T, request daemon.IPCRequest, id string) {
	t.Helper()
	want := map[string]any{"id": json.Number(id), "includeExtensions": true, "includePrivate": true, "includeRaw": true}
	if request.Method != "usage.detail" || !reflectUsageParams(request.Params, want) {
		t.Fatalf("detail request=%#v, want usage.detail %#v", request, want)
	}
}

func assertUsageCodecSafeText(t *testing.T, label, text string) {
	t.Helper()
	if !utf8.ValidString(text) {
		t.Fatalf("%s is not valid UTF-8", label)
	}
	for _, value := range text {
		if unicode.IsControl(value) && value != '\n' && value != '\t' {
			t.Fatalf("%s contains terminal control U+%04X: %q", label, value, text)
		}
	}
}
