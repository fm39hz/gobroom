package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	egress "github.com/fm39hz/gobroom/internal/adapter/renderers"
	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

type nativeChatRequestTransform struct{}

func (nativeChatRequestTransform) Definition() kernel.TransformDefinition {
	return kernel.TransformDefinition{Ref: extensions.Ref{Kind: kernel.RequestTransformKind, ID: "fixture.native-chat-options", ContractVersion: 1}, ImplementationVersion: "1", Label: "Native Chat options", Description: "Change typed options and tools for native egress.", Stage: kernel.TransformBeforeRequirements, Effects: []kernel.TransformEffect{kernel.TransformTools, kernel.TransformOptions}}
}

func (nativeChatRequestTransform) Apply(_ context.Context, request *kernel.NormalizedRequest, _ json.RawMessage) error {
	maxTokens, temperature, topP := 64, 0.7, 0.8
	request.Generation.MaxOutputTokens = &maxTokens
	request.Generation.Temperature = &temperature
	request.Generation.TopP = &topP
	request.Generation.StopSequences = []string{"done"}
	toolMetadata := request.Tools[0].Metadata
	request.Tools = []normalize.Tool{{Type: "function", Name: "lookup", Function: map[string]any{"name": "lookup", "parameters": map[string]any{"type": "object"}}, Metadata: toolMetadata}}
	choiceMetadata := request.ToolChoice.Metadata
	request.ToolChoice = normalize.ToolChoice{Mode: "tool", Name: "lookup", Set: true, Metadata: choiceMetadata}
	return nil
}

func TestChatAdapterForwardsAndPassthroughsSSE(t *testing.T) {
	adapter := NewAdapter()
	request := normalize.Request{Model: "public", SourceFormat: normalize.FormatOpenAIChat, Stream: true, Raw: map[string]any{"model": "public", "messages": []any{map[string]any{"role": "user", "content": "hi"}}}}
	route := kernel.Route{ID: "route:test", BaseURL: "https://provider.example/v1", ExternalModel: "upstream-model", Enabled: true}
	prepared, err := adapter.Prepare(context.Background(), request, route, kernel.Credential{Secret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.URL != "https://provider.example/v1/chat/completions" || prepared.Headers.Get("Authorization") != "Bearer secret" {
		t.Fatalf("prepared=%#v", prepared)
	}
	response := kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))}
	recorder := httptest.NewRecorder()
	if err := adapter.RenderResponse(context.Background(), response, recorder, normalize.FormatOpenAIChat, kernel.StreamHooks{Streaming: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(recorder.Body.String(), "data:") {
		t.Fatalf("body=%q", recorder.Body.String())
	}
}

func TestOpenAIChatDecoderRendersAnthropicMessagesFromSemanticEvents(t *testing.T) {
	composed, ok := NewAdapter().(kernel.ComposedAdapter)
	if !ok {
		t.Fatalf("OpenAI adapter type=%T", NewAdapter())
	}
	composed.Renderers[normalize.FormatAnthropic] = egress.AnthropicMessages{}
	body := `{"id":"chatcmpl_semantic","object":"chat.completion","model":"upstream","choices":[{"index":0,"message":{"role":"assistant","content":"Checking now.","tool_calls":[{"id":"call_123","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"weather\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":7,"completion_tokens":5,"total_tokens":12}}`
	response := kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
	writer := httptest.NewRecorder()
	var observed []kernel.ResponseEvent
	if err := composed.RenderResponse(context.Background(), response, writer, normalize.FormatAnthropic, kernel.StreamHooks{OnEvent: func(event kernel.ResponseEvent) { observed = append(observed, event) }}); err != nil {
		t.Fatal(err)
	}
	var message struct {
		ID      string           `json:"id"`
		Type    string           `json:"type"`
		Stop    string           `json:"stop_reason"`
		Usage   map[string]int64 `json:"usage"`
		Content []map[string]any `json:"content"`
	}
	if err := json.Unmarshal(writer.Body.Bytes(), &message); err != nil {
		t.Fatalf("decode Anthropic semantic response %q: %v", writer.Body.String(), err)
	}
	if message.ID != "chatcmpl_semantic" || message.Type != "message" || message.Stop != "tool_use" || len(message.Content) != 2 || message.Content[0]["text"] != "Checking now." {
		t.Fatalf("Anthropic response projection=%#v", message)
	}
	toolInput, ok := message.Content[1]["input"].(map[string]any)
	if !ok || toolInput["q"] != "weather" || message.Content[1]["id"] != "call_123" || message.Content[1]["name"] != "lookup" {
		t.Fatalf("Anthropic tool_use projection=%#v", message.Content[1])
	}
	if message.Usage["input_tokens"] != 7 || message.Usage["output_tokens"] != 5 {
		t.Fatalf("Anthropic usage projection=%#v", message.Usage)
	}
	seenText, seenTool := false, false
	for _, event := range observed {
		seenText = seenText || event.Kind == kernel.EventTextDelta
		seenTool = seenTool || event.Kind == kernel.EventToolCallDelta
	}
	if !seenText || !seenTool {
		t.Fatalf("OpenAI decoder did not produce canonical text/tool events: %#v", observed)
	}
}

func TestChatRequestReassemblesPromptPlanAtWireBoundary(t *testing.T) {
	request := normalize.Request{Model: "public", SourceFormat: normalize.FormatOpenAIChat, Prompt: normalize.PromptPlan{Layers: []normalize.PromptLayer{{Origin: normalize.PromptHarness, Role: "system", Text: "policy"}}}, Messages: []normalize.Message{{Role: "user", Content: "hello"}}}
	prepared, err := (Chat{}).Prepare(context.Background(), request, kernel.Route{BaseURL: "https://provider.test/v1", ExternalModel: "m"}, kernel.Credential{})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.NewDecoder(prepared.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	messages, _ := body["messages"].([]any)
	if len(messages) != 2 || messages[0].(map[string]any)["role"] != "system" || messages[1].(map[string]any)["role"] != "user" {
		t.Fatalf("wire messages=%#v", messages)
	}
}

func TestNativeChatMessageEncodesCanonicalTextAndImageParts(t *testing.T) {
	message := normalize.Message{Role: "user", Content: []normalize.ContentPart{
		{Type: "text", Text: "describe this"},
		{Type: "image", URL: "https://example.test/image.png"},
		{Type: "image", MediaType: "image/png", Data: "aGVsbG8="},
	}}
	encoded, err := nativeChatMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	parts, ok := encoded["content"].([]any)
	if !ok || len(parts) != 3 {
		t.Fatalf("content=%#v", encoded["content"])
	}
	text := parts[0].(map[string]any)
	urlImage := parts[1].(map[string]any)
	dataImage := parts[2].(map[string]any)
	if text["type"] != "text" || text["text"] != "describe this" {
		t.Fatalf("text part=%#v", text)
	}
	if urlImage["type"] != "image_url" || urlImage["image_url"].(map[string]any)["url"] != "https://example.test/image.png" {
		t.Fatalf("URL image part=%#v", urlImage)
	}
	if dataImage["type"] != "image_url" || dataImage["image_url"].(map[string]any)["url"] != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("base64 image part=%#v", dataImage)
	}
}

func TestChatPrepareEncodesAddedCanonicalTypedMessage(t *testing.T) {
	source := map[string]any{"role": "user", "content": "original"}
	request := normalize.Request{
		Model: "public", SourceFormat: normalize.FormatOpenAIChat,
		Raw:       map[string]any{"model": "public", "messages": []any{source}},
		Messages:  []normalize.Message{{Role: "user", Content: "original", Metadata: source}, {Role: "user", Content: []normalize.ContentPart{{Type: "text", Text: "added"}, {Type: "image", URL: "https://example.test/new.png"}}}},
		Mutations: normalize.RequestMutationSet{Messages: true},
	}
	prepared, err := (Chat{}).Prepare(context.Background(), request, kernel.Route{BaseURL: "https://provider.test/v1", ExternalModel: "m"}, kernel.Credential{})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.NewDecoder(prepared.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	messages := body["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("message count=%d want 2: %#v", len(messages), messages)
	}
	added := messages[1].(map[string]any)
	content := added["content"].([]any)
	if added["role"] != "user" || content[0].(map[string]any)["text"] != "added" || content[1].(map[string]any)["type"] != "image_url" {
		t.Fatalf("added canonical message was not encoded as Chat content: %#v", messages)
	}
}

func TestNativeChatMessageRejectsUnmappedCanonicalContentParts(t *testing.T) {
	_, err := nativeChatMessage(normalize.Message{Role: "assistant", Content: []normalize.ContentPart{{Type: "thinking", Text: "private"}}})
	if err == nil || !strings.Contains(err.Error(), "has no native message mapping") {
		t.Fatalf("unmapped content part was accepted: %v", err)
	}
}

func TestNativeChatEgressOverlaysOnlyTransformedTypedFacets(t *testing.T) {
	maxTokens, temperature, topP := 12, 0.2, 0.3
	request := normalize.Request{
		Model: "public", SourceFormat: normalize.FormatOpenAIChat,
		Raw: map[string]any{
			"model": "public", "messages": []any{map[string]any{"role": "user", "content": "original"}},
			"tools":       []any{map[string]any{"type": "function", "function": map[string]any{"name": "old"}, "vendor_tool": "preserve"}},
			"tool_choice": map[string]any{"type": "function", "function": map[string]any{"name": "old", "vendor_function": "preserve"}, "vendor_choice": "preserve"},
			"max_tokens":  float64(12), "temperature": float64(0.2), "top_p": float64(0.3), "stop": "old-stop",
			"vendor_extension": map[string]any{"preserve": true},
		},
		Messages:   []normalize.Message{{Role: "user", Content: "compressed", Metadata: map[string]any{"role": "user", "content": "original", "vendor_message": "preserve"}}},
		Tools:      []normalize.Tool{{Type: "function", Name: "old", Function: map[string]any{"name": "old", "parameters": map[string]any{"type": "object"}}, Metadata: map[string]any{"type": "function", "vendor_tool": "preserve", "function": map[string]any{"name": "old"}}}},
		ToolChoice: normalize.ToolChoice{Mode: "tool", Name: "old", Set: true, Metadata: map[string]any{"vendor_choice": "preserve", "function": map[string]any{"vendor_function": "preserve"}}},
		Generation: normalize.GenerationOptions{MaxOutputTokens: &maxTokens, Temperature: &temperature, TopP: &topP, StopSequences: []string{"old-stop"}},
	}
	registry := kernel.NewRequestTransformRegistry()
	transform := nativeChatRequestTransform{}
	if err := registry.Register(transform); err != nil {
		t.Fatal(err)
	}
	if err := registry.ApplyScopes(context.Background(), &request, []kernel.TransformBinding{{ID: "native-options", TransformRef: transform.Definition().Ref, Enabled: true, Scope: kernel.TransformScope{Kind: kernel.TransformScopeDaemon}}}, kernel.TransformScope{Kind: kernel.TransformScopeDaemon}); err != nil {
		t.Fatal(err)
	}
	if !request.Mutations.ToolChoice || !request.Mutations.Tools || !request.Mutations.GenerationMaxOutput || !request.Mutations.GenerationTemperature || !request.Mutations.GenerationTopP || !request.Mutations.GenerationStopSequences {
		t.Fatalf("kernel did not mark transformed facets: %#v", request.Mutations)
	}
	prepared, err := (Chat{}).Prepare(context.Background(), request, kernel.Route{BaseURL: "https://provider.test/v1", ExternalModel: "upstream"}, kernel.Credential{})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.NewDecoder(prepared.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["max_tokens"] != float64(64) || body["temperature"] != float64(0.7) || body["top_p"] != float64(0.8) || !reflect.DeepEqual(body["stop"], []any{"done"}) {
		t.Fatalf("generation options used stale raw values: %#v", body)
	}
	choice, ok := body["tool_choice"].(map[string]any)
	if !ok || choice["type"] != "function" || choice["function"].(map[string]any)["name"] != "lookup" {
		t.Fatalf("tool choice used stale raw value: %#v", body["tool_choice"])
	}
	tools, _ := body["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["function"].(map[string]any)["name"] != "lookup" {
		t.Fatalf("tool definitions used stale raw values: %#v", body["tools"])
	}
	if body["vendor_extension"].(map[string]any)["preserve"] != true {
		t.Fatalf("untouched opaque source extension was lost: %#v", body)
	}
	message := body["messages"].([]any)[0].(map[string]any)
	if message["vendor_message"] != "preserve" {
		t.Fatalf("untouched message metadata was lost: %#v", message)
	}
	if tools[0].(map[string]any)["vendor_tool"] != "preserve" {
		t.Fatalf("untouched tool metadata was lost: %#v", tools[0])
	}
	if choice["vendor_choice"] != "preserve" || choice["function"].(map[string]any)["vendor_function"] != "preserve" {
		t.Fatalf("untouched tool-choice metadata was lost: %#v", choice)
	}
}

func TestChatAdapterEmitsCanonicalSSEEvents(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"hello\",\"tool_calls\":[{\"id\":\"call-1\",\"function\":{\"name\":\"search\",\"arguments\":\"{}\"}}]}}]}\n\ndata: {\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}\n\ndata: [DONE]\n\n"
	var events []kernel.ResponseEvent
	recorder := httptest.NewRecorder()
	response := kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
	err := NewAdapter().RenderResponse(context.Background(), response, recorder, normalize.FormatOpenAIChat, kernel.StreamHooks{Streaming: true, OnEvent: func(event kernel.ResponseEvent) { events = append(events, event) }})
	if err != nil {
		t.Fatal(err)
	}
	seenText, seenTool, seenUsage := false, false, false
	for _, event := range events {
		seenText = seenText || event.Kind == kernel.EventTextDelta
		seenTool = seenTool || event.Kind == kernel.EventToolCallDelta
		seenUsage = seenUsage || event.Kind == kernel.EventUsage
	}
	if !seenText || !seenTool || !seenUsage {
		t.Fatalf("events=%#v", events)
	}
	if !strings.Contains(recorder.Body.String(), "[DONE]") {
		t.Fatalf("passthrough=%q", recorder.Body.String())
	}
}

func TestChatAdapterReportsMalformedSSE(t *testing.T) {
	var got kernel.ResponseEvent
	var streamErr error
	recorder := httptest.NewRecorder()
	response := kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {bad}\n\n"))}
	err := NewAdapter().RenderResponse(context.Background(), response, recorder, normalize.FormatOpenAIChat, kernel.StreamHooks{Streaming: true, OnEvent: func(event kernel.ResponseEvent) { got = event }, OnError: func(err error) { streamErr = err }})
	if err == nil || streamErr == nil || got.Kind != kernel.EventResponseError {
		t.Fatalf("err=%v streamErr=%v event=%#v", err, streamErr, got)
	}
}

func TestResponsesEventsKeepContinuityIdentity(t *testing.T) {
	body := "data: {\"type\":\"response.output_text.delta\",\"response_id\":\"resp_1\",\"item_id\":\"msg_1\",\"delta\":\"hi\"}\n\ndata: {\"type\":\"response.completed\",\"response_id\":\"resp_1\",\"response\":{\"status\":\"completed\"}}\n\n"
	var event kernel.ResponseEvent
	recorder := httptest.NewRecorder()
	response := kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
	if err := NewResponsesAdapter().RenderResponse(context.Background(), response, recorder, normalize.FormatOpenAIResponses, kernel.StreamHooks{Streaming: true, OnEvent: func(got kernel.ResponseEvent) {
		if got.Kind == kernel.EventTextDelta {
			event = got
		}
	}}); err != nil {
		t.Fatal(err)
	}
	if event.ResponseID != "resp_1" || event.ItemID != "msg_1" || event.ContentType != "output_text" {
		t.Fatalf("event=%#v", event)
	}
}

func TestResponsesJSONExposesResponseIDForSessionContinuity(t *testing.T) {
	body := `{"id":"resp_42","object":"response","status":"completed"}`
	var responseID string
	response := kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
	if err := NewResponsesAdapter().RenderResponse(context.Background(), response, httptest.NewRecorder(), normalize.FormatOpenAIResponses, kernel.StreamHooks{OnEvent: func(event kernel.ResponseEvent) {
		if event.Kind == kernel.EventResponseComplete {
			responseID = event.ResponseID
		}
	}}); err != nil {
		t.Fatal(err)
	}
	if responseID != "resp_42" {
		t.Fatalf("response ID for session continuity=%q", responseID)
	}
}

func TestResponsesEventsKeepOutputItemBoundaries(t *testing.T) {
	body := "data: {\"type\":\"response.output_item.added\",\"response_id\":\"resp_1\",\"item_id\":\"item_1\",\"item_type\":\"message\"}\n\ndata: {\"type\":\"response.output_item.done\",\"response_id\":\"resp_1\",\"item_id\":\"item_1\",\"status\":\"completed\"}\n\ndata: {\"type\":\"response.completed\",\"response_id\":\"resp_1\",\"response\":{\"status\":\"completed\"}}\n\n"
	var events []kernel.ResponseEvent
	recorder := httptest.NewRecorder()
	response := kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
	if err := NewResponsesAdapter().RenderResponse(context.Background(), response, recorder, normalize.FormatOpenAIResponses, kernel.StreamHooks{Streaming: true, OnEvent: func(event kernel.ResponseEvent) {
		if event.Kind != kernel.EventRawFrame {
			events = append(events, event)
		}
	}}); err != nil {
		t.Fatal(err)
	}
	startIndex, endIndex, completeIndex := -1, -1, -1
	for index, event := range events {
		switch event.Kind {
		case kernel.EventContentBlockStart:
			startIndex = index
		case kernel.EventContentBlockEnd:
			endIndex = index
		case kernel.EventResponseComplete:
			completeIndex = index
		}
	}
	if len(events) < 4 || events[0].Kind != kernel.EventResponseStarted || startIndex < 0 || endIndex <= startIndex || completeIndex <= endIndex {
		t.Fatalf("events=%#v", events)
	}
}

func TestChatJSONRendererPreservesBodyAndEmitsText(t *testing.T) {
	body := `{"id":"chat","choices":[{"message":{"content":"hello"},"finish_reason":"stop"}],"usage":{"input_tokens":2,"output_tokens":1}}`
	var events []kernel.ResponseEvent
	recorder := httptest.NewRecorder()
	response := kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
	if err := NewAdapter().RenderResponse(context.Background(), response, recorder, normalize.FormatOpenAIChat, kernel.StreamHooks{OnEvent: func(event kernel.ResponseEvent) { events = append(events, event) }}); err != nil {
		t.Fatal(err)
	}
	if recorder.Body.String() != body {
		t.Fatalf("body changed: %q", recorder.Body.String())
	}
	if len(events) < 2 || events[1].Kind != kernel.EventTextDelta || events[1].Text != "hello" {
		t.Fatalf("events=%#v", events)
	}
}

func TestAnthropicMessagesRequestMapsToOpenAIChatFromTypedIR(t *testing.T) {
	parsed, err := normalize.JSON("/v1/messages", http.Header{}, []byte(`{
"model":"junior","max_tokens":512,"temperature":0.2,"top_p":0.9,"stop_sequences":["stop"],
"system":[{"type":"text","text":"Be concise."}],
"tools":[{"name":"lookup","description":"Search","input_schema":{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}}],
"tool_choice":{"type":"tool","name":"lookup"},
"messages":[{"role":"user","content":[{"type":"text","text":"Check weather."},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]},
{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"lookup","input":{"q":"weather"}}]},
{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"Sunny."}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	composed := NewAdapter().(kernel.ComposedAdapter)
	plan := composed.PlanCompatibility(kernel.CompatibilityContext{Request: parsed.Request, Policy: kernel.CompatibilityPolicy{RequiredFacets: kernel.RequiredRequestFacets(parsed.Request)}})
	if !plan.Supported || plan.Fidelity != kernel.FidelityTranslated {
		t.Fatalf("representable Anthropic request was not admitted: %#v", plan)
	}
	route := kernel.Route{ID: "openai-route", BaseURL: "https://provider.test/v1", ExternalModel: "upstream-model", Enabled: true}
	prepared, err := composed.Prepare(context.Background(), parsed.Request, route, kernel.Credential{Secret: "credential"})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.NewDecoder(prepared.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["model"] != "upstream-model" || body["stream"] != false || body["max_tokens"] != float64(512) || body["temperature"] != 0.2 || body["top_p"] != 0.9 {
		t.Fatalf("translated OpenAI request options=%#v", body)
	}
	if stop, ok := body["stop"].([]any); !ok || len(stop) != 1 || stop[0] != "stop" {
		t.Fatalf("translated stop sequences=%#v", body["stop"])
	}
	if choice, ok := body["tool_choice"].(map[string]any); !ok || choice["type"] != "function" || choice["function"].(map[string]any)["name"] != "lookup" {
		t.Fatalf("translated tool choice=%#v", body["tool_choice"])
	}
	messages, ok := body["messages"].([]any)
	if !ok || len(messages) != 4 {
		t.Fatalf("translated messages=%#v", body["messages"])
	}
	if messages[0].(map[string]any)["role"] != "system" || messages[0].(map[string]any)["content"] != "Be concise." {
		t.Fatalf("translated system prompt=%#v", messages[0])
	}
	userParts := messages[1].(map[string]any)["content"].([]any)
	imageURL := userParts[1].(map[string]any)["image_url"].(map[string]any)["url"]
	if imageURL != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("translated Anthropic image source=%#v", userParts[1])
	}
	assistant := messages[2].(map[string]any)
	calls := assistant["tool_calls"].([]any)
	function := calls[0].(map[string]any)["function"].(map[string]any)
	if function["name"] != "lookup" || function["arguments"] != `{"q":"weather"}` {
		t.Fatalf("translated tool call=%#v", calls[0])
	}
	toolResult := messages[3].(map[string]any)
	if toolResult["role"] != "tool" || toolResult["tool_call_id"] != "call_1" || toolResult["content"] != "Sunny." {
		t.Fatalf("translated tool result=%#v", toolResult)
	}
}

func TestAnthropicThinkingIntentIsRejectedByOpenAIChatCompatibilityBeforeEncoding(t *testing.T) {
	parsed, err := normalize.JSON("/v1/messages", http.Header{}, []byte(`{"model":"junior","max_tokens":512,"thinking":{"type":"enabled","budget_tokens":2000},"messages":[{"role":"user","content":"think"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	composed := NewAdapter().(kernel.ComposedAdapter)
	plan := composed.PlanCompatibility(kernel.CompatibilityContext{Request: parsed.Request, Policy: kernel.CompatibilityPolicy{RequiredFacets: kernel.RequiredRequestFacets(parsed.Request)}})
	if plan.Supported || plan.Fidelity != kernel.FidelityUnsupported {
		t.Fatalf("unmapped Anthropic thinking budget was admitted to OpenAI Chat: %#v", plan)
	}
}

func TestKernelExcludesOpenAIChatRouteForAnthropicThinkingBeforeDispatch(t *testing.T) {
	parsed, err := normalize.JSON("/v1/messages", http.Header{}, []byte(`{"model":"role","max_tokens":1024,"thinking":{"type":"enabled","budget_tokens":2048},"messages":[{"role":"user","content":"reason carefully"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := kernel.BuildSnapshot(kernel.SnapshotInput{
		PublicModels: []kernel.PublicModel{{Name: "role", TargetRef: "physical"}},
		Nodes:        []kernel.ModelNode{{ID: "physical", Kind: kernel.ModelPhysical, Strategy: kernel.StrategyFallback, Members: []kernel.MemberRef{{Kind: kernel.MemberRoute, ID: "openai-route", Fidelity: kernel.FidelityExact}}}},
		Routes: []kernel.Route{{ID: "openai-route", Enabled: true, BaseURL: "http://provider.test/v1", ExternalModel: "model", OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{
			normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"openai-chat"}},
		}}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	var dispatched atomic.Int32
	composed := NewAdapter().(kernel.ComposedAdapter)
	composed.Transport = kernel.HTTPTransport{Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		dispatched.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}}
	gateway, err := kernel.New(snapshot, nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	gateway.Adapters["openai-chat"] = composed
	writer := httptest.NewRecorder()
	err = gateway.Execute(context.Background(), parsed.Request, kernel.Credential{}, writer)
	if err == nil || dispatched.Load() != 0 {
		t.Fatalf("unmapped Anthropic thinking intent reached OpenAI Chat: err=%v dispatches=%d", err, dispatched.Load())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }
