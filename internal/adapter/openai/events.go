package openai

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
)

// streamSSEEvents preserves every upstream line while observing common Chat
// and Responses event shapes. Rendering remains outside this observer.
func streamSSEEvents(body io.Reader, writer http.ResponseWriter, hooks kernel.StreamHooks) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 4096), 4*1024*1024)
	started := false
	for scanner.Scan() {
		line := scanner.Text()
		_, _ = io.WriteString(writer, line+"\n")
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		if !started {
			started = true
			if hooks.OnFirstByte != nil {
				hooks.OnFirstByte(time.Now())
			}
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			canonicalErr := kernel.ResponseEvent{At: time.Now(), Kind: kernel.EventResponseError, Error: "malformed SSE JSON: " + err.Error()}
			if hooks.OnEvent != nil {
				hooks.OnEvent(canonicalErr)
			}
			if hooks.OnError != nil {
				hooks.OnError(err)
			}
			return err
		}
		emitOpenAIEvent(payload, hooks.OnEvent)
	}
	if err := scanner.Err(); err != nil && hooks.OnError != nil {
		hooks.OnError(err)
	}
	return scanner.Err()
}

func emitOpenAIEvent(payload map[string]any, emit func(kernel.ResponseEvent)) {
	if emit == nil {
		return
	}
	now := time.Now()
	if choices, ok := payload["choices"].([]any); ok && len(choices) > 0 {
		choice, _ := choices[0].(map[string]any)
		delta, _ := choice["delta"].(map[string]any)
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
	}
	typ, _ := payload["type"].(string)
	switch {
	case strings.Contains(typ, "output_text.delta"):
		emit(kernel.ResponseEvent{At: now, Kind: kernel.EventTextDelta, Text: stringValue(payload["delta"])})
	case strings.Contains(typ, "reasoning") && strings.Contains(typ, "delta"):
		emit(kernel.ResponseEvent{At: now, Kind: kernel.EventThinkingDelta, Text: stringValue(payload["delta"])})
	case strings.Contains(typ, "function_call_arguments.delta"):
		emit(kernel.ResponseEvent{At: now, Kind: kernel.EventToolCallDelta, ToolArguments: stringValue(payload["delta"])})
	}
	if usage, ok := payload["usage"].(map[string]any); ok {
		event := kernel.UsageEvent{InputTokens: int64(numberValue(usage["input_tokens"])), OutputTokens: int64(numberValue(usage["output_tokens"]))}
		emit(kernel.ResponseEvent{At: now, Kind: kernel.EventUsage, Usage: &event})
	}
}

func numberValue(value any) float64 { n, _ := value.(float64); return n }
func stringValue(value any) string  { result, _ := value.(string); return result }
