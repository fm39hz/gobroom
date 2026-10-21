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

func TestTransformCommandsAreDiscoverable(t *testing.T) {
	root := newRootCommand(filepath.Join(t.TempDir(), "missing.sock"), func(string, string) error { return nil })
	for _, path := range [][]string{{"transforms", "list"}, {"transforms", "upsert"}, {"transforms", "delete"}} {
		command, _, err := root.Find(path)
		if err != nil || command == root {
			t.Fatalf("transform command %v missing: command=%v err=%v", path, command, err)
		}
	}
}

func TestTransformBindingsInvokeTypedRPCs(t *testing.T) {
	const input = `{"id":"binding-1","transformRef":{"kind":"request_transform","id":"fixture.normalize","contractVersion":1},"enabled":true,"scope":{"kind":"provider","id":"provider-1"},"order":2,"options":{"large":900719925474099312345,"nested":{"ratio":1.2300}}}`
	tests := []struct {
		name   string
		args   []string
		input  string
		method string
		params map[string]any
	}{
		{
			name: "list", args: []string{"transforms", "list"}, method: "transform_bindings.list",
		},
		{
			name: "upsert stdin", args: []string{"transforms", "upsert", "--file", "-"}, input: input,
			method: "transform_bindings.upsert",
			params: map[string]any{
				"id": "binding-1", "transformRef": map[string]any{"kind": "request_transform", "id": "fixture.normalize", "contractVersion": json.Number("1")},
				"enabled": true, "scope": map[string]any{"kind": "provider", "id": "provider-1"}, "order": json.Number("2"),
				"options": map[string]any{"large": json.Number("900719925474099312345"), "nested": map[string]any{"ratio": json.Number("1.2300")}},
			},
		},
		{
			name: "delete", args: []string{"transforms", "delete", "--id", "binding-2"}, method: "transform_bindings.delete",
			params: map[string]any{"id": "binding-2"},
		},
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

func TestTransformUpsertRejectsInvalidPayloadBeforeIPC(t *testing.T) {
	for _, input := range []string{
		`null`,
		`[]`,
		`{}`,
		`{"id":"b","transformRef":{"kind":"request_transform","id":"x","contractVersion":1},"unknown":true}`,
		`{"id":"b","transformRef":{"kind":"request_transform","id":"x","contractVersion":1}} {}`,
		`{"id":"b","transformRef":{"kind":"schema","id":"x","contractVersion":1}}`,
		strings.Repeat(" ", maxTransformInputBytes+1),
	} {
		root := newRootCommand(filepath.Join(t.TempDir(), "missing.sock"), func(string, string) error { return nil })
		root.SetArgs([]string{"transforms", "upsert", "--file", "-"})
		root.SetIn(strings.NewReader(input))
		if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "transform binding") {
			t.Fatalf("invalid payload accepted or reached IPC: err=%v", err)
		}
	}
}
