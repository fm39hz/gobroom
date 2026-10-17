package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
)

type IPCRequest struct {
	ID     string         `json:"id"`
	Method string         `json:"method"`
	Params map[string]any `json:"params,omitempty"`
}

type IPCResponse struct {
	ID     string `json:"id"`
	OK     bool   `json:"ok"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

type IPCHandler func(context.Context, IPCRequest) IPCResponse

type IPCServer struct {
	path       string
	handler    IPCHandler
	mu         sync.Mutex
	socket     *ipcSocket
	ownsSocket bool
	closed     chan struct{}
}

func NewIPCServer(path string, handler IPCHandler) *IPCServer {
	return &IPCServer{path: path, handler: handler}
}

func (s *IPCServer) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.socket != nil {
		return fmt.Errorf("IPC server is already started")
	}
	socket, err := acquireIPCSocket(ctx, s.path)
	if err != nil {
		return err
	}
	if err := s.startLocked(ctx, socket, true); err != nil {
		_ = socket.close()
		return err
	}
	return nil
}

func (s *IPCServer) startReserved(ctx context.Context, socket *ipcSocket) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.socket != nil {
		return fmt.Errorf("IPC server is already started")
	}
	return s.startLocked(ctx, socket, false)
}

func (s *IPCServer) startLocked(ctx context.Context, socket *ipcSocket, ownsSocket bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.socket = socket
	s.ownsSocket = ownsSocket
	closed := make(chan struct{})
	s.closed = closed
	go func() {
		select {
		case <-ctx.Done():
			_ = s.closeSocket(socket)
		case <-closed:
		}
	}()
	go s.acceptLoop(ctx, socket.listener)
	return nil
}

func (s *IPCServer) acceptLoop(ctx context.Context, listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go s.serveConn(ctx, conn)
	}
}

func (s *IPCServer) serveConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	decoder := json.NewDecoder(bufio.NewReader(conn))
	encoder := json.NewEncoder(conn)
	var request IPCRequest
	if err := decoder.Decode(&request); err != nil {
		_ = encoder.Encode(IPCResponse{OK: false, Error: err.Error()})
		return
	}
	response := s.handler(ctx, request)
	_ = encoder.Encode(response)
}

func (s *IPCServer) Close() error {
	return s.closeSocket(nil)
}

func (s *IPCServer) closeSocket(expected *ipcSocket) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.socket == nil || expected != nil && s.socket != expected {
		return nil
	}
	var err error
	if s.ownsSocket {
		err = s.socket.close()
	} else {
		// A daemon reservation remains owned until its workers/store have drained.
		err = s.socket.stopAccepting()
	}
	s.socket = nil
	close(s.closed)
	s.closed = nil
	return err
}

func CallIPC(ctx context.Context, path string, request IPCRequest) (IPCResponse, error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return IPCResponse{}, err
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return IPCResponse{}, err
	}
	var response IPCResponse
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&response); err != nil {
		return IPCResponse{}, err
	}
	if !response.OK {
		return response, fmt.Errorf("IPC %s: %s", request.Method, response.Error)
	}
	return response, nil
}
