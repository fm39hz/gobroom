package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/daemon"
)

func TestConnectionSetupCommandsInvokeTypedRPCs(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		method string
		input  string
		params map[string]any
	}{
		{name: "start", args: []string{"connections", "setup", "start", "--connection-id", "conn-1"}, method: "auth.setup.start", params: map[string]any{"connectionID": "conn-1"}},
		{name: "get", args: []string{"connections", "setup", "get", "--session-id", "session-1"}, method: "auth.setup.get", params: map[string]any{"sessionID": "session-1"}},
		{name: "cancel", args: []string{"connections", "setup", "cancel", "--session-id", "session-2"}, method: "auth.setup.cancel", params: map[string]any{"sessionID": "session-2"}},
		{name: "submit file", args: []string{"connections", "setup", "submit", "--session-id", "session-3", "--action-id", "action-1", "--revision", "42", "--input-file", "-"}, input: `{"apiKey":"hidden-secret","nested":{"enabled":true},"account":9007199254740993}`, method: "auth.setup.advance", params: map[string]any{"sessionID": "session-3", "actionID": "action-1", "revision": json.Number("42"), "value": map[string]any{"apiKey": "hidden-secret", "nested": map[string]any{"enabled": true}, "account": json.Number("9007199254740993")}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			socket := filepath.Join(t.TempDir(), "daemon.sock")
			requests := make(chan daemon.IPCRequest, 1)
			server := daemon.NewIPCServer(socket, func(_ context.Context, request daemon.IPCRequest) daemon.IPCResponse {
				requests <- request
				return daemon.IPCResponse{ID: request.ID, OK: true, Result: map[string]any{"status": "ok"}}
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := server.Start(ctx); err != nil {
				t.Fatal(err)
			}
			defer server.Close()

			previousIPC, previousJSON := ipcPath, outputJSON
			ipcPath, outputJSON = socket, true
			defer func() { ipcPath, outputJSON = previousIPC, previousJSON }()
			previousStdout := os.Stdout
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			os.Stdout = writer
			root := newRootCommand(socket, func(string, string) error { return nil })
			root.SetArgs(tt.args)
			root.SetIn(strings.NewReader(tt.input))
			executeErr := root.Execute()
			os.Stdout = previousStdout
			_ = writer.Close()
			_, _ = io.ReadAll(reader)
			_ = reader.Close()
			if executeErr != nil {
				t.Fatal(executeErr)
			}
			request := <-requests
			if request.Method != tt.method || !reflect.DeepEqual(request.Params, tt.params) {
				t.Fatalf("IPC request method=%q params=%#v, want method=%q params=%#v", request.Method, request.Params, tt.method, tt.params)
			}
		})
	}
}

func TestSetupSubmitRejectsInvalidInputWithoutEchoingIt(t *testing.T) {
	secret := "sensitive-value"
	tests := []struct {
		name  string
		input string
	}{
		{name: "malformed", input: `{"secret":"` + secret},
		{name: "array", input: `["` + secret + `"]`},
		{name: "null", input: `null`},
		{name: "scalar", input: `"` + secret + `"`},
		{name: "trailing document", input: `{"ok":true} {"secret":"` + secret + `"}`},
		{name: "oversized", input: `{"secret":"` + secret + strings.Repeat("x", maxSetupInputBytes) + `"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := readSetupValue(strings.NewReader(tt.input), "-")
			if err == nil {
				t.Fatal("invalid setup input was accepted")
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), tt.input) {
				t.Fatalf("setup input error exposed provided input: %v", err)
			}
		})
	}
}

func TestSetupRequiredFlagsRejectBeforeIPC(t *testing.T) {
	for _, args := range [][]string{
		{"connections", "setup", "start"},
		{"connections", "setup", "get"},
		{"connections", "setup", "cancel"},
		{"connections", "setup", "submit"},
		{"connections", "setup", "submit", "--session-id", "s"},
		{"connections", "setup", "submit", "--session-id", "s", "--action-id", "a"},
		{"connections", "setup", "submit", "--session-id", "s", "--action-id", "a", "--input-file", "-"},
	} {
		root := newRootCommand(filepath.Join(t.TempDir(), "missing.sock"), func(string, string) error { return nil })
		root.SetArgs(args)
		if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "required flag") {
			t.Fatalf("args %v error=%v, want missing required flag before IPC", args, err)
		}
	}
}
