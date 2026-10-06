package renderers

import (
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

type OpenAIChat struct{}

func (OpenAIChat) ID() normalize.Format { return normalize.FormatOpenAIChat }
func (OpenAIChat) SupportsResponse(options kernel.ResponseRenderContext) kernel.CompatibilityDecision {
	if options.ClientFormat != normalize.FormatOpenAIChat {
		return kernel.CompatibilityDecision{Reason: "OpenAI Chat renderer cannot render this client format"}
	}
	fidelity := kernel.FidelityTranslated
	if options.ProviderFormat == options.ClientFormat && options.ProviderStreaming == options.Streaming {
		fidelity = kernel.FidelityNative
	}
	return kernel.CompatibilityDecision{Supported: true, Fidelity: fidelity}
}
func (OpenAIChat) Begin(_ context.Context, options kernel.ResponseRenderContext, writer http.ResponseWriter) (kernel.ResponseRenderSession, error) {
	if options.ClientFormat != normalize.FormatOpenAIChat {
		return nil, fmt.Errorf("OpenAI Chat renderer cannot render %q", options.ClientFormat)
	}
	passthrough := options.ProviderFormat == options.ClientFormat && options.ProviderStreaming == options.Streaming
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
	return &openAIChatSession{writer: writer, status: options.Status, passthrough: passthrough, streaming: options.Streaming, model: options.Model, id: fmt.Sprintf("chatcmpl-gobroom-%d", time.Now().UnixNano()), created: time.Now().Unix(), tools: map[int]*toolAccumulator{}}, nil
}

type toolAccumulator struct {
	id, name, arguments string
	signature           string
}
type openAIChatSession struct {
	writer         http.ResponseWriter
	status         int
	committed      bool
	passthrough    bool
	streaming      bool
	model, id      string
	created        int64
	text, thinking strings.Builder
	tools          map[int]*toolAccumulator
	usage          *kernel.UsageEvent
	finish         string
	finished       bool
}

func (s *openAIChatSession) Emit(_ context.Context, event kernel.ResponseEvent) error {
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
		tool := s.tools[event.Index]
		if tool == nil {
			tool = &toolAccumulator{}
			s.tools[event.Index] = tool
		}
		if event.ToolCallID != "" {
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
			call := map[string]any{"index": event.Index, "function": map[string]any{"arguments": event.ToolArguments}}
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
		s.finish = openAIFinishReason(event.StopReason)
		if s.streaming && s.finish != "" {
			s.commit()
			return s.chunk(map[string]any{}, s.finish)
		}
	case kernel.EventUsage:
		if event.Usage != nil {
			usage := *event.Usage
			s.usage = &usage
		}
	case kernel.EventResponseComplete:
		s.finished = true
	}
	return nil
}

func (s *openAIChatSession) Finish(_ context.Context, decodeErr error) error {
	if decodeErr != nil {
		return decodeErr
	}
	if s.passthrough {
		return nil
	}
	if s.streaming {
		s.commit()
		if !s.finished {
			if err := s.chunk(map[string]any{}, s.finish); err != nil {
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
		for index := 0; index < len(s.tools); index++ {
			tool := s.tools[index]
			if tool == nil {
				continue
			}
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
		if s.finish == "" || s.finish == "stop" {
			s.finish = "tool_calls"
		}
	}
	choice := map[string]any{"index": 0, "message": message, "finish_reason": s.finish}
	body := map[string]any{"id": s.id, "object": "chat.completion", "created": s.created, "model": s.model, "choices": []any{choice}}
	if s.usage != nil {
		body["usage"] = map[string]any{"prompt_tokens": s.usage.InputTokens, "completion_tokens": s.usage.OutputTokens, "total_tokens": s.usage.InputTokens + s.usage.OutputTokens}
	}
	return json.NewEncoder(s.writer).Encode(body)
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
	data, err := json.Marshal(chunk)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.writer, "data: %s\n\n", data); err != nil {
		return err
	}
	if flusher, ok := s.writer.(http.Flusher); ok {
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
func (r WirePassthrough) SupportsResponse(options kernel.ResponseRenderContext) kernel.CompatibilityDecision {
	if options.ClientFormat != r.Format || options.ProviderFormat != r.Format {
		return kernel.CompatibilityDecision{Reason: "wire passthrough requires matching provider/client formats"}
	}
	return kernel.CompatibilityDecision{Supported: true, Fidelity: kernel.FidelityNative}
}
func (r WirePassthrough) Begin(_ context.Context, options kernel.ResponseRenderContext, writer http.ResponseWriter) (kernel.ResponseRenderSession, error) {
	if options.ClientFormat != r.Format || options.ProviderFormat != r.Format || options.Streaming != options.ProviderStreaming {
		return nil, fmt.Errorf("wire passthrough %q requires matching provider and client formats", r.Format)
	}
	copyHeaders(writer.Header(), options.Headers, false)
	return &wireSession{writer: writer, status: options.Status, format: r.Format}, nil
}

type wireSession struct {
	writer    http.ResponseWriter
	status    int
	committed bool
	format    normalize.Format
}

func (s *wireSession) Emit(_ context.Context, event kernel.ResponseEvent) error {
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
	if !s.committed {
		s.writer.WriteHeader(s.status)
		s.committed = true
	}
	return nil
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
	return []kernel.ResponseRenderer{OpenAIChat{}, WirePassthrough{Format: normalize.FormatOpenAIResponses}, WirePassthrough{Format: normalize.FormatAnthropic}}
}
