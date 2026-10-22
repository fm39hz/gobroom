package renderers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

type OpenAIChat struct{}

func (OpenAIChat) ID() normalize.Format { return normalize.FormatOpenAIChat }
func (OpenAIChat) SupportsResponse(options kernel.ResponseRenderContext) kernel.CompatibilityPlan {
	if options.ClientFormat != normalize.FormatOpenAIChat {
		return unsupportedResponsePlan(options, "OpenAI Chat renderer cannot render this client format")
	}
	passthrough := options.ProviderFormat == options.ClientFormat && options.ProviderStreaming == options.Streaming && !options.SemanticTransformActive
	if passthrough && !containsEvent(options.ProviderEvents, kernel.EventRawFrame) {
		return unsupportedResponsePlan(options, "native wire response requires a decoder that exposes raw frames")
	}
	return responseEventPlan(options, passthrough, openAIChatEvents(), "OpenAI Chat")
}
func (OpenAIChat) Begin(_ context.Context, options kernel.ResponseRenderContext, writer http.ResponseWriter) (kernel.ResponseRenderSession, error) {
	if options.ClientFormat != normalize.FormatOpenAIChat {
		return nil, fmt.Errorf("OpenAI Chat renderer cannot render %q", options.ClientFormat)
	}
	passthrough := options.ProviderFormat == options.ClientFormat && options.ProviderStreaming == options.Streaming && !options.SemanticTransformActive
	if passthrough {
		copyHeaders(writer.Header(), options.Headers, true)
		if options.Streaming {
			writer.Header().Set("Content-Type", "text/event-stream")
		} else {
			writer.Header().Set("Content-Type", "application/json")
		}
	} else if options.Streaming {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.Header().Set("Cache-Control", "no-cache")
	} else {
		writer.Header().Set("Content-Type", "application/json")
	}
	return &openAIChatSession{
		writer: writer, status: options.Status, passthrough: passthrough, streaming: options.Streaming, model: options.Model,
		includeUsage: options.IncludeUsage,
		id:           fmt.Sprintf("chatcmpl-gobroom-%d", time.Now().UnixNano()), created: time.Now().Unix(), tools: map[int]*toolAccumulator{},
		toolIndexesByItemID: map[string]int{}, toolIndexesByCallID: map[string]int{},
	}, nil
}

type toolAccumulator struct {
	id, name, arguments string
	signature           string
}
type openAIChatSession struct {
	writer              http.ResponseWriter
	status              int
	committed           bool
	passthrough         bool
	streaming           bool
	includeUsage        bool
	model, id           string
	created             int64
	text, thinking      strings.Builder
	tools               map[int]*toolAccumulator
	toolIndexesByItemID map[string]int
	toolIndexesByCallID map[string]int
	nextToolIndex       int
	usage               *kernel.UsageEvent
	finish              string
	finishSent          bool
	responseError       kernel.ClientFailure
	responseErrored     bool
	errorSent           bool
}

func (s *openAIChatSession) Emit(_ context.Context, event kernel.ResponseEvent) error {
	if event.Kind == kernel.EventResponseError {
		if !s.responseErrored {
			s.responseError = event.Error
			s.responseErrored = true
		}
		if s.streaming && s.committed && !s.errorSent {
			s.errorSent = true
			return writeOpenAIError(s.writer, s.responseError)
		}
		return nil
	}
	if s.responseErrored {
		return nil
	}
	if s.passthrough {
		if event.Kind != kernel.EventRawFrame || event.WireFormat != normalize.FormatOpenAIChat {
			return nil
		}
		s.commit()
		_, err := s.writer.Write(event.Raw)
		if flusher, ok := s.writer.(http.Flusher); ok {
			flusher.Flush()
		}
		return err
	}
	switch event.Kind {
	case kernel.EventTextDelta:
		s.text.WriteString(event.Text)
		if s.streaming {
			s.commit()
			return s.chunk(map[string]any{"content": event.Text}, "")
		}
	case kernel.EventThinkingDelta:
		s.thinking.WriteString(event.Text)
		if s.streaming {
			s.commit()
			return s.chunk(map[string]any{"reasoning_content": event.Text}, "")
		}
	case kernel.EventToolCallDelta:
		index := s.toolIndex(event)
		if event.ToolCallID != "" {
			if existing, found := s.toolIndexesByCallID[event.ToolCallID]; found && existing != index {
				return fmt.Errorf("OpenAI Chat tool call ID %q is assigned to multiple response items", event.ToolCallID)
			}
			s.toolIndexesByCallID[event.ToolCallID] = index
		}
		tool := s.tools[index]
		if tool == nil {
			tool = &toolAccumulator{}
			s.tools[index] = tool
		}
		if event.ToolCallID != "" {
			if tool.id != "" && tool.id != event.ToolCallID {
				return fmt.Errorf("OpenAI Chat tool call at index %d changed ID across response items", index)
			}
			tool.id = event.ToolCallID
		}
		if event.ToolName != "" {
			tool.name = event.ToolName
		}
		if len(event.Opaque) > 0 {
			var opaque struct {
				ThoughtSignature string `json:"thoughtSignature"`
			}
			if json.Unmarshal(event.Opaque, &opaque) == nil {
				tool.signature = opaque.ThoughtSignature
			}
		}
		tool.arguments += event.ToolArguments
		if s.streaming {
			s.commit()
			call := map[string]any{"index": index, "function": map[string]any{"arguments": event.ToolArguments}}
			if event.ToolCallID != "" {
				call["id"] = event.ToolCallID
				call["type"] = "function"
			}
			if event.ToolName != "" {
				call["function"].(map[string]any)["name"] = event.ToolName
			}
			if tool.signature != "" {
				call["thoughtSignature"] = tool.signature
			}
			return s.chunk(map[string]any{"tool_calls": []any{call}}, "")
		}
	case kernel.EventContentBlockEnd:
		if event.ItemID != "" {
			return nil
		}
		s.finish = openAIFinishReason(event.StopReason)
		if s.streaming && s.finish != "" {
			s.commit()
			if err := s.chunk(map[string]any{}, s.finish); err != nil {
				return err
			}
			s.finishSent = true
		}
	case kernel.EventUsage:
		if event.Usage != nil {
			usage := kernel.MergeProviderUsage(kernel.UsageEvent{}, *event.Usage)
			if s.usage != nil {
				usage = kernel.MergeProviderUsage(*s.usage, *event.Usage)
			}
			s.usage = &usage
		}
	case kernel.EventResponseComplete:
		// Final protocol framing is emitted once in Finish, after item validation.
	}
	return nil
}

func (s *openAIChatSession) Finish(_ context.Context, decodeErr error) error {
	if decodeErr != nil {
		return decodeErr
	}
	if s.responseErrored {
		return fmt.Errorf("upstream response error: %s", s.responseError.Message)
	}
	if s.passthrough {
		return nil
	}
	if err := s.validateToolCalls(); err != nil {
		return err
	}
	if s.finish == "" {
		if len(s.tools) > 0 {
			s.finish = "tool_calls"
		} else {
			s.finish = "stop"
		}
	}
	if s.streaming {
		s.commit()
		if !s.finishSent {
			if err := s.chunk(map[string]any{}, s.finish); err != nil {
				return err
			}
			s.finishSent = true
		}
		if s.includeUsage && s.usage != nil {
			chunk := map[string]any{
				"id": s.id, "object": "chat.completion.chunk", "created": s.created, "model": s.model,
				"choices": []any{},
				"usage":   chatUsage(s.usage),
			}
			if err := writeSSEJSON(s.writer, chunk); err != nil {
				return err
			}
		}
		_, err := io.WriteString(s.writer, "data: [DONE]\n\n")
		if flusher, ok := s.writer.(http.Flusher); ok {
			flusher.Flush()
		}
		return err
	}
	s.commit()
	message := map[string]any{"role": "assistant", "content": s.text.String()}
	if s.thinking.Len() > 0 {
		message["reasoning_content"] = s.thinking.String()
	}
	if len(s.tools) > 0 {
		calls := make([]any, 0, len(s.tools))
		indexes := make([]int, 0, len(s.tools))
		for index := range s.tools {
			indexes = append(indexes, index)
		}
		sort.Ints(indexes)
		for _, index := range indexes {
			tool := s.tools[index]
			arguments := tool.arguments
			if arguments == "" {
				arguments = "{}"
			}
			call := map[string]any{"id": tool.id, "type": "function", "function": map[string]any{"name": tool.name, "arguments": arguments}}
			if tool.signature != "" {
				call["thoughtSignature"] = tool.signature
			}
			calls = append(calls, call)
		}
		message["tool_calls"] = calls
		if s.finish == "stop" {
			s.finish = "tool_calls"
		}
	}
	choice := map[string]any{"index": 0, "message": message, "finish_reason": s.finish}
	body := map[string]any{"id": s.id, "object": "chat.completion", "created": s.created, "model": s.model, "choices": []any{choice}}
	if s.usage != nil {
		body["usage"] = chatUsage(s.usage)
	}
	return json.NewEncoder(s.writer).Encode(body)
}

func chatUsage(usage *kernel.UsageEvent) map[string]any {
	result := map[string]any{}
	if usage.InputTokens != nil {
		result["prompt_tokens"] = *usage.InputTokens
	}
	if usage.OutputTokens != nil {
		result["completion_tokens"] = *usage.OutputTokens
	}
	if usage.InputTokens != nil && usage.OutputTokens != nil && *usage.InputTokens >= 0 && *usage.OutputTokens >= 0 && *usage.InputTokens <= int64(^uint64(0)>>1)-*usage.OutputTokens {
		result["total_tokens"] = *usage.InputTokens + *usage.OutputTokens
	}
	if usage.CachedReadTokens != nil || usage.CachedWriteTokens != nil {
		result["prompt_tokens_details"] = struct {
			CachedTokens     *int64 `json:"cached_tokens,omitempty"`
			CacheWriteTokens *int64 `json:"cache_write_tokens,omitempty"`
		}{CachedTokens: usage.CachedReadTokens, CacheWriteTokens: usage.CachedWriteTokens}
	}
	if usage.ReasoningTokens != nil {
		result["completion_tokens_details"] = struct {
			ReasoningTokens *int64 `json:"reasoning_tokens,omitempty"`
		}{ReasoningTokens: usage.ReasoningTokens}
	}
	return result
}

func (s *openAIChatSession) toolIndex(event kernel.ResponseEvent) int {
	if event.ItemID == "" {
		return event.Index
	}
	if index, found := s.toolIndexesByItemID[event.ItemID]; found {
		return index
	}
	index := event.Index
	if _, occupied := s.tools[index]; occupied {
		index = s.nextToolIndex
		for s.tools[index] != nil {
			index++
		}
	}
	s.toolIndexesByItemID[event.ItemID] = index
	if index >= s.nextToolIndex {
		s.nextToolIndex = index + 1
	}
	return index
}

func (s *openAIChatSession) validateToolCalls() error {
	for index, tool := range s.tools {
		if tool.id == "" || tool.name == "" {
			return fmt.Errorf("OpenAI Chat tool call at index %d ended before its ID and name were complete", index)
		}
		arguments := strings.TrimSpace(tool.arguments)
		if arguments == "" {
			continue
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal([]byte(arguments), &object); err != nil || object == nil {
			return fmt.Errorf("OpenAI Chat tool call %q has invalid object arguments", tool.id)
		}
	}
	return nil
}

func (s *openAIChatSession) commit() {
	if !s.committed {
		s.writer.WriteHeader(s.status)
		s.committed = true
	}
}

func (s *openAIChatSession) chunk(delta map[string]any, finish string) error {
	var finishValue any
	if finish != "" {
		finishValue = finish
	}
	chunk := map[string]any{"id": s.id, "object": "chat.completion.chunk", "created": s.created, "model": s.model, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finishValue}}}
	return writeSSEJSON(s.writer, chunk)
}

func writeSSEJSON(writer http.ResponseWriter, chunk any) error {
	data, err := json.Marshal(chunk)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "data: %s\n\n", data); err != nil {
		return err
	}
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

func openAIFinishReason(reason string) string {
	switch reason {
	case "end_turn", "stop", "STOP":
		return "stop"
	case "tool_use", "tool_calls":
		return "tool_calls"
	case "max_tokens", "length", "MAX_TOKENS":
		return "length"
	case "":
		return ""
	default:
		return reason
	}
}

type WirePassthrough struct{ Format normalize.Format }

func (r WirePassthrough) ID() normalize.Format { return r.Format }
func (r WirePassthrough) SupportsResponse(options kernel.ResponseRenderContext) kernel.CompatibilityPlan {
	if options.ClientFormat != r.Format || options.ProviderFormat != r.Format || options.Streaming != options.ProviderStreaming || options.SemanticTransformActive {
		return unsupportedResponsePlan(options, "wire passthrough requires matching provider/client format and framing with no semantic response transform")
	}
	if !containsEvent(options.ProviderEvents, kernel.EventRawFrame) {
		return unsupportedResponsePlan(options, "wire passthrough requires a decoder that exposes raw frames")
	}
	return responseEventPlan(options, true, nil, "native wire passthrough")
}
func (r WirePassthrough) Begin(_ context.Context, options kernel.ResponseRenderContext, writer http.ResponseWriter) (kernel.ResponseRenderSession, error) {
	if options.ClientFormat != r.Format || options.ProviderFormat != r.Format || options.Streaming != options.ProviderStreaming || options.SemanticTransformActive {
		return nil, fmt.Errorf("wire passthrough %q requires matching provider and client formats", r.Format)
	}
	copyHeaders(writer.Header(), options.Headers, false)
	return &wireSession{writer: writer, status: options.Status, format: r.Format, streaming: options.Streaming}, nil
}

func openAIChatEvents() map[kernel.ResponseEventKind]bool {
	return map[kernel.ResponseEventKind]bool{
		kernel.EventTextDelta: true, kernel.EventThinkingDelta: true, kernel.EventToolCallDelta: true,
		kernel.EventUsage: true, kernel.EventResponseComplete: true, kernel.EventContentBlockEnd: true,
	}
}

func responseEventPlan(options kernel.ResponseRenderContext, passthrough bool, supported map[kernel.ResponseEventKind]bool, label string) kernel.CompatibilityPlan {
	reports := [][]kernel.FacetMapping{}
	policy := kernel.CompatibilityPolicy{RequiredFacets: []string{kernel.FacetWireResponse}}
	disposition := kernel.FacetTranslated
	if passthrough {
		disposition = kernel.FacetPreserved
	}
	reports = append(reports, []kernel.FacetMapping{{Facet: kernel.FacetWireResponse, Paths: []string{"response.wire"}, Disposition: disposition}})
	providerEvents := make(map[kernel.ResponseEventKind]bool, len(options.ProviderEvents))
	for _, event := range options.ProviderEvents {
		providerEvents[event] = true
	}
	for _, event := range options.RequiredEvents {
		facet := kernel.ResponseEventFacet(event)
		policy.RequiredFacets = append(policy.RequiredFacets, facet)
		mapping := kernel.FacetMapping{Facet: facet, Paths: []string{"response.events"}, Disposition: kernel.FacetUnsupported}
		switch {
		case !providerEvents[event]:
			mapping.Reason = fmt.Sprintf("%s cannot render %q because the provider decoder does not declare that event", label, event)
		case passthrough:
			mapping.Disposition = kernel.FacetPreserved
		case supported[event]:
			mapping.Disposition = kernel.FacetTranslated
		default:
			mapping.Reason = fmt.Sprintf("%s renderer does not support semantic event %q", label, event)
		}
		reports = append(reports, []kernel.FacetMapping{mapping})
	}
	return kernel.ComposeCompatibilityPlan(reports, policy)
}

func unsupportedResponsePlan(options kernel.ResponseRenderContext, reason string) kernel.CompatibilityPlan {
	reports := [][]kernel.FacetMapping{{{Facet: kernel.FacetWireResponse, Paths: []string{"response.wire"}, Disposition: kernel.FacetUnsupported, Reason: reason}}}
	policy := kernel.CompatibilityPolicy{RequiredFacets: []string{kernel.FacetWireResponse}}
	for _, event := range options.RequiredEvents {
		facet := kernel.ResponseEventFacet(event)
		policy.RequiredFacets = append(policy.RequiredFacets, facet)
		reports = append(reports, []kernel.FacetMapping{{Facet: facet, Paths: []string{"response.events"}, Disposition: kernel.FacetUnsupported, Reason: reason}})
	}
	return kernel.ComposeCompatibilityPlan(reports, policy)
}

func containsEvent(events []kernel.ResponseEventKind, wanted kernel.ResponseEventKind) bool {
	for _, event := range events {
		if event == wanted {
			return true
		}
	}
	return false
}

type wireSession struct {
	writer          http.ResponseWriter
	status          int
	committed       bool
	format          normalize.Format
	streaming       bool
	responseError   kernel.ClientFailure
	responseErrored bool
	errorSent       bool
}

func (s *wireSession) Emit(_ context.Context, event kernel.ResponseEvent) error {
	if event.Kind == kernel.EventResponseError {
		if !s.responseErrored {
			s.responseError = event.Error
			s.responseErrored = true
		}
		if s.streaming && s.committed && !s.errorSent {
			s.errorSent = true
			return writeProtocolError(s.writer, s.format, s.responseError)
		}
		return nil
	}
	if s.responseErrored {
		return nil
	}
	if event.Kind != kernel.EventRawFrame || event.WireFormat != s.format {
		return nil
	}
	if !s.committed {
		s.writer.WriteHeader(s.status)
		s.committed = true
	}
	_, err := s.writer.Write(event.Raw)
	if flusher, ok := s.writer.(http.Flusher); ok {
		flusher.Flush()
	}
	return err
}
func (s *wireSession) Finish(_ context.Context, err error) error {
	if err != nil {
		return err
	}
	if s.responseErrored {
		return fmt.Errorf("upstream response error: %s", s.responseError.Message)
	}
	if !s.committed {
		s.writer.WriteHeader(s.status)
		s.committed = true
	}
	return nil
}

type clientFailureBody struct {
	Code    kernel.ClientFailureCode `json:"code"`
	Type    string                   `json:"type"`
	Message string                   `json:"message"`
}

type openAIErrorEnvelope struct {
	Error clientFailureBody `json:"error"`
}

type responsesFailureEnvelope struct {
	Type     string `json:"type"`
	Response struct {
		Status string            `json:"status"`
		Error  clientFailureBody `json:"error"`
	} `json:"response"`
}

type anthropicFailureEnvelope struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Details struct {
			ErrorCode kernel.ClientFailureCode `json:"error_code"`
		} `json:"details"`
	} `json:"error"`
}

func writeOpenAIError(writer io.Writer, failure kernel.ClientFailure) error {
	data, err := json.Marshal(openAIErrorEnvelope{Error: clientFailureBody{Code: failure.Code, Type: failure.Type, Message: failure.Message}})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(writer, "data: %s\n\n", data)
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
	return err
}

func writeProtocolError(writer io.Writer, format normalize.Format, failure kernel.ClientFailure) error {
	switch format {
	case normalize.FormatOpenAIChat:
		return writeOpenAIError(writer, failure)
	case normalize.FormatOpenAIResponses:
		envelope := responsesFailureEnvelope{Type: "response.failed"}
		envelope.Response.Status = "failed"
		envelope.Response.Error = clientFailureBody{Code: failure.Code, Type: failure.Type, Message: failure.Message}
		data, err := json.Marshal(envelope)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(writer, "event: response.failed\ndata: %s\n\n", data)
		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}
		return err
	case normalize.FormatAnthropic:
		typ := "api_error"
		if failure.Type == "invalid_request_error" {
			typ = failure.Type
		}
		envelope := anthropicFailureEnvelope{Type: "error"}
		envelope.Error.Type = typ
		envelope.Error.Message = failure.Message
		envelope.Error.Details.ErrorCode = failure.Code
		data, err := json.Marshal(envelope)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(writer, "event: error\ndata: %s\n\n", data)
		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}
		return err
	default:
		return nil
	}
}

func copyHeaders(dst, src http.Header, openAI bool) {
	for key, values := range src {
		lower := strings.ToLower(key)
		if lower == "connection" || lower == "transfer-encoding" || lower == "content-length" || lower == "keep-alive" {
			continue
		}
		if openAI && lower == "content-type" {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func Builtins() []kernel.ResponseRenderer {
	return []kernel.ResponseRenderer{OpenAIChat{}, OpenAIResponses{}, AnthropicMessages{}}
}
