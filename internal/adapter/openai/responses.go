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

type Responses struct{ Client *http.Client }

const responsesUpstreamTimeout = 2 * time.Minute

func (Responses) ID() string                { return "openai-responses" }
func (Responses) Protocol() kernel.Protocol { return kernel.ProtocolOpenAIResponses }

type responsesRequestCodec struct{ adapter Responses }

func (c responsesRequestCodec) ID() string { return "openai-responses-json" }
func (c responsesRequestCodec) Prepare(ctx context.Context, request kernel.NormalizedRequest, route kernel.Route, credential kernel.Credential) (kernel.UpstreamRequest, error) {
	return c.adapter.Prepare(ctx, request, route, credential)
}

type responsesResponseCodec struct{ adapter Responses }

func (responsesResponseCodec) SupportsClientFormat(format normalize.Format) kernel.CompatibilityDecision {
	return kernel.CompatibilityDecision{Supported: format == normalize.FormatOpenAIResponses, Lossless: format == normalize.FormatOpenAIResponses, Reason: "Responses codec currently emits OpenAI Responses wire format"}
}

func (c responsesResponseCodec) ID() string { return "openai-responses-sse" }
func (c responsesResponseCodec) ClassifyError(status int, body []byte) kernel.ErrorClass {
	return c.adapter.ClassifyError(status, body)
}
func (c responsesResponseCodec) TranslateStream(ctx context.Context, response kernel.UpstreamResponse, writer http.ResponseWriter, source normalize.Format, hooks kernel.StreamHooks) error {
	return c.adapter.TranslateStream(ctx, response, writer, source, hooks)
}

func NewResponsesAdapter() kernel.ProviderAdapter {
	adapter := Responses{}
	return kernel.ComposedAdapter{AdapterID: "openai-responses", AdapterProtocol: kernel.ProtocolOpenAIResponses, Endpoint: kernel.HTTPJSONEndpoint{}, Request: responsesRequestCodec{adapter}, Transport: kernel.HTTPTransport{}, Response: responsesResponseCodec{adapter}}
}

func NewResponsesCodecs() (kernel.RequestCodec, kernel.ResponseCodec) {
	adapter := Responses{}
	return responsesRequestCodec{adapter}, responsesResponseCodec{adapter}
}

func (a Responses) Prepare(_ context.Context, request kernel.NormalizedRequest, route kernel.Route, credential kernel.Credential) (kernel.UpstreamRequest, error) {
	url := "/responses"
	body := map[string]any{}
	for key, value := range request.Raw {
		body[key] = value
	}
	if request.SourceFormat != normalize.FormatOpenAIResponses {
		delete(body, "reasoning_effort")
		for key, value := range normalize.OpenAIResponsesReasoning(request.Thinking) {
			body[key] = value
		}
	}
	body["model"] = route.ExternalModel
	body["stream"] = request.Stream
	if request.Continuity.PreviousResponse != "" {
		if _, supplied := body["previous_response_id"]; !supplied {
			body["previous_response_id"] = request.Continuity.PreviousResponse
		}
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

func (a Responses) Execute(ctx context.Context, request kernel.UpstreamRequest) (kernel.UpstreamResponse, error) {
	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: responsesUpstreamTimeout}
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

func (Responses) ClassifyError(status int, body []byte) kernel.ErrorClass {
	return Chat{}.ClassifyError(status, body)
}
func (a Responses) TranslateStream(_ context.Context, response kernel.UpstreamResponse, writer http.ResponseWriter, _ normalize.Format, hooks kernel.StreamHooks) error {
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
		hooks.OnFirstByte(time.Now())
	}
	data, err := io.ReadAll(response.Body)
	if err == nil {
		_, err = writer.Write(data)
		observeOpenAIJSON(data, hooks.OnEvent)
	}
	if err != nil && hooks.OnError != nil {
		hooks.OnError(err)
	}
	if err == nil && hooks.OnComplete != nil {
		hooks.OnComplete(kernel.UsageEvent{Status: "ok"})
	}
	return err
}
