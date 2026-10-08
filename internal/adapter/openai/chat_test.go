package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	egress "github.com/fm39hz/gobroom/internal/adapter/renderers"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

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
