package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/normalize"
)

// ClientIngress is a versioned client-wire decoder that produces the shared
// operation request IR. The API server owns auth, execution and error handling;
// modules contribute only endpoint mapping and decoding.
type ClientIngress interface {
	ID() string
	IngressRef() extensions.Ref
	OperationRef() extensions.Ref
	Routes() []ClientIngressRoute
	Decode(*http.Request) (normalize.Request, error)
}

type ClientIngressRoute struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

func ValidateClientIngress(codec ClientIngress) error {
	if codec == nil || strings.TrimSpace(codec.ID()) == "" || codec.ID() != strings.TrimSpace(codec.ID()) {
		return fmt.Errorf("client ingress requires a trimmed, non-empty ID")
	}
	ref := codec.IngressRef()
	if err := ref.Validate(); err != nil || ref.Kind != extensions.ClientIngressKind || ref.ID != codec.ID() {
		return fmt.Errorf("client ingress %q requires a matching exact client-ingress ref", codec.ID())
	}
	operationRef := codec.OperationRef()
	if err := operationRef.Validate(); err != nil || operationRef.Kind != "operation" {
		return fmt.Errorf("client ingress %q requires an exact operation ref", codec.ID())
	}
	routes := codec.Routes()
	if len(routes) == 0 {
		return fmt.Errorf("client ingress %q has no routes", codec.ID())
	}
	seen := make(map[string]bool, len(routes))
	for _, route := range routes {
		method := strings.ToUpper(strings.TrimSpace(route.Method))
		if (method != http.MethodGet && method != http.MethodPost) || !strings.HasPrefix(route.Path, "/") || strings.ContainsAny(route.Path, "{}*? ") {
			return fmt.Errorf("client ingress %q declares invalid route %q %q", codec.ID(), route.Method, route.Path)
		}
		key := method + " " + route.Path
		if seen[key] {
			return fmt.Errorf("client ingress %q repeats route %q", codec.ID(), key)
		}
		seen[key] = true
	}
	return nil
}

// JSONOperationIngress is a reusable JSON envelope ingress for a versioned
// semantic operation. Protocol-specific clients can instead implement
// ClientIngress and use their own wire decoder.
type JSONOperationIngress struct {
	CodecID                  string
	IngressContractVersion   uint64
	Operation                string
	OperationContractVersion uint64
	Endpoints                []ClientIngressRoute
}

func (c JSONOperationIngress) ID() string { return c.CodecID }
func (c JSONOperationIngress) IngressRef() extensions.Ref {
	return extensions.Ref{Kind: extensions.ClientIngressKind, ID: c.CodecID, ContractVersion: c.IngressContractVersion}
}
func (c JSONOperationIngress) OperationRef() extensions.Ref {
	return extensions.Ref{Kind: "operation", ID: c.Operation, ContractVersion: c.OperationContractVersion}
}
func (c JSONOperationIngress) Routes() []ClientIngressRoute {
	return append([]ClientIngressRoute(nil), c.Endpoints...)
}
func (c JSONOperationIngress) Decode(request *http.Request) (normalize.Request, error) {
	if request == nil || request.Body == nil {
		return normalize.Request{}, fmt.Errorf("ingress codec %q requires a request body", c.CodecID)
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return normalize.Request{}, err
	}
	return c.DecodeBody(request, body)
}

func (c JSONOperationIngress) DecodeBody(request *http.Request, body []byte) (normalize.Request, error) {
	result, err := normalize.JSON(request.URL.Path, request.Header, body)
	if err != nil {
		return normalize.Request{}, err
	}
	if c.Operation == "" || c.OperationContractVersion == 0 {
		return normalize.Request{}, fmt.Errorf("ingress codec %q requires an exact semantic operation", c.CodecID)
	}
	result.Request.Operation = normalize.Operation(c.Operation)
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
