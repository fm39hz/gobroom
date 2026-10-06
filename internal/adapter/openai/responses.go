package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

type Responses struct{ Client *http.Client }

const responsesUpstreamTimeout = 2 * time.Minute

func (Responses) ID() string { return "openai-responses" }

type responsesRequestCodec struct{ adapter Responses }

func (c responsesRequestCodec) ID() string { return "openai-responses-json" }
func (c responsesRequestCodec) DescribeCompatibility(input kernel.CompatibilityContext) []kernel.FacetMapping {
	native := input.Request.SourceFormat == normalize.FormatOpenAIResponses
	result := make([]kernel.FacetMapping, 0, len(input.Policy.RequiredFacets))
	for _, facet := range input.Policy.RequiredFacets {
		if kernel.IsResponseCompatibilityFacet(facet) {
			continue
		}
		mapping := kernel.FacetMapping{Facet: facet, Paths: []string{"request"}, Disposition: kernel.FacetUnsupported, Reason: "OpenAI Responses request codec cannot safely translate this facet from the client contract"}
		switch {
		case native && (facet == kernel.FacetWireRequest || facet == kernel.FacetPromptLayers || facet == kernel.FacetToolDefinitions || facet == kernel.FacetToolHistory || facet == kernel.FacetReasoningIntent || facet == kernel.FacetContinuity || facet == kernel.FacetVisionInput || facet == kernel.FacetAudioInput || facet == kernel.FacetVideoInput || facet == kernel.FacetDocumentInput || facet == kernel.FacetGenerationOptions):
			mapping.Disposition = kernel.FacetPreserved
			mapping.Reason = "the OpenAI Responses request is forwarded in its native wire contract"
		case !native && (facet == kernel.FacetReasoningIntent || facet == kernel.FacetContinuity):
			mapping.Disposition = kernel.FacetTranslated
			mapping.Reason = "the canonical reasoning or previous-response value is encoded as an OpenAI Responses field"
		}
		result = append(result, mapping)
	}
	return result
}
func (c responsesRequestCodec) Prepare(ctx context.Context, request kernel.NormalizedRequest, route kernel.Route, credential kernel.Credential) (kernel.UpstreamRequest, error) {
	return c.adapter.Prepare(ctx, request, route, credential)
}

type responsesResponseDecoder struct{ adapter Responses }

func (c responsesResponseDecoder) ID() string { return "openai-responses-sse" }
func (c responsesResponseDecoder) PossibleEvents() []kernel.ResponseEventKind {
	return openAIResponseEvents()
}
func (c responsesResponseDecoder) ClassifyError(status int, body []byte) kernel.ErrorClass {
	return c.adapter.ClassifyError(status, body)
}
func (c responsesResponseDecoder) Decode(ctx context.Context, response kernel.UpstreamResponse, emit func(kernel.ResponseEvent) error, hooks kernel.StreamHooks) error {
	return c.adapter.DecodeResponse(ctx, response, normalize.FormatOpenAIResponses, emit, hooks)
}
func NewResponsesAdapter() kernel.ProviderAdapter {
	adapter := Responses{}
	return kernel.ComposedAdapter{AdapterID: "openai-responses", Endpoint: kernel.HTTPJSONEndpoint{}, Request: responsesRequestCodec{adapter}, Transport: kernel.HTTPTransport{}, Response: responsesResponseDecoder{adapter}, Renderers: rendererMap(), ProviderFormat: normalize.FormatOpenAIResponses}
}

func NewResponsesCodecs() (kernel.RequestCodec, kernel.ResponseDecoder) {
	adapter := Responses{}
	return responsesRequestCodec{adapter}, responsesResponseDecoder{adapter}
}

func (Responses) DecodeResponse(ctx context.Context, response kernel.UpstreamResponse, wireFormat normalize.Format, emit func(kernel.ResponseEvent) error, hooks kernel.StreamHooks) error {
	return decodeOpenAIResponse(ctx, response, wireFormat, emit, hooks)
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
