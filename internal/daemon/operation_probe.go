package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/fm39hz/gobroom/internal/artifacts"
	"github.com/fm39hz/gobroom/internal/discovery"
	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/ingress"
	"github.com/fm39hz/gobroom/internal/normalize"
)

const operationProbePath = "/ipc/v1/operation-probe"
const maxProbeMetadataBytes = 128 << 10
const maxProbeFiles = 32

type OperationProbeRequest = discovery.OperationProbeInput

// ProbeFileSpec belongs to the frontend machine only. Path is never serialized
// into upload metadata, interpreted by the daemon, or returned in probe results.
type ProbeFileSpec struct {
	Role      string `json:"role"`
	Path      string `json:"path"`
	MediaType string `json:"mediaType,omitempty"`
}

// DecodeProbeFileSpecs is shared by interactive and headless frontends. It
// binds one bounded JSON array; no role/path delimiter language is accepted.
func DecodeProbeFileSpecs(reader io.Reader) ([]ProbeFileSpec, error) {
	encoded, err := io.ReadAll(io.LimitReader(reader, maxProbeMetadataBytes+1))
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxProbeMetadataBytes {
		return nil, fmt.Errorf("probe file manifest exceeds size limit")
	}
	encoded = bytes.TrimSpace(encoded)
	if len(encoded) == 0 || encoded[0] != '[' {
		return nil, fmt.Errorf("probe file manifest must be a JSON array")
	}
	var files []ProbeFileSpec
	if err := decodeStrictProbeJSON(encoded, &files); err != nil {
		return nil, err
	}
	if len(files) > maxProbeFiles {
		return nil, fmt.Errorf("too many probe files")
	}
	for _, file := range files {
		if strings.TrimSpace(file.Role) == "" || strings.TrimSpace(file.Path) == "" {
			return nil, fmt.Errorf("probe file requires non-empty role and local path")
		}
		if file.MediaType != "" {
			if _, _, err := mime.ParseMediaType(file.MediaType); err != nil {
				return nil, fmt.Errorf("probe file has invalid media type")
			}
		}
	}
	return files, nil
}

type probeUploadField struct {
	Field string `json:"field"`
	Role  string `json:"role"`
}
type probeUploadMetadata struct {
	Request OperationProbeRequest `json:"request"`
	Files   []probeUploadField    `json:"files"`
}

// CallOperationProbe shares ordinary RPC for JSON-only input. Binary files use
// one bounded multipart request and fresh local file handles, without base64.
func CallOperationProbe(ctx context.Context, path string, input OperationProbeRequest, files []ProbeFileSpec) (IPCResponse, error) {
	if err := ctx.Err(); err != nil {
		return IPCResponse{}, err
	}
	if input.ConnectionID == "" || input.OperationID == "" || input.ModelID == "" || len(input.Payload) == 0 || !json.Valid(input.Payload) {
		return IPCResponse{}, fmt.Errorf("probe requires connection, exact operation/model and JSON payload")
	}
	if len(files) == 0 {
		raw, err := json.Marshal(input)
		if err != nil {
			return IPCResponse{}, err
		}
		var params map[string]any
		if err := json.Unmarshal(raw, &params); err != nil {
			return IPCResponse{}, err
		}
		return CallIPC(ctx, path, IPCRequest{Method: "connections.test_operation", Params: params})
	}
	if len(files) > maxProbeFiles {
		return IPCResponse{}, fmt.Errorf("operation probe accepts at most %d files", maxProbeFiles)
	}
	opened := make([]*os.File, 0, len(files))
	defer func() {
		for _, file := range opened {
			_ = file.Close()
		}
	}()
	metadata := probeUploadMetadata{Request: input}
	mediaTypes := make([]string, len(files))
	for index, spec := range files {
		if strings.TrimSpace(spec.Role) == "" || strings.TrimSpace(spec.Path) == "" {
			return IPCResponse{}, fmt.Errorf("probe artifact requires role and local path")
		}
		info, err := os.Stat(spec.Path)
		if err != nil {
			return IPCResponse{}, err
		}
		if !info.Mode().IsRegular() || info.Size() > maxIPCMessageBytes {
			return IPCResponse{}, fmt.Errorf("probe artifact %q must be a bounded regular file", spec.Role)
		}
		file, err := os.Open(spec.Path)
		if err != nil {
			return IPCResponse{}, err
		}
		opened = append(opened, file)
		mediaType := spec.MediaType
		if mediaType == "" {
			mediaType = mime.TypeByExtension(filepath.Ext(spec.Path))
		}
		if mediaType == "" {
			mediaType = "application/octet-stream"
		}
		parsed, _, err := mime.ParseMediaType(mediaType)
		if err != nil {
			return IPCResponse{}, fmt.Errorf("probe artifact %q has invalid media type", spec.Role)
		}
		mediaTypes[index] = parsed
		metadata.Files = append(metadata.Files, probeUploadField{Field: "file-" + strconv.Itoa(index), Role: spec.Role})
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return IPCResponse{}, err
	}
	if len(encoded) > maxProbeMetadataBytes {
		return IPCResponse{}, fmt.Errorf("probe metadata exceeds size limit")
	}
	reader, writer := io.Pipe()
	form := multipart.NewWriter(writer)
	contentType := form.FormDataContentType()
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = reader.CloseWithError(ctx.Err()); _ = writer.CloseWithError(ctx.Err()) })
	defer func() { stop(); _ = reader.Close(); _ = writer.Close(); <-done }()
	go func() {
		defer close(done)
		writeErr := form.WriteField("request", string(encoded))
		for index, file := range opened {
			if writeErr != nil {
				break
			}
			if writeErr = ctx.Err(); writeErr != nil {
				break
			}
			header := make(textproto.MIMEHeader)
			header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": metadata.Files[index].Field, "filename": filepath.Base(files[index].Path)}))
			header.Set("Content-Type", mediaTypes[index])
			var part io.Writer
			part, writeErr = form.CreatePart(header)
			if writeErr == nil {
				_, writeErr = io.Copy(part, io.LimitReader(file, maxIPCMessageBytes+1))
			}
		}
		if writeErr == nil {
			writeErr = form.Close()
		}
		_ = writer.CloseWithError(writeErr)
	}()
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://gobroom.local"+operationProbePath, reader)
	if err != nil {
		return IPCResponse{}, err
	}
	httpRequest.Header.Set("Content-Type", contentType)
	return callIPCRequest(ctx, path, httpRequest, "connections.test_operation")
}

func (d *Daemon) serveOperationProbe(w http.ResponseWriter, r *http.Request) {
	failProbe := func(err error) {
		status := http.StatusBadRequest
		var oversized *http.MaxBytesError
		if errors.As(err, &oversized) {
			status = http.StatusRequestEntityTooLarge
		}
		_ = writeIPCResponse(w, status, IPCResponse{Error: err.Error()})
	}
	if d.providerRegistry == nil || d.artifactStore == nil {
		failProbe(fmt.Errorf("probe runtime or artifact store is unavailable"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxIPCMessageBytes)
	reader, err := r.MultipartReader()
	if err != nil {
		failProbe(fmt.Errorf("operation probe requires multipart form-data"))
		return
	}
	part, err := reader.NextPart()
	if err != nil {
		failProbe(fmt.Errorf("probe metadata must be the first part"))
		return
	}
	if part.FormName() != "request" || part.FileName() != "" {
		_ = part.Close()
		failProbe(fmt.Errorf("probe request metadata must precede all files"))
		return
	}
	encoded, err := io.ReadAll(io.LimitReader(part, maxProbeMetadataBytes+1))
	_ = part.Close()
	if err != nil || len(encoded) > maxProbeMetadataBytes {
		failProbe(fmt.Errorf("probe metadata exceeds size limit or could not be read"))
		return
	}
	var metadata probeUploadMetadata
	if err := decodeStrictProbeJSON(encoded, &metadata); err != nil {
		failProbe(err)
		return
	}
	if len(metadata.Files) > maxProbeFiles {
		failProbe(fmt.Errorf("too many probe files"))
		return
	}
	for _, artifact := range metadata.Request.Artifacts {
		if artifact.Body != nil {
			failProbe(fmt.Errorf("probe metadata cannot reference daemon body leases"))
			return
		}
	}
	snapshot, err := d.providerRegistry.OperationSnapshot()
	if err != nil {
		failProbe(err)
		return
	}
	service := discovery.Service{Store: d.store, Bindings: d.providerBindings, Operations: snapshot, Artifacts: d.artifactStore}
	definition, task, ports, err := service.OperationProbeContract(metadata.Request)
	if err != nil {
		failProbe(err)
		return
	}
	byRole := make(map[string]extensions.ArtifactInput, len(ports))
	counts := make(map[string]int, len(ports))
	for _, port := range ports {
		byRole[port.Role] = port
	}
	for _, artifact := range metadata.Request.Artifacts {
		port, ok := byRole[artifact.Role]
		if !ok || port.TypeRef != artifact.TypeRef {
			failProbe(fmt.Errorf("inline probe artifact role/type is not declared"))
			return
		}
		counts[artifact.Role]++
	}
	codec := ingress.MultipartOperationIngress{CodecID: "gobroom.operation-probe", IngressContractVersion: 1, Operation: normalize.Operation(task.ID), OperationContractVersion: task.ContractVersion}
	seen := map[string]bool{"request": true}
	for _, file := range metadata.Files {
		port, ok := byRole[file.Role]
		if !ok || file.Field == "" || seen[file.Field] {
			failProbe(fmt.Errorf("probe upload field/role is undeclared or repeated"))
			return
		}
		seen[file.Field] = true
		counts[file.Role]++
		descriptor, ok := snapshot.ExtensionCatalog().Descriptor(port.TypeRef)
		if !ok || descriptor.ResourceBounds.MaxInputBytes <= 0 {
			failProbe(fmt.Errorf("probe artifact has no positive size bound"))
			return
		}
		codec.Bindings = append(codec.Bindings, ingress.MultipartArtifactBinding{Field: file.Field, Role: file.Role, TypeRef: port.TypeRef, MaxBytes: min(descriptor.ResourceBounds.MaxInputBytes, int64(maxIPCMessageBytes))})
	}
	for _, port := range ports {
		if counts[port.Role] < port.MinCount || counts[port.Role] > port.MaxCount {
			failProbe(fmt.Errorf("probe artifact role %q requires %d..%d values", port.Role, port.MinCount, port.MaxCount))
			return
		}
	}
	requestJSON, err := json.Marshal(struct {
		Model     string                   `json:"model"`
		Payload   json.RawMessage          `json:"operationPayload"`
		Artifacts []extensions.ArtifactRef `json:"artifacts,omitempty"`
	}{metadata.Request.ModelID, metadata.Request.Payload, metadata.Request.Artifacts})
	if err != nil {
		failProbe(err)
		return
	}
	ctx := artifacts.WithStore(r.Context(), d.artifactStore)
	decoded, err := codec.DecodeParts(r.WithContext(ctx), reader, requestJSON)
	if err != nil {
		failProbe(err)
		return
	}
	cleaned := false
	cleanup := func() error {
		if cleaned {
			return nil
		}
		cleaned = true
		var releaseErr error
		for _, artifact := range decoded.Artifacts {
			if artifact.Body != nil {
				releaseErr = errors.Join(releaseErr, d.artifactStore.Release(artifact.Owner, *artifact.Body))
			}
		}
		return releaseErr
	}
	defer cleanup()
	metadata.Request.Artifacts = decoded.Artifacts
	metadata.Request.ExpectedOperation, metadata.Request.ExpectedProvider = task, definition
	result, err := service.TestOperation(ctx, metadata.Request)
	err = errors.Join(err, cleanup())
	if err != nil {
		failProbe(err)
		return
	}
	if err := ctx.Err(); err != nil {
		failProbe(err)
		return
	}
	_ = writeIPCResponse(w, http.StatusOK, IPCResponse{OK: true, Result: result})
}

func decodeStrictProbeJSON(encoded []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("one probe metadata envelope is required")
	}
	return nil
}
