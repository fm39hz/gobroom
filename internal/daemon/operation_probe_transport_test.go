package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/discovery"
)

func TestCallOperationProbeStreamsMultipartMetadataAndFiles(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "daemon.sock")
	filePath := filepath.Join(dir, "sample.wav")
	fileBody := []byte("small audio fixture\x00\x01")
	if err := os.WriteFile(filePath, fileBody, 0o600); err != nil {
		t.Fatal(err)
	}
	input := discovery.OperationProbeInput{
		ConnectionID: "conn-exact", OperationID: "audio.transcribe.v1", ModelID: "vendor/asr-exact",
		Payload: json.RawMessage(`{"language":"vi","temperature":0}`),
	}
	seen := make(chan error, 1)
	server := NewIPCServer(socket, func(context.Context, IPCRequest) IPCResponse {
		return IPCResponse{Error: "binary upload was dispatched to ordinary IPC"}
	})
	server.operationProbe = func(w http.ResponseWriter, r *http.Request) {
		reader, err := r.MultipartReader()
		if err != nil {
			seen <- err
			return
		}
		metadataPart, err := reader.NextPart()
		if err != nil {
			seen <- err
			return
		}
		if metadataPart.FormName() != "request" || metadataPart.FileName() != "" {
			seen <- errors.New("metadata was not the first non-file request part")
			return
		}
		metadataBytes, err := io.ReadAll(metadataPart)
		_ = metadataPart.Close()
		if err != nil {
			seen <- err
			return
		}
		var metadata probeUploadMetadata
		if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
			seen <- err
			return
		}
		if metadata.Request.ConnectionID != input.ConnectionID || metadata.Request.OperationID != input.OperationID || metadata.Request.ModelID != input.ModelID || string(metadata.Request.Payload) != string(input.Payload) {
			seen <- errors.New("probe metadata did not preserve exact request fields")
			return
		}
		if len(metadata.Files) != 1 || metadata.Files[0] != (probeUploadField{Field: "file-0", Role: "audio-input"}) {
			seen <- errors.New("probe metadata did not preserve role/field mapping")
			return
		}
		if bytes := string(metadataBytes); strings.Contains(bytes, filePath) {
			seen <- errors.New("local file path appeared in upload metadata")
			return
		}
		part, err := reader.NextPart()
		if err != nil {
			seen <- err
			return
		}
		mediaType, _, parseErr := mime.ParseMediaType(part.Header.Get("Content-Type"))
		body, readErr := io.ReadAll(part)
		_ = part.Close()
		if parseErr != nil || mediaType != "audio/wav" || readErr != nil || string(body) != string(fileBody) {
			seen <- errors.New("uploaded bytes or media type changed")
			return
		}
		if extra, err := reader.NextPart(); err != io.EOF {
			if extra != nil {
				_ = extra.Close()
			}
			seen <- errors.New("unexpected additional upload part")
			return
		}
		seen <- nil
		_ = writeIPCResponse(w, http.StatusOK, IPCResponse{OK: true, Result: map[string]string{"status": "accepted"}})
	}
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	response, err := CallOperationProbe(context.Background(), socket, input, []ProbeFileSpec{{Role: "audio-input", Path: filePath}})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-seen; err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), filePath) {
		t.Fatalf("local file path appeared in probe result: %s", encoded)
	}
}

func TestCallOperationProbeWithoutFilesUsesOrdinaryIPC(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "daemon.sock")
	input := discovery.OperationProbeInput{
		ConnectionID: "conn-json", OperationID: "audio.inspect.v1", ModelID: "vendor/model",
		Payload: json.RawMessage(`{"input":"hello"}`),
	}
	dispatched := make(chan IPCRequest, 1)
	server := NewIPCServer(socket, func(_ context.Context, request IPCRequest) IPCResponse {
		dispatched <- request
		return IPCResponse{ID: request.ID, OK: true, Result: "ok"}
	})
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	if _, err := CallOperationProbe(context.Background(), socket, input, nil); err != nil {
		t.Fatal(err)
	}
	request := <-dispatched
	if request.Method != "connections.test_operation" {
		t.Fatalf("JSON-only probe method=%q", request.Method)
	}
	var params struct {
		ConnectionID string          `json:"connectionID"`
		OperationID  string          `json:"operationID"`
		ModelID      string          `json:"modelID"`
		Payload      json.RawMessage `json:"payload"`
	}
	encoded, err := json.Marshal(request.Params)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &params); err != nil {
		t.Fatal(err)
	}
	if params.ConnectionID != input.ConnectionID || params.OperationID != input.OperationID || params.ModelID != input.ModelID || string(params.Payload) != string(input.Payload) {
		t.Fatalf("JSON-only probe params=%s", encoded)
	}
}

func TestCallOperationProbeRejectsInvalidLocalFilesBeforeDispatch(t *testing.T) {
	dir := t.TempDir()
	regularPath := filepath.Join(dir, "file.bin")
	if err := os.WriteFile(regularPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "daemon.sock")
	dispatches := make(chan struct{}, 1)
	server := NewIPCServer(socket, func(context.Context, IPCRequest) IPCResponse {
		dispatches <- struct{}{}
		return IPCResponse{OK: true}
	})
	server.operationProbe = func(http.ResponseWriter, *http.Request) { dispatches <- struct{}{} }
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	for _, fixture := range []struct {
		name string
		file ProbeFileSpec
	}{
		{name: "directory", file: ProbeFileSpec{Role: "input", Path: dir}},
		{name: "empty role", file: ProbeFileSpec{Path: regularPath}},
		{name: "empty path", file: ProbeFileSpec{Role: "input"}},
		{name: "missing path", file: ProbeFileSpec{Role: "input", Path: filepath.Join(dir, "missing")}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			if _, err := CallOperationProbe(context.Background(), socket, discovery.OperationProbeInput{
				ConnectionID: "connection", OperationID: "audio.inspect", ModelID: "model", Payload: json.RawMessage(`{}`),
			}, []ProbeFileSpec{fixture.file}); err == nil {
				t.Fatal("invalid local file was accepted")
			}
			select {
			case <-dispatches:
				t.Fatal("invalid local file reached IPC dispatch")
			default:
			}
		})
	}
}

func TestCallOperationProbeCancellationUnblocksUploadAndHandler(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "daemon.sock")
	filePath := filepath.Join(dir, "large.bin")
	file, err := os.Create(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(8 << 20); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	started, canceled := make(chan struct{}), make(chan struct{})
	server := NewIPCServer(socket, func(context.Context, IPCRequest) IPCResponse {
		return IPCResponse{Error: "multipart probe was routed to JSON IPC"}
	})
	server.operationProbe = func(_ http.ResponseWriter, request *http.Request) {
		close(started)
		// Like the real multipart handler, consume the request body. net/http
		// observes an interrupted upload through that read, not an idle handler.
		_, _ = io.Copy(io.Discard, request.Body)
		<-request.Context().Done()
		close(canceled)
	}
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := CallOperationProbe(ctx, socket, discovery.OperationProbeInput{
			ConnectionID: "conn-cancel", OperationID: "audio.transcribe.v1", ModelID: "vendor/asr",
			Payload: json.RawMessage(`{}`),
		}, []ProbeFileSpec{{Role: "audio-input", Path: filePath, MediaType: "application/octet-stream"}})
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("multipart probe handler did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled probe returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("operation probe call remained blocked after cancellation")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("client cancellation did not cancel the multipart handler")
	}
}
