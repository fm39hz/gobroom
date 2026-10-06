package api

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/fm39hz/gobroom/internal/normalize"
)

// OperationIngress binds client paths and methods to operation-neutral input.
// New endpoint families register here and share the daemon's existing data
// plane authentication, execution and error boundary.
type OperationIngress interface {
	ID() string
	Routes() []IngressRoute
	Decode(*http.Request) (normalize.Request, error)
}

type IngressRoute struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

type JSONOperationIngress struct {
	CodecID   string
	Operation normalize.Operation
	Endpoints []IngressRoute
}

func (c JSONOperationIngress) ID() string { return c.CodecID }
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
	return result.Request, nil
}

func validateOperationIngress(codec OperationIngress) error {
	if codec == nil || strings.TrimSpace(codec.ID()) == "" {
		return fmt.Errorf("operation ingress codec requires an ID")
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
