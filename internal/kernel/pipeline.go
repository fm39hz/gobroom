package kernel

import (
	"context"
	"net/http"
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

type Transport interface {
	ID() string
	Execute(context.Context, UpstreamRequest) (UpstreamResponse, error)
}

type ComposedAdapter struct {
	AdapterID       string
	AdapterProtocol Protocol
	Request         RequestCodec
	Transport       Transport
	Response        ResponseCodec
}

func (a ComposedAdapter) ID() string         { return a.AdapterID }
func (a ComposedAdapter) Protocol() Protocol { return a.AdapterProtocol }
func (a ComposedAdapter) Prepare(ctx context.Context, req NormalizedRequest, route Route, credential Credential) (UpstreamRequest, error) {
	return a.Request.Prepare(ctx, req, route, credential)
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
