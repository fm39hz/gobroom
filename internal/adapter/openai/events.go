package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

func emitOpenAIEvent(payload map[string]any, emit func(kernel.ResponseEvent)) {
	if emit == nil {
		return
	}
	now := time.Now()
	objectType := stringValue(payload["object"])
	typ := stringValue(payload["type"])
	responseID := stringValue(payload["response_id"])
	itemID := stringValue(payload["item_id"])
	if responseID == "" && strings.HasPrefix(objectType, "chat.completion") {
		responseID = stringValue(payload["id"])
	}
	if responseID == "" && objectType == "response" {
		responseID = stringValue(payload["id"])
	}
	if responseID == "" {
		if response, ok := payload["response"].(map[string]any); ok {
			responseID = stringValue(response["id"])
		}
	}
	if responseID != "" {
		emit(kernel.ResponseEvent{At: now, Kind: kernel.EventResponseStarted, ResponseID: responseID})
	}
	if choices, ok := payload["choices"].([]any); ok && len(choices) > 0 {
		choice, _ := choices[0].(map[string]any)
		delta, _ := choice["delta"].(map[string]any)
		if len(delta) == 0 {
			delta, _ = choice["message"].(map[string]any)
		}
		if text, ok := delta["content"].(string); ok && text != "" {
			emit(kernel.ResponseEvent{At: now, Kind: kernel.EventTextDelta, Text: text})
		}
		if text, ok := delta["reasoning_content"].(string); ok && text != "" {
			emit(kernel.ResponseEvent{At: now, Kind: kernel.EventThinkingDelta, Text: text})
		}
		if calls, ok := delta["tool_calls"].([]any); ok {
			for index, raw := range calls {
				call, _ := raw.(map[string]any)
				function, _ := call["function"].(map[string]any)
				emit(kernel.ResponseEvent{At: now, Kind: kernel.EventToolCallDelta, Index: index, ToolCallID: stringValue(call["id"]), ToolName: stringValue(function["name"]), ToolArguments: stringValue(function["arguments"])})
			}
		}
		if finish := stringValue(choice["finish_reason"]); finish != "" {
			emit(kernel.ResponseEvent{At: now, Kind: kernel.EventContentBlockEnd, StopReason: finish})
			emit(kernel.ResponseEvent{At: now, Kind: kernel.EventResponseComplete})
		}
	}
	switch {
	case objectType == "response" && typ == "":
		emit(kernel.ResponseEvent{At: now, Kind: kernel.EventResponseComplete, ResponseID: responseID})
	case strings.Contains(typ, "output_item.added"):
		item, _ := payload["item"].(map[string]any)
		itemType := stringValue(payload["item_type"])
		if itemType == "" {
			itemType = stringValue(item["type"])
		}
		if itemID == "" {
			itemID = stringValue(item["id"])
		}
		callID := stringValue(item["call_id"])
		if callID == "" && itemType == "function_call" {
			callID = stringValue(item["id"])
		}
		toolName := stringValue(item["name"])
		emit(kernel.ResponseEvent{At: now, Kind: kernel.EventContentBlockStart, ResponseID: responseID, ItemID: itemID, BlockType: itemType, ToolCallID: callID, ToolName: toolName})
		if itemType == "function_call" && (callID != "" || toolName != "") {
			emit(kernel.ResponseEvent{At: now, Kind: kernel.EventToolCallDelta, ResponseID: responseID, ItemID: itemID, ContentType: "function_call", ToolCallID: callID, ToolName: toolName, ToolArguments: stringValue(item["arguments"])})
		}
	case strings.Contains(typ, "output_item.done"):
		emit(kernel.ResponseEvent{At: now, Kind: kernel.EventContentBlockEnd, ResponseID: responseID, ItemID: itemID, StopReason: stringValue(payload["status"])})
	case strings.Contains(typ, "output_text.delta"):
		emit(kernel.ResponseEvent{At: now, Kind: kernel.EventTextDelta, ResponseID: responseID, ItemID: itemID, ContentType: "output_text", Text: stringValue(payload["delta"])})
	case strings.Contains(typ, "reasoning") && strings.Contains(typ, "delta"):
		emit(kernel.ResponseEvent{At: now, Kind: kernel.EventThinkingDelta, ResponseID: responseID, ItemID: itemID, ContentType: "reasoning", Text: stringValue(payload["delta"])})
	case strings.Contains(typ, "function_call_arguments.delta"):
		emit(kernel.ResponseEvent{At: now, Kind: kernel.EventToolCallDelta, ResponseID: responseID, ItemID: itemID, ContentType: "function_call", ToolArguments: stringValue(payload["delta"])})
	case typ == "response.completed":
		response, _ := payload["response"].(map[string]any)
		emit(kernel.ResponseEvent{At: now, Kind: kernel.EventContentBlockEnd, ResponseID: responseID, StopReason: stringValue(response["status"])})
		emit(kernel.ResponseEvent{At: now, Kind: kernel.EventResponseComplete, ResponseID: responseID})
	}
	if usage, ok := payload["usage"].(map[string]any); ok {
		inputTokens := usage["input_tokens"]
		if inputTokens == nil {
			inputTokens = usage["prompt_tokens"]
		}
		outputTokens := usage["output_tokens"]
		if outputTokens == nil {
			outputTokens = usage["completion_tokens"]
		}
		event := kernel.UsageEvent{InputTokens: int64(numberValue(inputTokens)), OutputTokens: int64(numberValue(outputTokens))}
		emit(kernel.ResponseEvent{At: now, Kind: kernel.EventUsage, Usage: &event})
	}
}

func decodeOpenAIResponse(ctx context.Context, response kernel.UpstreamResponse, wireFormat normalize.Format, emit func(kernel.ResponseEvent) error, hooks kernel.StreamHooks) error {
	if response.Body == nil {
		return fmt.Errorf("provider returned an empty response body")
	}
	contentType := strings.ToLower(response.Headers.Get("content-type"))
	if strings.Contains(contentType, "text/event-stream") {
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 4096), 4*1024*1024)
		scanner.Split(splitSSELines)
		started := false
		completed := false
		usage := kernel.UsageEvent{}
		for scanner.Scan() {
			if err := ctx.Err(); err != nil {
				return err
			}
			rawLine := append([]byte(nil), scanner.Bytes()...)
			line := strings.TrimRight(string(rawLine), "\r\n")
			if err := emit(kernel.ResponseEvent{At: time.Now(), Kind: kernel.EventRawFrame, WireFormat: wireFormat, Raw: rawLine}); err != nil {
				return err
			}
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(strings.TrimSuffix(line, "\n"), "data:"))
			if data == "" {
				continue
			}
			if data == "[DONE]" {
				completed = true
				if err := emit(kernel.ResponseEvent{At: time.Now(), Kind: kernel.EventResponseComplete}); err != nil {
					return err
				}
				continue
			}
			if !started && hooks.OnFirstByte != nil {
				hooks.OnFirstByte(time.Now())
				started = true
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(data), &payload); err != nil {
				return fmt.Errorf("decode upstream SSE JSON: %w", err)
			}
			var events []kernel.ResponseEvent
			emitOpenAIEvent(payload, func(event kernel.ResponseEvent) { events = append(events, event) })
			for _, event := range events {
				if event.Kind == kernel.EventResponseComplete {
					completed = true
				}
				if event.Usage != nil {
					usage = *event.Usage
				}
				if err := emit(event); err != nil {
					return err
				}
			}
		}
		if err := scanner.Err(); err != nil {
			return err
		}
		if !completed {
			return io.ErrUnexpectedEOF
		}
		if hooks.OnComplete != nil {
			usage.Status = "ok"
			hooks.OnComplete(usage)
		}
		return nil
	}
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("decode upstream JSON: %w", err)
	}
	var events []kernel.ResponseEvent
	emitOpenAIEvent(payload, func(event kernel.ResponseEvent) { events = append(events, event) })
	if hooks.OnFirstByte != nil {
		hooks.OnFirstByte(time.Now())
	}
	if err := emit(kernel.ResponseEvent{At: time.Now(), Kind: kernel.EventRawFrame, WireFormat: wireFormat, Raw: append([]byte(nil), data...)}); err != nil {
		return err
	}
	var usage kernel.UsageEvent
	completed := false
	for _, event := range events {
		if event.Kind == kernel.EventResponseComplete {
			completed = true
		}
		if event.Usage != nil {
			usage = *event.Usage
		}
		if err := emit(event); err != nil {
			return err
		}
	}
	if !completed {
		return io.ErrUnexpectedEOF
	}
	if hooks.OnComplete != nil {
		usage.Status = "ok"
		hooks.OnComplete(usage)
	}
	return nil
}

func splitSSELines(data []byte, atEOF bool) (int, []byte, error) {
	if index := bytes.IndexByte(data, '\n'); index >= 0 {
		return index + 1, data[:index+1], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func numberValue(value any) float64 { n, _ := value.(float64); return n }
func stringValue(value any) string  { result, _ := value.(string); return result }
