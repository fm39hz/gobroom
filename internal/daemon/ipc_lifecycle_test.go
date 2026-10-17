package daemon

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/store"
)

func TestCallIPCCancellationReachesHandler(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.sock")
	started, canceled := make(chan struct{}), make(chan struct{})
	server := NewIPCServer(path, func(ctx context.Context, request IPCRequest) IPCResponse {
		if request.Method == "block" {
			close(started)
			<-ctx.Done()
			close(canceled)
			return IPCResponse{ID: request.ID, Error: ctx.Err().Error()}
		}
		return ipcFixtureHandler(ctx, request)
	})
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := CallIPC(ctx, path, IPCRequest{ID: "cancel", Method: "block"}); result <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("IPC handler did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled client returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("CallIPC kept waiting after its context was canceled")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("client disconnection did not cancel the server handler")
	}
	if _, err := CallIPC(context.Background(), path, IPCRequest{ID: "status", Method: "status"}); err != nil {
		t.Fatalf("canceling a request stopped the IPC server: %v", err)
	}
}

func TestIPCCloseDrainsHandlersBeforeReleasingOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.sock")
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	server := NewIPCServer(path, func(ctx context.Context, request IPCRequest) IPCResponse {
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return IPCResponse{ID: request.ID, OK: true}
	})
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { releaseOnce.Do(func() { close(release) }); _ = server.Close() }()
	clientDone := make(chan error, 1)
	go func() {
		_, err := CallIPC(context.Background(), path, IPCRequest{ID: "drain", Method: "block"})
		clientDone <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("IPC handler did not start")
	}
	closedA, closedB := make(chan error, 1), make(chan error, 1)
	go func() { closedA <- server.Close() }()
	go func() { closedB <- server.Close() }()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("IPC Close did not cancel the handler")
	}
	for _, closed := range []chan error{closedA, closedB} {
		select {
		case err := <-closed:
			t.Fatalf("Close completed while its handler was still draining: %v", err)
		default:
		}
	}
	if err := server.Start(context.Background()); err == nil {
		t.Fatal("same IPC server restarted while an old handler was draining")
	}
	successor := NewIPCServer(path, ipcFixtureHandler)
	if err := successor.Start(context.Background()); !errors.Is(err, ErrIPCInUse) {
		t.Fatalf("socket ownership was released before drain completion: %v", err)
	}
	releaseOnce.Do(func() { close(release) })
	for _, closed := range []chan error{closedA, closedB} {
		select {
		case err := <-closed:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("Close did not finish after handler exit")
		}
	}
	if err := <-clientDone; err == nil {
		t.Fatal("canceled handler produced a successful client reply")
	}
	if err := successor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer successor.Close()
}

func TestIPCCloseUnblocksPartialRequestBody(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.sock")
	socket, err := acquireIPCSocket(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	entered, readDone := make(chan struct{}), make(chan error, 1)
	run := newIPCRun(context.Background(), socket, true, http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(entered)
		_, err := io.Copy(io.Discard, request.Body)
		readDone <- err
	}))
	defer run.close()
	connection, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := io.WriteString(connection, "POST /ipc/v1 HTTP/1.1\r\nHost: gobroom.local\r\nContent-Length: 4096\r\n\r\n{"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("partial-body request was not accepted")
	}
	closed := make(chan error, 1)
	go func() { closed <- run.close() }()
	select {
	case err := <-readDone:
		if err == nil {
			t.Fatal("incomplete body unexpectedly completed")
		}
	case <-time.After(time.Second):
		t.Fatal("IPC cancellation left a request body read blocked")
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("partial request prevented IPC shutdown")
	}
}

func TestIPCRejectsInvalidOrOversizedEnvelopeBeforeDispatch(t *testing.T) {
	for _, fixture := range []struct {
		name, body string
		length     int64
		status     int
	}{
		{"invalid", "{", 1, http.StatusBadRequest},
		{"multiple", `{"id":"a","method":"status"} {"id":"b","method":"status"}`, -1, http.StatusBadRequest},
		{"oversized", `{}`, maxIPCMessageBytes + 1, http.StatusRequestEntityTooLarge},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			calls := 0
			server := NewIPCServer("unused", func(context.Context, IPCRequest) IPCResponse { calls++; return IPCResponse{OK: true} })
			request := httptest.NewRequest(http.MethodPost, ipcEndpointPath, strings.NewReader(fixture.body))
			if fixture.length >= 0 {
				request.ContentLength = fixture.length
			}
			response := httptest.NewRecorder()
			server.serveRequest(response, request)
			if response.Code != fixture.status || calls != 0 {
				t.Fatalf("invalid envelope reached dispatch: status=%d calls=%d body=%q", response.Code, calls, response.Body.String())
			}
		})
	}
}

func TestDaemonStopDrainsIPCBeforeClosingStore(t *testing.T) {
	directory := t.TempDir()
	state, err := store.Open(filepath.Join(directory, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	server := NewIPCServer(filepath.Join(directory, "daemon.sock"), func(ctx context.Context, request IPCRequest) IPCResponse {
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		if err := state.DB.Ping(); err != nil {
			t.Errorf("store closed while IPC handler was draining: %v", err)
		}
		return IPCResponse{ID: request.ID, Error: ctx.Err().Error()}
	})
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cleanupDone := make(chan struct{})
	service := &Daemon{ipc: server, runCtx: ctx, runCancel: cancel, stop: func() { _ = state.Close(); close(cleanupDone) }}
	defer func() { releaseOnce.Do(func() { close(release) }); _ = service.Stop(context.Background()) }()
	clientDone := make(chan error, 1)
	go func() {
		_, err := CallIPC(context.Background(), server.path, IPCRequest{ID: "drain", Method: "block"})
		clientDone <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("IPC handler did not start")
	}
	stopped := make(chan error, 1)
	go func() { stopped <- service.Stop(context.Background()) }()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("daemon Stop did not cancel IPC handler")
	}
	if err := state.DB.Ping(); err != nil {
		t.Fatalf("store closed before handler exited: %v", err)
	}
	select {
	case <-cleanupDone:
		t.Fatal("daemon cleanup ran before IPC drain")
	default:
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("daemon did not finish IPC drain")
	}
	if err := state.DB.Ping(); err == nil {
		t.Fatal("daemon did not close its store after draining IPC")
	}
	<-clientDone
}

func TestDaemonIPCShutdownAcknowledgesAndWaitReturns(t *testing.T) {
	config := listenerTestConfig(t)
	config.HTTPEnabled, config.HTTPControl = false, false
	service := New(config)
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer service.Stop(context.Background())
	waited := make(chan error, 1)
	go func() { waited <- service.Wait(context.Background()) }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	response, err := CallIPC(ctx, config.IPCPath, IPCRequest{ID: "shutdown", Method: "shutdown"})
	if err != nil || !response.OK {
		t.Fatalf("shutdown was not acknowledged: response=%#v err=%v", response, err)
	}
	select {
	case err := <-waited:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("daemon Wait did not return after IPC shutdown")
	}
	if service.Addresses() != (ListenerAddresses{}) {
		t.Fatal("shutdown retained ready addresses")
	}
	if err := service.store.DB.Ping(); err == nil {
		t.Fatal("Wait returned before the store closed")
	}
	successor := NewIPCServer(config.IPCPath, ipcFixtureHandler)
	if err := successor.Start(context.Background()); err != nil {
		t.Fatalf("shutdown retained IPC ownership: %v", err)
	}
	defer successor.Close()
}
