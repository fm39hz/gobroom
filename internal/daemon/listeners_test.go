package daemon

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/provider"
)

func listenerTestConfig(t *testing.T) Config {
	t.Helper()
	directory := t.TempDir()
	return Config{
		DBPath: filepath.Join(directory, "state.db"), IPCPath: filepath.Join(directory, "daemon.sock"),
		ArtifactStoreDir: filepath.Join(directory, "artifacts"),
		HTTPEnabled:      true, HTTPAddr: "127.0.0.1:0", HTTPControl: true, HTTPControlAddr: "127.0.0.1:0",
	}
}

func unusedListenerAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func TestDaemonHTTPBindFailureDoesNotPublishOrInitialize(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	config := listenerTestConfig(t)
	config.HTTPAddr = occupied.Addr().String()
	service := New(config)
	if err := service.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "bind HTTP data plane") {
		t.Fatalf("occupied data port was not reported by Start: %v", err)
	}
	for _, path := range []string{config.DBPath, config.IPCPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("failed port reservation changed %q: %v", path, err)
		}
	}
	if service.Addresses() != (ListenerAddresses{}) {
		t.Fatalf("failed startup published listener addresses: %#v", service.Addresses())
	}
}

func TestDaemonControlBindFailureRollsBackDataPort(t *testing.T) {
	config := listenerTestConfig(t)
	address := unusedListenerAddress(t)
	// Data owns this port first; control must fail and release the data reservation.
	config.HTTPAddr, config.HTTPControlAddr = address, address
	service := New(config)
	if err := service.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "bind HTTP control plane") {
		t.Fatalf("control/data port conflict was not reported by Start: %v", err)
	}
	rebound, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("failed control bind leaked the data-plane port: %v", err)
	}
	defer rebound.Close()
	if _, err := os.Stat(config.IPCPath); !os.IsNotExist(err) {
		t.Fatalf("failed HTTP startup published IPC: %v", err)
	}
}

func TestDaemonIPCFailureRollsBackHTTPAndAllowsRetry(t *testing.T) {
	config := listenerTestConfig(t)
	config.HTTPAddr = unusedListenerAddress(t)
	config.IPCPath = ""
	service := New(config)
	if err := service.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "IPC path is required") {
		t.Fatalf("expected IPC startup failure: %v", err)
	}
	if service.stop != nil || service.Addresses() != (ListenerAddresses{}) {
		t.Fatal("failed IPC startup left the daemon marked as running")
	}
	rebound, err := net.Listen("tcp", config.HTTPAddr)
	if err != nil {
		t.Fatalf("failed IPC startup leaked HTTP: %v", err)
	}
	_ = rebound.Close()
	service.config.IPCPath = filepath.Join(filepath.Dir(config.DBPath), "retry.sock")
	if err := service.Start(context.Background()); err != nil {
		t.Fatalf("daemon could not start after startup rollback: %v", err)
	}
	defer service.Stop(context.Background())
	if _, err := CallIPC(context.Background(), service.Addresses().IPC, IPCRequest{ID: "status", Method: "status"}); err != nil {
		t.Fatalf("retried daemon did not serve IPC: %v", err)
	}
}

func TestDaemonIPCConflictDoesNotInitializeAnotherDatabase(t *testing.T) {
	config := listenerTestConfig(t)
	owner := New(config)
	if err := owner.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer owner.Stop(context.Background())
	contenderConfig := config
	contenderConfig.DBPath = filepath.Join(filepath.Dir(config.DBPath), "contender.db")
	contender := New(contenderConfig)
	if err := contender.Start(context.Background()); !errors.Is(err, ErrIPCInUse) {
		t.Fatalf("second daemon did not reject an owned IPC path: %v", err)
	}
	if _, err := os.Stat(contenderConfig.DBPath); !os.IsNotExist(err) {
		t.Fatalf("IPC conflict initialized a second database: %v", err)
	}
	if _, err := CallIPC(context.Background(), config.IPCPath, IPCRequest{ID: "owner", Method: "status"}); err != nil {
		t.Fatalf("contender disturbed the original daemon: %v", err)
	}
}

func TestDaemonInitializationFailureReleasesReservedSockets(t *testing.T) {
	config := listenerTestConfig(t)
	config.HTTPAddr = unusedListenerAddress(t)
	initializationError := errors.New("fixture module initialization failed")
	module := provider.RuntimeModuleFunc{
		Name: "fixture.fail-startup", Version: "test", HostContractVersion: provider.RuntimeModuleHostContractVersion,
		Init: func(*provider.RuntimeRegistry) error { return initializationError },
	}
	failed := NewWithProviderModules(config, module)
	if err := failed.Start(context.Background()); !errors.Is(err, initializationError) {
		t.Fatalf("expected module initialization failure: %v", err)
	}
	if _, err := os.Lstat(config.IPCPath); !os.IsNotExist(err) {
		t.Fatalf("initialization failure retained the reserved socket: %v", err)
	}
	replacement := New(config)
	if err := replacement.Start(context.Background()); err != nil {
		t.Fatalf("startup rollback leaked IPC ownership or HTTP port: %v", err)
	}
	defer replacement.Stop(context.Background())
	if _, err := CallIPC(context.Background(), config.IPCPath, IPCRequest{ID: "replacement", Method: "status"}); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonRetainsIPCOwnershipUntilCleanupCompletes(t *testing.T) {
	config := listenerTestConfig(t)
	config.HTTPEnabled, config.HTTPControl = false, false
	owner := New(config)
	if err := owner.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	cleanupEntered := make(chan struct{})
	allowCleanup := make(chan struct{})
	var release sync.Once
	defer func() {
		release.Do(func() { close(allowCleanup) })
		_ = owner.Stop(context.Background())
	}()
	cleanup := owner.stop
	owner.stop = func() {
		close(cleanupEntered)
		<-allowCleanup
		cleanup()
	}
	stopped := make(chan error, 1)
	go func() { stopped <- owner.Stop(context.Background()) }()
	select {
	case <-cleanupEntered:
	case <-time.After(time.Second):
		t.Fatal("daemon did not enter cleanup")
	}
	successor := NewIPCServer(config.IPCPath, ipcFixtureHandler)
	if err := successor.Start(context.Background()); !errors.Is(err, ErrIPCInUse) {
		t.Fatalf("IPC ownership was released before workers/store cleanup: %v", err)
	}
	release.Do(func() { close(allowCleanup) })
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("daemon cleanup did not finish")
	}
	if err := successor.Start(context.Background()); err != nil {
		t.Fatalf("cleanup did not release IPC ownership: %v", err)
	}
	defer successor.Close()
}

func TestDaemonWaitPreservesCleanupError(t *testing.T) {
	config := listenerTestConfig(t)
	config.HTTPEnabled, config.HTTPControl = false, false
	service := New(config)
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer service.Stop(context.Background())
	waited := make(chan error, 1)
	go func() { waited <- service.Wait(context.Background()) }()
	if err := os.Remove(config.IPCPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.IPCPath, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := service.Stop(context.Background()); err == nil || !strings.Contains(err.Error(), "changed ownership") {
		t.Fatalf("expected IPC cleanup ownership error: %v", err)
	}
	select {
	case err := <-waited:
		if err == nil || !strings.Contains(err.Error(), "changed ownership") {
			t.Fatalf("Wait lost the shutdown failure: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Wait did not finish after shutdown failure")
	}
	if data, err := os.ReadFile(config.IPCPath); err != nil || string(data) != "replacement" {
		t.Fatalf("shutdown removed the replacement path: data=%q err=%v", data, err)
	}
}

func TestDaemonPublishesBoundListenersAndReleasesThemOnStop(t *testing.T) {
	config := listenerTestConfig(t)
	service := New(config)
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer service.Stop(context.Background())
	addresses := service.Addresses()
	if addresses.IPC != config.IPCPath || addresses.HTTP == "" || addresses.HTTPControl == "" || strings.HasSuffix(addresses.HTTP, ":0") || strings.HasSuffix(addresses.HTTPControl, ":0") {
		t.Fatalf("Start did not publish bound addresses: %#v", addresses)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	for _, check := range []struct {
		address string
		path    string
		status  int
	}{
		{addresses.HTTP, "/v1/models", http.StatusOK},
		{addresses.HTTP, "/api/status", http.StatusNotFound},
		{addresses.HTTPControl, "/api/status", http.StatusOK},
		{addresses.HTTPControl, "/v1/models", http.StatusNotFound},
	} {
		response, err := client.Get("http://" + check.address + check.path)
		if err != nil {
			t.Fatalf("listener was not reachable immediately after Start: %v", err)
		}
		_ = response.Body.Close()
		if response.StatusCode != check.status {
			t.Fatalf("%s%s returned %d, want %d", check.address, check.path, response.StatusCode, check.status)
		}
	}
	if err := service.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "already started") {
		t.Fatalf("duplicate Start replaced a running instance: %v", err)
	}
	if _, err := CallIPC(context.Background(), addresses.IPC, IPCRequest{ID: "status", Method: "status"}); err != nil {
		t.Fatalf("duplicate Start disturbed the existing daemon: %v", err)
	}
	if err := service.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if service.Addresses() != (ListenerAddresses{}) {
		t.Fatalf("stopped daemon retained ready addresses: %#v", service.Addresses())
	}
	for _, address := range []string{addresses.HTTP, addresses.HTTPControl} {
		listener, err := net.Listen("tcp", address)
		if err != nil {
			t.Fatalf("Stop did not release %s: %v", address, err)
		}
		_ = listener.Close()
	}
}
