package kernel

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestHTTPTransportDoesNotRedirectPreparedActions(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirected.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer issuer.Close()
	response, err := (HTTPTransport{}).Execute(context.Background(), UpstreamRequest{Method: http.MethodPost, URL: issuer.URL, Body: strings.NewReader("frozen business body")})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.Status != http.StatusTemporaryRedirect || redirected.Load() != 0 {
		t.Fatalf("HTTP client followed a prepared action to another issuer: status=%d redirects=%d", response.Status, redirected.Load())
	}
}

func TestHTTPTransportDoesNotImplicitlyReplayAfterAmbiguousEmptyBodyDispatch(t *testing.T) {
	var dispatched atomic.Int32
	var connections atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/warm" {
			_, _ = io.WriteString(w, "warm")
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		dispatched.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack fixture connection: %v", err)
			return
		}
		_ = conn.Close() // Effect occurred, response lost on a reused connection.
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.Start()
	defer server.Close()
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	warm, err := client.Get(server.URL + "/warm")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, warm.Body)
	_ = warm.Body.Close()
	_, err = (HTTPTransport{Client: client}).Execute(context.Background(), UpstreamRequest{
		Method: http.MethodPost, URL: server.URL + "/once", Headers: http.Header{"Idempotency-Key": {"provider-guarantee-is-kernel-scoped"}},
	})
	if err == nil || dispatched.Load() != 1 || connections.Load() != 1 {
		t.Fatalf("net/http duplicated an ambiguous dispatch outside kernel policy: error=%v dispatched=%d connections=%d", err, dispatched.Load(), connections.Load())
	}
}
