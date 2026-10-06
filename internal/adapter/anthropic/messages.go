package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	egress "github.com/fm39hz/gobroom/internal/adapter/renderers"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

type Messages struct{ Client *http.Client }

const defaultUpstreamTimeout = 2 * time.Minute

func (Messages) ID() string { return "anthropic-messages" }

type messagesRequestCodec struct{ adapter Messages }

func (c messagesRequestCodec) ID() string { return "anthropic-messages-json" }
func (c messagesRequestCodec) DescribeCompatibility(input kernel.CompatibilityContext) []kernel.FacetMapping {
	native := input.Request.SourceFormat == normalize.FormatAnthropic
	result := make([]kernel.FacetMapping, 0, len(input.Policy.RequiredFacets))
	for _, facet := range input.Policy.RequiredFacets {
		if kernel.IsResponseCompatibilityFacet(facet) {
			continue
		}
		mapping := kernel.FacetMapping{Facet: facet, Paths: []string{"request"}, Disposition: kernel.FacetUnsupported, Reason: "Anthropic Messages request codec has no declared mapping for this facet"}
		switch facet {
		case kernel.FacetWireRequest:
			if native {
				mapping.Disposition = kernel.FacetPreserved
			} else {
				mapping.Disposition = kernel.FacetTranslated
			}
		case kernel.FacetPromptLayers:
			if native {
				mapping.Disposition = kernel.FacetPreserved
			} else if anthropicPromptLayersRepresentable(input.Request) {
				mapping.Disposition = kernel.FacetTranslated
				mapping.Reason = "text prompt layers are mapped to Anthropic's system field"
			} else {
				mapping.Reason = "Anthropic system conversion cannot preserve structured prompt parts"
			}
		case kernel.FacetToolDefinitions:
			if native {
				mapping.Disposition = kernel.FacetPreserved
			} else if anthropicToolDefinitionsRepresentable(input.Request) {
				mapping.Disposition = kernel.FacetTranslated
				mapping.Reason = "function definitions are mapped to Anthropic tool declarations"
			} else {
				mapping.Reason = "one or more function declarations have no representable name"
			}
		case kernel.FacetReasoningIntent:
			if native {
				mapping.Disposition = kernel.FacetPreserved
			} else {
				mapping.Disposition = kernel.FacetTranslated
				mapping.Reason = "canonical thinking intent is mapped to Anthropic thinking fields"
			}
		case kernel.FacetToolHistory, kernel.FacetContinuity:
			if native && facet == kernel.FacetToolHistory {
				mapping.Disposition = kernel.FacetPreserved
				mapping.Reason = "tool history is forwarded in native Anthropic messages"
			}
		case kernel.FacetVisionInput, kernel.FacetAudioInput, kernel.FacetVideoInput, kernel.FacetDocumentInput, kernel.FacetGenerationOptions:
			if native {
				mapping.Disposition = kernel.FacetPreserved
				mapping.Reason = "the native Anthropic payload is forwarded without rewriting"
			}
		}
		result = append(result, mapping)
	}
	return result
}

func anthropicPromptLayersRepresentable(request kernel.NormalizedRequest) bool {
	for _, layer := range request.Prompt.Layers {
		if len(layer.Parts) > 0 {
			return false
		}
	}
	return true
}

func anthropicToolDefinitionsRepresentable(request kernel.NormalizedRequest) bool {
	for _, tool := range request.Tools {
		name := tool.Name
		if tool.Function != nil {
			if functionName, ok := tool.Function["name"].(string); ok && functionName != "" {
				name = functionName
			}
		}
		if name == "" {
			return false
		}
	}
	return true
}
func (c messagesRequestCodec) Prepare(ctx context.Context, request kernel.NormalizedRequest, route kernel.Route, credential kernel.Credential) (kernel.UpstreamRequest, error) {
	return c.adapter.Prepare(ctx, request, route, credential)
}

type messagesResponseDecoder struct{ adapter Messages }

func (c messagesResponseDecoder) ID() string { return "anthropic-sse" }
func (c messagesResponseDecoder) PossibleEvents() []kernel.ResponseEventKind {
	return []kernel.ResponseEventKind{
		kernel.EventRawFrame, kernel.EventResponseStarted, kernel.EventContentBlockStart,
		kernel.EventContentBlockEnd, kernel.EventTextDelta, kernel.EventThinkingDelta,
		kernel.EventToolCallDelta, kernel.EventUsage, kernel.EventResponseComplete,
	}
}
func (c messagesResponseDecoder) ClassifyError(status int, body []byte) kernel.ErrorClass {
	return c.adapter.ClassifyError(status, body)
}
func (c messagesResponseDecoder) Decode(ctx context.Context, response kernel.UpstreamResponse, emit func(kernel.ResponseEvent) error, hooks kernel.StreamHooks) error {
	return c.adapter.DecodeResponse(ctx, response, emit, hooks)
}
func NewAdapter() kernel.ProviderAdapter {
	adapter := Messages{}
	return kernel.ComposedAdapter{AdapterID: "anthropic-messages", Endpoint: kernel.HTTPJSONEndpoint{}, Request: messagesRequestCodec{adapter}, Transport: kernel.HTTPTransport{}, Response: messagesResponseDecoder{adapter}, Renderers: rendererMap(), ProviderFormat: normalize.FormatAnthropic}
}

func rendererMap() map[normalize.Format]kernel.ResponseRenderer {
	result := make(map[normalize.Format]kernel.ResponseRenderer)
	for _, renderer := range egress.Builtins() {
		result[renderer.ID()] = renderer
	}
	return result
}

func NewMessageCodecs() (kernel.RequestCodec, kernel.ResponseDecoder) {
	adapter := Messages{}
	return messagesRequestCodec{adapter}, messagesResponseDecoder{adapter}
}

func (Messages) DecodeResponse(ctx context.Context, response kernel.UpstreamResponse, emit func(kernel.ResponseEvent) error, hooks kernel.StreamHooks) error {
	if response.Body == nil {
		return fmt.Errorf("Anthropic response body is empty")
	}
	contentType := strings.ToLower(response.Headers.Get("content-type"))
	if strings.Contains(contentType, "text/event-stream") {
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 4096), 4*1024*1024)
		scanner.Split(splitAnthropicSSELines)
		var usage kernel.UsageEvent
		started := false
		toolIndex := -1
		for scanner.Scan() {
			if err := ctx.Err(); err != nil {
				return err
			}
			rawLine := append([]byte(nil), scanner.Bytes()...)
			line := strings.TrimRight(string(rawLine), "\r\n")
			if err := emit(kernel.ResponseEvent{At: time.Now(), Kind: kernel.EventRawFrame, WireFormat: normalize.FormatAnthropic, Raw: rawLine}); err != nil {
				return err
			}
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "data:"), "\n"))
			if data == "" || data == "[DONE]" {
				continue
			}
			var event map[string]any
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				return fmt.Errorf("decode Anthropic SSE event: %w", err)
			}
			if !started {
				started = true
				if hooks.OnFirstByte != nil {
					hooks.OnFirstByte(time.Now())
				}
			}
			updatedUsage, updatedToolIndex, err := emitAnthropicEvents(event, usage, toolIndex, emit)
			usage, toolIndex = updatedUsage, updatedToolIndex
			if err != nil {
				return err
			}
			if stringValue(event["type"]) == "message_stop" {
				usage.Status = "ok"
				if hooks.OnComplete != nil {
					hooks.OnComplete(usage)
				}
			}
		}
		return scanner.Err()
	}
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("decode Anthropic response: %w", err)
	}
	if hooks.OnFirstByte != nil {
		hooks.OnFirstByte(time.Now())
	}
	if err := emit(kernel.ResponseEvent{At: time.Now(), Kind: kernel.EventRawFrame, WireFormat: normalize.FormatAnthropic, Raw: append([]byte(nil), data...)}); err != nil {
		return err
	}
	usage, _, err := emitAnthropicEvents(payload, kernel.UsageEvent{}, -1, emit)
	if err != nil {
		return err
	}
	usage.Status = "ok"
	if hooks.OnComplete != nil {
		hooks.OnComplete(usage)
	}
	return nil
}

func splitAnthropicSSELines(data []byte, atEOF bool) (int, []byte, error) {
	if index := bytes.IndexByte(data, '\n'); index >= 0 {
		return index + 1, data[:index+1], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func emitAnthropicEvents(event map[string]any, usage kernel.UsageEvent, toolIndex int, emit func(kernel.ResponseEvent) error) (kernel.UsageEvent, int, error) {
	now := time.Now()
	if message, ok := event["message"].(map[string]any); ok {
		if id := stringValue(message["id"]); id != "" {
			if err := emit(kernel.ResponseEvent{At: now, Kind: kernel.EventResponseStarted, ResponseID: id}); err != nil {
				return usage, toolIndex, err
			}
		}
		if rawUsage, ok := message["usage"].(map[string]any); ok {
			usage.InputTokens = int64(numberValue(rawUsage["input_tokens"]))
		}
		if blocks, ok := message["content"].([]any); ok {
			for index, raw := range blocks {
				block, _ := raw.(map[string]any)
				if err := emitAnthropicBlock(block, index, emit); err != nil {
					return usage, toolIndex, err
				}
			}
		}
	}
	if stringValue(event["type"]) == "" || stringValue(event["type"]) == "message" {
		if id := stringValue(event["id"]); id != "" {
			if err := emit(kernel.ResponseEvent{At: now, Kind: kernel.EventResponseStarted, ResponseID: id}); err != nil {
				return usage, toolIndex, err
			}
		}
		if rawUsage, ok := event["usage"].(map[string]any); ok {
			usage.InputTokens = int64(numberValue(rawUsage["input_tokens"]))
			usage.OutputTokens = int64(numberValue(rawUsage["output_tokens"]))
		}
		if blocks, ok := event["content"].([]any); ok {
			for index, raw := range blocks {
				block, _ := raw.(map[string]any)
				if err := emitAnthropicBlock(block, index, emit); err != nil {
					return usage, toolIndex, err
				}
			}
		}
		stop := stringValue(event["stop_reason"])
		if stop != "" {
			if err := emit(kernel.ResponseEvent{At: now, Kind: kernel.EventContentBlockEnd, StopReason: stop, Usage: &usage}); err != nil {
				return usage, toolIndex, err
			}
			if err := emit(kernel.ResponseEvent{At: now, Kind: kernel.EventResponseComplete, Usage: &usage}); err != nil {
				return usage, toolIndex, err
			}
		}
	}
	switch stringValue(event["type"]) {
	case "message_start":
		message, _ := event["message"].(map[string]any)
		rawUsage, _ := message["usage"].(map[string]any)
		usage.InputTokens = int64(numberValue(rawUsage["input_tokens"]))
	case "content_block_start":
		block, _ := event["content_block"].(map[string]any)
		toolIndex++
		if err := emitAnthropicBlock(block, toolIndex, emit); err != nil {
			return usage, toolIndex, err
		}
	case "content_block_delta":
		delta, _ := event["delta"].(map[string]any)
		if text := stringValue(delta["text"]); text != "" {
			if err := emit(kernel.ResponseEvent{At: now, Kind: kernel.EventTextDelta, Text: text}); err != nil {
				return usage, toolIndex, err
			}
		}
		if thinking := stringValue(delta["thinking"]); thinking != "" {
			if err := emit(kernel.ResponseEvent{At: now, Kind: kernel.EventThinkingDelta, Text: thinking}); err != nil {
				return usage, toolIndex, err
			}
		}
		if partial := stringValue(delta["partial_json"]); partial != "" {
			if err := emit(kernel.ResponseEvent{At: now, Kind: kernel.EventToolCallDelta, Index: toolIndex, ToolArguments: partial}); err != nil {
				return usage, toolIndex, err
			}
		}
	case "message_delta":
		delta, _ := event["delta"].(map[string]any)
		rawUsage, _ := event["usage"].(map[string]any)
		if value := int64(numberValue(rawUsage["output_tokens"])); value > 0 {
			usage.OutputTokens = value
		}
		if err := emit(kernel.ResponseEvent{At: now, Kind: kernel.EventUsage, Usage: &usage}); err != nil {
			return usage, toolIndex, err
		}
		if err := emit(kernel.ResponseEvent{At: now, Kind: kernel.EventContentBlockEnd, StopReason: stringValue(delta["stop_reason"]), Usage: &usage}); err != nil {
			return usage, toolIndex, err
		}
	case "message_stop":
		if err := emit(kernel.ResponseEvent{At: now, Kind: kernel.EventResponseComplete, Usage: &usage}); err != nil {
			return usage, toolIndex, err
		}
	}
	return usage, toolIndex, nil
}

func emitAnthropicBlock(block map[string]any, index int, emit func(kernel.ResponseEvent) error) error {
	now := time.Now()
	typ := stringValue(block["type"])
	if err := emit(kernel.ResponseEvent{At: now, Kind: kernel.EventContentBlockStart, Index: index, BlockType: typ}); err != nil {
		return err
	}
	switch typ {
	case "text":
		if text := stringValue(block["text"]); text != "" {
			return emit(kernel.ResponseEvent{At: now, Kind: kernel.EventTextDelta, Index: index, Text: text})
		}
	case "thinking":
		if text := stringValue(block["thinking"]); text != "" {
			return emit(kernel.ResponseEvent{At: now, Kind: kernel.EventThinkingDelta, Index: index, Text: text})
		}
	case "tool_use":
		arguments := ""
		if input, exists := block["input"]; exists && input != nil {
			encoded, err := json.Marshal(input)
			if err != nil {
				return err
			}
			arguments = string(encoded)
		}
		return emit(kernel.ResponseEvent{At: now, Kind: kernel.EventToolCallDelta, Index: index, ToolCallID: stringValue(block["id"]), ToolName: stringValue(block["name"]), ToolArguments: arguments})
	}
	return nil
}

func (a Messages) Prepare(_ context.Context, request kernel.NormalizedRequest, route kernel.Route, credential kernel.Credential) (kernel.UpstreamRequest, error) {
	url := "/messages"
	body := map[string]any{}
	if request.SourceFormat == normalize.FormatAnthropic {
		for key, value := range request.Raw {
			body[key] = value
		}
	} else {
		if len(request.Prompt.Layers) > 0 {
			parts := make([]map[string]any, 0, len(request.Prompt.Layers))
			for _, layer := range request.Prompt.Layers {
				parts = append(parts, map[string]any{"type": "text", "text": layer.Text})
			}
			body["system"] = parts
		}
		body["messages"] = make([]map[string]any, 0, len(request.Messages))
		for _, message := range request.Messages {
			body["messages"] = append(body["messages"].([]map[string]any), map[string]any{"role": message.Role, "content": message.Content})
		}
		if len(request.Tools) > 0 {
			body["tools"] = anthropicTools(request.Tools)
		}
	}
	body["model"] = route.ExternalModel
	body["stream"] = request.Stream
	if _, ok := body["max_tokens"]; !ok {
		body["max_tokens"] = 4096
	}
	if request.SourceFormat != normalize.FormatAnthropic {
		applyAnthropicThinking(body, request.Thinking)
	}
	data, err := json.Marshal(body)
	if err != nil {
		return kernel.UpstreamRequest{}, err
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("anthropic-version", "2023-06-01")
	secret := credential.Secret
	if secret != "" {
		headers.Set("x-api-key", secret)
	}
	return kernel.UpstreamRequest{Method: http.MethodPost, URL: url, Headers: headers, Body: bytes.NewReader(data)}, nil
}

func applyAnthropicThinking(body map[string]any, intent normalize.ThinkingIntent) {
	translated := normalize.AnthropicReasoning(intent)
	for key, value := range translated {
		body[key] = value
	}
	if intent.Mode == "budget" && intent.BudgetTokens > 0 {
		if max, ok := body["max_tokens"].(float64); !ok || int(max) <= intent.BudgetTokens {
			body["max_tokens"] = intent.BudgetTokens + 1024
		}
	}
}

func (a Messages) Execute(ctx context.Context, request kernel.UpstreamRequest) (kernel.UpstreamResponse, error) {
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

func (Messages) ClassifyError(status int, body []byte) kernel.ErrorClass {
	lower := strings.ToLower(string(body))
	if status == 401 || status == 403 || strings.Contains(lower, "authentication") || strings.Contains(lower, "api key") {
		return kernel.ErrorAuth
	}
	if status == 408 || status == 409 || status == 429 || status >= 500 || strings.Contains(lower, "rate limit") || strings.Contains(lower, "quota") {
		return kernel.ErrorCooldown
	}
	if status >= 400 {
		return kernel.ErrorTerminal
	}
	return ""
}
func anthropicTools(tools []normalize.Tool) []map[string]any {
	result := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		fn := tool.Function
		if fn == nil {
			fn = map[string]any{}
		}
		name, _ := fn["name"].(string)
		if name == "" {
			name = tool.Name
		}
		item := map[string]any{"name": name}
		if description, ok := fn["description"].(string); ok {
			item["description"] = description
		}
		if schema, ok := fn["parameters"]; ok {
			item["input_schema"] = schema
		} else {
			item["input_schema"] = map[string]any{"type": "object"}
		}
		result = append(result, item)
	}
	return result
}

func numberValue(value any) float64 {
	switch n := value.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
}

func stringValue(value any) string { result, _ := value.(string); return result }
