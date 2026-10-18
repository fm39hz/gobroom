package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/fm39hz/gobroom/internal/daemon"
)

func TestConnectionActivationCommandsInvokeTypedRPCs(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		method string
		params map[string]any
	}{
		{name: "list default limit", args: []string{"connections", "activation-list"}, method: "credential_activation.list", params: map[string]any{"limit": json.Number("128")}},
		{name: "list selected limit", args: []string{"connections", "activation-list", "--limit", "12"}, method: "credential_activation.list", params: map[string]any{"limit": json.Number("12")}},
		{name: "get", args: []string{"connections", "activation-get", "--connection-id", "conn-1"}, method: "credential_activation.get", params: map[string]any{"connectionID": "conn-1"}},
		{name: "retry", args: []string{"connections", "activation-retry", "--connection-id", "conn-2"}, method: "credential_activation.retry", params: map[string]any{"connectionID": "conn-2"}},
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
			executeErr := root.Execute()
			os.Stdout = previousStdout
			_ = writer.Close()
			if _, err := io.ReadAll(reader); err != nil {
				t.Fatal(err)
			}
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
