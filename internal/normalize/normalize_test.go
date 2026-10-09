package normalize

import (
	"net/http"
	"strings"
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

func TestIdempotencyKeyIsRequestScopedAndValidated(t *testing.T) {
	headers := http.Header{"Idempotency-Key": []string{"client-key-01"}}
	result, err := Map("/v1/chat/completions", headers, map[string]any{"model": "m", "messages": []any{map[string]any{"role": "user", "content": "hello"}}})
	if err != nil || result.Request.Transport.IdempotencyKey != "client-key-01" {
		t.Fatalf("idempotency key=%q err=%v", result.Request.Transport.IdempotencyKey, err)
	}
	for _, invalid := range []string{"invalid\r\nX-Injected: yes", strings.Repeat("x", 256)} {
		if _, err := Map("/v1/chat/completions", http.Header{"Idempotency-Key": []string{invalid}}, map[string]any{"model": "m"}); err == nil {
			t.Fatalf("invalid idempotency key was accepted: %q", invalid)
		}
	}
	if _, err := Map("/v1/chat/completions", http.Header{"Idempotency-Key": []string{"first", "second"}}, map[string]any{"model": "m"}); err == nil {
		t.Fatal("duplicate Idempotency-Key headers were accepted")
	}
}

func TestSystemAndDeveloperInstructionsAreSeparatedFromConversation(t *testing.T) {
	result, err := Map("/v1/chat/completions", http.Header{}, map[string]any{"model": "m", "messages": []any{
		map[string]any{"role": "system", "content": "harness policy"}, map[string]any{"role": "developer", "content": "provider policy"}, map[string]any{"role": "user", "content": "hello"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Request.Prompt.Layers) != 2 || result.Request.Prompt.Layers[0].Origin != PromptInline || result.Request.Prompt.Layers[0].Role != "system" || result.Request.Prompt.Layers[1].Role != "developer" {
		t.Fatalf("prompt plan=%#v", result.Request.Prompt)
	}
	if len(result.Request.Messages) != 1 || result.Request.Messages[0].Role != "user" {
		t.Fatalf("conversation=%#v", result.Request.Messages)
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

func TestOpenAIGenerationAndParallelToolChoiceNormalizeIntoTypedIR(t *testing.T) {
	chat, err := Map("/v1/chat/completions", http.Header{}, map[string]any{
		"model": "m", "messages": []any{map[string]any{"role": "user", "content": "hi"}},
		"max_completion_tokens": float64(88), "temperature": float64(0.4), "top_p": float64(0.8), "stop": []any{"done"},
		"tool_choice": map[string]any{"type": "function", "function": map[string]any{"name": "lookup", "vendor_function": "retain"}, "vendor_choice": "retain"}, "parallel_tool_calls": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if chat.Request.Generation.MaxOutputTokens == nil || *chat.Request.Generation.MaxOutputTokens != 88 || chat.Request.Generation.Temperature == nil || *chat.Request.Generation.Temperature != 0.4 || chat.Request.Generation.TopP == nil || *chat.Request.Generation.TopP != 0.8 || len(chat.Request.Generation.StopSequences) != 1 || chat.Request.Generation.StopSequences[0] != "done" {
		t.Fatalf("OpenAI Chat generation options=%#v", chat.Request.Generation)
	}
	if chat.Request.ToolChoice.Mode != "tool" || chat.Request.ToolChoice.Name != "lookup" || !chat.Request.ToolChoice.Set || !chat.Request.ToolChoice.DisableParallelTools || chat.Request.ToolChoice.Metadata["vendor_choice"] != "retain" || chat.Request.ToolChoice.Metadata["function"].(map[string]any)["vendor_function"] != "retain" {
		t.Fatalf("OpenAI Chat tool choice=%#v", chat.Request.ToolChoice)
	}

	responses, err := Map("/v1/responses", http.Header{}, map[string]any{"model": "m", "input": "hi", "max_output_tokens": float64(33), "temperature": float64(0.2)})
	if err != nil {
		t.Fatal(err)
	}
	if responses.Request.Generation.MaxOutputTokens == nil || *responses.Request.Generation.MaxOutputTokens != 33 || responses.Request.Generation.Temperature == nil || *responses.Request.Generation.Temperature != 0.2 {
		t.Fatalf("OpenAI Responses generation options=%#v", responses.Request.Generation)
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

func TestAnthropicMessagesNormalizeToTypedConversationToolsAndOptions(t *testing.T) {
	result, err := JSON("/v1/messages", http.Header{}, []byte(`{
"model":"role","max_tokens":512,"temperature":0.2,"top_p":0.9,"stop_sequences":["done"],
"system":[{"type":"text","text":"Follow policy."}],
"tools":[{"name":"lookup","description":"Look up information","input_schema":{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}}],
"tool_choice":{"type":"tool","name":"lookup","disable_parallel_tool_use":true},
"messages":[
 {"role":"user","content":[{"type":"text","text":"Find weather."}]},
 {"role":"assistant","content":[{"type":"text","text":"Checking."},{"type":"tool_use","id":"call_1","name":"lookup","input":{"q":"weather"}}]},
 {"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"Sunny."}]}
]}`))
	if err != nil {
		t.Fatal(err)
	}
	request := result.Request
	if request.SourceFormat != FormatAnthropic || len(request.Prompt.Layers) != 1 || len(request.Prompt.Layers[0].Parts) != 1 || request.Prompt.Layers[0].Parts[0].Text != "Follow policy." {
		t.Fatalf("Anthropic system prompt normalization=%#v", request.Prompt)
	}
	if len(request.Tools) != 1 || request.Tools[0].Name != "lookup" || request.Tools[0].Function["parameters"] == nil {
		t.Fatalf("Anthropic tools normalization=%#v", request.Tools)
	}
	if request.ToolChoice.Mode != "tool" || request.ToolChoice.Name != "lookup" || !request.ToolChoice.DisableParallelTools {
		t.Fatalf("Anthropic tool choice normalization=%#v", request.ToolChoice)
	}
	if request.Generation.MaxOutputTokens == nil || *request.Generation.MaxOutputTokens != 512 || request.Generation.Temperature == nil || *request.Generation.Temperature != 0.2 || request.Generation.TopP == nil || *request.Generation.TopP != 0.9 || len(request.Generation.StopSequences) != 1 || request.Generation.StopSequences[0] != "done" {
		t.Fatalf("Anthropic generation options normalization=%#v", request.Generation)
	}
	if len(request.Messages) != 3 || request.Messages[1].Role != "assistant" || len(request.Messages[1].ToolCalls) != 1 || request.Messages[1].ToolCalls[0].ID != "call_1" || request.Messages[2].Role != "tool" || request.Messages[2].ToolCallID != "call_1" || request.Messages[2].Content != "Sunny." {
		t.Fatalf("Anthropic tool-use history normalization=%#v", request.Messages)
	}
	if len(request.UnsupportedFacets) != 0 {
		t.Fatalf("representable Anthropic request marked unsupported: %v", request.UnsupportedFacets)
	}
}

func TestAnthropicOpaqueThinkingAndToolResultErrorsBecomeExplicitFacets(t *testing.T) {
	result, err := JSON("/v1/messages", http.Header{}, []byte(`{"model":"role","max_tokens":128,"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"private","signature":"issuer-signature"},{"type":"tool_use","id":"call_1","name":"lookup","input":{}},{"type":"text","text":"trailing text"}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"failed","is_error":true}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, facet := range result.Request.UnsupportedFacets {
		got[facet] = true
	}
	for _, required := range []string{"reasoning.signature", "tools.block_order", "tools.result_status"} {
		if !got[required] {
			t.Errorf("unsupported Anthropic semantic %q was not retained: %v", required, result.Request.UnsupportedFacets)
		}
	}
}

func TestAnthropicAdaptiveEffortBecomesTypedReasoningIntent(t *testing.T) {
	result, err := JSON("/v1/messages", http.Header{}, []byte(`{"model":"role","max_tokens":256,"thinking":{"type":"adaptive"},"output_config":{"effort":"high"},"messages":[{"role":"user","content":"think"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Request.Thinking.Mode != "level" || result.Request.Thinking.Effort != "high" || result.Request.Thinking.Source != "output_config" {
		t.Fatalf("reasoning intent=%#v", result.Request.Thinking)
	}
	for _, facet := range result.Request.UnsupportedFacets {
		if facet == "reasoning.intent" {
			t.Fatalf("valid adaptive effort marked unsupported: %v", result.Request.UnsupportedFacets)
		}
	}
}

func TestAnthropicMalformedThinkingIntentIsExplicitlyUnsupported(t *testing.T) {
	for _, body := range []string{
		`{"model":"role","thinking":{},"messages":[]}`,
		`{"model":"role","thinking":{"type":"enabled"},"messages":[]}`,
		`{"model":"role","thinking":{"type":"enabled","budget_tokens":1.5},"messages":[]}`,
		`{"model":"role","thinking":{"type":"adaptive","budget_tokens":1000},"messages":[]}`,
		`{"model":"role","thinking":{"type":"future-mode"},"messages":[]}`,
		`{"model":"role","thinking":{"type":"adaptive"},"output_config":{"effort":"high","future":true},"messages":[]}`,
		`{"model":"role","thinking":{"type":"adaptive"},"output_config":{"effort":4},"messages":[]}`,
	} {
		result, err := JSON("/v1/messages", http.Header{}, []byte(body))
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, facet := range result.Request.UnsupportedFacets {
			found = found || facet == "reasoning.intent"
		}
		if !found {
			t.Errorf("malformed reasoning intent was not retained as unsupported: %s", body)
		}
	}
}

func TestStreamingDefaultsToNonStreamAndHonorsExplicitChoice(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/messages", "/v1/responses"} {
		result, err := Map(path, http.Header{}, map[string]any{"model": "model-a", "messages": []any{}})
		if err != nil {
			t.Fatal(err)
		}
		if result.Request.Stream {
			t.Errorf("%s defaulted stream=true", path)
		}
	}
	for _, want := range []bool{false, true} {
		result, err := Map("/v1/messages", http.Header{}, map[string]any{"model": "model-a", "messages": []any{}, "stream": want})
		if err != nil {
			t.Fatal(err)
		}
		if result.Request.Stream != want {
			t.Errorf("explicit stream=%v normalized to %v", want, result.Request.Stream)
		}
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
