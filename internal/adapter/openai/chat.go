package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	egress "github.com/fm39hz/gobroom/internal/adapter/renderers"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

type Chat struct{ Client *http.Client }

const defaultUpstreamTimeout = 2 * time.Minute

func (Chat) ID() string { return "openai-chat" }

type chatRequestCodec struct{ adapter Chat }

func (c chatRequestCodec) ID() string { return "openai-chat-json" }
func (c chatRequestCodec) DescribeCompatibility(input kernel.CompatibilityContext) []kernel.FacetMapping {
	return openAIChatFacetReport(input, normalize.FormatOpenAIChat)
}
func (c chatRequestCodec) Prepare(ctx context.Context, request kernel.NormalizedRequest, route kernel.Route, credential kernel.Credential) (kernel.UpstreamRequest, error) {
	return c.adapter.Prepare(ctx, request, route, credential)
}

func openAIChatFacetReport(input kernel.CompatibilityContext, nativeFormat normalize.Format) []kernel.FacetMapping {
	native := input.Request.SourceFormat == nativeFormat
	result := make([]kernel.FacetMapping, 0, len(input.Policy.RequiredFacets))
	for _, facet := range input.Policy.RequiredFacets {
		if kernel.IsResponseCompatibilityFacet(facet) {
			continue
		}
		mapping := kernel.FacetMapping{Facet: facet, Paths: []string{"request"}, Disposition: kernel.FacetUnsupported, Reason: "OpenAI Chat request codec has no declared mapping for this input facet"}
		if native {
			switch facet {
			case kernel.FacetWireRequest, kernel.FacetPromptLayers, kernel.FacetToolDefinitions, kernel.FacetToolHistory, kernel.FacetReasoningIntent, kernel.FacetVisionInput, kernel.FacetAudioInput, kernel.FacetVideoInput, kernel.FacetDocumentInput, kernel.FacetGenerationOptions:
				mapping.Disposition = kernel.FacetPreserved
				mapping.Reason = "the OpenAI Chat request is forwarded in its native wire contract"
			}
		}
		result = append(result, mapping)
	}
	return result
}

type chatResponseDecoder struct{ adapter Chat }

func (c chatResponseDecoder) ID() string { return "openai-sse" }
func (c chatResponseDecoder) PossibleEvents() []kernel.ResponseEventKind {
	return openAIResponseEvents()
}
func (c chatResponseDecoder) ClassifyError(status int, body []byte) kernel.ErrorClass {
	return c.adapter.ClassifyError(status, body)
}

func openAIResponseEvents() []kernel.ResponseEventKind {
	return []kernel.ResponseEventKind{
		kernel.EventRawFrame, kernel.EventResponseStarted, kernel.EventContentBlockStart,
		kernel.EventContentBlockEnd, kernel.EventTextDelta, kernel.EventThinkingDelta,
		kernel.EventToolCallDelta, kernel.EventUsage, kernel.EventResponseComplete,
	}
}
func (c chatResponseDecoder) Decode(ctx context.Context, response kernel.UpstreamResponse, emit func(kernel.ResponseEvent) error, hooks kernel.StreamHooks) error {
	return c.adapter.DecodeResponse(ctx, response, normalize.FormatOpenAIChat, emit, hooks)
}
func NewAdapter() kernel.ProviderAdapter {
	adapter := Chat{}
	return kernel.ComposedAdapter{AdapterID: "openai-chat", Endpoint: kernel.HTTPJSONEndpoint{}, Request: chatRequestCodec{adapter}, Transport: kernel.HTTPTransport{}, Response: chatResponseDecoder{adapter}, Renderers: rendererMap(), ProviderFormat: normalize.FormatOpenAIChat}
}

func rendererMap() map[normalize.Format]kernel.ResponseRenderer {
	result := make(map[normalize.Format]kernel.ResponseRenderer)
	for _, renderer := range egress.Builtins() {
		result[renderer.ID()] = renderer
	}
	return result
}

func NewChatCodecs() (kernel.RequestCodec, kernel.ResponseDecoder) {
	adapter := Chat{}
	return chatRequestCodec{adapter}, chatResponseDecoder{adapter}
}

func (Chat) DecodeResponse(ctx context.Context, response kernel.UpstreamResponse, wireFormat normalize.Format, emit func(kernel.ResponseEvent) error, hooks kernel.StreamHooks) error {
	return decodeOpenAIResponse(ctx, response, wireFormat, emit, hooks)
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
