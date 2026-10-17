package daemon

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
)

// An immutable generation owns its request context and drain result. Watchers
// always close that generation, so an old cancellation cannot stop a restart.
type ipcRun struct {
	socket     *ipcSocket
	ownsSocket bool
	server     *http.Server
	cancel     context.CancelFunc
	once       sync.Once
	closed     chan struct{}
	served     chan error
	closeErr   error
}

func newIPCRun(parent context.Context, socket *ipcSocket, ownsSocket bool, handler http.Handler) *ipcRun {
	ctx, cancel := context.WithCancel(parent)
	run := &ipcRun{
		socket: socket, ownsSocket: ownsSocket, cancel: cancel,
		closed: make(chan struct{}), served: make(chan error, 1),
	}
	run.server = newDaemonHTTPServer("", handler, ctx)
	run.server.SetKeepAlivesEnabled(false)
	go func() {
		err := run.server.Serve(ipcContextListener{Listener: socket.listener, ctx: ctx})
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		run.served <- err
		if err != nil {
			_ = run.close()
		}
	}()
	go func() {
		select {
		case <-ctx.Done():
			_ = run.close()
		case <-run.closed:
		}
	}()
	return run
}

func (run *ipcRun) close() error {
	run.once.Do(func() {
		go func() {
			run.cancel()
			shutdownErr := run.server.Shutdown(context.Background())
			serveErr := <-run.served
			var socketErr error
			if run.ownsSocket {
				socketErr = run.socket.close()
			} else {
				socketErr = run.socket.stopAccepting()
			}
			run.closeErr = errors.Join(shutdownErr, serveErr, socketErr)
			close(run.closed)
		}()
	})
	<-run.closed
	return run.closeErr
}

// Closing every accepted connection on cancellation also releases clients
// blocked before a complete HTTP header/body reaches the request handler.
type ipcContextListener struct {
	net.Listener
	ctx context.Context
}

func (listener ipcContextListener) Accept() (net.Conn, error) {
	connection, err := listener.Listener.Accept()
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(listener.ctx, func() { _ = connection.Close() })
	return &ipcContextConn{Conn: connection, stop: stop}, nil
}

type ipcContextConn struct {
	net.Conn
	stop func() bool
}

func (connection *ipcContextConn) Close() error {
	connection.stop()
	return connection.Conn.Close()
}
