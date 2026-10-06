package gemini

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

func TestPrepareMapsReasoningToGeminiThinkingConfig(t *testing.T) {
	request := normalize.Request{SourceFormat: normalize.FormatOpenAIChat, Stream: false, Messages: []normalize.Message{{Role: "user", Content: "hello"}}, Raw: map[string]any{"max_tokens": float64(123)}, Thinking: normalize.ThinkingIntent{Mode: "budget", BudgetTokens: 512}}
	prepared, err := (Gemini{}).Prepare(context.Background(), request, kernel.Route{ID: "r", BaseURL: "https://gemini.test", ExternalModel: "gemini-2"}, kernel.Credential{Secret: "key"})
	if err != nil {
		t.Fatal(err)
	}
	var body generateContentRequest
	if err := json.NewDecoder(prepared.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.GenerationConfig == nil || body.GenerationConfig.ThinkingConfig == nil || body.GenerationConfig.ThinkingConfig.ThinkingBudget == nil || *body.GenerationConfig.ThinkingConfig.ThinkingBudget != 512 {
		t.Fatalf("thinking=%#v", body.GenerationConfig)
	}
	if body.GenerationConfig.MaxOutputTokens == nil || *body.GenerationConfig.MaxOutputTokens != 123 {
		t.Fatalf("generationConfig=%#v", body.GenerationConfig)
	}
	if prepared.URL != "/models/gemini-2:generateContent" || prepared.Headers.Get("x-goog-api-key") != "key" {
		t.Fatalf("prepared=%#v", prepared)
	}
}

func TestPrepareTranslatesChatHistoryToolsAndStreamEndpoint(t *testing.T) {
	request := normalize.Request{
		Stream: true,
		Messages: []normalize.Message{
			{Role: "system", Content: "be concise"},
			{Role: "user", Content: "search this"},
			{Role: "assistant", ToolCalls: []normalize.ToolCall{{ID: "call-1", Name: "search", Arguments: map[string]any{"q": "x"}, Metadata: map[string]any{"thoughtSignature": "sig"}}}},
			{Role: "tool", ToolCallID: "call-1", Content: `{"ok":true}`},
		},
		Tools: []normalize.Tool{{Type: "function", Function: map[string]any{"name": "search", "description": "search", "parameters": map[string]any{"type": "object"}}}},
		Raw:   map[string]any{"tool_choice": "required"},
	}
	prepared, err := (Gemini{}).Prepare(context.Background(), request, kernel.Route{BaseURL: "https://gemini.test", ExternalModel: "gemini-3"}, kernel.Credential{})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.URL != "/models/gemini-3:streamGenerateContent?alt=sse" {
		t.Fatalf("stream endpoint=%q", prepared.URL)
	}
	var body generateContentRequest
	if err := json.NewDecoder(prepared.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.SystemInstruction == nil {
		t.Fatalf("system instruction missing: %#v", body)
	}
	if len(body.Contents) != 3 || len(body.Contents[1].Parts) == 0 {
		t.Fatalf("contents=%#v", body.Contents)
	}
	call := body.Contents[1].Parts[0]
	if call.ThoughtSignature != "sig" {
		t.Fatalf("thought signature missing: %#v", call)
	}
	toolReply := body.Contents[2].Parts[0].FunctionResponse
	if toolReply == nil || toolReply.Name != "search" {
		t.Fatalf("tool reply=%#v", toolReply)
	}
	if body.ToolConfig == nil || body.ToolConfig.FunctionCallingConfig.Mode != "ANY" {
		t.Fatalf("tool config=%#v", body.ToolConfig)
	}
}

func TestPrepareBindsGenerationOptionsAndNamedToolChoice(t *testing.T) {
	request := normalize.Request{
		Messages: []normalize.Message{{Role: "user", Content: "call lookup"}},
		Tools:    []normalize.Tool{{Type: "function", Function: map[string]any{"name": "lookup", "parameters": map[string]any{"type": "object"}}}},
		Raw: map[string]any{
			"max_tokens": float64(100), "max_completion_tokens": float64(80), "top_p": 0.8,
			"stop": []any{"END", "STOP"}, "tool_choice": map[string]any{"type": "function", "function": map[string]any{"name": "lookup"}},
		},
	}
	body, err := geminiRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	config := body.GenerationConfig
	if config == nil || config.MaxOutputTokens == nil || *config.MaxOutputTokens != 80 || config.TopP == nil || *config.TopP != 0.8 {
		t.Fatalf("generation config=%#v", config)
	}
	if len(config.StopSequences) != 2 || config.StopSequences[0] != "END" || config.StopSequences[1] != "STOP" {
		t.Fatalf("stop sequences=%#v", config.StopSequences)
	}
	toolConfig := body.ToolConfig.FunctionCallingConfig
	if toolConfig.Mode != "ANY" || len(toolConfig.AllowedFunctionNames) != 1 || toolConfig.AllowedFunctionNames[0] != "lookup" {
		t.Fatalf("named tool choice=%#v", toolConfig)
	}
}

func TestPrepareRejectsUnrepresentableCandidateAndToolOptions(t *testing.T) {
	for _, raw := range []map[string]any{
		{"n": float64(2)},
		{"tool_choice": "required"},
		{"logprobs": true},
		{"response_format": map[string]any{"type": "unsupported"}},
		{"response_format": map[string]any{"type": "json_schema", "json_schema": map[string]any{}}},
		{"unknown_generation_option": float64(1)},
	} {
		request := normalize.Request{Messages: []normalize.Message{{Role: "user", Content: "hi"}}, Raw: raw}
		if _, err := geminiRequest(request); err == nil {
			t.Fatalf("expected explicit error for incompatible OpenAI options %#v", raw)
		}
	}
}

func TestPrepareMapsAllowedToolsByName(t *testing.T) {
	request := normalize.Request{
		Messages: []normalize.Message{{Role: "user", Content: "call one"}},
		Tools:    []normalize.Tool{{Type: "function", Function: map[string]any{"name": "one"}}, {Type: "function", Function: map[string]any{"name": "two"}}},
		Raw: map[string]any{"tool_choice": map[string]any{"type": "allowed_tools", "allowed_tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "two"}},
		}}},
	}
	body, err := geminiRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	choice := body.ToolConfig.FunctionCallingConfig
	if choice.Mode != "ANY" || len(choice.AllowedFunctionNames) != 1 || choice.AllowedFunctionNames[0] != "two" {
		t.Fatalf("allowed function mapping=%#v", choice)
	}
}

func TestPrepareRejectsUnsupportedContentInsteadOfDroppingIt(t *testing.T) {
	request := normalize.Request{Messages: []normalize.Message{{Role: "user", Content: []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.test/image.png"}}}}}}
	if _, err := (Gemini{}).Prepare(context.Background(), request, kernel.Route{ExternalModel: "gemini"}, kernel.Credential{}); err == nil || !strings.Contains(err.Error(), "not silently dropped") {
		t.Fatalf("expected explicit unsupported-media error, got %v", err)
	}
}

func TestPrepareMapsBase64ImageToInlineData(t *testing.T) {
	request := normalize.Request{Messages: []normalize.Message{{Role: "user", Content: []any{map[string]any{
		"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,aGVsbG8="},
	}}}}}
	body, err := geminiRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	inline := body.Contents[0].Parts[0].InlineData
	if inline == nil || inline.MIMEType != "image/png" || inline.Data != "aGVsbG8=" {
		t.Fatalf("inline image=%#v", inline)
	}
}

func TestGeminiToolThoughtSignatureSurvivesOpenAIHistoryNormalization(t *testing.T) {
	result, err := normalize.Map("/v1/chat/completions", http.Header{}, map[string]any{
		"model": "role",
		"messages": []any{
			map[string]any{"role": "user", "content": "find it"},
			map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{
				"id": "call-1", "type": "function", "thoughtSignature": "opaque-signature",
				"function": map[string]any{"name": "search", "arguments": `{"q":"x"}`},
			}}},
			map[string]any{"role": "tool", "tool_call_id": "call-1", "content": `{"ok":true}`},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := geminiRequest(result.Request)
	if err != nil {
		t.Fatal(err)
	}
	parts := body.Contents[1].Parts
	if len(parts) == 0 || parts[0].ThoughtSignature != "opaque-signature" {
		t.Fatalf("signature did not survive normalization: %#v", parts)
	}
}

func TestTranslateGeminiJSONToOpenAIChat(t *testing.T) {
	body := `{"candidates":[{"content":{"parts":[{"text":"hello"},{"functionCall":{"name":"lookup","args":{"id":7},"id":"call-7"},"thoughtSignature":"sig"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":2}}`
	recorder := httptest.NewRecorder()
	var events []kernel.ResponseEvent
	var completed kernel.UsageEvent
	err := NewAdapter().RenderResponse(context.Background(), kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, recorder, normalize.FormatOpenAIChat, kernel.StreamHooks{OnEvent: func(event kernel.ResponseEvent) { events = append(events, event) }, OnComplete: func(event kernel.UsageEvent) { completed = event }})
	if err != nil {
		t.Fatal(err)
	}
	var response openAIChatResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	choice := response.Choices[0]
	message := choice.Message
	if message.Content != "hello" || choice.FinishReason != "tool_calls" {
		t.Fatalf("translated choice=%#v", choice)
	}
	tool := message.ToolCalls[0]
	if tool.ThoughtSignature != "sig" {
		t.Fatalf("signature lost: %#v", tool)
	}
	if completed.InputTokens != 4 || completed.OutputTokens != 2 {
		t.Fatalf("usage=%#v", completed)
	}
	if len(events) < 2 {
		t.Fatalf("canonical events=%#v", events)
	}
}

func TestTranslateGeminiSSEEmitsIncrementalOpenAIChunks(t *testing.T) {
	body := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"one\"}]}}]}\n\ndata: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\" two\"}]},\"finishReason\":\"MAX_TOKENS\"}],\"usageMetadata\":{\"promptTokenCount\":3,\"candidatesTokenCount\":5}}\n\n"
	recorder := httptest.NewRecorder()
	var completed kernel.UsageEvent
	err := NewAdapter().RenderResponse(context.Background(), kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}, recorder, normalize.FormatOpenAIChat, kernel.StreamHooks{Streaming: true, OnComplete: func(event kernel.UsageEvent) { completed = event }})
	if err != nil {
		t.Fatal(err)
	}
	output := recorder.Body.String()
	if !strings.Contains(output, "data: [DONE]") {
		t.Fatalf("OpenAI stream missing terminal marker: %q", output)
	}
	var textChunks, sawLength bool
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
			continue
		}
		var chunk openAIChatChunk
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk); err != nil {
			t.Fatal(err)
		}
		choice := chunk.Choices[0]
		if choice.Delta.Content != nil && (*choice.Delta.Content == "one" || *choice.Delta.Content == " two") {
			textChunks = true
		}
		if choice.FinishReason != nil && *choice.FinishReason == "length" {
			sawLength = true
		}
	}
	if !textChunks || !sawLength {
		t.Fatalf("typed OpenAI SSE chunks did not preserve content and finish reason: %q", output)
	}
	if completed.InputTokens != 3 || completed.OutputTokens != 5 {
		t.Fatalf("usage=%#v", completed)
	}
}

func TestTranslateGeminiSSERejectsMalformedAndTruncatedStreams(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "malformed event", body: "data: {not-json}\n\n"},
		{name: "missing terminal candidate reason", body: "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"partial\"}]}}]}\n\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			var callbackErr error
			err := NewAdapter().RenderResponse(context.Background(), kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(test.body))}, recorder, normalize.FormatOpenAIChat, kernel.StreamHooks{OnError: func(err error) { callbackErr = err }})
			if err == nil || callbackErr == nil {
				t.Fatalf("expected stream failure, returned=%v callback=%v", err, callbackErr)
			}
			if strings.Contains(recorder.Body.String(), "data: [DONE]") {
				t.Fatalf("failed stream was marked complete: %q", recorder.Body.String())
			}
		})
	}
}

func TestTranslateGeminiJSONRejectsMissingCandidates(t *testing.T) {
	response := kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"promptFeedback":{"blockReason":"SAFETY"}}`))}
	if err := NewAdapter().RenderResponse(context.Background(), response, httptest.NewRecorder(), normalize.FormatOpenAIChat, kernel.StreamHooks{}); err == nil {
		t.Fatal("missing Gemini candidates were treated as a successful empty completion")
	}
}
