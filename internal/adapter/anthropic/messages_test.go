package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

type nativeAnthropicRequestTransform struct{}

func (nativeAnthropicRequestTransform) Definition() kernel.TransformDefinition {
	return kernel.TransformDefinition{Ref: extensions.Ref{Kind: kernel.RequestTransformKind, ID: "fixture.native-anthropic", ContractVersion: 1}, ImplementationVersion: "1", Label: "Native Anthropic fixture", Description: "Mutate typed native Anthropic facets.", Stage: kernel.TransformBeforeRequirements, Effects: []kernel.TransformEffect{kernel.TransformPrompt, kernel.TransformInput, kernel.TransformTools, kernel.TransformThinking, kernel.TransformOptions}}
}

func (nativeAnthropicRequestTransform) Apply(_ context.Context, request *kernel.NormalizedRequest, _ json.RawMessage) error {
	request.Prompt.Layers[0].Parts[0].Text = "new system policy"
	for index := range request.Messages {
		message := &request.Messages[index]
		switch message.Role {
		case "user":
			if message.ToolCallID != "" {
				message.Content = "new result"
			} else if len(message.ToolCalls) > 0 {
				message.Content = "new assistant text"
				message.ToolCalls[0].Arguments.(map[string]any)["q"] = "updated"
			} else {
				message.Content = "new user text"
			}
		case "assistant":
			message.Content = "new assistant text"
			message.ToolCalls[0].Arguments.(map[string]any)["q"] = "updated"
		case "tool":
			message.Content = "new result"
		}
	}
	request.Tools[0].Name = "lookup_v2"
	request.Tools[0].Function["name"] = "lookup_v2"
	request.Tools[0].Function["description"] = "updated tool"
	request.ToolChoice.Name = "lookup_v2"
	maxTokens, temperature, topP := 2048, 0.6, 0.9
	request.Generation.MaxOutputTokens, request.Generation.Temperature, request.Generation.TopP = &maxTokens, &temperature, &topP
	request.Generation.StopSequences = []string{"new stop"}
	request.Thinking.BudgetTokens = 2048
	return nil
}

func TestAnthropicToolAndTextEventsBecomeOpenAIChunks(t *testing.T) {
	body := strings.NewReader("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":12}}}\n\ndata: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_1\",\"name\":\"search\"}}\n\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"partial_json\":\"{}\"}}\n\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"done\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":4}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
	recorder := httptest.NewRecorder()
	var usage kernel.UsageEvent
	var events []kernel.ResponseEvent
	if err := NewAdapter().RenderResponse(context.Background(), kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(body)}, recorder, normalize.FormatOpenAIChat, kernel.StreamHooks{Streaming: true, OnEvent: func(event kernel.ResponseEvent) { events = append(events, event) }, OnComplete: func(event kernel.UsageEvent) { usage = event }}); err != nil {
		t.Fatal(err)
	}
	output := recorder.Body.String()
	if !strings.Contains(output, "tool_calls") || !strings.Contains(output, "done") || !strings.Contains(output, "tool_calls") {
		t.Fatalf("output=%s", output)
	}
	if usage.InputTokens != 12 || usage.OutputTokens != 4 {
		t.Fatalf("usage=%#v", usage)
	}
	seenText, seenTool, seenUsage, seenStart, seenEnd := false, false, false, false, false
	for _, event := range events {
		seenText = seenText || event.Kind == kernel.EventTextDelta
		seenTool = seenTool || event.Kind == kernel.EventToolCallDelta
		seenUsage = seenUsage || event.Kind == kernel.EventUsage
		seenStart = seenStart || event.Kind == kernel.EventContentBlockStart
		seenEnd = seenEnd || event.Kind == kernel.EventContentBlockEnd
	}
	if !seenText || !seenTool || !seenUsage || !seenStart || !seenEnd {
		t.Fatalf("canonical events=%#v", events)
	}
}

func TestAnthropicPassthroughJSONPreservesBodyAndReportsUsage(t *testing.T) {
	body := `{"id":"msg_live","type":"message","model":"claude-opus-4-8","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn","usage":{"input_tokens":10370,"output_tokens":4}}`
	recorder := httptest.NewRecorder()
	var usage kernel.UsageEvent
	err := NewAdapter().RenderResponse(context.Background(), kernel.UpstreamResponse{
		Status:  http.StatusOK,
		Headers: http.Header{"Content-Type": []string{"application/json"}},
		Body:    io.NopCloser(strings.NewReader(body)),
	}, recorder, normalize.FormatAnthropic, kernel.StreamHooks{OnComplete: func(event kernel.UsageEvent) { usage = event }})
	if err != nil {
		t.Fatal(err)
	}
	if recorder.Body.String() != body {
		t.Fatalf("Anthropic passthrough body changed: %s", recorder.Body.String())
	}
	if usage.Status != "ok" || usage.InputTokens != 10370 || usage.OutputTokens != 4 {
		t.Fatalf("usage=%#v", usage)
	}
}

func TestAnthropicPassthroughStreamReportsUsageWithoutChangingEvents(t *testing.T) {
	body := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":21}}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":7}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	recorder := httptest.NewRecorder()
	var usage kernel.UsageEvent
	err := NewAdapter().RenderResponse(context.Background(), kernel.UpstreamResponse{
		Status:  http.StatusOK,
		Headers: http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:    io.NopCloser(strings.NewReader(body)),
	}, recorder, normalize.FormatAnthropic, kernel.StreamHooks{Streaming: true, OnComplete: func(event kernel.UsageEvent) { usage = event }})
	if err != nil {
		t.Fatal(err)
	}
	if recorder.Body.String() != body {
		t.Fatalf("Anthropic SSE passthrough body changed: %s", recorder.Body.String())
	}
	if usage.Status != "ok" || usage.InputTokens != 21 || usage.OutputTokens != 7 {
		t.Fatalf("usage=%#v", usage)
	}
}

func TestAnthropicDecoderEmitsThinkingSignatureAsCanonicalEvent(t *testing.T) {
	body := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_signed\",\"usage\":{\"input_tokens\":3}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\",\"signature\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"reasoning\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"signed-by-provider\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":4}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	var seenThinking, seenSignature bool
	response := kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
	if err := NewAdapter().RenderResponse(context.Background(), response, httptest.NewRecorder(), normalize.FormatOpenAIChat, kernel.StreamHooks{Streaming: true, OnEvent: func(event kernel.ResponseEvent) {
		seenThinking = seenThinking || event.Kind == kernel.EventThinkingDelta && event.Text == "reasoning" && event.Index == 0
		seenSignature = seenSignature || event.Kind == kernel.EventThinkingSignature && event.Signature == "signed-by-provider" && event.Index == 0
	}}); err != nil {
		t.Fatal(err)
	}
	if !seenThinking || !seenSignature {
		t.Fatalf("Anthropic decoder lost thinking/signature events: thinking=%v signature=%v", seenThinking, seenSignature)
	}
}

func TestAnthropicPrepareTranslatesReasoningWithoutMetadataShim(t *testing.T) {
	request := normalize.Request{SourceFormat: normalize.FormatOpenAIChat, Raw: map[string]any{"messages": []any{}}, Thinking: normalize.ThinkingIntent{Mode: "level", Effort: "high"}}
	prepared, err := (Messages{}).Prepare(context.Background(), request, kernel.Route{ID: "route", BaseURL: "https://provider.test", ExternalModel: "claude"}, kernel.Credential{})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.NewDecoder(prepared.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["metadata"]; ok {
		t.Fatalf("metadata shim remains: %#v", body)
	}
	thinking, ok := body["thinking"].(map[string]any)
	if !ok || thinking["type"] != "adaptive" {
		t.Fatalf("thinking=%#v", body["thinking"])
	}
	output, ok := body["output_config"].(map[string]any)
	if !ok || output["effort"] != "high" {
		t.Fatalf("output_config=%#v", body["output_config"])
	}
}

func TestAnthropicRequestMapsPromptPlanToTopLevelSystem(t *testing.T) {
	request := normalize.Request{SourceFormat: normalize.FormatOpenAIChat, Prompt: normalize.PromptPlan{Layers: []normalize.PromptLayer{{Origin: normalize.PromptInline, Role: "system", Text: "policy"}}}, Messages: []normalize.Message{{Role: "user", Content: "hello"}}}
	prepared, err := (Messages{}).Prepare(context.Background(), request, kernel.Route{BaseURL: "https://provider.test", ExternalModel: "claude"}, kernel.Credential{})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.NewDecoder(prepared.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	system, ok := body["system"].([]any)
	if !ok || len(system) != 1 || system[0].(map[string]any)["text"] != "policy" {
		t.Fatalf("system=%#v", body["system"])
	}
}

func TestNativeAnthropicEgressOverlaysTypedFacetsAndPreservesBlocks(t *testing.T) {
	parsed, err := normalize.JSON("/v1/messages", http.Header{}, []byte(`{
"model":"role","max_tokens":512,"temperature":0.2,"top_p":0.3,"stop_sequences":["old stop"],
"thinking":{"type":"enabled","budget_tokens":1024},
"system":[{"type":"text","text":"old system","cache_control":{"type":"ephemeral"}}],
"tools":[{"name":"lookup","description":"old tool","input_schema":{"type":"object"},"vendor_tool":"keep"}],
"tool_choice":{"type":"tool","name":"lookup","disable_parallel_tool_use":true,"vendor_choice":"keep"},
"messages":[
 {"role":"user","content":[{"type":"text","text":"old user","cache_control":{"type":"ephemeral"}}]},
 {"role":"assistant","content":[{"type":"text","text":"old assistant"},{"type":"tool_use","id":"call_1","name":"lookup","input":{"q":"old"}}]},
 {"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"old result"}]},
 {"role":"assistant","content":[{"type":"vendor_custom_block","payload":{"opaque":"keep"}}]}
]}`))
	if err != nil {
		t.Fatal(err)
	}
	registry := kernel.NewRequestTransformRegistry()
	transform := nativeAnthropicRequestTransform{}
	if err := registry.Register(transform); err != nil {
		t.Fatal(err)
	}
	if err := registry.ApplyScopes(context.Background(), &parsed.Request, []kernel.TransformBinding{{ID: "native-anthropic", TransformRef: transform.Definition().Ref, Enabled: true, Scope: kernel.TransformScope{Kind: kernel.TransformScopeDaemon}}}, kernel.TransformScope{Kind: kernel.TransformScopeDaemon}); err != nil {
		t.Fatal(err)
	}
	adapter := NewAdapter().(kernel.ComposedAdapter)
	policy := kernel.CompatibilityPolicy{RequiredFacets: kernel.RequiredRequestFacets(parsed.Request)}
	plan := adapter.PlanCompatibility(kernel.CompatibilityContext{Request: parsed.Request, Policy: policy})
	if !plan.Supported {
		t.Fatalf("native Anthropic transform overlay was not admitted: %#v", plan)
	}
	prepared, err := (Messages{}).Prepare(context.Background(), parsed.Request, kernel.Route{BaseURL: "https://provider.test/v1", ExternalModel: "claude"}, kernel.Credential{})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.NewDecoder(prepared.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["max_tokens"] != float64(2048) || body["temperature"] != float64(0.6) || body["top_p"] != float64(0.9) || !reflect.DeepEqual(body["stop_sequences"], []any{"new stop"}) {
		t.Fatalf("Anthropic generation fields were stale: %#v", body)
	}
	if thinking, ok := body["thinking"].(map[string]any); !ok || thinking["type"] != "enabled" || thinking["budget_tokens"] != float64(2048) {
		t.Fatalf("Anthropic thinking was stale: %#v", body["thinking"])
	}
	system := body["system"].([]any)[0].(map[string]any)
	if system["text"] != "new system policy" || system["cache_control"].(map[string]any)["type"] != "ephemeral" {
		t.Fatalf("Anthropic system block lost transformed/opaque fields: %#v", system)
	}
	tools := body["tools"].([]any)
	if tools[0].(map[string]any)["description"] != "updated tool" || tools[0].(map[string]any)["vendor_tool"] != "keep" {
		t.Fatalf("Anthropic tool overlay lost metadata: %#v", tools)
	}
	choice := body["tool_choice"].(map[string]any)
	if choice["name"] != "lookup_v2" || choice["vendor_choice"] != "keep" {
		t.Fatalf("Anthropic tool-choice overlay lost metadata: %#v", choice)
	}
	messages := body["messages"].([]any)
	if len(messages) != 4 || messages[3].(map[string]any)["content"].([]any)[0].(map[string]any)["type"] != "vendor_custom_block" {
		t.Fatalf("opaque unsupported message block was not forwarded unchanged: %#v", messages)
	}
	userContent := messages[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	assistantContent := messages[1].(map[string]any)["content"].([]any)
	toolUse := assistantContent[1].(map[string]any)
	toolResult := messages[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if userContent["text"] != "new user text" || userContent["cache_control"].(map[string]any)["type"] != "ephemeral" || assistantContent[0].(map[string]any)["text"] != "new assistant text" || toolUse["input"].(map[string]any)["q"] != "updated" || toolResult["content"] != "new result" {
		t.Fatalf("Anthropic message block overlay lost content/order/provenance: %#v", messages)
	}
}

func TestNativeAnthropicOverlayRejectsReorderedSourceMessages(t *testing.T) {
	parsed, err := normalize.JSON("/v1/messages", http.Header{}, []byte(`{"model":"role","max_tokens":32,"messages":[{"role":"user","content":"first"},{"role":"assistant","content":"second"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	messages := parsed.Request.Messages
	messages[0], messages[1] = messages[1], messages[0]
	_, err = overlayAnthropicMessages(parsed.Request.Raw["messages"], messages)
	if err == nil || !strings.Contains(err.Error(), "message reorder cannot be safely overlaid") {
		t.Fatalf("reordered Anthropic source messages were not rejected explicitly: %v", err)
	}
}

func TestNativeAnthropicOverlayAppendsTypedMessagesAndToolResults(t *testing.T) {
	parsed, err := normalize.JSON("/v1/messages", http.Header{}, []byte(`{"model":"role","max_tokens":32,"messages":[{"role":"user","content":[{"type":"text","text":"keep"},{"type":"vendor_custom_block","payload":{"opaque":"keep"}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	messages := append([]normalize.Message(nil), parsed.Request.Messages...)
	messages = append(messages,
		normalize.Message{Role: "assistant", Content: "new assistant text", ToolCalls: []normalize.ToolCall{{ID: "call-new", Type: "function", Name: "lookup", Arguments: map[string]any{"q": "x"}}}},
		normalize.Message{Role: "tool", ToolCallID: "call-new", Content: "new tool result"},
	)
	updated, err := overlayAnthropicMessages(parsed.Request.Raw["messages"], messages)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated) != 3 {
		t.Fatalf("message count=%d want original plus two additions: %#v", len(updated), updated)
	}
	original := updated[0].(map[string]any)
	blocks := original["content"].([]any)
	if blocks[1].(map[string]any)["type"] != "vendor_custom_block" {
		t.Fatalf("opaque source block moved or changed: %#v", original)
	}
	assistant := updated[1].(map[string]any)
	assistantBlocks := assistant["content"].([]any)
	if assistant["role"] != "assistant" || assistantBlocks[0].(map[string]any)["text"] != "new assistant text" || assistantBlocks[1].(map[string]any)["type"] != "tool_use" {
		t.Fatalf("new assistant/tool-use message was not projected: %#v", assistant)
	}
	toolResult := updated[2].(map[string]any)
	resultBlock := toolResult["content"].([]any)[0].(map[string]any)
	if toolResult["role"] != "user" || resultBlock["type"] != "tool_result" || resultBlock["tool_use_id"] != "call-new" || resultBlock["content"] != "new tool result" {
		t.Fatalf("new tool result was not projected into Anthropic user content: %#v", toolResult)
	}
}

func TestNativeAnthropicOverlayRejectsUnrepresentableAddedContent(t *testing.T) {
	parsed, err := normalize.JSON("/v1/messages", http.Header{}, []byte(`{"model":"role","max_tokens":32,"messages":[{"role":"user","content":"original"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	messages := append([]normalize.Message(nil), parsed.Request.Messages...)
	messages = append(messages, normalize.Message{Role: "assistant", Content: []normalize.ContentPart{{Type: "thinking", Text: "unsigned"}}})
	if _, err := overlayAnthropicMessages(parsed.Request.Raw["messages"], messages); err == nil || !strings.Contains(err.Error(), "no native insertion mapping") {
		t.Fatalf("unrepresentable added thinking content was not rejected: %v", err)
	}
}

func TestNativeAnthropicOverlayRejectsAdditionsInsertedBetweenSourceMessages(t *testing.T) {
	parsed, err := normalize.JSON("/v1/messages", http.Header{}, []byte(`{"model":"role","max_tokens":32,"messages":[{"role":"user","content":"first"},{"role":"assistant","content":"second"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	messages := []normalize.Message{parsed.Request.Messages[0], {Role: "assistant", Content: "inserted"}, parsed.Request.Messages[1]}
	if _, err := overlayAnthropicMessages(parsed.Request.Raw["messages"], messages); err == nil || !strings.Contains(err.Error(), "must form a suffix") {
		t.Fatalf("middle insertion was not rejected explicitly: %v", err)
	}
}
