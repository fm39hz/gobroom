package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/adapter/usagewire"
	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

func TestPrepareMapsReasoningToGeminiThinkingConfig(t *testing.T) {
	request := normalize.Request{SourceFormat: normalize.FormatOpenAIChat, Stream: false, Messages: []normalize.Message{{Role: "user", Content: "hello"}}, Raw: map[string]any{"max_tokens": float64(123)}, Thinking: normalize.ThinkingIntent{Mode: "budget", BudgetTokens: 512}}
	prepared, err := (Gemini{}).Prepare(context.Background(), request, kernel.Route{ID: "r", BaseURL: "https://gemini.test", ExternalModel: "gemini-2", CredentialPresentation: kernel.CredentialPresentation{Header: "x-goog-api-key"}}, kernel.Credential{Secret: "key"})
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

func TestPrepareAcceptsTheChatBudgetDeclaredByCompatibility(t *testing.T) {
	input, err := normalize.JSON("/v1/chat/completions", http.Header{}, []byte(`{"model":"role","messages":[{"role":"user","content":"hello"}],"reasoning":{"max_output_tokens":2048},"max_tokens":4096}`))
	if err != nil {
		t.Fatal(err)
	}
	adapter := NewAdapter()
	plan := adapter.PlanCompatibility(kernel.CompatibilityContext{Request: input.Request, Policy: kernel.CompatibilityPolicy{RequiredFacets: kernel.RequiredRequestFacets(input.Request)}})
	if !plan.Supported {
		t.Fatalf("budget must be admitted by the declared codec contract: %#v", plan)
	}
	prepared, err := adapter.Prepare(t.Context(), input.Request, kernel.Route{BaseURL: "https://gemini.test", ExternalModel: "gemini-3-flash", CredentialPresentation: kernel.CredentialPresentation{Header: "x-goog-api-key"}}, kernel.Credential{Secret: "fixture-key"})
	if err != nil {
		t.Fatal(err)
	}
	if closer, ok := prepared.Body.(io.Closer); ok {
		defer closer.Close()
	}
	var body generateContentRequest
	if err := json.NewDecoder(prepared.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.GenerationConfig == nil || body.GenerationConfig.ThinkingConfig == nil || body.GenerationConfig.ThinkingConfig.ThinkingBudget == nil || *body.GenerationConfig.ThinkingConfig.ThinkingBudget != 2048 {
		t.Fatalf("admitted explicit budget was not encoded: %#v", body.GenerationConfig)
	}
}

func TestPreparePreservesEquivalentBudgetSpellings(t *testing.T) {
	for _, number := range []string{"2048", "2048.0", "2.048e3"} {
		payload, err := json.Marshal(map[string]any{"model": "role", "messages": []map[string]string{{"role": "user", "content": "hello"}}, "reasoning": map[string]any{"max_output_tokens": json.RawMessage(number)}, "max_tokens": json.RawMessage("4096.0")})
		if err != nil {
			t.Fatal(err)
		}
		input, err := normalize.JSON("/v1/chat/completions", http.Header{}, payload)
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := NewAdapter().Prepare(t.Context(), input.Request, kernel.Route{BaseURL: "https://gemini.test", ExternalModel: "gemini-3-flash", CredentialPresentation: kernel.CredentialPresentation{Header: "x-goog-api-key"}}, kernel.Credential{Secret: "fixture-key"})
		if err != nil {
			t.Fatalf("admitted integer spelling %s failed wire binding: %v", number, err)
		}
		var body generateContentRequest
		if err := json.NewDecoder(prepared.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if closer, ok := prepared.Body.(io.Closer); ok {
			_ = closer.Close()
		}
		if body.GenerationConfig == nil || body.GenerationConfig.MaxOutputTokens == nil || *body.GenerationConfig.MaxOutputTokens != 4096 || body.GenerationConfig.ThinkingConfig == nil || body.GenerationConfig.ThinkingConfig.ThinkingBudget == nil || *body.GenerationConfig.ThinkingConfig.ThinkingBudget != 2048 {
			t.Fatalf("budget spelling lost wire intent: %#v", body.GenerationConfig)
		}
	}
}

func TestGeminiCompatibilityPlanRejectsUnsupportedRequestFacetsBeforeDispatch(t *testing.T) {
	cases := []struct {
		name    string
		request normalize.Request
		facet   string
	}{
		{
			name:    "provider-scoped continuity",
			request: normalize.Request{Model: "gemini", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, SourceFormat: normalize.FormatOpenAIChat, Messages: []normalize.Message{{Role: "user", Content: "hello"}}, Continuity: normalize.ContinuityState{PreviousResponse: "resp_1"}},
			facet:   kernel.FacetContinuity,
		},
		{
			name:    "strict tool schema",
			request: normalize.Request{Model: "gemini", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, SourceFormat: normalize.FormatOpenAIChat, Tools: []normalize.Tool{{Type: "function", Function: map[string]any{"name": "lookup", "strict": true}}}},
			facet:   kernel.FacetToolDefinitions,
		},
		{
			name:    "remote image URL",
			request: normalize.Request{Model: "gemini", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, SourceFormat: normalize.FormatOpenAIChat, Messages: []normalize.Message{{Role: "user", Content: []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://images.example/image.png"}}}}}, Modalities: normalize.Modalities{Vision: true}},
			facet:   kernel.FacetVisionInput,
		},
		{
			name:    "unsupported generation option",
			request: normalize.Request{Model: "gemini", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, SourceFormat: normalize.FormatOpenAIChat, Raw: map[string]any{"model": "gemini", "messages": []any{}, "logprobs": true}},
			facet:   kernel.FacetGenerationOptions,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			context := kernel.CompatibilityContext{Request: test.request, Operation: normalize.OperationChatGenerate}
			context.Policy.RequiredFacets = append(kernel.RequiredRequestFacets(test.request), kernel.FacetWireResponse)
			plan := NewAdapter().PlanCompatibility(context)
			if plan.Supported || plan.Fidelity != kernel.FidelityUnsupported {
				t.Fatalf("unsupported request reached an eligible plan: %#v", plan)
			}
			for _, mapping := range plan.Mappings {
				if mapping.Facet == test.facet && mapping.Disposition == kernel.FacetUnsupported && mapping.Reason != "" {
					return
				}
			}
			t.Fatalf("plan omitted an explained rejection for facet %q: %#v", test.facet, plan)
		})
	}
}

func TestGeminiReasonsAboutBudgetSeparatelyFromEffort(t *testing.T) {
	request := normalize.Request{
		Model: "gemini", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1,
		SourceFormat: normalize.FormatOpenAIChat, Messages: []normalize.Message{{Role: "user", Content: "hello"}},
		Thinking: normalize.ThinkingIntent{Mode: "budget", BudgetTokens: 1024},
	}
	policy := kernel.CompatibilityPolicy{RequiredFacets: kernel.RequiredRequestFacets(request)}
	plan := NewAdapter().PlanCompatibility(kernel.CompatibilityContext{Request: request, Policy: policy})
	if !plan.Supported {
		t.Fatalf("Gemini rejected an exactly mapped token budget: %#v", plan)
	}
	found := false
	for _, mapping := range plan.Mappings {
		if mapping.Facet == kernel.FacetReasoningBudget {
			found = mapping.Disposition == kernel.FacetTranslated && strings.Contains(mapping.Reason, "exact reasoning token budget")
		}
	}
	if !found {
		t.Fatalf("compatibility plan did not declare exact budget translation: %#v", plan.Mappings)
	}
	request.Thinking.BudgetTokens = 0
	plan = NewAdapter().PlanCompatibility(kernel.CompatibilityContext{Request: request, Policy: kernel.CompatibilityPolicy{RequiredFacets: kernel.RequiredRequestFacets(request)}})
	if plan.Supported {
		t.Fatalf("missing required budget value was admitted: %#v", plan)
	}
	request.Thinking.BudgetTokens = 1024
	request.UnsupportedFacets = []string{kernel.FacetReasoningBudget}
	plan = NewAdapter().PlanCompatibility(kernel.CompatibilityContext{Request: request, Policy: kernel.CompatibilityPolicy{RequiredFacets: kernel.RequiredRequestFacets(request)}})
	if plan.Supported {
		t.Fatalf("codec reclaimed a normalization-rejected reasoning budget: %#v", plan)
	}
}

func TestKernelRejectsGeminiContinuityBeforePreparingUpstreamRequest(t *testing.T) {
	snapshot, err := kernel.BuildSnapshot(kernel.SnapshotInput{
		PublicModels: []kernel.PublicModel{{Name: "role", TargetRef: "role"}},
		Nodes:        []kernel.ModelNode{{ID: "role", Kind: kernel.ModelCombo, Strategy: kernel.StrategyFallback, Members: []kernel.MemberRef{{Kind: kernel.MemberRoute, ID: "gemini-route"}}}},
		Routes: []kernel.Route{{ID: "gemini-route", Enabled: true, BaseURL: "https://must-not-be-called.invalid", ExternalModel: "gemini-test", OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{
			normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"gemini"}},
		}}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := kernel.New(snapshot, nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	adapter := NewAdapter()
	gateway.Adapters[adapter.ID()] = adapter
	request := normalize.Request{
		Model: "role", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1,
		SourceFormat: normalize.FormatOpenAIChat, Messages: []normalize.Message{{Role: "user", Content: "hello"}},
		Continuity: normalize.ContinuityState{PreviousResponse: "resp_1"},
	}
	err = gateway.Execute(context.Background(), request, kernel.Credential{}, httptest.NewRecorder())
	if !errors.Is(err, kernel.ErrNoRoute) {
		t.Fatalf("continuity-incompatible Gemini route was not rejected during admission: %v", err)
	}
}

func TestPrepareTranslatesChatHistoryToolsAndStreamEndpoint(t *testing.T) {
	request := normalize.Request{
		Stream: true,
		Messages: []normalize.Message{
			{Role: "system", Content: "be concise"},
			{Role: "user", Content: "search this"},
			{Role: "assistant", ToolCalls: []normalize.ToolCall{{ID: "call-1", Name: "search", Arguments: map[string]any{"q": "x"}, Signature: "sig", SignatureIssuer: extensions.Ref{Kind: "provider-definition", ID: "gemini", ContractVersion: 1}}}},
			{Role: "tool", ToolCallID: "call-1", Content: `{"ok":true}`},
		},
		Tools: []normalize.Tool{{Type: "function", Function: map[string]any{"name": "search", "description": "search", "parameters": map[string]any{"type": "object"}}}},
		Raw:   map[string]any{"tool_choice": "required"},
	}
	prepared, err := (Gemini{}).Prepare(context.Background(), request, kernel.Route{DefinitionRef: extensions.Ref{Kind: "provider-definition", ID: "gemini", ContractVersion: 1}, BaseURL: "https://gemini.test", ExternalModel: "gemini-3"}, kernel.Credential{})
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

func TestGeminiSignatureHistoryRequiresExactSessionIssuerAttestation(t *testing.T) {
	issuer := extensions.Ref{Kind: "provider-definition", ID: "gemini", ContractVersion: 1}
	otherIssuer := extensions.Ref{Kind: "provider-definition", ID: "other", ContractVersion: 1}
	for _, test := range []struct {
		name   string
		ref    extensions.Ref
		wantOK bool
	}{
		{name: "attested exact issuer", ref: issuer, wantOK: true},
		{name: "missing issuer proof", wantOK: false},
		{name: "different provider issuer", ref: otherIssuer, wantOK: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := normalize.Request{SourceFormat: normalize.FormatOpenAIChat, Messages: []normalize.Message{{Role: "assistant", ToolCalls: []normalize.ToolCall{{ID: "call-1", Name: "lookup", Arguments: map[string]any{}, Signature: "opaque-sig", SignatureIssuer: test.ref}}}}}
			policy := kernel.CompatibilityPolicy{RequiredFacets: kernel.RequiredRequestFacets(request)}
			plan := NewAdapter().PlanCompatibility(kernel.CompatibilityContext{Request: request, Route: kernel.Route{DefinitionRef: issuer}, Policy: policy})
			if plan.Supported != test.wantOK {
				t.Fatalf("signature admission supported=%v want %v: %#v", plan.Supported, test.wantOK, plan)
			}
		})
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
	if len(parts) == 0 || parts[0].ThoughtSignature != "opaque-signature" || result.Request.Messages[1].ToolCalls[0].Signature != "opaque-signature" {
		t.Fatalf("signature did not survive normalization: %#v", parts)
	}
}

func TestTranslateGeminiJSONToOpenAIChat(t *testing.T) {
	body := `{"candidates":[{"content":{"parts":[{"text":"hello"},{"functionCall":{"name":"lookup","args":{"id":7},"id":"call-7"},"thoughtSignature":"sig"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":2,"thoughtsTokenCount":0}}`
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
	if !geminiCountIs(completed.InputTokens, 4) || !geminiCountIs(completed.OutputTokens, 2) {
		t.Fatalf("usage=%#v", completed)
	}
	if len(events) < 2 {
		t.Fatalf("canonical events=%#v", events)
	}
}

func TestTranslateGeminiSSEEmitsIncrementalOpenAIChunks(t *testing.T) {
	body := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"one\"}]}}]}\n\ndata: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\" two\"}]},\"finishReason\":\"MAX_TOKENS\"}],\"usageMetadata\":{\"promptTokenCount\":3,\"candidatesTokenCount\":5,\"thoughtsTokenCount\":0}}\n\n"
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
	if !geminiCountIs(completed.InputTokens, 3) || !geminiCountIs(completed.OutputTokens, 5) {
		t.Fatalf("usage=%#v", completed)
	}
}

func geminiCountIs(value *int64, want int64) bool {
	return value != nil && *value == want
}

func TestGeminiUsagePreservesMissingFieldsAndExplicitZero(t *testing.T) {
	var metadata geminiUsageMetadata
	if err := json.Unmarshal([]byte(`{"promptTokenCount":0}`), &metadata); err != nil {
		t.Fatal(err)
	}
	if !geminiCountIs(metadata.PromptTokenCount.Value, 0) || metadata.CandidatesTokenCount.Value != nil {
		t.Fatalf("typed usage metadata=%#v", metadata)
	}
	// Native patches accumulate before canonical full snapshots are derived.
	state := (geminiUsageState{}).merge(&geminiUsageMetadata{
		PromptTokenCount: usagewire.Counter{Value: new(int64(7))}, CandidatesTokenCount: usagewire.Counter{Value: new(int64(3))}, ThoughtsTokenCount: usagewire.Counter{Value: new(int64(0))},
	})
	merged := state.merge(&metadata).snapshot()
	if !geminiCountIs(merged.InputTokens, 0) || !geminiCountIs(merged.OutputTokens, 3) {
		t.Fatalf("merged usage=%#v", merged)
	}
	standalone := geminiUsage(&metadata)
	if standalone.OutputTokens != nil || standalone.UpdateKind != kernel.UsageFullSnapshot {
		t.Fatalf("standalone snapshot invented absent output: %#v", standalone)
	}
}

func TestGeminiUsageFractionalCountIsUnknownWithIssue(t *testing.T) {
	var metadata geminiUsageMetadata
	if err := json.Unmarshal([]byte(`{"promptTokenCount":1.5,"candidatesTokenCount":7,"thoughtsTokenCount":0}`), &metadata); err != nil {
		t.Fatal(err)
	}
	usage := geminiUsage(&metadata)
	if usage.InputTokens != nil || !geminiCountIs(usage.OutputTokens, 7) || len(usage.AccountingIssues) != 1 || usage.AccountingIssues[0].Code != "invalid_usage_counter" {
		t.Fatalf("fractional count must be unknown with an issue, preserving valid siblings: %#v", usage)
	}
}

func TestGeminiSSEUsageMergesPartialSnapshotsAndKnownZero(t *testing.T) {
	body := "data: {\"usageMetadata\":{\"promptTokenCount\":8,\"candidatesTokenCount\":2,\"thoughtsTokenCount\":0}}\n\n" +
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"done\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"candidatesTokenCount\":0}}\n\n"
	var completed kernel.UsageEvent
	err := (Gemini{}).DecodeResponse(context.Background(), kernel.UpstreamResponse{
		Status: http.StatusOK, Headers: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body)),
	}, func(kernel.ResponseEvent) error { return nil }, kernel.StreamHooks{OnComplete: func(usage kernel.UsageEvent) { completed = usage }})
	if err != nil {
		t.Fatal(err)
	}
	if !geminiCountIs(completed.InputTokens, 8) || !geminiCountIs(completed.OutputTokens, 0) {
		t.Fatalf("merged SSE usage=%#v", completed)
	}
}

func TestGeminiSSEClientAndProviderReceiveSameMergedUsageSnapshots(t *testing.T) {
	body := "data: {\"usageMetadata\":{\"promptTokenCount\":8,\"candidatesTokenCount\":2,\"thoughtsTokenCount\":0}}\n\n" +
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"done\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"candidatesTokenCount\":5}}\n\n"
	var providerSnapshots, clientSnapshots []kernel.UsageEvent
	var completed kernel.UsageEvent
	err := (Gemini{}).DecodeResponse(context.Background(), kernel.UpstreamResponse{
		Status: http.StatusOK, Headers: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body)),
	}, func(event kernel.ResponseEvent) error {
		if event.Kind == kernel.EventUsage && event.Usage != nil {
			clientSnapshots = append(clientSnapshots, event.Usage.Clone())
		}
		return nil
	}, kernel.StreamHooks{
		OnProviderUsage: func(event kernel.UsageEvent) { providerSnapshots = append(providerSnapshots, event.Clone()) },
		OnComplete:      func(event kernel.UsageEvent) { completed = event },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(providerSnapshots) != 2 || len(clientSnapshots) != 2 {
		t.Fatalf("usage-only and candidate frames must both emit client/provider snapshots: client=%#v provider=%#v", clientSnapshots, providerSnapshots)
	}
	for index := range providerSnapshots {
		if !geminiCountIs(providerSnapshots[index].InputTokens, 8) || !geminiCountIs(clientSnapshots[index].InputTokens, 8) ||
			!geminiCountIs(providerSnapshots[index].OutputTokens, []int64{2, 5}[index]) || !geminiCountIs(clientSnapshots[index].OutputTokens, []int64{2, 5}[index]) {
			t.Fatalf("client/provider usage snapshot %d diverged: client=%#v provider=%#v", index, clientSnapshots[index], providerSnapshots[index])
		}
	}
	if !geminiCountIs(completed.InputTokens, 8) || !geminiCountIs(completed.OutputTokens, 5) {
		t.Fatalf("completion usage=%#v", completed)
	}
}

func TestGeminiUsageIncludesReportedThoughtsInCanonicalOutput(t *testing.T) {
	var metadata geminiUsageMetadata
	if err := json.Unmarshal([]byte(`{"promptTokenCount":11,"candidatesTokenCount":6,"totalTokenCount":20,"cachedContentTokenCount":4,"thoughtsTokenCount":3}`), &metadata); err != nil {
		t.Fatal(err)
	}
	usage := geminiUsage(&metadata)
	if !geminiCountIs(usage.InputTokens, 11) || !geminiCountIs(usage.OutputTokens, 9) ||
		!geminiCountIs(usage.CachedReadTokens, 4) || !geminiCountIs(usage.ReasoningTokens, 3) {
		t.Fatalf("Gemini canonical usage must include reported thoughts in output while cache remains an input subset: %#v", usage)
	}

	// With no thought count, a matching reported total proves there are no
	// additional output components beyond candidatesTokenCount.
	var provenOutput geminiUsageMetadata
	if err := json.Unmarshal([]byte(`{"promptTokenCount":11,"candidatesTokenCount":6,"totalTokenCount":17}`), &provenOutput); err != nil {
		t.Fatal(err)
	}
	usage = geminiUsage(&provenOutput)
	if !geminiCountIs(usage.OutputTokens, 6) || usage.ReasoningTokens != nil {
		t.Fatalf("matching total should prove candidate-only output without inventing reasoning usage: %#v", usage)
	}
	var ambiguousTotal geminiUsageMetadata
	if err := json.Unmarshal([]byte(`{"promptTokenCount":11,"candidatesTokenCount":6,"totalTokenCount":20}`), &ambiguousTotal); err != nil {
		t.Fatal(err)
	}
	usage = geminiUsage(&ambiguousTotal)
	if usage.OutputTokens != nil || !geminiCountIs(ambiguousTotal.CandidatesTokenCount.Value, 6) {
		t.Fatalf("mismatched aggregate total must remain unproven while retaining candidates: %#v / %#v", usage, ambiguousTotal)
	}

	// An explicitly reported zero is known and supports candidate-only output,
	// even when no total was included.
	var knownNonThinking geminiUsageMetadata
	if err := json.Unmarshal([]byte(`{"promptTokenCount":11,"candidatesTokenCount":6,"thoughtsTokenCount":0}`), &knownNonThinking); err != nil {
		t.Fatal(err)
	}
	usage = geminiUsage(&knownNonThinking)
	if !geminiCountIs(usage.OutputTokens, 6) || !geminiCountIs(usage.ReasoningTokens, 0) {
		t.Fatalf("explicit zero thoughts should preserve known candidate output: %#v", usage)
	}

	// Without thoughts or a total that accounts for all output components,
	// preserve the known candidate field but leave canonical output unknown.
	var partial geminiUsageMetadata
	if err := json.Unmarshal([]byte(`{"promptTokenCount":11,"candidatesTokenCount":6,"cachedContentTokenCount":4}`), &partial); err != nil {
		t.Fatal(err)
	}
	usage = geminiUsage(&partial)
	if !geminiCountIs(usage.InputTokens, 11) || usage.OutputTokens != nil || usage.ReasoningTokens != nil || !geminiCountIs(usage.CachedReadTokens, 4) || !geminiCountIs(partial.CandidatesTokenCount.Value, 6) {
		t.Fatalf("unproven Gemini output must remain unknown while valid fields survive: %#v", usage)
	}
}

func TestGeminiToolCallWithoutProviderIDGetsStableResponseIdentity(t *testing.T) {
	issuer := extensions.Ref{Kind: "provider-definition", ID: "gemini", ContractVersion: 1}
	payload := geminiGenerateContentResponse{Candidates: []geminiCandidate{{Content: geminiContent{Parts: []geminiPart{{FunctionCall: &geminiFunctionCall{Name: "lookup", Args: json.RawMessage(`{"q":"x"}`)}}}}}}}
	response, events, _ := openAIResponse(payload, issuer)
	if len(response.Choices) != 1 || len(response.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("translated Gemini response=%#v", response)
	}
	id := response.Choices[0].Message.ToolCalls[0].ID
	if id == "" || !strings.HasPrefix(id, "call_gemini_") {
		t.Fatalf("Gemini tool call lacks synthesized OpenAI correlation ID: %q", id)
	}
	if len(events) != 1 || events[0].Kind != kernel.EventToolCallDelta || events[0].ToolCallID != id || events[0].ToolName != "lookup" {
		t.Fatalf("canonical tool event diverges from rendered call identity: %#v", events)
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
