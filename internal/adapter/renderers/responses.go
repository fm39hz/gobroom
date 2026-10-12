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

// OpenAIResponses renders canonical events as the OpenAI Responses JSON/SSE
// contract. Exact native Responses wire may pass through when no transform is
// active; cross-protocol routes always use this semantic projection.
type OpenAIResponses struct{}

func (OpenAIResponses) ID() normalize.Format { return normalize.FormatOpenAIResponses }

func (OpenAIResponses) SupportsResponse(options kernel.ResponseRenderContext) kernel.CompatibilityPlan {
	if options.ClientFormat != normalize.FormatOpenAIResponses {
		return unsupportedResponsePlan(options, "OpenAI Responses renderer cannot render this client format")
	}
	passthrough := options.ProviderFormat == normalize.FormatOpenAIResponses && options.ProviderStreaming == options.Streaming && !options.SemanticTransformActive
	if passthrough && !containsEvent(options.ProviderEvents, kernel.EventRawFrame) {
		return unsupportedResponsePlan(options, "native Responses passthrough requires a decoder that exposes raw frames")
	}
	supported := map[kernel.ResponseEventKind]bool{
		kernel.EventResponseStarted: true, kernel.EventContentBlockStart: true,
		kernel.EventContentBlockEnd: true, kernel.EventTextDelta: true,
		kernel.EventThinkingDelta: true, kernel.EventToolCallDelta: true,
		kernel.EventUsage: true, kernel.EventResponseComplete: true,
	}
	return responseEventPlan(options, passthrough, supported, "OpenAI Responses")
}

func (OpenAIResponses) Begin(_ context.Context, options kernel.ResponseRenderContext, writer http.ResponseWriter) (kernel.ResponseRenderSession, error) {
	if options.ClientFormat != normalize.FormatOpenAIResponses {
		return nil, fmt.Errorf("OpenAI Responses renderer cannot render %q", options.ClientFormat)
	}
	passthrough := options.ProviderFormat == normalize.FormatOpenAIResponses && options.ProviderStreaming == options.Streaming && !options.SemanticTransformActive
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
		writer.Header().Set("X-Accel-Buffering", "no")
	} else {
		writer.Header().Set("Content-Type", "application/json")
	}
	return &openAIResponsesSession{
		writer: writer, status: options.Status, model: options.Model, streaming: options.Streaming, passthrough: passthrough,
		createdAt: time.Now().Unix(), responseID: fmt.Sprintf("resp_gobroom_%d", time.Now().UnixNano()),
		messagesBySource: map[string]*responsesOutputItem{}, reasoningBySource: map[string]*responsesOutputItem{},
		toolsByIndex: map[int]*responsesOutputItem{}, toolIndexesByItemID: map[string]int{}, toolIndexesByCallID: map[string]int{},
		outputItemsByID: map[string]*responsesOutputItem{},
	}, nil
}

type responsesOutputItem struct {
	id, kind, sourceID string
	outputIndex        int
	contentIndex       int
	callID, name       string
	text, arguments    strings.Builder
	opened             bool
}

type openAIResponsesSession struct {
	writer              http.ResponseWriter
	status              int
	model               string
	streaming           bool
	passthrough         bool
	committed           bool
	started             bool
	completed           bool
	createdAt           int64
	responseID          string
	finishReason        string
	usage               *kernel.UsageEvent
	items               []*responsesOutputItem
	messagesBySource    map[string]*responsesOutputItem
	reasoningBySource   map[string]*responsesOutputItem
	toolsByIndex        map[int]*responsesOutputItem
	toolIndexesByItemID map[string]int
	toolIndexesByCallID map[string]int
	outputItemsByID     map[string]*responsesOutputItem
	nextToolIndex       int
}

func (s *openAIResponsesSession) Emit(_ context.Context, event kernel.ResponseEvent) error {
	if s.passthrough {
		if event.Kind != kernel.EventRawFrame || event.WireFormat != normalize.FormatOpenAIResponses {
			return nil
		}
		s.commit()
		if _, err := s.writer.Write(event.Raw); err != nil {
			return err
		}
		if flusher, ok := s.writer.(http.Flusher); ok {
			flusher.Flush()
		}
		return nil
	}
	if event.ResponseID != "" {
		s.responseID = event.ResponseID
	}
	switch event.Kind {
	case kernel.EventRawFrame:
		return nil
	case kernel.EventResponseStarted:
		return s.ensureStarted()
	case kernel.EventTextDelta:
		item, err := s.messageItem(event.ItemID)
		if err != nil {
			return err
		}
		item.text.WriteString(event.Text)
		if s.streaming && event.Text != "" {
			return s.emit("response.output_text.delta", map[string]any{
				"type": "response.output_text.delta", "item_id": item.id, "output_index": item.outputIndex,
				"content_index": item.contentIndex, "delta": event.Text,
			})
		}
	case kernel.EventThinkingDelta:
		item, err := s.reasoningItem(event.ItemID)
		if err != nil {
			return err
		}
		item.text.WriteString(event.Text)
		if s.streaming && event.Text != "" {
			return s.emit("response.reasoning_summary_text.delta", map[string]any{
				"type": "response.reasoning_summary_text.delta", "item_id": item.id, "output_index": item.outputIndex,
				"summary_index": 0, "delta": event.Text,
			})
		}
	case kernel.EventToolCallDelta:
		item, delta, err := s.toolItem(event)
		if err != nil {
			return err
		}
		if s.streaming && delta != "" {
			return s.emit("response.function_call_arguments.delta", map[string]any{
				"type": "response.function_call_arguments.delta", "item_id": item.id,
				"output_index": item.outputIndex, "delta": delta,
			})
		}
	case kernel.EventContentBlockEnd:
		if event.ItemID == "" {
			s.finishReason = event.StopReason
		}
	case kernel.EventUsage:
		if event.Usage != nil {
			usage := *event.Usage
			s.usage = &usage
		}
	case kernel.EventResponseComplete:
		s.completed = true
	case kernel.EventResponseError:
		return fmt.Errorf("upstream response error: %s", event.Error)
	case kernel.EventThinkingSignature:
		return fmt.Errorf("OpenAI Responses renderer cannot represent an opaque thinking signature")
	case kernel.EventArtifact:
		return fmt.Errorf("OpenAI Responses renderer has no declared artifact projection")
	}
	return nil
}

func (s *openAIResponsesSession) Finish(_ context.Context, decodeErr error) error {
	if decodeErr != nil {
		return decodeErr
	}
	if s.passthrough {
		return nil
	}
	if !s.completed {
		return fmt.Errorf("OpenAI Responses semantic projection ended before response completion")
	}
	if err := s.validateTools(); err != nil {
		return err
	}
	if !s.started {
		if err := s.ensureStarted(); err != nil {
			return err
		}
	}
	if s.streaming {
		for _, item := range s.items {
			if err := s.finishStreamItem(item); err != nil {
				return err
			}
		}
		return s.emit("response.completed", map[string]any{"type": "response.completed", "response": s.responseObject(s.responseStatus())})
	}
	s.commit()
	return json.NewEncoder(s.writer).Encode(s.responseObject(s.responseStatus()))
}

func (s *openAIResponsesSession) messageItem(sourceID string) (*responsesOutputItem, error) {
	if item := s.messagesBySource[sourceID]; item != nil {
		return item, nil
	}
	item, err := s.newItem("message", sourceID, "msg_")
	if err != nil {
		return nil, err
	}
	s.messagesBySource[sourceID] = item
	if s.streaming {
		if err := s.addItem(item, map[string]any{"type": "message", "id": item.id, "role": "assistant", "status": "in_progress", "content": []any{}}); err != nil {
			return nil, err
		}
		if err := s.emit("response.content_part.added", map[string]any{
			"type": "response.content_part.added", "item_id": item.id, "output_index": item.outputIndex,
			"content_index": item.contentIndex, "part": map[string]any{"type": "output_text", "text": ""},
		}); err != nil {
			return nil, err
		}
	}
	return item, nil
}

func (s *openAIResponsesSession) reasoningItem(sourceID string) (*responsesOutputItem, error) {
	if item := s.reasoningBySource[sourceID]; item != nil {
		return item, nil
	}
	item, err := s.newItem("reasoning", sourceID, "rsn_")
	if err != nil {
		return nil, err
	}
	s.reasoningBySource[sourceID] = item
	if s.streaming {
		if err := s.addItem(item, map[string]any{"type": "reasoning", "id": item.id, "summary": []any{}}); err != nil {
			return nil, err
		}
		if err := s.emit("response.reasoning_summary_part.added", map[string]any{
			"type": "response.reasoning_summary_part.added", "item_id": item.id,
			"output_index": item.outputIndex, "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": ""},
		}); err != nil {
			return nil, err
		}
	}
	return item, nil
}

func (s *openAIResponsesSession) toolItem(event kernel.ResponseEvent) (*responsesOutputItem, string, error) {
	index := event.Index
	if event.ItemID != "" {
		if existing, ok := s.toolIndexesByItemID[event.ItemID]; ok {
			index = existing
		} else {
			if s.toolsByIndex[index] != nil {
				index = s.nextToolIndex
				for s.toolsByIndex[index] != nil {
					index++
				}
			}
			s.toolIndexesByItemID[event.ItemID] = index
			if index >= s.nextToolIndex {
				s.nextToolIndex = index + 1
			}
		}
	}
	item := s.toolsByIndex[index]
	if item == nil {
		item = &responsesOutputItem{kind: "function_call", sourceID: event.ItemID}
		s.toolsByIndex[index] = item
	}
	wasOpened := item.opened
	if event.ToolCallID != "" {
		if existing, ok := s.toolIndexesByCallID[event.ToolCallID]; ok && existing != index {
			return nil, "", fmt.Errorf("OpenAI Responses tool call ID %q is assigned to multiple response items", event.ToolCallID)
		}
		if item.callID != "" && item.callID != event.ToolCallID {
			return nil, "", fmt.Errorf("OpenAI Responses tool call item changed call ID")
		}
		item.callID = event.ToolCallID
		s.toolIndexesByCallID[event.ToolCallID] = index
	}
	if event.ToolName != "" {
		item.name = event.ToolName
	}
	item.arguments.WriteString(event.ToolArguments)
	if wasOpened {
		return item, event.ToolArguments, nil
	}
	if item.callID == "" || item.name == "" {
		return item, "", nil
	}
	if item.sourceID != "" {
		item.id = item.sourceID
	} else {
		item.id = fmt.Sprintf("fc_gobroom_%d_%d", s.createdAt, index)
	}
	if existing := s.outputItemsByID[item.id]; existing != nil && existing != item {
		return nil, "", fmt.Errorf("OpenAI Responses output item ID %q is duplicated", item.id)
	}
	s.outputItemsByID[item.id] = item
	item.outputIndex = len(s.items)
	s.items = append(s.items, item)
	item.opened = true
	if s.streaming {
		if err := s.emit("response.output_item.added", map[string]any{
			"type": "response.output_item.added", "output_index": item.outputIndex,
			"item": map[string]any{"type": "function_call", "id": item.id, "call_id": item.callID, "name": item.name, "arguments": "", "status": "in_progress"},
		}); err != nil {
			return nil, "", err
		}
		return item, item.arguments.String(), nil
	}
	return item, "", nil
}

func (s *openAIResponsesSession) newItem(kind, sourceID, prefix string) (*responsesOutputItem, error) {
	if err := s.ensureStarted(); err != nil {
		return nil, err
	}
	id := sourceID
	if id == "" {
		id = prefix + strings.TrimPrefix(s.responseID, "resp_")
	}
	if existing := s.outputItemsByID[id]; existing != nil {
		return nil, fmt.Errorf("OpenAI Responses output item ID %q is duplicated", id)
	}
	item := &responsesOutputItem{id: id, kind: kind, sourceID: sourceID, outputIndex: len(s.items)}
	s.outputItemsByID[id] = item
	s.items = append(s.items, item)
	return item, nil
}

func (s *openAIResponsesSession) addItem(item *responsesOutputItem, value map[string]any) error {
	item.opened = true
	return s.emit("response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": item.outputIndex, "item": value})
}

func (s *openAIResponsesSession) finishStreamItem(item *responsesOutputItem) error {
	switch item.kind {
	case "message":
		content := map[string]any{"type": "output_text", "text": item.text.String(), "annotations": []any{}}
		if err := s.emit("response.output_text.done", map[string]any{"type": "response.output_text.done", "item_id": item.id, "output_index": item.outputIndex, "content_index": item.contentIndex, "text": item.text.String()}); err != nil {
			return err
		}
		if err := s.emit("response.content_part.done", map[string]any{"type": "response.content_part.done", "item_id": item.id, "output_index": item.outputIndex, "content_index": item.contentIndex, "part": content}); err != nil {
			return err
		}
		return s.emit("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": item.outputIndex, "item": map[string]any{"type": "message", "id": item.id, "role": "assistant", "status": "completed", "content": []any{content}}})
	case "reasoning":
		part := map[string]any{"type": "summary_text", "text": item.text.String()}
		if err := s.emit("response.reasoning_summary_text.done", map[string]any{"type": "response.reasoning_summary_text.done", "item_id": item.id, "output_index": item.outputIndex, "summary_index": 0, "text": item.text.String()}); err != nil {
			return err
		}
		if err := s.emit("response.reasoning_summary_part.done", map[string]any{"type": "response.reasoning_summary_part.done", "item_id": item.id, "output_index": item.outputIndex, "summary_index": 0, "part": part}); err != nil {
			return err
		}
		return s.emit("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": item.outputIndex, "item": map[string]any{"type": "reasoning", "id": item.id, "summary": []any{part}}})
	case "function_call":
		arguments := normalizedToolArguments(item.arguments.String())
		if err := s.emit("response.function_call_arguments.done", map[string]any{"type": "response.function_call_arguments.done", "item_id": item.id, "output_index": item.outputIndex, "arguments": arguments}); err != nil {
			return err
		}
		return s.emit("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": item.outputIndex, "item": map[string]any{"type": "function_call", "id": item.id, "call_id": item.callID, "name": item.name, "arguments": arguments, "status": "completed"}})
	default:
		return fmt.Errorf("unsupported OpenAI Responses output item kind %q", item.kind)
	}
}

func (s *openAIResponsesSession) validateTools() error {
	for index, item := range s.toolsByIndex {
		if item.callID == "" || item.name == "" {
			return fmt.Errorf("OpenAI Responses tool call at index %d ended before its ID and name were complete", index)
		}
		arguments := strings.TrimSpace(item.arguments.String())
		if arguments == "" {
			continue
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal([]byte(arguments), &object); err != nil || object == nil {
			return fmt.Errorf("OpenAI Responses tool call %q has invalid object arguments", item.callID)
		}
	}
	return nil
}

func normalizedToolArguments(arguments string) string {
	if strings.TrimSpace(arguments) == "" {
		return "{}"
	}
	return arguments
}

func (s *openAIResponsesSession) output() []any {
	result := make([]any, 0, len(s.items))
	for _, item := range s.items {
		switch item.kind {
		case "message":
			content := map[string]any{"type": "output_text", "text": item.text.String(), "annotations": []any{}}
			result = append(result, map[string]any{"type": "message", "id": item.id, "role": "assistant", "status": "completed", "content": []any{content}})
		case "reasoning":
			result = append(result, map[string]any{"type": "reasoning", "id": item.id, "summary": []any{map[string]any{"type": "summary_text", "text": item.text.String()}}})
		case "function_call":
			result = append(result, map[string]any{"type": "function_call", "id": item.id, "call_id": item.callID, "name": item.name, "arguments": normalizedToolArguments(item.arguments.String()), "status": "completed"})
		}
	}
	return result
}

func (s *openAIResponsesSession) responseObject(status string) map[string]any {
	response := map[string]any{
		"id": s.responseID, "object": "response", "created_at": s.createdAt,
		"status": status, "model": s.model, "output": s.output(),
	}
	if status == "incomplete" {
		response["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
	}
	if s.usage != nil {
		response["usage"] = map[string]any{
			"input_tokens": s.usage.InputTokens, "output_tokens": s.usage.OutputTokens,
			"total_tokens": s.usage.InputTokens + s.usage.OutputTokens,
		}
	}
	return response
}

func (s *openAIResponsesSession) ensureStarted() error {
	if s.started {
		return nil
	}
	s.started = true
	if !s.streaming {
		return nil
	}
	if err := s.emit("response.created", map[string]any{"type": "response.created", "response": s.responseObject("in_progress")}); err != nil {
		return err
	}
	return s.emit("response.in_progress", map[string]any{"type": "response.in_progress", "response": s.responseObject("in_progress")})
}

func (s *openAIResponsesSession) emit(name string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	s.commit()
	if _, err := fmt.Fprintf(s.writer, "event: %s\ndata: %s\n\n", name, encoded); err != nil {
		return err
	}
	if flusher, ok := s.writer.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

func (s *openAIResponsesSession) commit() {
	if s.committed {
		return
	}
	s.writer.WriteHeader(s.status)
	s.committed = true
}

func (s *openAIResponsesSession) responseStatus() string {
	if openAIFinishReason(s.finishReason) == "length" {
		return "incomplete"
	}
	return "completed"
}
