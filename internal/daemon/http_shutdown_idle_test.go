package daemon

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestDaemonHTTPServerHasFiniteHeaderBudget(t *testing.T) {
	server := newDaemonHTTPServer("", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), context.Background())
	if server.ReadHeaderTimeout <= 0 {
		t.Fatal("daemon HTTP server has no finite read-header timeout")
	}
	if server.ReadHeaderTimeout >= 4*time.Second {
		t.Fatalf("read-header timeout %s leaves too little of the 5s stop budget for shutdown polling and cleanup", server.ReadHeaderTimeout)
	}
}

func TestDaemonHTTPServerShutdownExpiresNewConnection(t *testing.T) {
	server := newDaemonHTTPServer("", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), context.Background())

	newConnection := make(chan struct{}, 1)
	closedConnection := make(chan struct{}, 1)
	server.ConnState = func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			newConnection <- struct{}{}
		case http.StateClosed:
			closedConnection <- struct{}{}
		}
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("loopback listener unavailable: %v", err)
	}
	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() { _ = server.Close() })

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	select {
	case <-newConnection:
	case <-time.After(time.Second):
		t.Fatal("server did not report the accepted connection as StateNew")
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if err := server.Shutdown(stopCtx); err != nil {
		t.Fatalf("shutdown with a client that sent no request headers: %v", err)
	}
	select {
	case <-closedConnection:
	case <-stopCtx.Done():
		t.Fatal("stalled connection did not close before the shutdown budget expired")
	}
	select {
	case <-serveDone:
	case <-time.After(time.Second):
		t.Fatal("Serve did not return after shutdown")
	}
}
