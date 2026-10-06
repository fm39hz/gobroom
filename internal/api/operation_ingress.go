package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/normalize"
)

// OperationIngress binds client paths and methods to operation-neutral input.
// New endpoint families register here and share the daemon's existing data
// plane authentication, execution and error boundary.
type OperationIngress interface {
	ID() string
	OperationRef() extensions.Ref
	Routes() []IngressRoute
	Decode(*http.Request) (normalize.Request, error)
}

type IngressRoute struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

type JSONOperationIngress struct {
	CodecID                  string
	Operation                normalize.Operation
	OperationContractVersion uint64
	Endpoints                []IngressRoute
}

func (c JSONOperationIngress) ID() string { return c.CodecID }
func (c JSONOperationIngress) OperationRef() extensions.Ref {
	return extensions.Ref{Kind: "operation", ID: string(c.Operation), ContractVersion: c.OperationContractVersion}
}
func (c JSONOperationIngress) Routes() []IngressRoute {
	return append([]IngressRoute(nil), c.Endpoints...)
}
func (c JSONOperationIngress) Decode(request *http.Request) (normalize.Request, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return normalize.Request{}, err
	}
	result, err := normalize.JSON(request.URL.Path, request.Header, body)
	if err != nil {
		return normalize.Request{}, err
	}
	if c.Operation == "" {
		return normalize.Request{}, fmt.Errorf("ingress codec %q has no semantic operation", c.CodecID)
	}
	result.Request.Operation = c.Operation
	if c.OperationContractVersion == 0 {
		return normalize.Request{}, fmt.Errorf("ingress codec %q has no operation contract version", c.CodecID)
	}
	result.Request.OperationContractVersion = c.OperationContractVersion
	var envelope struct {
		Payload json.RawMessage `json:"operationPayload"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return normalize.Request{}, fmt.Errorf("decode operation payload envelope: %w", err)
	}
	result.Request.OperationPayload = append(json.RawMessage(nil), envelope.Payload...)
	return result.Request, nil
}

func validateOperationIngress(codec OperationIngress) error {
	if codec == nil || strings.TrimSpace(codec.ID()) == "" {
		return fmt.Errorf("operation ingress codec requires an ID")
	}
	if err := codec.OperationRef().Validate(); err != nil || codec.OperationRef().Kind != "operation" {
		return fmt.Errorf("operation ingress codec %q requires an exact operation reference", codec.ID())
	}
	routes := codec.Routes()
	if len(routes) == 0 {
		return fmt.Errorf("operation ingress codec %q has no endpoint routes", codec.ID())
	}
	seen := map[string]bool{}
	for _, route := range routes {
		method := strings.ToUpper(strings.TrimSpace(route.Method))
		if method != http.MethodGet && method != http.MethodPost {
			return fmt.Errorf("operation ingress codec %q has unsupported method %q", codec.ID(), route.Method)
		}
		if !strings.HasPrefix(route.Path, "/") || strings.ContainsAny(route.Path, "{}*? ") {
			return fmt.Errorf("operation ingress codec %q has invalid exact path %q", codec.ID(), route.Path)
		}
		key := method + " " + route.Path
		if seen[key] {
			return fmt.Errorf("operation ingress codec %q repeats route %q", codec.ID(), key)
		}
		seen[key] = true
	}
	return nil
}
