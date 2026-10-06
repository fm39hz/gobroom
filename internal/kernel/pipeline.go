package kernel

import (
	"context"
	"fmt"
	"io"
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

type ResponseDecoder interface {
	ID() string
	ClassifyError(status int, body []byte) ErrorClass
	Decode(context.Context, UpstreamResponse, func(ResponseEvent) error, StreamHooks) error
}

type ResponseRenderContext struct {
	Status            int
	Headers           http.Header
	ClientFormat      normalize.Format
	ProviderFormat    normalize.Format
	Streaming         bool
	ProviderStreaming bool
	Model             string
}

type ResponseRenderSession interface {
	Emit(context.Context, ResponseEvent) error
	Finish(context.Context, error) error
}

type ResponseRenderer interface {
	ID() normalize.Format
	SupportsResponse(ResponseRenderContext) CompatibilityDecision
	Begin(context.Context, ResponseRenderContext, http.ResponseWriter) (ResponseRenderSession, error)
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
	Endpoint        Endpoint
	EndpointOptions EndpointOptions
	Request         RequestCodec
	Transport       Transport
	Response        ResponseDecoder
	Renderers       map[normalize.Format]ResponseRenderer
	ProviderFormat  normalize.Format
}

func (a ComposedAdapter) ID() string { return a.AdapterID }
func (a ComposedAdapter) NegotiateClientFormat(format normalize.Format, streaming bool) CompatibilityDecision {
	renderer, ok := a.Renderers[format]
	if !ok {
		return CompatibilityDecision{Reason: "no client renderer is registered for the requested format"}
	}
	return renderer.SupportsResponse(ResponseRenderContext{ClientFormat: format, ProviderFormat: a.ProviderFormat, Streaming: streaming, ProviderStreaming: streaming})
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
func (a ComposedAdapter) RenderResponse(ctx context.Context, response UpstreamResponse, writer http.ResponseWriter, source normalize.Format, hooks StreamHooks) error {
	renderer := a.Renderers[source]
	if renderer == nil {
		return fmt.Errorf("adapter %q has no renderer for client format %q", a.AdapterID, source)
	}
	providerStreaming := strings.Contains(strings.ToLower(response.Headers.Get("content-type")), "text/event-stream")
	session, err := renderer.Begin(ctx, ResponseRenderContext{Status: response.Status, Headers: response.Headers, ClientFormat: source, ProviderFormat: a.ProviderFormat, Streaming: hooks.Streaming, ProviderStreaming: providerStreaming, Model: hooks.Model}, writer)
	if err != nil {
		return err
	}
	decodeHooks := hooks
	decodeHooks.OnEvent = nil
	decodeHooks.OnError = nil
	decodeHooks.TransformResponse = nil
	firstByte := false
	signalFirstByte := func(at time.Time) {
		if firstByte {
			return
		}
		firstByte = true
		if hooks.OnFirstByte != nil {
			hooks.OnFirstByte(at)
		}
	}
	decodeHooks.OnFirstByte = signalFirstByte
	decoderCompleted := false
	completionUsage := UsageEvent{}
	decodeHooks.OnComplete = func(event UsageEvent) {
		decoderCompleted = true
		completionUsage = event
	}
	providerComplete := false
	firstEvent := false
	emit := func(event ResponseEvent) error {
		if event.Kind == EventResponseComplete {
			providerComplete = true
			if event.Usage != nil {
				completionUsage = *event.Usage
			}
		}
		if event.Kind == EventUsage && event.Usage != nil {
			completionUsage = *event.Usage
		}
		if !firstEvent {
			firstEvent = true
			signalFirstByte(time.Now())
		}
		if hooks.TransformResponse != nil {
			transformed, transformErr := hooks.TransformResponse(ctx, event)
			if transformErr != nil {
				return transformErr
			}
			event = transformed
		}
		if hooks.OnEvent != nil {
			hooks.OnEvent(event)
		}
		return session.Emit(ctx, event)
	}
	decodeErr := a.Response.Decode(ctx, response, emit, decodeHooks)
	if decodeErr == nil && !providerComplete {
		decodeErr = io.ErrUnexpectedEOF
	}
	if decodeErr != nil {
		_ = emit(ResponseEvent{At: time.Now(), Kind: EventResponseError, Error: decodeErr.Error()})
	}
	finishErr := session.Finish(ctx, decodeErr)
	if decodeErr != nil {
		if hooks.OnError != nil {
			hooks.OnError(decodeErr)
		}
		return decodeErr
	}
	if finishErr != nil {
		if hooks.OnError != nil {
			hooks.OnError(finishErr)
		}
		return finishErr
	}
	if !decoderCompleted {
		completionUsage.Status = "ok"
	}
	if hooks.OnComplete != nil {
		hooks.OnComplete(completionUsage)
	}
	return nil
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
