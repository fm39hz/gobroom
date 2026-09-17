package normalize

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func JSON(path string, headers http.Header, payload []byte) (Result, error) {
	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		return Result{}, fmt.Errorf("invalid JSON: %w", err)
	}
	return Map(path, headers, body)
}

func Map(path string, headers http.Header, body map[string]any) (Result, error) {
	format := DetectWithHeaders(path, body, headers)
	model, _ := body["model"].(string)
	if strings.TrimSpace(model) == "" {
		return Result{}, fmt.Errorf("model is required")
	}

	r := Request{
		Model: model, SourceFormat: format, Stream: boolValue(body["stream"], true),
		Tools: normalizeTools(body["tools"]), Extensions: map[string]any{}, Raw: body,
		Transport: TransportHints{AcceptJSON: strings.Contains(strings.ToLower(headers.Get("accept")), "application/json"), AcceptSSE: strings.Contains(strings.ToLower(headers.Get("accept")), "text/event-stream")},
	}
	r.Messages = normalizeMessages(body, format)
	r.Thinking = normalizeThinking(body)
	r.Session = SessionContext{ID: stringValue(headers.Get("x-session-id")), Client: headers.Get("user-agent"), Conversation: stringValue(body["conversation_id"])}
	r.Continuity = normalizeContinuity(body)
	r.Modalities = detectModalities(body)
	for k, v := range body {
		if !isCoreKey(k) {
			r.Extensions[k] = v
		}
	}
	normalizeToolCalls(&r)
	return Result{Request: r, ReceivedAt: time.Now()}, nil
}

func normalizeMessages(body map[string]any, format Format) []Message {
	if raw, ok := body["messages"].([]any); ok {
		return messagesFromArray(raw)
	}
	if input, ok := body["input"].([]any); ok {
		return messagesFromArray(input)
	}
	if text, ok := body["input"].(string); ok {
		return []Message{{Role: "user", Content: text}}
	}
	if contents, ok := body["contents"].([]any); ok {
		result := make([]Message, 0, len(contents))
		for _, value := range contents {
			m, _ := value.(map[string]any)
			role := stringValue(m["role"])
			if role == "model" {
				role = "assistant"
			}
			result = append(result, Message{Role: role, Content: m["parts"], Metadata: m})
		}
		return result
	}
	return nil
}

func messagesFromArray(raw []any) []Message {
	result := make([]Message, 0, len(raw))
	for _, value := range raw {
		m, ok := value.(map[string]any)
		if !ok {
			continue
		}
		msg := Message{Role: stringValue(m["role"]), Content: m["content"], Name: stringValue(m["name"]), ToolCallID: stringValue(m["tool_call_id"]), Metadata: m}
		if calls, ok := m["tool_calls"].([]any); ok {
			for _, rawCall := range calls {
				c, _ := rawCall.(map[string]any)
				fn, _ := c["function"].(map[string]any)
				msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: stringValue(c["id"]), Type: stringValue(c["type"]), Name: stringValue(fn["name"]), Arguments: fn["arguments"]})
			}
		}
		result = append(result, msg)
	}
	return result
}

func normalizeTools(value any) []Tool {
	raw, _ := value.([]any)
	result := make([]Tool, 0, len(raw))
	for _, value := range raw {
		m, ok := value.(map[string]any)
		if ok {
			result = append(result, Tool{Type: stringValue(m["type"]), Name: stringValue(m["name"]), Function: mapValue(m["function"]), Metadata: m})
		}
	}
	return result
}

func normalizeThinking(body map[string]any) ThinkingIntent {
	if effort := stringValue(body["reasoning_effort"]); effort != "" {
		return ThinkingIntent{Mode: "effort", Effort: effort, Source: "reasoning_effort"}
	}
	if thinking, ok := body["thinking"].(map[string]any); ok {
		return ThinkingIntent{Mode: stringValue(thinking["type"]), Effort: stringValue(thinking["effort"]), BudgetTokens: intValue(thinking["budget_tokens"]), Source: "thinking"}
	}
	if reasoning, ok := body["reasoning"].(map[string]any); ok {
		return ThinkingIntent{Mode: "effort", Effort: stringValue(reasoning["effort"]), Source: "reasoning"}
	}
	return ThinkingIntent{Mode: "auto", Source: "default"}
}

func normalizeContinuity(body map[string]any) ContinuityState {
	return ContinuityState{ResponseID: stringValue(body["response_id"]), PreviousResponse: stringValue(body["previous_response_id"])}
}

func detectModalities(body map[string]any) Modalities {
	var result Modalities
	var visit func(any)
	visit = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			t := strings.ToLower(stringValue(v["type"]))
			result.Vision = result.Vision || t == "image" || t == "image_url" || t == "input_image"
			result.AudioInput = result.AudioInput || t == "audio" || t == "input_audio" || t == "audio_url"
			result.VideoInput = result.VideoInput || t == "video" || t == "input_video" || t == "video_url"
			result.PDF = result.PDF || t == "file" || t == "document" || t == "input_file" || strings.Contains(strings.ToLower(stringValue(v["media_type"])), "pdf")
			for _, child := range v {
				visit(child)
			}
		case []any:
			for _, child := range v {
				visit(child)
			}
		case string:
			lower := strings.ToLower(v)
			result.Vision = result.Vision || strings.Contains(lower, "data:image/")
			result.AudioInput = result.AudioInput || strings.Contains(lower, "data:audio/")
			result.PDF = result.PDF || strings.Contains(lower, "data:application/pdf")
		}
	}
	visit(body)
	return result
}

func normalizeToolCalls(r *Request) {
	sequence := 0
	for i := range r.Messages {
		for j := range r.Messages[i].ToolCalls {
			if r.Messages[i].ToolCalls[j].ID == "" {
				sequence++
				r.Messages[i].ToolCalls[j].ID = fmt.Sprintf("call_gorouter_%d", sequence)
			}
		}
	}
}

func isCoreKey(k string) bool {
	switch k {
	case "model", "messages", "input", "contents", "stream", "tools", "reasoning_effort", "thinking", "reasoning", "conversation_id", "response_id", "previous_response_id":
		return true
	}
	return false
}
func boolValue(v any, fallback bool) bool {
	b, ok := v.(bool)
	if !ok {
		return fallback
	}
	return b
}
func intValue(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}
func stringValue(v any) string      { s, _ := v.(string); return s }
func mapValue(v any) map[string]any { m, _ := v.(map[string]any); return m }
