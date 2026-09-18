package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

type Chat struct{ Client *http.Client }

const defaultUpstreamTimeout = 2 * time.Minute

func (Chat) ID() string                { return "openai-chat" }
func (Chat) Protocol() kernel.Protocol { return kernel.ProtocolOpenAIChat }

type chatRequestCodec struct{ adapter Chat }

func (c chatRequestCodec) ID() string { return "openai-chat-json" }
func (c chatRequestCodec) Prepare(ctx context.Context, request kernel.NormalizedRequest, route kernel.Route, credential kernel.Credential) (kernel.UpstreamRequest, error) {
	return c.adapter.Prepare(ctx, request, route, credential)
}

type chatResponseCodec struct{ adapter Chat }

func (c chatResponseCodec) ID() string { return "openai-sse" }
func (c chatResponseCodec) ClassifyError(status int, body []byte) kernel.ErrorClass {
	return c.adapter.ClassifyError(status, body)
}
func (c chatResponseCodec) TranslateStream(ctx context.Context, response kernel.UpstreamResponse, writer http.ResponseWriter, source normalize.Format, hooks kernel.StreamHooks) error {
	return c.adapter.TranslateStream(ctx, response, writer, source, hooks)
}

func NewAdapter() kernel.ProviderAdapter {
	adapter := Chat{}
	return kernel.ComposedAdapter{AdapterID: "openai-chat", AdapterProtocol: kernel.ProtocolOpenAIChat, Request: chatRequestCodec{adapter}, Transport: kernel.HTTPTransport{}, Response: chatResponseCodec{adapter}}
}

func (a Chat) Prepare(_ context.Context, request kernel.NormalizedRequest, route kernel.Route, credential kernel.Credential) (kernel.UpstreamRequest, error) {
	if route.BaseURL == "" {
		return kernel.UpstreamRequest{}, fmt.Errorf("route %s has no base URL", route.ID)
	}
	base := strings.TrimRight(route.BaseURL, "/")
	url := base
	if !strings.HasSuffix(url, "/chat/completions") {
		url += "/chat/completions"
	}
	body := map[string]any{}
	for key, value := range request.Raw {
		body[key] = value
	}
	body["model"] = route.ExternalModel
	body["stream"] = request.Stream
	if len(request.Messages) > 0 {
		body["messages"] = request.Messages
	}
	data, err := json.Marshal(body)
	if err != nil {
		return kernel.UpstreamRequest{}, err
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	secret := credential.Secret
	if secret != "" {
		headers.Set("Authorization", "Bearer "+secret)
	}
	return kernel.UpstreamRequest{Method: http.MethodPost, URL: url, Headers: headers, Body: bytes.NewReader(data)}, nil
}

func (a Chat) Execute(ctx context.Context, request kernel.UpstreamRequest) (kernel.UpstreamResponse, error) {
	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: defaultUpstreamTimeout}
	}
	req, err := http.NewRequestWithContext(ctx, request.Method, request.URL, request.Body)
	if err != nil {
		return kernel.UpstreamResponse{}, err
	}
	req.Header = request.Headers
	response, err := client.Do(req)
	if err != nil {
		return kernel.UpstreamResponse{}, err
	}
	return kernel.UpstreamResponse{Status: response.StatusCode, Headers: response.Header, Body: response.Body}, nil
}

func (Chat) ClassifyError(status int, body []byte) kernel.ErrorClass {
	lower := strings.ToLower(string(body))
	switch {
	case status == 401 || status == 403:
		return kernel.ErrorAuth
	case strings.Contains(lower, "invalid api key"), strings.Contains(lower, "authentication"), strings.Contains(lower, "unauthorized"):
		return kernel.ErrorAuth
	case status == 408 || status == 409 || status == 429 || status >= 500, strings.Contains(lower, "rate limit"), strings.Contains(lower, "quota"):
		return kernel.ErrorCooldown
	case status >= 400:
		return kernel.ErrorTerminal
	default:
		return ""
	}
}

func (a Chat) TranslateStream(ctx context.Context, response kernel.UpstreamResponse, writer http.ResponseWriter, _ normalize.Format, hooks kernel.StreamHooks) error {
	for key, values := range response.Headers {
		for _, value := range values {
			writer.Header().Add(key, value)
		}
	}
	writer.WriteHeader(response.Status)
	if hooks.OnFirstByte != nil {
		hooks.OnFirstByte(timeNow())
	}
	_, err := io.Copy(writer, response.Body)
	if err != nil {
		if hooks.OnError != nil {
			hooks.OnError(err)
		}
		return err
	}
	if hooks.OnComplete != nil {
		hooks.OnComplete(kernel.UsageEvent{Status: "ok"})
	}
	return nil
}

// Isolated for deterministic adapter tests.
var timeNow = func() time.Time { return time.Now() }
