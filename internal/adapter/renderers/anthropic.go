package renderers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

// AnthropicMessages renders canonical response events into the Anthropic
// Messages response envelope. Native Anthropic responses retain the exact-wire
// path when no semantic response transform is active.
type AnthropicMessages struct{}

func (AnthropicMessages) ID() normalize.Format { return normalize.FormatAnthropic }

func (AnthropicMessages) SupportsResponse(options kernel.ResponseRenderContext) kernel.CompatibilityPlan {
	if options.ClientFormat != normalize.FormatAnthropic {
		return unsupportedResponsePlan(options, "Anthropic Messages renderer cannot render this client format")
	}
	passthrough := options.ProviderFormat == normalize.FormatAnthropic && options.ProviderStreaming == options.Streaming && !options.SemanticTransformActive
	if passthrough && !containsEvent(options.ProviderEvents, kernel.EventRawFrame) {
		return unsupportedResponsePlan(options, "native Anthropic response requires a decoder that exposes raw frames")
	}
	supported := map[kernel.ResponseEventKind]bool{
		kernel.EventResponseStarted:   true,
		kernel.EventContentBlockStart: true,
		kernel.EventContentBlockEnd:   true,
		kernel.EventTextDelta:         true,
		kernel.EventThinkingDelta:     true,
		kernel.EventThinkingSignature: true,
		kernel.EventToolCallDelta:     true,
		kernel.EventUsage:             true,
		kernel.EventResponseComplete:  true,
	}
	if !passthrough && containsEvent(options.RequiredEvents, kernel.EventThinkingDelta) {
		if options.SemanticTransformActive {
			return unsupportedResponsePlan(options, "Anthropic thinking signatures cannot be preserved across semantic response transforms")
		}
		if !containsEvent(options.ProviderEvents, kernel.EventThinkingSignature) {
			return unsupportedResponsePlan(options, "Anthropic thinking output requires a decoder that preserves the issuer signature")
		}
		options.RequiredEvents = append(options.RequiredEvents, kernel.EventThinkingSignature)
	}
	return responseEventPlan(options, passthrough, supported, "Anthropic Messages")
}

func (AnthropicMessages) Begin(_ context.Context, options kernel.ResponseRenderContext, writer http.ResponseWriter) (kernel.ResponseRenderSession, error) {
	if options.ClientFormat != normalize.FormatAnthropic {
		return nil, fmt.Errorf("Anthropic Messages renderer cannot render %q", options.ClientFormat)
	}
	passthrough := options.ProviderFormat == normalize.FormatAnthropic && options.ProviderStreaming == options.Streaming && !options.SemanticTransformActive
	if passthrough {
		copyHeaders(writer.Header(), options.Headers, false)
	} else if options.Streaming {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.Header().Set("Cache-Control", "no-cache")
		writer.Header().Set("X-Accel-Buffering", "no")
	} else {
		writer.Header().Set("Content-Type", "application/json")
	}
	return &anthropicSession{
		writer: writer, status: options.Status, passthrough: passthrough, streaming: options.Streaming,
		renderThinking: !passthrough && !options.SemanticTransformActive && containsEvent(options.ProviderEvents, kernel.EventThinkingSignature),
		model:          options.Model, messageID: fmt.Sprintf("msg_gobroom_%d", time.Now().UnixNano()),
		blocksByKey: map[string]*anthropicBlock{}, blockOrder: []*anthropicBlock{},
	}, nil
}

type anthropicBlock struct {
	key, kind, itemID string
	index             int
	toolID, toolName  string
	signature         string
	text, arguments   strings.Builder
	opened, closed    bool
}

type anthropicSession struct {
	writer         http.ResponseWriter
	status         int
	model          string
	messageID      string
	streaming      bool
	passthrough    bool
	renderThinking bool
	committed      bool
	started        bool
	finished       bool
	stopReason     string
	usage          kernel.UsageEvent
	blocksByKey    map[string]*anthropicBlock
	blockOrder     []*anthropicBlock
}

func (s *anthropicSession) Emit(_ context.Context, event kernel.ResponseEvent) error {
	if s.passthrough {
		if event.Kind != kernel.EventRawFrame || event.WireFormat != normalize.FormatAnthropic {
			return nil
		}
		s.commit()
		_, err := s.writer.Write(event.Raw)
		if flusher, ok := s.writer.(http.Flusher); ok {
			flusher.Flush()
		}
		return err
	}
	if event.Kind == kernel.EventRawFrame {
		return nil
	}
	if event.ResponseID != "" {
		s.messageID = event.ResponseID
	}
	if event.Usage != nil {
		s.usage = *event.Usage
	}
	if err := s.startMessage(); err != nil {
		return err
	}
	switch event.Kind {
	case kernel.EventResponseStarted:
		return nil
	case kernel.EventContentBlockStart:
		kind, err := anthropicBlockType(event.BlockType)
		if err != nil {
			return err
		}
		block := s.getBlock(event, kind)
		if kind == "tool_use" {
			block.toolID, block.toolName = event.ToolCallID, event.ToolName
			if block.toolID == "" || block.toolName == "" {
				return nil
			}
			return s.openToolBlock(block)
		}
		return s.openBlock(block)
	case kernel.EventTextDelta:
		block := s.getBlock(event, "text")
		block.text.WriteString(event.Text)
		if err := s.openBlock(block); err != nil {
			return err
		}
		if s.streaming {
			return s.emitSSE("content_block_delta", map[string]any{"type": "content_block_delta", "index": block.index, "delta": map[string]any{"type": "text_delta", "text": event.Text}})
		}
	case kernel.EventToolCallDelta:
		block := s.getBlock(event, "tool_use")
		if event.ToolCallID != "" {
			block.toolID = event.ToolCallID
		}
		if event.ToolName != "" {
			block.toolName = event.ToolName
		}
		block.arguments.WriteString(event.ToolArguments)
		if err := s.openToolBlock(block); err != nil {
			return err
		}
		if s.streaming && event.ToolArguments != "" {
			return s.emitSSE("content_block_delta", map[string]any{"type": "content_block_delta", "index": block.index, "delta": map[string]any{"type": "input_json_delta", "partial_json": event.ToolArguments}})
		}
	case kernel.EventThinkingDelta:
		if !s.renderThinking {
			return nil
		}
		block := s.getBlock(event, "thinking")
		block.text.WriteString(event.Text)
		if err := s.openBlock(block); err != nil {
			return err
		}
		if s.streaming && event.Text != "" {
			return s.emitSSE("content_block_delta", map[string]any{"type": "content_block_delta", "index": block.index, "delta": map[string]any{"type": "thinking_delta", "thinking": event.Text}})
		}
	case kernel.EventThinkingSignature:
		if !s.renderThinking {
			return nil
		}
		block := s.getBlock(event, "thinking")
		block.signature = event.Signature
		if err := s.openBlock(block); err != nil {
			return err
		}
		if s.streaming && event.Signature != "" {
			return s.emitSSE("content_block_delta", map[string]any{"type": "content_block_delta", "index": block.index, "delta": map[string]any{"type": "signature_delta", "signature": event.Signature}})
		}
	case kernel.EventContentBlockEnd:
		if event.ItemID == "" && event.StopReason != "" {
			reason, err := anthropicStopReason(event.StopReason)
			if err != nil {
				return err
			}
			s.stopReason = reason
		}
		if event.ItemID != "" {
			if block := s.findBlockByItemID(event.ItemID); block != nil {
				return s.closeBlock(block)
			}
			return nil
		}
		if event.StopReason != "" {
			for _, block := range s.blockOrder {
				if err := s.closeBlock(block); err != nil {
					return err
				}
			}
		}
	case kernel.EventUsage:
		if event.Usage != nil {
			s.usage = *event.Usage
		}
	case kernel.EventResponseComplete:
		s.finished = true
		for _, block := range s.blockOrder {
			if err := s.closeBlock(block); err != nil {
				return err
			}
		}
		if s.stopReason == "" || s.stopReason == "end_turn" && s.hasToolUseBlock() {
			if s.hasToolUseBlock() {
				s.stopReason = "tool_use"
			} else {
				s.stopReason = "end_turn"
			}
		}
	case kernel.EventResponseError:
		return fmt.Errorf("upstream response error: %s", event.Error)
	}
	return nil
}

func (s *anthropicSession) Finish(_ context.Context, decodeErr error) error {
	if decodeErr != nil {
		return decodeErr
	}
	if s.passthrough {
		return nil
	}
	if s.streaming {
		if err := s.startMessage(); err != nil {
			return err
		}
		if !s.finished {
			for _, block := range s.blockOrder {
				if err := s.closeBlock(block); err != nil {
					return err
				}
			}
		}
		if s.stopReason == "" {
			s.stopReason = "end_turn"
		}
		if err := s.emitSSE("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": s.stopReason, "stop_sequence": nil}, "usage": map[string]any{"output_tokens": s.usage.OutputTokens}}); err != nil {
			return err
		}
		if err := s.emitSSE("message_stop", map[string]any{"type": "message_stop"}); err != nil {
			return err
		}
		return nil
	}
	content := make([]map[string]any, 0, len(s.blockOrder))
	for _, block := range s.blockOrder {
		switch block.kind {
		case "text":
			content = append(content, map[string]any{"type": "text", "text": block.text.String()})
		case "thinking":
			if block.signature == "" {
				return fmt.Errorf("Anthropic thinking block has no issuer signature")
			}
			content = append(content, map[string]any{"type": "thinking", "thinking": block.text.String(), "signature": block.signature})
		case "tool_use":
			input := map[string]any{}
			if raw := strings.TrimSpace(block.arguments.String()); raw != "" {
				if err := json.Unmarshal([]byte(raw), &input); err != nil {
					return fmt.Errorf("decode Anthropic tool input: %w", err)
				}
				if input == nil {
					return fmt.Errorf("Anthropic tool input must be a JSON object")
				}
			}
			content = append(content, map[string]any{"type": "tool_use", "id": block.toolID, "name": block.toolName, "input": input})
		}
	}
	if s.stopReason == "" {
		s.stopReason = "end_turn"
	}
	message := map[string]any{"id": s.messageID, "type": "message", "role": "assistant", "model": s.model, "content": content, "stop_reason": s.stopReason, "stop_sequence": nil, "usage": map[string]any{"input_tokens": s.usage.InputTokens, "output_tokens": s.usage.OutputTokens}}
	encoded, err := json.Marshal(message)
	if err != nil {
		return err
	}
	s.commit()
	_, err = fmt.Fprintf(s.writer, "%s\n", encoded)
	return err
}

func (s *anthropicSession) startMessage() error {
	if s.started {
		return nil
	}
	s.started = true
	if !s.streaming {
		return nil
	}
	s.commit()
	message := map[string]any{"id": s.messageID, "type": "message", "role": "assistant", "model": s.model, "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": map[string]any{"input_tokens": s.usage.InputTokens, "output_tokens": 0}}
	return s.emitSSE("message_start", map[string]any{"type": "message_start", "message": message})
}

func (s *anthropicSession) getBlock(event kernel.ResponseEvent, kind string) *anthropicBlock {
	key := kind + ":"
	if event.ItemID != "" {
		key += "item:" + event.ItemID
	} else {
		key += fmt.Sprintf("index:%d", event.Index)
	}
	if block := s.blocksByKey[key]; block != nil {
		return block
	}
	block := &anthropicBlock{key: key, kind: kind, itemID: event.ItemID, index: len(s.blockOrder)}
	s.blocksByKey[key] = block
	s.blockOrder = append(s.blockOrder, block)
	return block
}

func (s *anthropicSession) findBlockByItemID(itemID string) *anthropicBlock {
	for _, block := range s.blockOrder {
		if block.itemID == itemID {
			return block
		}
	}
	return nil
}

func (s *anthropicSession) hasToolUseBlock() bool {
	for _, block := range s.blockOrder {
		if block.kind == "tool_use" {
			return true
		}
	}
	return false
}

func (s *anthropicSession) openToolBlock(block *anthropicBlock) error {
	if block.opened {
		return nil
	}
	if block.toolID == "" || block.toolName == "" {
		return fmt.Errorf("Anthropic tool rendering requires a tool ID and name")
	}
	return s.openBlockWithContent(block, map[string]any{"type": "tool_use", "id": block.toolID, "name": block.toolName, "input": map[string]any{}})
}

func (s *anthropicSession) openBlock(block *anthropicBlock) error {
	if block.opened {
		return nil
	}
	var content map[string]any
	switch block.kind {
	case "text":
		content = map[string]any{"type": "text", "text": ""}
	case "thinking":
		content = map[string]any{"type": "thinking", "thinking": "", "signature": ""}
	default:
		return fmt.Errorf("unsupported Anthropic semantic content block %q", block.kind)
	}
	return s.openBlockWithContent(block, content)
}

func (s *anthropicSession) openBlockWithContent(block *anthropicBlock, content map[string]any) error {
	block.opened = true
	if s.streaming {
		return s.emitSSE("content_block_start", map[string]any{"type": "content_block_start", "index": block.index, "content_block": content})
	}
	return nil
}

func (s *anthropicSession) closeBlock(block *anthropicBlock) error {
	if block.closed {
		return nil
	}
	if block.kind == "thinking" && block.signature == "" {
		return fmt.Errorf("Anthropic thinking block ended without its issuer signature")
	}
	block.closed = true
	if s.streaming && block.opened {
		return s.emitSSE("content_block_stop", map[string]any{"type": "content_block_stop", "index": block.index})
	}
	return nil
}

func (s *anthropicSession) emitSSE(name string, data any) error {
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.writer, "event: %s\ndata: %s\n\n", name, encoded); err != nil {
		return err
	}
	if flusher, ok := s.writer.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

func (s *anthropicSession) commit() {
	if !s.committed {
		s.writer.WriteHeader(s.status)
		s.committed = true
	}
}

func anthropicBlockType(kind string) (string, error) {
	switch kind {
	case "", "text", "message", "output_text":
		return "text", nil
	case "tool_use", "function_call":
		return "tool_use", nil
	case "thinking", "reasoning":
		return "thinking", nil
	default:
		return "", fmt.Errorf("unsupported Anthropic content block type %q", kind)
	}
}

func anthropicStopReason(reason string) (string, error) {
	switch reason {
	case "stop", "STOP", "end_turn", "completed", "OTHER":
		return "end_turn", nil
	case "tool_calls", "tool_use", "function_call", "MALFORMED_FUNCTION_CALL", "UNEXPECTED_TOOL_CALL":
		return "tool_use", nil
	case "length", "max_tokens", "MAX_TOKENS":
		return "max_tokens", nil
	case "refusal", "content_filter", "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY", "IMAGE_PROHIBITED_CONTENT":
		return "refusal", nil
	case "stop_sequence", "pause_turn", "model_context_window_exceeded":
		return reason, nil
	default:
		return "", fmt.Errorf("Anthropic renderer cannot represent stop reason %q", reason)
	}
}
