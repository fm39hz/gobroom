package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	anthropic := input.Request.SourceFormat == normalize.FormatAnthropic
	result := make([]kernel.FacetMapping, 0, len(input.Policy.RequiredFacets))
	for _, facet := range input.Policy.RequiredFacets {
		if kernel.IsResponseCompatibilityFacet(facet) {
			continue
		}
		mapping := kernel.FacetMapping{Facet: facet, Paths: []string{"request"}, Disposition: kernel.FacetUnsupported, Reason: "OpenAI Chat request codec has no declared mapping for this input facet"}
		if native {
			switch facet {
			case kernel.FacetWireRequest, kernel.FacetPromptLayers, kernel.FacetToolDefinitions, kernel.FacetToolHistory, kernel.FacetToolChoice, kernel.FacetReasoningIntent, kernel.FacetVisionInput, kernel.FacetAudioInput, kernel.FacetVideoInput, kernel.FacetDocumentInput, kernel.FacetGenerationOptions:
				mapping.Disposition = kernel.FacetPreserved
				mapping.Reason = "the OpenAI Chat request is forwarded in its native wire contract"
			}
		} else if anthropic {
			mapping.Disposition = kernel.FacetUnsupported
			mapping.Reason = "Anthropic request semantics are not represented by the OpenAI Chat codec"
			switch facet {
			case kernel.FacetWireRequest:
				mapping.Disposition, mapping.Reason = kernel.FacetTranslated, "Anthropic Messages envelopes are rebuilt from typed request IR"
			case kernel.FacetPromptLayers:
				if openAIAnthropicPromptRepresentable(input.Request) {
					mapping.Disposition, mapping.Reason = kernel.FacetTranslated, "text system blocks are encoded as OpenAI Chat system messages"
				}
			case kernel.FacetToolDefinitions:
				if openAIAnthropicToolsRepresentable(input.Request) {
					mapping.Disposition, mapping.Reason = kernel.FacetTranslated, "Anthropic input schemas are encoded as OpenAI function declarations"
				}
			case kernel.FacetToolHistory:
				if openAIAnthropicHistoryRepresentable(input.Request) {
					mapping.Disposition, mapping.Reason = kernel.FacetTranslated, "tool_use/tool_result history is mapped to assistant tool_calls and tool messages"
				}
			case kernel.FacetToolChoice:
				if openAIAnthropicToolChoiceRepresentable(input.Request.ToolChoice) && openAIAnthropicToolChoiceMatchesTools(input.Request) {
					mapping.Disposition, mapping.Reason = kernel.FacetTranslated, "Anthropic tool choice is mapped to OpenAI Chat tool_choice"
				}
			case kernel.FacetGenerationOptions:
				if openAIAnthropicGenerationRepresentable(input.Request.Generation) {
					mapping.Disposition, mapping.Reason = kernel.FacetTranslated, "common token, temperature, top-p and stop options are encoded as OpenAI Chat fields"
				}
			case kernel.FacetVisionInput:
				if openAIAnthropicContentRepresentable(input.Request) {
					mapping.Disposition, mapping.Reason = kernel.FacetTranslated, "Anthropic base64/URL image sources are encoded as OpenAI image_url parts"
				}
			}
		}
		result = append(result, mapping)
	}
	return result
}

func openAIAnthropicPromptRepresentable(request kernel.NormalizedRequest) bool {
	for _, layer := range request.Prompt.Layers {
		if len(layer.Parts) == 0 {
			continue
		}
		for _, part := range layer.Parts {
			if part.Type != "text" || part.Metadata["cache_control"] != nil {
				return false
			}
		}
	}
	return true
}

func openAIAnthropicToolsRepresentable(request kernel.NormalizedRequest) bool {
	for _, unsupported := range request.UnsupportedFacets {
		if unsupported == kernel.FacetToolDefinitions {
			return false
		}
	}
	for _, tool := range request.Tools {
		name := tool.Name
		if tool.Function != nil {
			if value, ok := tool.Function["name"].(string); ok && value != "" {
				name = value
			}
		}
		if strings.TrimSpace(name) == "" || tool.Function == nil || tool.Function["parameters"] == nil {
			return false
		}
	}
	return true
}

func openAIAnthropicHistoryRepresentable(request kernel.NormalizedRequest) bool {
	for _, facet := range request.UnsupportedFacets {
		if facet == kernel.FacetToolHistory || facet == kernel.FacetReasoningSignature || facet == kernel.FacetToolResultStatus || facet == kernel.FacetToolBlockOrder || facet == kernel.FacetOpaqueContent {
			return false
		}
	}
	return openAIAnthropicContentRepresentable(request)
}

func openAIAnthropicToolChoiceRepresentable(choice normalize.ToolChoice) bool {
	if !choice.Set {
		return true
	}
	switch choice.Mode {
	case "auto", "any", "none":
		return choice.Name == ""
	case "tool":
		return strings.TrimSpace(choice.Name) != ""
	default:
		return false
	}
}

func openAIAnthropicToolChoiceMatchesTools(request kernel.NormalizedRequest) bool {
	if !request.ToolChoice.Set || request.ToolChoice.Mode != "tool" {
		return true
	}
	for _, tool := range request.Tools {
		name := tool.Name
		if tool.Function != nil {
			if value, ok := tool.Function["name"].(string); ok && value != "" {
				name = value
			}
		}
		if name == request.ToolChoice.Name {
			return true
		}
	}
	return false
}

func openAIAnthropicGenerationRepresentable(options normalize.GenerationOptions) bool {
	if len(options.Unsupported) > 0 {
		return false
	}
	if options.MaxOutputTokens != nil && *options.MaxOutputTokens <= 0 {
		return false
	}
	if options.Temperature != nil && (*options.Temperature < 0 || *options.Temperature > 2) {
		return false
	}
	if options.TopP != nil && (*options.TopP < 0 || *options.TopP > 1) {
		return false
	}
	return true
}

func openAIAnthropicContentRepresentable(request kernel.NormalizedRequest) bool {
	for _, message := range request.Messages {
		if _, err := openAIChatContent(message.Content); err != nil {
			return false
		}
		for _, call := range message.ToolCalls {
			if call.ID == "" || strings.TrimSpace(call.Name) == "" {
				return false
			}
		}
	}
	return true
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
	var body map[string]any
	if request.SourceFormat == normalize.FormatOpenAIChat {
		body = make(map[string]any, len(request.Raw)+2)
		for key, value := range request.Raw {
			body[key] = value
		}
		body["model"] = route.ExternalModel
		body["stream"] = request.Stream
		if len(request.Messages) > 0 || request.Mutations.Messages || request.Mutations.Prompt {
			messages := make([]any, 0, len(request.Messages)+len(request.Prompt.Layers))
			for _, layer := range request.Prompt.Layers {
				messages = append(messages, map[string]any{"role": layer.Role, "content": layer.Text})
			}
			for _, message := range request.Messages {
				encoded, err := nativeChatMessage(message)
				if err != nil {
					return kernel.UpstreamRequest{}, err
				}
				messages = append(messages, encoded)
			}
			body["messages"] = messages
		}
		if request.Mutations.Tools {
			if len(request.Tools) == 0 {
				delete(body, "tools")
			} else {
				tools := make([]map[string]any, 0, len(request.Tools))
				for _, tool := range request.Tools {
					tools = append(tools, nativeChatTool(tool))
				}
				body["tools"] = tools
			}
		}
		if request.Mutations.ToolChoice {
			if request.ToolChoice.Set {
				choice, err := openAIChatToolChoice(request.ToolChoice)
				if err != nil {
					return kernel.UpstreamRequest{}, err
				}
				body["tool_choice"] = choice
			} else {
				delete(body, "tool_choice")
			}
			if request.ToolChoice.DisableParallelTools {
				body["parallel_tool_calls"] = false
			} else {
				body["parallel_tool_calls"] = true
			}
		}
		applyTransformedChatGeneration(body, request)
		if request.Mutations.Thinking {
			delete(body, "thinking")
			delete(body, "reasoning")
			if request.Thinking.Effort == "" {
				delete(body, "reasoning_effort")
			} else {
				body["reasoning_effort"] = request.Thinking.Effort
			}
		}
	} else if request.SourceFormat == normalize.FormatAnthropic {
		if len(request.UnsupportedFacets) > 0 {
			return kernel.UpstreamRequest{}, fmt.Errorf("Anthropic request contains unsupported semantic facets: %s", strings.Join(request.UnsupportedFacets, ", "))
		}
		if request.Thinking.Mode != "" && request.Thinking.Mode != "inherit" || request.Thinking.Effort != "" || request.Thinking.BudgetTokens > 0 {
			return kernel.UpstreamRequest{}, fmt.Errorf("OpenAI Chat codec cannot translate Anthropic thinking intent without a declared provider capability")
		}
		var err error
		body, err = encodeAnthropicRequestAsOpenAIChat(request, route.ExternalModel)
		if err != nil {
			return kernel.UpstreamRequest{}, err
		}
	} else {
		return kernel.UpstreamRequest{}, fmt.Errorf("OpenAI Chat codec cannot encode client format %q", request.SourceFormat)
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

func openAIChatToolChoice(choice normalize.ToolChoice) (any, error) {
	metadata := copyObject(choice.Metadata)
	functionMetadata := copyObject(openAIObject(metadata["function"]))
	delete(metadata, "function")
	switch choice.Mode {
	case "none", "auto", "required":
		if len(metadata) > 0 || len(functionMetadata) > 0 {
			return nil, fmt.Errorf("OpenAI tool choice mode %q cannot preserve opaque object metadata", choice.Mode)
		}
		return choice.Mode, nil
	case "any":
		if len(metadata) > 0 || len(functionMetadata) > 0 {
			return nil, fmt.Errorf("OpenAI tool choice mode %q cannot preserve opaque object metadata", choice.Mode)
		}
		return "required", nil
	case "tool":
		if strings.TrimSpace(choice.Name) == "" {
			return nil, fmt.Errorf("OpenAI tool choice requires a function name")
		}
		if functionMetadata == nil {
			functionMetadata = map[string]any{}
		}
		functionMetadata["name"] = choice.Name
		metadata["type"] = "function"
		metadata["function"] = functionMetadata
		return metadata, nil
	default:
		return nil, fmt.Errorf("OpenAI Chat cannot encode tool choice %q", choice.Mode)
	}
}

func nativeChatMessage(message normalize.Message) (map[string]any, error) {
	if message.Metadata == nil {
		switch message.Content.(type) {
		case nil, string, []normalize.ContentPart:
		default:
			return nil, fmt.Errorf("new OpenAI Chat message content %T has no canonical wire mapping", message.Content)
		}
		switch message.Role {
		case "system", "developer", "user", "assistant", "tool":
		default:
			return nil, fmt.Errorf("new OpenAI Chat message role %q has no native mapping", message.Role)
		}
		if message.Role == "tool" {
			if message.ToolCallID == "" || len(message.ToolCalls) > 0 {
				return nil, fmt.Errorf("new OpenAI Chat tool messages require a call ID and cannot contain tool calls")
			}
		} else {
			if message.ToolCallID != "" {
				return nil, fmt.Errorf("new OpenAI Chat %s messages cannot carry a tool-result call ID", message.Role)
			}
			if len(message.ToolCalls) > 0 && message.Role != "assistant" {
				return nil, fmt.Errorf("new OpenAI Chat tool calls require the assistant role")
			}
		}
		if message.Content == nil && len(message.ToolCalls) == 0 {
			return nil, fmt.Errorf("new OpenAI Chat %s messages require content or an assistant tool call", message.Role)
		}
	}
	encoded := copyObject(message.Metadata)
	encoded["role"] = message.Role
	if message.Name != "" {
		encoded["name"] = message.Name
	}
	if message.ToolCallID != "" {
		encoded["tool_call_id"] = message.ToolCallID
	}
	if message.Content == nil {
		delete(encoded, "content")
	} else {
		content, err := nativeChatContent(message.Content, message.Role)
		if err != nil {
			return nil, err
		}
		encoded["content"] = content
	}
	if len(message.ToolCalls) == 0 {
		delete(encoded, "tool_calls")
	} else {
		calls := make([]map[string]any, 0, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			if call.ID == "" || call.Name == "" {
				return nil, fmt.Errorf("OpenAI Chat tool calls require stable IDs and function names")
			}
			if call.Type != "" && call.Type != "function" {
				return nil, fmt.Errorf("OpenAI Chat tool-call type %q has no native mapping", call.Type)
			}
			item := copyObject(call.Metadata)
			function := copyObject(openAIObject(item["function"]))
			if function == nil {
				function = map[string]any{}
			}
			arguments, err := openAIArguments(call.Arguments)
			if err != nil {
				return nil, fmt.Errorf("encode tool call %q arguments: %w", call.Name, err)
			}
			function["name"], function["arguments"] = call.Name, arguments
			item["id"], item["type"], item["function"] = call.ID, "function", function
			calls = append(calls, item)
		}
		encoded["tool_calls"] = calls
	}
	return encoded, nil
}

func nativeChatContent(content any, role string) (any, error) {
	parts, ok := content.([]normalize.ContentPart)
	if !ok {
		return content, nil
	}
	blocks := make([]any, 0, len(parts))
	for index, part := range parts {
		block := copyObject(part.Metadata)
		switch part.Type {
		case "text":
			block["type"], block["text"] = "text", part.Text
		case "image":
			if role != "user" {
				return nil, fmt.Errorf("OpenAI Chat cannot encode image content for message role %q", role)
			}
			imageURL := part.URL
			if imageURL == "" && part.Data != "" && part.MediaType != "" {
				imageURL = "data:" + part.MediaType + ";base64," + part.Data
			}
			if imageURL == "" {
				return nil, fmt.Errorf("OpenAI Chat image content part %d has no URL or typed data", index)
			}
			image := copyObject(openAIObject(block["image_url"]))
			image["url"] = imageURL
			block["type"], block["image_url"] = "image_url", image
		default:
			return nil, fmt.Errorf("OpenAI Chat content part %q has no native message mapping", part.Type)
		}
		blocks = append(blocks, block)
	}
	return blocks, nil
}

func nativeChatTool(tool normalize.Tool) map[string]any {
	encoded := copyObject(tool.Metadata)
	if tool.Type != "" {
		encoded["type"] = tool.Type
	}
	if tool.Function != nil {
		function := copyObject(tool.Function)
		if tool.Name != "" {
			function["name"] = tool.Name
		}
		encoded["function"] = function
	}
	return encoded
}

func copyObject(source map[string]any) map[string]any {
	result := make(map[string]any, len(source)+2)
	for key, value := range source {
		result[key] = value
	}
	return result
}

func openAIObject(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

func applyTransformedChatGeneration(body map[string]any, request kernel.NormalizedRequest) {
	mutations := request.Mutations
	if mutations.GenerationMaxOutput {
		keys := []string{"max_completion_tokens", "max_tokens"}
		preferred := "max_tokens"
		for _, key := range keys {
			if _, exists := request.Raw[key]; exists {
				preferred = key
				break
			}
		}
		for _, key := range keys {
			delete(body, key)
		}
		if request.Generation.MaxOutputTokens != nil {
			body[preferred] = *request.Generation.MaxOutputTokens
		}
	}
	if mutations.GenerationTemperature {
		if request.Generation.Temperature == nil {
			delete(body, "temperature")
		} else {
			body["temperature"] = *request.Generation.Temperature
		}
	}
	if mutations.GenerationTopP {
		if request.Generation.TopP == nil {
			delete(body, "top_p")
		} else {
			body["top_p"] = *request.Generation.TopP
		}
	}
	if mutations.GenerationStopSequences {
		if len(request.Generation.StopSequences) == 0 {
			delete(body, "stop")
		} else {
			body["stop"] = append([]string(nil), request.Generation.StopSequences...)
		}
	}
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
