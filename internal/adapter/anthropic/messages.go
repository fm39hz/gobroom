package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
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
	responses := input.Request.SourceFormat == normalize.FormatOpenAIResponses
	result := make([]kernel.FacetMapping, 0, len(input.Policy.RequiredFacets))
	for _, facet := range input.Policy.RequiredFacets {
		if kernel.IsResponseCompatibilityFacet(facet) {
			continue
		}
		mapping := kernel.FacetMapping{Facet: facet, Paths: []string{"request"}, Disposition: kernel.FacetUnsupported, Reason: "Anthropic Messages request codec has no declared mapping for this facet"}
		switch facet {
		case kernel.FacetWireRequest:
			if native {
				if input.Request.Mutations.OperationPayload || input.Request.Mutations.Modalities || input.Request.Mutations.Requirements {
					mapping.Reason = "native Anthropic operation payload/modalities have no egress overlay"
				} else {
					mapping.Disposition = kernel.FacetPreserved
				}
			} else if !responses || anthropicResponsesEnvelopeRepresentable(input.Request) {
				mapping.Disposition = kernel.FacetTranslated
			} else {
				mapping.Reason = "Responses request contains fields Anthropic Messages cannot preserve"
			}
		case kernel.FacetPromptLayers:
			if native {
				if input.Request.Mutations.Prompt && len(input.Request.Prompt.Layers) != 1 {
					mapping.Reason = "native Anthropic system overlay requires one representable prompt layer"
				} else {
					mapping.Disposition = kernel.FacetPreserved
				}
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
		case kernel.FacetToolChoice:
			if native {
				if input.Request.Mutations.ToolChoice && (!anthropicToolChoiceRepresentable(input.Request.ToolChoice) || !anthropicToolChoiceMatchesTools(input.Request)) {
					mapping.Reason = "transformed native tool choice does not match the typed tool set"
				} else {
					mapping.Disposition = kernel.FacetPreserved
				}
			} else if len(input.Request.ToolChoice.Metadata) == 0 && anthropicToolChoiceRepresentable(input.Request.ToolChoice) && anthropicToolChoiceMatchesTools(input.Request) {
				mapping.Disposition = kernel.FacetTranslated
				mapping.Reason = "canonical tool choice is mapped to Anthropic tool_choice"
			}
		case kernel.FacetReasoningIntent:
			if native {
				if input.Request.Mutations.Thinking && anthropicHasUnsupportedFacet(input.Request, "reasoning.intent") {
					mapping.Reason = "native thinking transform cannot rewrite an unsupported source reasoning envelope"
				} else {
					mapping.Disposition = kernel.FacetPreserved
				}
			} else if !anthropicHasUnsupportedFacet(input.Request, kernel.FacetReasoningIntent) && anthropicReasoningIntentRepresentable(input.Request.Thinking) {
				mapping.Disposition = kernel.FacetTranslated
				mapping.Reason = "canonical thinking intent is mapped to Anthropic thinking fields"
			} else {
				mapping.Reason = "Anthropic requires a supported reasoning mode and a positive budget for enabled thinking"
			}
		case kernel.FacetReasoningBudget:
			if native {
				if input.Request.Mutations.Thinking && anthropicHasUnsupportedFacet(input.Request, kernel.FacetReasoningBudget) {
					mapping.Reason = "native thinking transform cannot rewrite an unsupported source reasoning budget"
				} else {
					mapping.Disposition = kernel.FacetPreserved
					mapping.Reason = "native Anthropic reasoning budget is forwarded unchanged"
				}
			} else if !anthropicHasUnsupportedFacet(input.Request, kernel.FacetReasoningBudget) && !anthropicHasUnsupportedFacet(input.Request, kernel.FacetReasoningIntent) && (input.Request.Thinking.Mode == "budget" || input.Request.Thinking.Mode == "enabled") && input.Request.Thinking.BudgetTokens > 0 && input.Request.Thinking.Effort == "" {
				mapping.Disposition = kernel.FacetTranslated
				mapping.Reason = "reasoning budget is mapped to Anthropic thinking.budget_tokens"
				if maxOutput := input.Request.Generation.MaxOutputTokens; maxOutput != nil && *maxOutput <= input.Request.Thinking.BudgetTokens {
					effective := input.Request.Thinking.BudgetTokens + 1024
					mapping.Disposition = kernel.FacetDegraded
					mapping.Reason = "Anthropic requires max_tokens above the reasoning budget; explicit loss approval is required"
					mapping.Losses = []kernel.LossRecord{{
						ID: "anthropic.reasoning-budget-raises-max-output", Requested: *maxOutput, Effective: effective,
						SemanticPaths: []string{"generation.max_output_tokens", "thinking.budget_tokens"},
					}}
				}
			}
		case kernel.FacetToolHistory, kernel.FacetContinuity:
			if native && facet == kernel.FacetToolHistory {
				mapping.Disposition = kernel.FacetPreserved
				mapping.Reason = "tool history is forwarded in native Anthropic messages"
			}
		case kernel.FacetVisionInput, kernel.FacetAudioInput, kernel.FacetVideoInput, kernel.FacetDocumentInput:
			if native {
				mapping.Disposition = kernel.FacetPreserved
				mapping.Reason = "the native Anthropic payload is forwarded without rewriting"
			} else if facet == kernel.FacetVisionInput && anthropicTranslatedVisionRepresentable(input.Request) {
				mapping.Disposition = kernel.FacetTranslated
				mapping.Reason = "typed base64 and URL images are encoded as Anthropic image source blocks"
			}
		case kernel.FacetGenerationOptions:
			if native {
				mapping.Disposition = kernel.FacetPreserved
				mapping.Reason = "native Anthropic generation options are forwarded without rewriting"
			} else if anthropicGenerationOptionsRepresentable(input.Request) {
				mapping.Disposition = kernel.FacetTranslated
				mapping.Reason = "max output tokens, temperature, top-p and stop sequences are mapped to Anthropic fields"
			} else {
				mapping.Reason = "one or more generation options have no declared Anthropic mapping"
			}
		case kernel.FacetReasoningSignature, kernel.FacetOpaqueContent, kernel.FacetToolResultStatus, kernel.FacetToolBlockOrder, kernel.FacetClientMetadata:
			if native {
				mapping.Disposition = kernel.FacetPreserved
				mapping.Reason = "provider-only Anthropic semantics are forwarded in the native request"
			}
		}
		result = append(result, mapping)
	}
	return result
}

func anthropicToolChoiceRepresentable(choice normalize.ToolChoice) bool {
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

func anthropicToolChoice(choice normalize.ToolChoice) map[string]any {
	if !choice.Set {
		return nil
	}
	result := map[string]any{"type": choice.Mode}
	if choice.Mode == "tool" {
		result["name"] = choice.Name
	}
	if choice.DisableParallelTools {
		result["disable_parallel_tool_use"] = true
	}
	return result
}

func anthropicToolChoiceMatchesTools(request kernel.NormalizedRequest) bool {
	if !request.ToolChoice.Set || request.ToolChoice.Mode != "tool" {
		return true
	}
	for _, tool := range request.Tools {
		name := tool.Name
		if tool.Function != nil {
			if functionName, ok := tool.Function["name"].(string); ok && functionName != "" {
				name = functionName
			}
		}
		if name == request.ToolChoice.Name {
			return true
		}
	}
	return false
}

func anthropicPromptLayersRepresentable(request kernel.NormalizedRequest) bool {
	for _, layer := range request.Prompt.Layers {
		for _, part := range layer.Parts {
			if part.Type != "text" || part.Metadata["cache_control"] != nil {
				return false
			}
		}
	}
	return true
}

func anthropicToolDefinitionsRepresentable(request kernel.NormalizedRequest) bool {
	for _, tool := range request.Tools {
		if tool.Function == nil || tool.Function["parameters"] == nil {
			return false
		}
		if strict, ok := tool.Function["strict"].(bool); ok && strict {
			return false
		}
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

func anthropicResponsesEnvelopeRepresentable(request kernel.NormalizedRequest) bool {
	allowed := map[string]bool{
		"model": true, "stream": true, "input": true, "instructions": true,
		"tools": true, "tool_choice": true, "parallel_tool_calls": true,
		"max_output_tokens": true, "max_completion_tokens": true,
		"temperature": true, "top_p": true, "stop": true, "reasoning": true,
	}
	for key := range request.Raw {
		if !allowed[key] {
			return false
		}
	}
	for key := range request.Extensions {
		if !allowed[key] {
			return false
		}
	}
	return true
}

func anthropicTranslatedVisionRepresentable(request kernel.NormalizedRequest) bool {
	found := false
	for _, message := range request.Messages {
		parts, ok := message.Content.([]normalize.ContentPart)
		if !ok {
			continue
		}
		for _, part := range parts {
			if part.Type != "image" {
				continue
			}
			if part.Detail != "" || part.URL == "" && (part.Data == "" || part.MediaType == "") {
				return false
			}
			if strings.HasPrefix(part.URL, "data:") {
				if _, _, err := anthropicImageDataSource(part); err != nil {
					return false
				}
			}
			found = true
		}
	}
	return found
}

func anthropicGenerationOptionsRepresentable(request kernel.NormalizedRequest) bool {
	return len(request.Generation.Unsupported) == 0
}

func anthropicReasoningIntentRepresentable(intent normalize.ThinkingIntent) bool {
	switch intent.Mode {
	case "disabled", "auto", "level":
		return true
	case "budget", "enabled":
		return intent.BudgetTokens > 0 && intent.Effort == ""
	default:
		return false
	}
}

func (c messagesRequestCodec) Prepare(ctx context.Context, request kernel.NormalizedRequest, route kernel.Route, credential kernel.Credential) (kernel.UpstreamRequest, error) {
	return c.adapter.Prepare(ctx, request, route, credential)
}
func (c messagesRequestCodec) PrepareWithCompatibility(ctx context.Context, request kernel.NormalizedRequest, route kernel.Route, credential kernel.Credential, plan kernel.CompatibilityPlan) (kernel.UpstreamRequest, error) {
	return c.adapter.PrepareWithCompatibility(ctx, request, route, credential, plan)
}

type messagesResponseDecoder struct{ adapter Messages }

func (c messagesResponseDecoder) ID() string { return "anthropic-sse" }
func (c messagesResponseDecoder) PossibleEvents() []kernel.ResponseEventKind {
	return []kernel.ResponseEventKind{
		kernel.EventRawFrame, kernel.EventResponseStarted, kernel.EventContentBlockStart,
		kernel.EventContentBlockEnd, kernel.EventTextDelta, kernel.EventThinkingDelta,
		kernel.EventThinkingSignature, kernel.EventToolCallDelta, kernel.EventUsage, kernel.EventResponseComplete,
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
		var nativeUsage anthropicUsageState
		started := false
		completed := false
		toolIndex := -1
		for scanner.Scan() {
			if err := ctx.Err(); err != nil {
				return err
			}
			rawLine := append([]byte(nil), scanner.Bytes()...)
			line := strings.TrimRight(string(rawLine), "\r\n")
			if !strings.HasPrefix(line, "data:") {
				if err := emit(kernel.ResponseEvent{At: time.Now(), Kind: kernel.EventRawFrame, WireFormat: normalize.FormatAnthropic, Raw: rawLine}); err != nil {
					return err
				}
				continue
			}
			data := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "data:"), "\n"))
			if data == "" || data == "[DONE]" {
				if err := emit(kernel.ResponseEvent{At: time.Now(), Kind: kernel.EventRawFrame, WireFormat: normalize.FormatAnthropic, Raw: rawLine}); err != nil {
					return err
				}
				continue
			}
			event, err := decodeAnthropicContent([]byte(data))
			if err != nil {
				return fmt.Errorf("decode Anthropic SSE event: %w", err)
			}
			if stringValue(event["type"]) == "error" || event["error"] != nil {
				if err := reportAnthropicProviderError([]byte(data), nativeUsage.snapshot(), hooks); err != nil {
					return err
				}
			}
			usageFrame, err := decodeAnthropicUsageFrame([]byte(data))
			if err != nil {
				return fmt.Errorf("decode Anthropic usage: %w", err)
			}
			if err := emit(kernel.ResponseEvent{At: time.Now(), Kind: kernel.EventRawFrame, WireFormat: normalize.FormatAnthropic, Raw: rawLine}); err != nil {
				return err
			}
			if !started {
				started = true
				if hooks.OnFirstByte != nil {
					hooks.OnFirstByte(time.Now())
				}
			}
			updatedUsage, updatedToolIndex, err := emitAnthropicEvents(event, usageFrame, nativeUsage, toolIndex, emit)
			nativeUsage, toolIndex = updatedUsage, updatedToolIndex
			if hooks.OnProviderUsage != nil {
				hooks.OnProviderUsage(nativeUsage.snapshot())
			}
			if err != nil {
				return err
			}
			if stringValue(event["type"]) == "message_stop" {
				completed = true
			}
		}
		if err := scanner.Err(); err != nil {
			return err
		}
		if !completed {
			return io.ErrUnexpectedEOF
		}
		usage := nativeUsage.snapshot()
		usage.Status = "ok"
		if hooks.OnComplete != nil {
			hooks.OnComplete(usage)
		}
		return nil
	}
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	payload, err := decodeAnthropicContent(data)
	if err != nil {
		return fmt.Errorf("decode Anthropic response: %w", err)
	}
	if stringValue(payload["type"]) == "error" || payload["error"] != nil {
		return reportAnthropicProviderError(data, kernel.UsageEvent{}, hooks)
	}
	usageFrame, err := decodeAnthropicUsageFrame(data)
	if err != nil {
		return fmt.Errorf("decode Anthropic usage: %w", err)
	}
	if hooks.OnFirstByte != nil {
		hooks.OnFirstByte(time.Now())
	}
	if err := emit(kernel.ResponseEvent{At: time.Now(), Kind: kernel.EventRawFrame, WireFormat: normalize.FormatAnthropic, Raw: append([]byte(nil), data...)}); err != nil {
		return err
	}
	completed := false
	nativeUsage, _, err := emitAnthropicEvents(payload, usageFrame, anthropicUsageState{}, -1, func(event kernel.ResponseEvent) error {
		if event.Kind == kernel.EventResponseComplete {
			completed = true
		}
		return emit(event)
	})
	usage := nativeUsage.snapshot()
	if hooks.OnProviderUsage != nil {
		hooks.OnProviderUsage(usage)
	}
	if err != nil {
		return err
	}
	if !completed {
		return io.ErrUnexpectedEOF
	}
	usage.Status = "ok"
	if hooks.OnComplete != nil {
		hooks.OnComplete(usage)
	}
	return nil
}

func reportAnthropicProviderError(data []byte, usage kernel.UsageEvent, hooks kernel.StreamHooks) error {
	if hooks.OnProviderUsage != nil {
		hooks.OnProviderUsage(usage)
	}
	if len(data) > kernel.MaxProviderResponseErrorBytes {
		return fmt.Errorf("Anthropic terminal error exceeds its evidence bound")
	}
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("invalid Anthropic terminal error event")
	}
	evidenceData, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("invalid Anthropic terminal error envelope")
	}
	evidence, err := kernel.NewProviderResponseError(evidenceData)
	if err != nil {
		return err
	}
	return evidence
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

type anthropicUsageDTO struct {
	InputTokens       *int64              `json:"input_tokens"`
	OutputTokens      *int64              `json:"output_tokens"`
	CachedReadTokens  *int64              `json:"cache_read_input_tokens"`
	CachedWriteTokens *int64              `json:"cache_creation_input_tokens"`
	ReasoningTokens   *int64              `json:"reasoning_tokens"`
	AccountingIssues  []kernel.UsageIssue `json:"-"`
}

type anthropicUsageFrame struct {
	MessageUsage *anthropicUsageDTO
	EventUsage   *anthropicUsageDTO
}

// Keep Anthropic's disjoint wire components native while merging partial
// snapshots. Canonical input is derived from the three known components once.
type anthropicUsageState struct {
	InputTokens       *int64
	OutputTokens      *int64
	CachedReadTokens  *int64
	CachedWriteTokens *int64
	ReasoningTokens   *int64
	AccountingIssues  []kernel.UsageIssue
}

func (state anthropicUsageState) apply(update *anthropicUsageDTO) anthropicUsageState {
	if update == nil {
		return state
	}
	if update.InputTokens != nil {
		state.InputTokens = copyAnthropicCount(update.InputTokens)
	}
	if update.OutputTokens != nil {
		state.OutputTokens = copyAnthropicCount(update.OutputTokens)
	}
	if update.CachedReadTokens != nil {
		state.CachedReadTokens = copyAnthropicCount(update.CachedReadTokens)
	}
	if update.CachedWriteTokens != nil {
		state.CachedWriteTokens = copyAnthropicCount(update.CachedWriteTokens)
	}
	if update.ReasoningTokens != nil {
		state.ReasoningTokens = copyAnthropicCount(update.ReasoningTokens)
	}
	state.AccountingIssues = kernel.MergeProviderUsage(kernel.UsageEvent{AccountingIssues: state.AccountingIssues}, kernel.UsageEvent{AccountingIssues: update.AccountingIssues}).AccountingIssues
	return state
}

func (state anthropicUsageState) snapshot() kernel.UsageEvent {
	var input *int64
	if state.InputTokens != nil && state.CachedReadTokens != nil && state.CachedWriteTokens != nil {
		base, read, write := *state.InputTokens, *state.CachedReadTokens, *state.CachedWriteTokens
		const maxInt64 = int64(^uint64(0) >> 1)
		if base <= maxInt64-read && base+read <= maxInt64-write {
			value := base + read + write
			input = &value
		}
	}
	return kernel.UsageEvent{InputTokens: input, OutputTokens: copyAnthropicCount(state.OutputTokens),
		CachedReadTokens: copyAnthropicCount(state.CachedReadTokens), CachedWriteTokens: copyAnthropicCount(state.CachedWriteTokens),
		ReasoningTokens: copyAnthropicCount(state.ReasoningTokens), AccountingIssues: append([]kernel.UsageIssue(nil), state.AccountingIssues...), Status: "ok"}
}

func copyAnthropicCount(count *int64) *int64 {
	if count == nil {
		return nil
	}
	value := *count
	return &value
}

// Decode semantic content without converting optional usage numbers through
// float64. The original response bytes remain available for native passthrough.
func decodeAnthropicContent(data []byte) (map[string]any, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	delete(fields, "usage")
	if message := bytes.TrimSpace(fields["message"]); len(message) > 0 && message[0] == '{' {
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(message, &nested); err != nil {
			return nil, err
		}
		delete(nested, "usage")
		encoded, err := json.Marshal(nested)
		if err != nil {
			return nil, err
		}
		fields["message"] = encoded
	}
	content, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	var event map[string]any
	if err := json.Unmarshal(content, &event); err != nil {
		return nil, err
	}
	return event, nil
}

func decodeAnthropicUsageFrame(data []byte) (anthropicUsageFrame, error) {
	var raw struct {
		Message json.RawMessage `json:"message"`
		Usage   json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return anthropicUsageFrame{}, err
	}
	var frame anthropicUsageFrame
	if len(raw.Message) > 0 && strings.TrimSpace(string(raw.Message)) != "null" {
		var message struct {
			Usage json.RawMessage `json:"usage"`
		}
		if err := json.Unmarshal(raw.Message, &message); err != nil {
			return anthropicUsageFrame{}, err
		}
		if len(message.Usage) > 0 && strings.TrimSpace(string(message.Usage)) != "null" {
			var usage anthropicUsageDTO
			if err := json.Unmarshal(message.Usage, &usage); err != nil {
				return anthropicUsageFrame{}, err
			}
			frame.MessageUsage = &usage
		}
	}
	if len(raw.Usage) > 0 && strings.TrimSpace(string(raw.Usage)) != "null" {
		var usage anthropicUsageDTO
		if err := json.Unmarshal(raw.Usage, &usage); err != nil {
			return anthropicUsageFrame{}, err
		}
		frame.EventUsage = &usage
	}
	return frame, nil
}

func emitAnthropicEvents(event map[string]any, frame anthropicUsageFrame, usage anthropicUsageState, toolIndex int, emit func(kernel.ResponseEvent) error) (anthropicUsageState, int, error) {
	now := time.Now()
	usage = usage.apply(frame.MessageUsage)
	usage = usage.apply(frame.EventUsage)
	snapshot := usage.snapshot()
	if message, ok := event["message"].(map[string]any); ok {
		if id := stringValue(message["id"]); id != "" {
			if err := emit(kernel.ResponseEvent{At: now, Kind: kernel.EventResponseStarted, ResponseID: id, Usage: &snapshot}); err != nil {
				return usage, toolIndex, err
			}
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
			if err := emit(kernel.ResponseEvent{At: now, Kind: kernel.EventResponseStarted, ResponseID: id, Usage: &snapshot}); err != nil {
				return usage, toolIndex, err
			}
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
			if err := emit(kernel.ResponseEvent{At: now, Kind: kernel.EventContentBlockEnd, StopReason: stop, Usage: &snapshot}); err != nil {
				return usage, toolIndex, err
			}
			if err := emit(kernel.ResponseEvent{At: now, Kind: kernel.EventResponseComplete, Usage: &snapshot}); err != nil {
				return usage, toolIndex, err
			}
		}
	}
	switch stringValue(event["type"]) {
	case "message_start":
		message, _ := event["message"].(map[string]any)
		if id := stringValue(message["id"]); id != "" {
			if err := emit(kernel.ResponseEvent{At: now, Kind: kernel.EventResponseStarted, ResponseID: id, Usage: &snapshot}); err != nil {
				return usage, toolIndex, err
			}
		}
	case "content_block_start":
		block, _ := event["content_block"].(map[string]any)
		toolIndex++
		if err := emitAnthropicBlock(block, toolIndex, emit); err != nil {
			return usage, toolIndex, err
		}
	case "content_block_delta":
		delta, _ := event["delta"].(map[string]any)
		blockIndex := int(numberValue(event["index"]))
		if _, exists := event["index"]; !exists {
			blockIndex = toolIndex
		}
		if text := stringValue(delta["text"]); text != "" {
			if err := emit(kernel.ResponseEvent{At: now, Kind: kernel.EventTextDelta, Index: blockIndex, Text: text}); err != nil {
				return usage, toolIndex, err
			}
		}
		if thinking := stringValue(delta["thinking"]); thinking != "" {
			if err := emit(kernel.ResponseEvent{At: now, Kind: kernel.EventThinkingDelta, Index: blockIndex, Text: thinking}); err != nil {
				return usage, toolIndex, err
			}
		}
		if signature := stringValue(delta["signature"]); signature != "" {
			if err := emit(kernel.ResponseEvent{At: now, Kind: kernel.EventThinkingSignature, Index: blockIndex, Signature: signature}); err != nil {
				return usage, toolIndex, err
			}
		}
		if partial := stringValue(delta["partial_json"]); partial != "" {
			if err := emit(kernel.ResponseEvent{At: now, Kind: kernel.EventToolCallDelta, Index: blockIndex, ToolArguments: partial}); err != nil {
				return usage, toolIndex, err
			}
		}
	case "message_delta":
		delta, _ := event["delta"].(map[string]any)
		if err := emit(kernel.ResponseEvent{At: now, Kind: kernel.EventUsage, Usage: &snapshot}); err != nil {
			return usage, toolIndex, err
		}
		if err := emit(kernel.ResponseEvent{At: now, Kind: kernel.EventContentBlockEnd, StopReason: stringValue(delta["stop_reason"]), Usage: &snapshot}); err != nil {
			return usage, toolIndex, err
		}
	case "message_stop":
		if err := emit(kernel.ResponseEvent{At: now, Kind: kernel.EventResponseComplete, Usage: &snapshot}); err != nil {
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
			if err := emit(kernel.ResponseEvent{At: now, Kind: kernel.EventThinkingDelta, Index: index, Text: text}); err != nil {
				return err
			}
		}
		if signature := stringValue(block["signature"]); signature != "" {
			return emit(kernel.ResponseEvent{At: now, Kind: kernel.EventThinkingSignature, Index: index, Signature: signature})
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

func (a Messages) Prepare(ctx context.Context, request kernel.NormalizedRequest, route kernel.Route, credential kernel.Credential) (kernel.UpstreamRequest, error) {
	return a.PrepareWithCompatibility(ctx, request, route, credential, kernel.CompatibilityPlan{})
}

func (a Messages) PrepareWithCompatibility(ctx context.Context, request kernel.NormalizedRequest, route kernel.Route, credential kernel.Credential, plan kernel.CompatibilityPlan) (kernel.UpstreamRequest, error) {
	url := "/messages"
	body := map[string]any{}
	if request.SourceFormat == normalize.FormatAnthropic {
		for key, value := range request.Raw {
			body[key] = value
		}
		if err := overlayNativeAnthropicMutations(body, request); err != nil {
			return kernel.UpstreamRequest{}, err
		}
	} else {
		if len(request.Prompt.Layers) > 0 {
			parts := make([]map[string]any, 0, len(request.Prompt.Layers))
			for _, layer := range request.Prompt.Layers {
				if len(layer.Parts) == 0 {
					if layer.Text != "" {
						parts = append(parts, map[string]any{"type": "text", "text": layer.Text})
					}
					continue
				}
				for _, part := range layer.Parts {
					if part.Type != "text" || part.Metadata["cache_control"] != nil {
						return kernel.UpstreamRequest{}, fmt.Errorf("Anthropic system prompt cannot encode prompt part %q", part.Type)
					}
					parts = append(parts, map[string]any{"type": "text", "text": part.Text})
				}
			}
			if len(parts) > 0 {
				body["system"] = parts
			}
		}
		body["messages"] = make([]map[string]any, 0, len(request.Messages))
		for _, message := range request.Messages {
			content, err := anthropicTranslatedContent(message.Content)
			if err != nil {
				return kernel.UpstreamRequest{}, fmt.Errorf("encode %s message for Anthropic: %w", message.Role, err)
			}
			body["messages"] = append(body["messages"].([]map[string]any), map[string]any{"role": message.Role, "content": content})
		}
		if len(request.Tools) > 0 {
			body["tools"] = anthropicTools(request.Tools)
		}
		if request.ToolChoice.Set {
			if !anthropicToolChoiceRepresentable(request.ToolChoice) || !anthropicToolChoiceMatchesTools(request) {
				return kernel.UpstreamRequest{}, fmt.Errorf("Anthropic codec cannot represent tool choice %q", request.ToolChoice.Mode)
			}
			body["tool_choice"] = anthropicToolChoice(request.ToolChoice)
		}
		if !anthropicGenerationOptionsRepresentable(request) {
			return kernel.UpstreamRequest{}, fmt.Errorf("Anthropic codec cannot represent generation options: %s", strings.Join(request.Generation.Unsupported, ", "))
		}
		if maxOutput := request.Generation.MaxOutputTokens; maxOutput != nil {
			body["max_tokens"] = *maxOutput
		}
		if temperature := request.Generation.Temperature; temperature != nil {
			body["temperature"] = *temperature
		}
		if topP := request.Generation.TopP; topP != nil {
			body["top_p"] = *topP
		}
		if request.Generation.StopSequences != nil {
			body["stop_sequences"] = append([]string(nil), request.Generation.StopSequences...)
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
	for _, loss := range plan.Losses {
		if loss.ID != "anthropic.reasoning-budget-raises-max-output" {
			continue
		}
		requested, requestedOK := lossInteger(loss.Requested)
		effective, effectiveOK := lossInteger(loss.Effective)
		maxOutput := request.Generation.MaxOutputTokens
		if !requestedOK || !effectiveOK || maxOutput == nil || requested != *maxOutput || effective != request.Thinking.BudgetTokens+1024 || effective <= requested {
			return kernel.UpstreamRequest{}, fmt.Errorf("approved Anthropic reasoning-budget loss does not match the request")
		}
		body["max_tokens"] = effective
	}
	data, err := json.Marshal(body)
	if err != nil {
		return kernel.UpstreamRequest{}, err
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("anthropic-version", "2023-06-01")
	return kernel.PrepareBytesRequest(ctx, http.MethodPost, url, headers, data, credential, kernel.CredentialAuthorization(route.CredentialPresentation), request.Transport.IdempotencyKey != "")
}

func lossInteger(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), int64(int(typed)) == typed
	case float64:
		converted := int(typed)
		return converted, float64(converted) == typed
	default:
		return 0, false
	}
}

func anthropicTranslatedContent(content any) (any, error) {
	if _, isString := content.(string); isString || content == nil {
		return content, nil
	}
	parts, ok := content.([]normalize.ContentPart)
	if !ok {
		return nil, fmt.Errorf("typed content part contract required, got %T", content)
	}
	translated := make([]normalize.ContentPart, len(parts))
	for index, part := range parts {
		part.Metadata = nil
		if part.Type == "image" {
			if part.Detail != "" {
				return nil, fmt.Errorf("Anthropic image source cannot preserve detail=%q", part.Detail)
			}
			if strings.HasPrefix(part.URL, "data:") {
				mediaType, data, err := anthropicImageDataSource(part)
				if err != nil {
					return nil, err
				}
				part.URL = ""
				part.MediaType, part.Data = mediaType, data
			}
		}
		translated[index] = part
	}
	return nativeAnthropicAddedContent(translated)
}

func anthropicImageDataSource(part normalize.ContentPart) (string, string, error) {
	if part.URL == "" {
		if part.MediaType == "" || part.Data == "" {
			return "", "", fmt.Errorf("Anthropic image requires a URL or typed base64 source")
		}
		if _, err := base64.StdEncoding.DecodeString(part.Data); err != nil {
			return "", "", fmt.Errorf("decode image base64: %w", err)
		}
		return part.MediaType, part.Data, nil
	}
	if !strings.HasPrefix(part.URL, "data:") {
		return "", "", fmt.Errorf("image URL is not a data URI")
	}
	metadata, encoded, found := strings.Cut(strings.TrimPrefix(part.URL, "data:"), ",")
	if !found || !strings.HasSuffix(metadata, ";base64") {
		return "", "", fmt.Errorf("Anthropic inline image must use a base64 data URI")
	}
	mediaType, _, err := mime.ParseMediaType(strings.TrimSuffix(metadata, ";base64"))
	if err != nil || !strings.HasPrefix(mediaType, "image/") {
		return "", "", fmt.Errorf("Anthropic inline image has an invalid media type")
	}
	if _, err := base64.StdEncoding.DecodeString(encoded); err != nil {
		return "", "", fmt.Errorf("decode image base64: %w", err)
	}
	return mediaType, encoded, nil
}

func applyAnthropicThinking(body map[string]any, intent normalize.ThinkingIntent) {
	translated := normalize.AnthropicReasoning(intent)
	for key, value := range translated {
		body[key] = value
	}
	if (intent.Mode == "budget" || intent.Mode == "enabled") && intent.BudgetTokens > 0 {
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
