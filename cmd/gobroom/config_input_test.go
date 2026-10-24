package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/daemon"
)

func TestConfigCommandsPreserveLargeNumbers(t *testing.T) {
	const input = `{"options":{"large":900719925474099312345,"nested":{"version":18446744073709551615}},"contractVersion":18446744073709551615}`
	want := map[string]any{
		"options": map[string]any{
			"large":  json.Number("900719925474099312345"),
			"nested": map[string]any{"version": json.Number("18446744073709551615")},
		},
		"contractVersion": json.Number("18446744073709551615"),
	}
	commands := []struct{ name, method string }{
		{"validate", "config.validate"}, {"diff", "config.diff"}, {"apply", "config.apply"},
	}
	for _, command := range commands {
		t.Run(command.name, func(t *testing.T) {
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
			root.SetArgs([]string{"config-" + command.name})
			root.SetIn(strings.NewReader(input))
			var executeErr error
			if cli := os.Getenv("GOBROOM_TEST_CLI"); cli != "" {
				commandCtx, cancelCommand := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancelCommand()
				external := exec.CommandContext(commandCtx, cli, "--ipc", socket, "config-"+command.name)
				external.Stdin = strings.NewReader(input)
				_, executeErr = external.CombinedOutput()
			} else {
				executeErr = root.Execute()
			}
			os.Stdout = previousStdout
			_ = writer.Close()
			_, _ = io.ReadAll(reader)
			_ = reader.Close()
			if executeErr != nil {
				t.Fatal(executeErr)
			}
			request := <-requests
			if request.Method != command.method || !reflect.DeepEqual(request.Params, want) {
				t.Fatalf("IPC request method=%q params=%#v, want method=%q params=%#v", request.Method, request.Params, command.method, want)
			}
		})
	}
}

func TestConfigCommandsRejectInvalidInputBeforeIPC(t *testing.T) {
	invalid := []string{
		`{`, `null`, `[]`, `{} {}`, `{} trailing`,
	}
	commands := []string{"config-validate", "config-diff", "config-apply"}
	for _, command := range commands {
		for _, input := range invalid {
			t.Run(command+"/"+input, func(t *testing.T) {
				socket := filepath.Join(t.TempDir(), "daemon.sock")
				requests := make(chan daemon.IPCRequest, 1)
				server := daemon.NewIPCServer(socket, func(_ context.Context, request daemon.IPCRequest) daemon.IPCResponse {
					requests <- request
					return daemon.IPCResponse{ID: request.ID, OK: true}
				})
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if err := server.Start(ctx); err != nil {
					t.Fatal(err)
				}
				defer server.Close()
				root := newRootCommand(socket, func(string, string) error { return nil })
				root.SetArgs([]string{command})
				root.SetIn(strings.NewReader(input))
				if err := root.Execute(); err == nil {
					t.Fatal("invalid config input accepted")
				}
				select {
				case request := <-requests:
					t.Fatalf("invalid input reached IPC: method=%q", request.Method)
				default:
				}
			})
		}
	}
}

func TestDecodeJSONInputEnforcesIPCSizeLimit(t *testing.T) {
	if _, err := decodeJSONInput(strings.NewReader("{}" + strings.Repeat(" ", maxJSONInputBytes))); err == nil {
		t.Fatal("oversized config input accepted")
	}
}

func TestRouteExplainPreservesRequestNumberTokens(t *testing.T) {
	for _, source := range []string{"stdin", "file"} {
		t.Run(source, func(t *testing.T) {
			socket := filepath.Join(t.TempDir(), "daemon.sock")
			requests := make(chan daemon.IPCRequest, 1)
			server := daemon.NewIPCServer(socket, func(_ context.Context, r daemon.IPCRequest) daemon.IPCResponse {
				requests <- r
				return daemon.IPCResponse{ID: r.ID, OK: true, Result: []any{}}
			})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if err := server.Start(ctx); err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			priorIPC, priorJSON := ipcPath, outputJSON
			ipcPath, outputJSON = socket, true
			defer func() { ipcPath, outputJSON = priorIPC, priorJSON }()
			const input = `{"model":"role","reasoning":{"max_output_tokens":2048.0000000000001}}`
			path := "-"
			if source == "file" {
				path = filepath.Join(t.TempDir(), "request.json")
				if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cmd := newRootCommand(socket, func(string, string) error { return nil })
			cmd.SetArgs([]string{"route-explain", "--model", "role", "--request-file", path})
			cmd.SetIn(strings.NewReader(input))
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			request := <-requests
			encoded, err := json.Marshal(request.Params)
			if err != nil {
				t.Fatal(err)
			}
			var observed struct {
				Request struct {
					Reasoning struct {
						Budget json.Number `json:"max_output_tokens"`
					} `json:"reasoning"`
				} `json:"request"`
			}
			if err := json.Unmarshal(encoded, &observed); err != nil {
				t.Fatal(err)
			}
			if observed.Request.Reasoning.Budget.String() != "2048.0000000000001" {
				t.Fatalf("CLI rounded request before IPC: %s", encoded)
			}
		})
	}
}
