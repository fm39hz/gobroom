package kernel

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fm39hz/gobroom/internal/normalize"
)

type RequestCodec interface {
	ID() string
	Prepare(context.Context, NormalizedRequest, Route, Credential) (UpstreamRequest, error)
}

type ResponseCodec interface {
	ID() string
	ClassifyError(status int, body []byte) ErrorClass
	TranslateStream(context.Context, UpstreamResponse, http.ResponseWriter, normalize.Format, StreamHooks) error
}

type ClientFormatSupport interface {
	SupportsClientFormat(normalize.Format) CompatibilityDecision
}

type Transport interface {
	ID() string
	Execute(context.Context, UpstreamRequest) (UpstreamResponse, error)
}

type Endpoint interface {
	ID() string
	Resolve(baseURL, operationPath string, options EndpointOptions) (string, error)
}

type EndpointOptions struct {
	Path  string            `json:"path,omitempty"`
	Query map[string]string `json:"query,omitempty"`
}

// HTTPJSONEndpoint combines a provider's API root with the relative path
// selected by its request/model codec. Transport remains a separate primitive.
type HTTPJSONEndpoint struct{}

func (HTTPJSONEndpoint) ID() string { return "http-json" }
func (HTTPJSONEndpoint) Resolve(baseURL, operationPath string, options EndpointOptions) (string, error) {
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return "", fmt.Errorf("invalid HTTP endpoint base URL %q", baseURL)
	}
	if options.Path != "" {
		operationPath = options.Path
	}
	operation, err := url.Parse(strings.TrimSpace(operationPath))
	if err != nil || operation.IsAbs() || operation.Host != "" || operation.Fragment != "" {
		return "", fmt.Errorf("invalid relative endpoint path %q", operationPath)
	}
	escapedPath := strings.TrimRight(base.EscapedPath(), "/") + "/" + strings.TrimLeft(operation.EscapedPath(), "/")
	decodedPath, err := url.PathUnescape(escapedPath)
	if err != nil {
		return "", fmt.Errorf("invalid escaped endpoint path %q: %w", escapedPath, err)
	}
	base.Path = decodedPath
	canonicalPath := (&url.URL{Path: decodedPath}).EscapedPath()
	if canonicalPath == escapedPath {
		base.RawPath = ""
	} else {
		base.RawPath = escapedPath
	}
	if operation.RawQuery != "" {
		if base.RawQuery != "" {
			base.RawQuery += "&"
		}
		base.RawQuery += operation.RawQuery
	}
	query := base.Query()
	for key, value := range options.Query {
		query.Set(key, value)
	}
	if len(options.Query) > 0 {
		base.RawQuery = query.Encode()
	}
	return base.String(), nil
}

type ComposedAdapter struct {
	AdapterID       string
	AdapterProtocol Protocol
	Endpoint        Endpoint
	EndpointOptions EndpointOptions
	Request         RequestCodec
	Transport       Transport
	Response        ResponseCodec
}

func (a ComposedAdapter) ID() string         { return a.AdapterID }
func (a ComposedAdapter) Protocol() Protocol { return a.AdapterProtocol }
func (a ComposedAdapter) NegotiateClientFormat(format normalize.Format) CompatibilityDecision {
	if support, ok := a.Response.(ClientFormatSupport); ok {
		return support.SupportsClientFormat(format)
	}
	return CompatibilityDecision{Reason: "response codec does not declare client formats"}
}
func (a ComposedAdapter) Prepare(ctx context.Context, req NormalizedRequest, route Route, credential Credential) (UpstreamRequest, error) {
	upstream, err := a.Request.Prepare(ctx, req, route, credential)
	if err != nil {
		return UpstreamRequest{}, err
	}
	if a.Endpoint == nil {
		return UpstreamRequest{}, fmt.Errorf("adapter %q has no endpoint primitive", a.AdapterID)
	}
	upstream.URL, err = a.Endpoint.Resolve(route.BaseURL, upstream.URL, a.EndpointOptions)
	if err != nil {
		return UpstreamRequest{}, fmt.Errorf("resolve endpoint %q: %w", a.Endpoint.ID(), err)
	}
	return upstream, nil
}
func (a ComposedAdapter) Execute(ctx context.Context, req UpstreamRequest) (UpstreamResponse, error) {
	return a.Transport.Execute(ctx, req)
}
func (a ComposedAdapter) ClassifyError(status int, body []byte) ErrorClass {
	return a.Response.ClassifyError(status, body)
}
func (a ComposedAdapter) TranslateStream(ctx context.Context, response UpstreamResponse, writer http.ResponseWriter, source normalize.Format, hooks StreamHooks) error {
	return a.Response.TranslateStream(ctx, response, writer, source, hooks)
}

type HTTPTransport struct {
	Client  *http.Client
	Timeout time.Duration
}

func (t HTTPTransport) ID() string { return "http" }
func (t HTTPTransport) Execute(ctx context.Context, request UpstreamRequest) (UpstreamResponse, error) {
	client := t.Client
	if client == nil {
		timeout := t.Timeout
		if timeout <= 0 {
			timeout = 2 * time.Minute
		}
		client = &http.Client{Timeout: timeout}
	}
	req, err := http.NewRequestWithContext(ctx, request.Method, request.URL, request.Body)
	if err != nil {
		return UpstreamResponse{}, err
	}
	req.Header = request.Headers
	response, err := client.Do(req)
	if err != nil {
		return UpstreamResponse{}, err
	}
	return UpstreamResponse{Status: response.StatusCode, Headers: response.Header, Body: response.Body}, nil
}
