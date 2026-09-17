package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
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
	path     string
	listener net.Listener
	handler  IPCHandler
}

func NewIPCServer(path string, handler IPCHandler) *IPCServer {
	return &IPCServer{path: path, handler: handler}
}

func (s *IPCServer) Start(ctx context.Context) error {
	if s.path == "" {
		return fmt.Errorf("IPC path is required")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	_ = os.Remove(s.path)
	listener, err := net.Listen("unix", s.path)
	if err != nil {
		return err
	}
	s.listener = listener
	if err := os.Chmod(s.path, 0o600); err != nil {
		listener.Close()
		return err
	}
	go func() { <-ctx.Done(); _ = listener.Close() }()
	go s.acceptLoop(ctx)
	return nil
}

func (s *IPCServer) acceptLoop(ctx context.Context) {
	for {
		conn, err := s.listener.Accept()
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
	if s.listener == nil {
		return nil
	}
	err := s.listener.Close()
	_ = os.Remove(s.path)
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
