package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
)

const (
	ipcEndpointPath    = "/ipc/v1"
	maxIPCMessageBytes = 32 << 20
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
	path           string
	handler        IPCHandler
	afterReply     func(IPCRequest, IPCResponse)
	operationProbe http.HandlerFunc
	mu             sync.Mutex
	run            *ipcRun
}

func NewIPCServer(path string, handler IPCHandler) *IPCServer {
	return &IPCServer{path: path, handler: handler}
}

func (s *IPCServer) runningLocked() bool {
	if s.run == nil {
		return false
	}
	select {
	case <-s.run.closed:
		return false
	default:
		return true
	}
}

func (s *IPCServer) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runningLocked() {
		return fmt.Errorf("IPC server is already started or draining")
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
	if s.runningLocked() {
		return fmt.Errorf("IPC server is already started or draining")
	}
	return s.startLocked(ctx, socket, false)
}

func (s *IPCServer) startLocked(ctx context.Context, socket *ipcSocket, ownsSocket bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+ipcEndpointPath, s.serveRequest)
	if s.operationProbe != nil {
		mux.HandleFunc("POST "+operationProbePath, s.operationProbe)
	}
	s.run = newIPCRun(ctx, socket, ownsSocket, mux)
	return nil
}

func (s *IPCServer) serveRequest(writer http.ResponseWriter, request *http.Request) {
	if request.ContentLength > maxIPCMessageBytes {
		_ = writeIPCResponse(writer, http.StatusRequestEntityTooLarge, IPCResponse{Error: "IPC request exceeds size limit"})
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxIPCMessageBytes)
	var envelope IPCRequest
	if err := decodeIPCEnvelope(request.Body, &envelope); err != nil {
		status := http.StatusBadRequest
		var oversized *http.MaxBytesError
		if errors.As(err, &oversized) {
			status = http.StatusRequestEntityTooLarge
		}
		_ = writeIPCResponse(writer, status, IPCResponse{ID: envelope.ID, Error: err.Error()})
		return
	}
	response := s.handler(request.Context(), envelope)
	if err := request.Context().Err(); err != nil {
		response = IPCResponse{ID: envelope.ID, Error: err.Error()}
	}
	_ = writeIPCResponse(writer, http.StatusOK, response)
	if s.afterReply != nil {
		s.afterReply(envelope, response)
	}
}

func writeIPCResponse(writer http.ResponseWriter, status int, response IPCResponse) error {
	payload, err := json.Marshal(response)
	if err != nil || len(payload) > maxIPCMessageBytes {
		status = http.StatusInternalServerError
		message := "IPC response cannot be encoded"
		if err == nil {
			message = "IPC response exceeds size limit"
		}
		payload, _ = json.Marshal(IPCResponse{ID: response.ID, Error: message})
	}
	writer.Header().Set("Content-Type", "application/json")
	// An explicit length lets shutdown acknowledge its response completely
	// before cancellation closes the connection; no final chunk is required.
	writer.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	writer.WriteHeader(status)
	if _, err := writer.Write(payload); err != nil {
		return err
	}
	return http.NewResponseController(writer).Flush()
}

func decodeIPCEnvelope(reader io.Reader, target any) error {
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("only one IPC envelope is allowed per request")
		}
		return err
	}
	return nil
}

// Close cancels request contexts, closes connections and waits for handlers to
// return. Concurrent closes share the same drain; ownership remains held until
// that drain completes (and, for a daemon reservation, until store cleanup).
func (s *IPCServer) Close() error {
	s.mu.Lock()
	run := s.run
	s.mu.Unlock()
	if run == nil {
		return nil
	}
	return run.close()
}

func CallIPC(ctx context.Context, path string, request IPCRequest) (IPCResponse, error) {
	if err := ctx.Err(); err != nil {
		return IPCResponse{}, err
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return IPCResponse{}, err
	}
	if len(payload) > maxIPCMessageBytes {
		return IPCResponse{}, fmt.Errorf("IPC request exceeds size limit")
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://gobroom.local"+ipcEndpointPath, bytes.NewReader(payload))
	if err != nil {
		return IPCResponse{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	return callIPCRequest(ctx, path, httpRequest, request.Method)
}

func callIPCRequest(ctx context.Context, path string, httpRequest *http.Request, method string) (IPCResponse, error) {
	var dialer net.Dialer
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", path)
		},
		DisableKeepAlives: true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	httpResponse, err := client.Do(httpRequest)
	if err != nil {
		if ctx.Err() != nil {
			return IPCResponse{}, ctx.Err()
		}
		return IPCResponse{}, err
	}
	defer httpResponse.Body.Close()
	limited := &io.LimitedReader{R: httpResponse.Body, N: maxIPCMessageBytes + 1}
	var response IPCResponse
	decodeErr := decodeIPCEnvelope(limited, &response)
	if ctx.Err() != nil {
		return IPCResponse{}, ctx.Err()
	}
	if limited.N == 0 {
		return IPCResponse{}, fmt.Errorf("IPC response exceeds size limit")
	}
	if decodeErr != nil {
		return IPCResponse{}, decodeErr
	}
	if !response.OK {
		return response, fmt.Errorf("IPC %s: %s", method, response.Error)
	}
	if httpResponse.StatusCode != http.StatusOK {
		return response, fmt.Errorf("IPC %s: HTTP status %d", method, httpResponse.StatusCode)
	}
	return response, nil
}
