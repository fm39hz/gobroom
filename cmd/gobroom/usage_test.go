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
	"unicode"
	"unicode/utf8"

	"github.com/fm39hz/gobroom/internal/daemon"
)

func TestUsageDetailInvokesExplicitTypedRPC(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		params map[string]any
	}{
		{name: "public extensions by default", args: []string{"usage", "detail", "41"}, params: map[string]any{"id": json.Number("41"), "includeExtensions": true, "includePrivate": false, "includeRaw": false}},
		{name: "private is explicit and implies extensions", args: []string{"usage", "detail", "42", "--include-private"}, params: map[string]any{"id": json.Number("42"), "includeExtensions": true, "includePrivate": true, "includeRaw": false}},
		{name: "raw is explicit independently", args: []string{"usage", "detail", "43", "--include-raw"}, params: map[string]any{"id": json.Number("43"), "includeExtensions": true, "includePrivate": false, "includeRaw": true}},
		{name: "private and raw can both be selected", args: []string{"usage", "detail", "44", "--include-private", "--include-raw"}, params: map[string]any{"id": json.Number("44"), "includeExtensions": true, "includePrivate": true, "includeRaw": true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			socket := filepath.Join(t.TempDir(), "daemon.sock")
			requests := make(chan daemon.IPCRequest, 1)
			server := daemon.NewIPCServer(socket, func(_ context.Context, request daemon.IPCRequest) daemon.IPCResponse {
				requests <- request
				return daemon.IPCResponse{ID: request.ID, OK: true, Result: map[string]any{"id": 41}}
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
			_, _ = io.ReadAll(reader)
			_ = reader.Close()
			if executeErr != nil {
				t.Fatal(executeErr)
			}
			request := <-requests
			if request.Method != "usage.detail" || !reflect.DeepEqual(request.Params, tt.params) {
				t.Fatalf("IPC request method=%q params=%#v, want usage.detail %#v", request.Method, request.Params, tt.params)
			}
		})
	}
}

func TestUsageListRemainsListRPC(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "daemon.sock")
	requests := make(chan daemon.IPCRequest, 1)
	server := daemon.NewIPCServer(socket, func(_ context.Context, request daemon.IPCRequest) daemon.IPCResponse {
		requests <- request
		return daemon.IPCResponse{ID: request.ID, OK: true, Result: []any{}}
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
	root.SetArgs([]string{"usage"})
	executeErr := root.Execute()
	os.Stdout = previousStdout
	_ = writer.Close()
	_, _ = io.ReadAll(reader)
	_ = reader.Close()
	if executeErr != nil {
		t.Fatal(executeErr)
	}
	request := <-requests
	if request.Method != "usage.list" || len(request.Params) != 0 {
		t.Fatalf("usage list request=%#v; want usage.list with no params", request)
	}
}

func TestUsageDetailRejectsNonPositiveOrMalformedIDBeforeIPC(t *testing.T) {
	for _, id := range []string{"0", "-1", "not-an-id", "9223372036854775808"} {
		t.Run(id, func(t *testing.T) {
			socket := filepath.Join(t.TempDir(), "daemon.sock")
			requests := make(chan daemon.IPCRequest, 1)
			server := daemon.NewIPCServer(socket, func(_ context.Context, request daemon.IPCRequest) daemon.IPCResponse {
				requests <- request
				return daemon.IPCResponse{ID: request.ID, OK: true, Result: map[string]any{}}
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := server.Start(ctx); err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			root := newRootCommand(socket, func(string, string) error { return nil })
			args := []string{"usage", "detail"}
			if id == "-1" {
				args = append(args, "--")
			}
			root.SetArgs(append(args, id))
			err := root.Execute()
			if err == nil {
				t.Fatalf("usage detail ID %q was accepted", id)
			}
			select {
			case request := <-requests:
				t.Fatalf("invalid usage detail ID %q reached IPC as %#v", id, request)
			default:
			}
		})
	}
}

func TestUsageDetailOutputEscapesAuthorizedTerminalControls(t *testing.T) {
	for _, jsonFlag := range []string{"false", "true"} {
		t.Run("json="+jsonFlag, func(t *testing.T) {
			const marker = "AUTHORIZED_USAGE_DETAIL_MARKER"
			var controls strings.Builder
			for value := rune(0); value <= 0x9f; value++ {
				if unicode.IsControl(value) {
					controls.WriteRune(value)
				}
			}
			socket := filepath.Join(t.TempDir(), "daemon.sock")
			requests := make(chan daemon.IPCRequest, 1)
			server := daemon.NewIPCServer(socket, func(_ context.Context, request daemon.IPCRequest) daemon.IPCResponse {
				requests <- request
				return daemon.IPCResponse{ID: request.ID, OK: true, Result: map[string]any{
					"id":       17,
					"private":  marker,
					"rawUsage": map[string]any{"value": marker + controls.String()},
				}}
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := server.Start(ctx); err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			output, err := executeUsageDetailCodecCLI(t, socket, []string{"--json=" + jsonFlag, "usage", "detail", "17", "--include-private", "--include-raw"})
			if err != nil {
				t.Fatal(err)
			}
			request := <-requests
			want := map[string]any{"id": json.Number("17"), "includeExtensions": true, "includePrivate": true, "includeRaw": true}
			if request.Method != "usage.detail" || !reflect.DeepEqual(request.Params, want) {
				t.Fatalf("authorized detail request=%#v, want usage.detail %#v", request, want)
			}
			if !utf8.Valid(output) || !strings.Contains(string(output), marker) {
				t.Fatalf("authorized detail was lost or malformed: %q", output)
			}
			for _, value := range string(output) {
				if unicode.IsControl(value) && value != '\n' && value != '\t' {
					t.Fatalf("authorized detail output contains terminal control U+%04X: %q", value, output)
				}
			}
		})
	}
}

func TestUsageDetailOutputIsBoundedRegardlessOfJSONFlag(t *testing.T) {
	for _, jsonFlag := range []string{"false", "true"} {
		t.Run("json="+jsonFlag, func(t *testing.T) {
			socket := filepath.Join(t.TempDir(), "daemon.sock")
			server := daemon.NewIPCServer(socket, func(_ context.Context, request daemon.IPCRequest) daemon.IPCResponse {
				return daemon.IPCResponse{ID: request.ID, OK: true, Result: map[string]any{
					"id":       17,
					"rawUsage": map[string]any{"value": strings.Repeat("x", 256<<10) + "UNBOUNDED_DETAIL_TAIL"},
				}}
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := server.Start(ctx); err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			output, err := executeUsageDetailCodecCLI(t, socket, []string{"--json=" + jsonFlag, "usage", "detail", "17", "--include-raw"})
			if err != nil {
				t.Fatal(err)
			}
			if len(output) == 0 || len(output) > 64<<10 || strings.Contains(string(output), "UNBOUNDED_DETAIL_TAIL") {
				t.Fatalf("usage detail output escaped the 64 KiB bound: bytes=%d tail=%v", len(output), strings.Contains(string(output), "UNBOUNDED_DETAIL_TAIL"))
			}
		})
	}
}

func executeUsageDetailCodecCLI(t *testing.T, socket string, args []string) ([]byte, error) {
	t.Helper()
	previousIPC, previousJSON, previousStdout := ipcPath, outputJSON, os.Stdout
	defer func() { ipcPath, outputJSON, os.Stdout = previousIPC, previousJSON, previousStdout }()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	output := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(reader)
		output <- data
	}()
	os.Stdout = writer
	root := newRootCommand(socket, func(string, string) error { return nil })
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs(args)
	err = root.Execute()
	os.Stdout = previousStdout
	_ = writer.Close()
	return <-output, err
}
