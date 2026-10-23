package tui

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/fm39hz/gobroom/internal/daemon"
)

func TestUsageDetailRequiresDAndSubmitsOnlyChosenClasses(t *testing.T) {
	const privateMarker = "PRIVATE_USAGE_MARKER_9471"
	requests := make(chan daemon.IPCRequest, 3)
	denied := true
	socket := filepath.Join(t.TempDir(), "daemon.sock")
	server := daemon.NewIPCServer(socket, func(_ context.Context, request daemon.IPCRequest) daemon.IPCResponse {
		requests <- request
		if request.Method != "usage.detail" {
			return daemon.IPCResponse{ID: request.ID, Error: "unexpected RPC: " + request.Method}
		}
		if denied {
			return daemon.IPCResponse{ID: request.ID, Error: "private usage details are disabled by host policy"}
		}
		return daemon.IPCResponse{ID: request.ID, OK: true, Result: map[string]any{
			"id":       17,
			"private":  privateMarker,
			"rawUsage": map[string]any{"value": "RAW_TEXT_MARKER_7246\u001b[31mred\u001b[0m"},
		}}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	model := newUsageDetailTestApp(socket)
	if len(requests) != 0 {
		t.Fatal("loading or selecting usage rows fetched private detail")
	}
	updated, command := model.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	model = updated.(*app)
	if command != nil || model.selectedEntry() == nil || model.selectedEntry().key != "17" {
		t.Fatalf("keyboard row selection did not select usage event 17: selected=%#v command=%v", model.selectedEntry(), command)
	}
	if len(requests) != 0 {
		t.Fatal("moving the usage selection fetched detail")
	}
	updated, command = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(*app)
	if command != nil || model.focus != focusInspector || model.mode != modeBrowse {
		t.Fatalf("Enter should only focus the inspector: mode=%v focus=%v command=%v", model.mode, model.focus, command)
	}
	if len(requests) != 0 {
		t.Fatal("Enter fetched usage detail without an explicit d action")
	}

	updated, command = model.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	model = updated.(*app)
	if command != nil || model.mode != modeForm || model.form == nil {
		t.Fatalf("usage d should open the detail options form: mode=%v form=%#v command=%v", model.mode, model.form, command)
	}
	if got := getUsageDetailFormValue(model.form, "includeExtensions"); got != "true" {
		t.Fatalf("public extension option default=%q, want true", got)
	}
	for _, field := range []string{"includePrivate", "includeRaw"} {
		if got := getUsageDetailFormValue(model.form, field); got != "false" {
			t.Fatalf("%s default=%q, want false", field, got)
		}
	}
	setFormValue(model.form, "includePrivate", "true")
	setFormValue(model.form, "includeRaw", "true")
	updated, command = model.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	model = updated.(*app)
	if command == nil {
		t.Fatal("submitting explicit detail options did not produce an async request")
	}
	result := command()
	message, ok := result.(actionMsg)
	if !ok {
		t.Fatalf("detail command returned %T, want actionMsg", result)
	}
	request := <-requests
	wantParams := map[string]any{"id": json.Number("17"), "includeExtensions": true, "includePrivate": true, "includeRaw": true}
	if request.Method != "usage.detail" || !reflectUsageParams(request.Params, wantParams) {
		t.Fatalf("detail request method=%q params=%#v, want usage.detail %#v", request.Method, request.Params, wantParams)
	}
	if message.err == nil {
		t.Fatal("host denial was not returned to the TUI")
	}
	updated, _ = model.Update(message)
	model = updated.(*app)
	if strings.Contains(model.lastError, privateMarker) || strings.Contains(model.detail.View(), privateMarker) {
		t.Fatal("denied detail exposed the private marker")
	}

	denied = false
	updated, command = model.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	model = updated.(*app)
	if model.mode != modeForm || command != nil {
		t.Fatalf("second explicit d action did not reopen options: mode=%v command=%v", model.mode, command)
	}
	setFormValue(model.form, "includePrivate", "true")
	setFormValue(model.form, "includeRaw", "true")
	updated, command = model.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	model = updated.(*app)
	if command == nil {
		t.Fatal("submitting granted detail options did not produce an async request")
	}
	result = command()
	message, ok = result.(actionMsg)
	if !ok || message.err != nil {
		t.Fatalf("granted detail request result=%#v, want successful actionMsg", result)
	}
	request = <-requests
	if request.Method != "usage.detail" || !reflectUsageParams(request.Params, wantParams) {
		t.Fatalf("granted detail request method=%q params=%#v", request.Method, request.Params)
	}
	updated, _ = model.Update(message)
	model = updated.(*app)
	view := model.detail.View()
	if !strings.Contains(view, privateMarker) || !strings.Contains(view, "RAW_TEXT_MARKER_7246") || strings.Contains(view, "\x1b[31m") {
		t.Fatalf("explicit detail was not shown as safe text: %q", view)
	}
}

func TestUsageDetailKeyDoesNotReplaceConnectionDelete(t *testing.T) {
	model := newApp(filepath.Join(t.TempDir(), "unused.sock"))
	model.activePanel = int(dashboardConnections)
	model.setPaneItems(model.activeContextIndex(), []entry{{key: "connection-1", title: "account", payload: connection{ID: "connection-1"}}})
	updated, command := model.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	model = updated.(*app)
	if command != nil || model.mode != modeConfirm || model.confirmMethod != "connections.delete" {
		t.Fatalf("connection d action changed: mode=%v confirmMethod=%q command=%v", model.mode, model.confirmMethod, command)
	}
}

func newUsageDetailTestApp(socket string) *app {
	model := newApp(socket)
	model.activePanel = int(dashboardRuntime)
	model.activeTabs[dashboardRuntime] = 0
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*app)
	_ = model.setPaneItems(model.activeContextIndex(), []entry{
		{key: "16", title: "model-before", detail: "Usage event", payload: usageRecord{ID: 16, LogicalModel: "model-before"}},
		{key: "17", title: "model-a", detail: "Usage event", payload: usageRecord{ID: 17, LogicalModel: "model-a"}},
	})
	return model
}

func getUsageDetailFormValue(form *formState, key string) string {
	for index, field := range form.fields {
		if field.key == key {
			return form.inputs[index].Value()
		}
	}
	return "<missing>"
}

func reflectUsageParams(got, want map[string]any) bool {
	return reflect.DeepEqual(got, want)
}
