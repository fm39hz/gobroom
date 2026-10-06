package openai

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

func TestChatAdapterForwardsAndPassthroughsSSE(t *testing.T) {
	adapter := NewAdapter()
	request := normalize.Request{Model: "public", Stream: true, Raw: map[string]any{"model": "public", "messages": []any{map[string]any{"role": "user", "content": "hi"}}}}
	route := kernel.Route{ID: "route:test", BaseURL: "https://provider.example/v1", ExternalModel: "upstream-model", Enabled: true}
	prepared, err := adapter.Prepare(context.Background(), request, route, kernel.Credential{Secret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.URL != "https://provider.example/v1/chat/completions" || prepared.Headers.Get("Authorization") != "Bearer secret" {
		t.Fatalf("prepared=%#v", prepared)
	}
	response := kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))}
	recorder := httptest.NewRecorder()
	if err := adapter.TranslateStream(context.Background(), response, recorder, normalize.FormatOpenAIChat, kernel.StreamHooks{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(recorder.Body.String(), "data:") {
		t.Fatalf("body=%q", recorder.Body.String())
	}
}

func TestChatRequestReassemblesPromptPlanAtWireBoundary(t *testing.T) {
	request := normalize.Request{Model: "public", Prompt: normalize.PromptPlan{Layers: []normalize.PromptLayer{{Origin: normalize.PromptHarness, Role: "system", Text: "policy"}}}, Messages: []normalize.Message{{Role: "user", Content: "hello"}}}
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

func TestChatCodecDeclaresOnlyChatClientContract(t *testing.T) {
	codec := chatResponseCodec{}
	if !codec.SupportsClientFormat(normalize.FormatOpenAIChat).Supported {
		t.Fatal("Chat codec should support Chat Completions ingress")
	}
	if codec.SupportsClientFormat(normalize.FormatAnthropic).Supported || codec.SupportsClientFormat(normalize.FormatOpenAIResponses).Supported {
		t.Fatal("Chat passthrough must not claim other wire contracts")
	}
}

func TestChatAdapterEmitsCanonicalSSEEvents(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"hello\",\"tool_calls\":[{\"id\":\"call-1\",\"function\":{\"name\":\"search\",\"arguments\":\"{}\"}}]}}]}\n\ndata: {\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}\n\ndata: [DONE]\n\n"
	var events []kernel.ResponseEvent
	recorder := httptest.NewRecorder()
	response := kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
	err := (Chat{}).TranslateStream(context.Background(), response, recorder, normalize.FormatOpenAIChat, kernel.StreamHooks{OnEvent: func(event kernel.ResponseEvent) { events = append(events, event) }})
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
	err := (Chat{}).TranslateStream(context.Background(), response, recorder, normalize.FormatOpenAIChat, kernel.StreamHooks{OnEvent: func(event kernel.ResponseEvent) { got = event }, OnError: func(err error) { streamErr = err }})
	if err == nil || streamErr == nil || got.Kind != kernel.EventResponseError {
		t.Fatalf("err=%v streamErr=%v event=%#v", err, streamErr, got)
	}
}

func TestResponsesEventsKeepContinuityIdentity(t *testing.T) {
	body := "data: {\"type\":\"response.output_text.delta\",\"response_id\":\"resp_1\",\"item_id\":\"msg_1\",\"delta\":\"hi\"}\n\n"
	var event kernel.ResponseEvent
	recorder := httptest.NewRecorder()
	response := kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
	if err := (Responses{}).TranslateStream(context.Background(), response, recorder, normalize.FormatOpenAIResponses, kernel.StreamHooks{OnEvent: func(got kernel.ResponseEvent) { event = got }}); err != nil {
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
	if err := (Responses{}).TranslateStream(context.Background(), response, httptest.NewRecorder(), normalize.FormatOpenAIResponses, kernel.StreamHooks{OnEvent: func(event kernel.ResponseEvent) {
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
	body := "data: {\"type\":\"response.output_item.added\",\"response_id\":\"resp_1\",\"item_id\":\"item_1\",\"item_type\":\"message\"}\n\ndata: {\"type\":\"response.output_item.done\",\"response_id\":\"resp_1\",\"item_id\":\"item_1\",\"status\":\"completed\"}\n\n"
	var events []kernel.ResponseEvent
	recorder := httptest.NewRecorder()
	response := kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
	if err := (Responses{}).TranslateStream(context.Background(), response, recorder, normalize.FormatOpenAIResponses, kernel.StreamHooks{OnEvent: func(event kernel.ResponseEvent) { events = append(events, event) }}); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Kind != kernel.EventContentBlockStart || events[1].Kind != kernel.EventContentBlockEnd || events[1].StopReason != "completed" {
		t.Fatalf("events=%#v", events)
	}
}

func TestChatJSONRendererPreservesBodyAndEmitsText(t *testing.T) {
	body := `{"id":"chat","choices":[{"message":{"content":"hello"}}],"usage":{"input_tokens":2,"output_tokens":1}}`
	var events []kernel.ResponseEvent
	recorder := httptest.NewRecorder()
	response := kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
	if err := (Chat{}).TranslateStream(context.Background(), response, recorder, normalize.FormatOpenAIChat, kernel.StreamHooks{OnEvent: func(event kernel.ResponseEvent) { events = append(events, event) }}); err != nil {
		t.Fatal(err)
	}
	if recorder.Body.String() != body {
		t.Fatalf("body changed: %q", recorder.Body.String())
	}
	if len(events) == 0 || events[0].Kind != kernel.EventTextDelta || events[0].Text != "hello" {
		t.Fatalf("events=%#v", events)
	}
}
