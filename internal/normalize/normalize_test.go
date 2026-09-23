package normalize

import (
	"net/http"
	"testing"
)

func TestResponsesNormalizesToSemanticMessages(t *testing.T) {
	result, err := Map("/v1/responses", http.Header{}, map[string]any{
		"model": "tech-lead", "input": []any{map[string]any{"role": "user", "content": "hello"}}, "stream": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Request.SourceFormat != FormatOpenAIResponses || len(result.Request.Messages) != 1 || result.Request.Messages[0].Role != "user" {
		t.Fatalf("unexpected result: %#v", result.Request)
	}
}

func TestThinkingToolsAndModalitiesAreCaptured(t *testing.T) {
	result, err := Map("/v1/chat/completions", http.Header{}, map[string]any{
		"model": "g4f/model", "messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,x"}}}}, map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"function": map[string]any{"name": "search", "arguments": "{}"}}}}},
		"tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "search"}}}, "reasoning_effort": "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Request.Modalities.Vision || result.Request.Thinking.Effort != "high" || len(result.Request.Tools) != 1 {
		t.Fatalf("normalization lost semantics: %#v", result.Request)
	}
	if result.Request.Messages[1].ToolCalls[0].ID == "" {
		t.Fatal("tool call ID was not synthesized")
	}
}

func TestEndpointWinsOverBodyHeuristic(t *testing.T) {
	result, err := Map("/v1/messages", http.Header{}, map[string]any{"model": "claude", "messages": []any{}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Request.SourceFormat != FormatAnthropic {
		t.Fatalf("got %s", result.Request.SourceFormat)
	}
}

func TestPreferredConnectionHeaderIsCaptured(t *testing.T) {
	headers := http.Header{"X-Connection-Id": []string{"conn-b"}}
	result, err := Map("/v1/chat/completions", headers, map[string]any{"model": "public", "messages": []any{}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Request.Transport.PreferredConnectionID != "conn-b" {
		t.Fatalf("preferred connection=%q", result.Request.Transport.PreferredConnectionID)
	}
}

func TestAbsentReasoningMeansInherit(t *testing.T) {
	result, err := Map("/v1/chat/completions", http.Header{}, map[string]any{"model": "public", "messages": []any{}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Request.Thinking.Mode != "inherit" {
		t.Fatalf("thinking=%#v", result.Request.Thinking)
	}
}

func TestReasoningPrecedenceAndDialectTranslation(t *testing.T) {
	intent := ResolveReasoning(ReasoningPolicy{Explicit: ThinkingIntent{Mode: "inherit"}, Preset: ThinkingIntent{Mode: "level", Effort: "high", Source: "preset"}, Combo: ThinkingIntent{Mode: "level", Effort: "low"}})
	if intent.Effort != "high" || intent.Source != "preset" {
		t.Fatalf("resolved=%#v", intent)
	}
	anthropic := AnthropicReasoning(intent)
	if anthropic["output_config"] == nil || anthropic["thinking"] == nil {
		t.Fatalf("anthropic=%#v", anthropic)
	}
	responses := OpenAIResponsesReasoning(intent)
	if responses["reasoning"] == nil {
		t.Fatalf("responses=%#v", responses)
	}
	gemini := GeminiReasoning(intent)
	if gemini["generationConfig"] == nil {
		t.Fatalf("gemini=%#v", gemini)
	}
}
