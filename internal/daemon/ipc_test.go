package daemon

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func ipcFixtureHandler(_ context.Context, request IPCRequest) IPCResponse {
	return IPCResponse{ID: request.ID, OK: true, Result: "owned"}
}

func TestIPCProcessHelper(t *testing.T) {
	path := os.Getenv("GOBROOM_TEST_IPC_OWNER_PATH")
	if path == "" {
		return
	}
	server := NewIPCServer(path, ipcFixtureHandler)
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	_, _ = fmt.Fprintln(os.Stdout, "ready")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}

func TestIPCSocketOwnershipSurvivesContentionAndOwnerCrash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestIPCProcessHelper$")
	command.Env = append(os.Environ(), "GOBROOM_TEST_IPC_OWNER_PATH="+path)
	command.Stderr = os.Stderr
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	})
	ready, err := bufio.NewReader(output).ReadString('\n')
	if err != nil || ready != "ready\n" {
		t.Fatalf("owner subprocess did not start: output=%q err=%v", ready, err)
	}
	identity, err := os.Lstat(path)
	if err != nil || identity.Mode().Perm() != 0o600 {
		t.Fatalf("owner socket permissions: identity=%v err=%v", identity, err)
	}
	contender := NewIPCServer(path, ipcFixtureHandler)
	if err := contender.Start(ctx); !errors.Is(err, ErrIPCInUse) {
		t.Fatalf("second process could replace the owner socket: %v", err)
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(identity, after) {
		t.Fatalf("contender changed the active socket: %v", err)
	}
	if _, err := CallIPC(ctx, path, IPCRequest{ID: "still-owned", Method: "status"}); err != nil {
		t.Fatalf("contender broke the owner's IPC: %v", err)
	}
	// Kill bypasses Go cleanup, leaving a socket pathname and lock file behind.
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("owner unexpectedly exited normally")
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("crash fixture did not leave a stale socket: %v", err)
	}
	if err := contender.Start(ctx); err != nil {
		t.Fatalf("successor could not reclaim crash residue: %v", err)
	}
	defer contender.Close()
	if _, err := CallIPC(ctx, path, IPCRequest{ID: "successor", Method: "status"}); err != nil {
		t.Fatalf("successor IPC failed: %v", err)
	}
}

func TestIPCServerPreservesNonSocketPaths(t *testing.T) {
	for _, kind := range []string{"file", "symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "protected")
			switch kind {
			case "file":
				if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink("missing-target", path); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			server := NewIPCServer(path, ipcFixtureHandler)
			if err := server.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "non-socket") {
				t.Fatalf("non-socket target was accepted: %v", err)
			}
			_ = server.Close()
			after, err := os.Lstat(path)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("IPC startup modified protected path: %v", err)
			}
			if kind == "file" {
				data, err := os.ReadFile(path)
				if err != nil || string(data) != "keep" {
					t.Fatalf("IPC startup changed file contents: data=%q err=%v", data, err)
				}
			}
		})
	}
}

func TestIPCServerPreservesActiveUnmanagedSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "other.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	defer listener.Close()
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	server := NewIPCServer(path, ipcFixtureHandler)
	if err := server.Start(context.Background()); !errors.Is(err, ErrIPCInUse) {
		t.Fatalf("unmanaged active listener was treated as stale: %v", err)
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("active unmanaged socket was replaced: %v", err)
	}
	_ = listener.Close()
	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("failed probe retained ownership after unmanaged listener stopped: %v", err)
	}
	defer server.Close()
}

func TestIPCServerClosePreservesReplacementPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.sock")
	server := NewIPCServer(path, ipcFixtureHandler)
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := server.Close(); err == nil || !strings.Contains(err.Error(), "changed ownership") {
		t.Fatalf("replacement ownership was not reported: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "replacement" {
		t.Fatalf("IPC Close deleted a replacement: data=%q err=%v", data, err)
	}
}

func TestIPCServerCancellationReleasesOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := NewIPCServer(path, ipcFixtureHandler)
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	closed := server.run.closed
	cancel()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not release socket ownership")
	}
	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("same server could not restart after cancellation: %v", err)
	}
	defer server.Close()
	if _, err := CallIPC(context.Background(), path, IPCRequest{ID: "restarted", Method: "status"}); err != nil {
		t.Fatalf("old cancellation closed the replacement listener: %v", err)
	}
}
