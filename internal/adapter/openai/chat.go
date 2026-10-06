package openai

import (
	"bytes"
	"context"
	"encoding/json"
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

func (chatResponseCodec) SupportsClientFormat(format normalize.Format) kernel.CompatibilityDecision {
	return kernel.CompatibilityDecision{Supported: format == normalize.FormatOpenAIChat, Lossless: format == normalize.FormatOpenAIChat, Reason: "OpenAI Chat codec currently emits Chat Completions wire format"}
}

func (c chatResponseCodec) ID() string { return "openai-sse" }
func (c chatResponseCodec) ClassifyError(status int, body []byte) kernel.ErrorClass {
	return c.adapter.ClassifyError(status, body)
}
func (c chatResponseCodec) TranslateStream(ctx context.Context, response kernel.UpstreamResponse, writer http.ResponseWriter, source normalize.Format, hooks kernel.StreamHooks) error {
	return c.adapter.TranslateStream(ctx, response, writer, source, hooks)
}

func NewAdapter() kernel.ProviderAdapter {
	adapter := Chat{}
	return kernel.ComposedAdapter{AdapterID: "openai-chat", AdapterProtocol: kernel.ProtocolOpenAIChat, Endpoint: kernel.HTTPJSONEndpoint{}, Request: chatRequestCodec{adapter}, Transport: kernel.HTTPTransport{}, Response: chatResponseCodec{adapter}}
}

func NewChatCodecs() (kernel.RequestCodec, kernel.ResponseCodec) {
	adapter := Chat{}
	return chatRequestCodec{adapter}, chatResponseCodec{adapter}
}

func (a Chat) Prepare(_ context.Context, request kernel.NormalizedRequest, route kernel.Route, credential kernel.Credential) (kernel.UpstreamRequest, error) {
	url := "/chat/completions"
	body := map[string]any{}
	for key, value := range request.Raw {
		body[key] = value
	}
	body["model"] = route.ExternalModel
	body["stream"] = request.Stream
	if len(request.Messages) > 0 {
		messages := make([]kernel.Message, 0, len(request.Messages)+len(request.Prompt.Layers))
		for _, layer := range request.Prompt.Layers {
			messages = append(messages, kernel.Message{Role: layer.Role, Content: layer.Text})
		}
		messages = append(messages, request.Messages...)
		body["messages"] = messages
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
	if strings.Contains(strings.ToLower(response.Headers.Get("content-type")), "text/event-stream") {
		return streamSSEEvents(response.Body, writer, hooks)
	}
	if hooks.OnFirstByte != nil {
		hooks.OnFirstByte(timeNow())
	}
	data, err := io.ReadAll(response.Body)
	if err == nil {
		_, err = writer.Write(data)
		observeOpenAIJSON(data, hooks.OnEvent)
	}
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
