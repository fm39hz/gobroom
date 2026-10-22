package renderers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

func TestOpenAIResponsesRendererPreservesExactNativeWire(t *testing.T) {
	renderer := OpenAIResponses{}
	wire := "event: response.created\ndata: {\"type\":\"response.created\"}\n\n"
	options := kernel.ResponseRenderContext{
		Status: 200, Headers: http.Header{"Content-Type": []string{"text/event-stream"}},
		ClientFormat: normalize.FormatOpenAIResponses, ProviderFormat: normalize.FormatOpenAIResponses,
		Streaming: true, ProviderStreaming: true,
		ProviderEvents: []kernel.ResponseEventKind{kernel.EventRawFrame, kernel.EventResponseComplete},
		RequiredEvents: []kernel.ResponseEventKind{kernel.EventResponseComplete},
	}
	plan := renderer.SupportsResponse(options)
	if !plan.Supported || plan.Fidelity != kernel.FidelityNative {
		t.Fatalf("matching native Responses stream was not admitted: %#v", plan)
	}
	writer := httptest.NewRecorder()
	session, err := renderer.Begin(context.Background(), options, writer)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Emit(context.Background(), kernel.ResponseEvent{Kind: kernel.EventRawFrame, WireFormat: normalize.FormatOpenAIResponses, Raw: []byte(wire)}); err != nil {
		t.Fatal(err)
	}
	if err := session.Emit(context.Background(), kernel.ResponseEvent{Kind: kernel.EventResponseComplete}); err != nil {
		t.Fatal(err)
	}
	if err := session.Finish(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if writer.Body.String() != wire || writer.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("native Responses wire was changed: content-type=%q body=%q", writer.Header().Get("Content-Type"), writer.Body.String())
	}
}

func TestOpenAIResponsesRendererProjectsSemanticJSON(t *testing.T) {
	renderer := OpenAIResponses{}
	options := kernel.ResponseRenderContext{
		Status: 200, ClientFormat: normalize.FormatOpenAIResponses, ProviderFormat: normalize.FormatOpenAIChat,
		ProviderEvents: []kernel.ResponseEventKind{kernel.EventResponseStarted, kernel.EventTextDelta, kernel.EventThinkingDelta, kernel.EventToolCallDelta, kernel.EventUsage, kernel.EventResponseComplete},
		RequiredEvents: []kernel.ResponseEventKind{kernel.EventTextDelta, kernel.EventThinkingDelta, kernel.EventToolCallDelta, kernel.EventResponseComplete},
	}
	plan := renderer.SupportsResponse(options)
	if !plan.Supported || plan.Fidelity != kernel.FidelityTranslated {
		t.Fatalf("semantic Responses rendering was not admitted: %#v", plan)
	}
	writer := httptest.NewRecorder()
	session, err := renderer.Begin(context.Background(), options, writer)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []kernel.ResponseEvent{
		{Kind: kernel.EventResponseStarted, ResponseID: "resp_semantic"},
		{Kind: kernel.EventTextDelta, Text: "answer"},
		{Kind: kernel.EventThinkingDelta, Text: "summary"},
		{Kind: kernel.EventToolCallDelta, Index: 0, ToolCallID: "call_1", ToolName: "lookup", ToolArguments: `{"q":"x"}`},
		{Kind: kernel.EventUsage, Usage: &kernel.UsageEvent{InputTokens: new(int64(11)), OutputTokens: new(int64(7))}},
		{Kind: kernel.EventResponseComplete, ResponseID: "resp_semantic"},
	} {
		if err := session.Emit(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	if err := session.Finish(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	var response struct {
		ID     string           `json:"id"`
		Object string           `json:"object"`
		Status string           `json:"status"`
		Model  string           `json:"model"`
		Output []map[string]any `json:"output"`
		Usage  map[string]int64 `json:"usage"`
	}
	if err := json.Unmarshal(writer.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode Responses JSON %q: %v", writer.Body.String(), err)
	}
	if response.ID != "resp_semantic" || response.Object != "response" || response.Status != "completed" || response.Model != "" || len(response.Output) != 3 {
		t.Fatalf("Responses JSON envelope=%#v", response)
	}
	message, reasoning, call := response.Output[0], response.Output[1], response.Output[2]
	if message["type"] != "message" || message["id"] != "msg_semantic" || reasoning["type"] != "reasoning" || reasoning["summary"] == nil || call["type"] != "function_call" || call["call_id"] != "call_1" || call["name"] != "lookup" || call["arguments"] != `{"q":"x"}` {
		t.Fatalf("Responses output items=%#v", response.Output)
	}
	if response.Usage["input_tokens"] != 11 || response.Usage["output_tokens"] != 7 || response.Usage["total_tokens"] != 18 {
		t.Fatalf("Responses usage=%#v", response.Usage)
	}
}

func TestOpenAIResponsesRendererStreamsItemLifecycleAndFinalEnvelope(t *testing.T) {
	renderer := OpenAIResponses{}
	options := kernel.ResponseRenderContext{
		Status: 200, ClientFormat: normalize.FormatOpenAIResponses, ProviderFormat: normalize.FormatOpenAIChat,
		Streaming: true, ProviderStreaming: true,
		ProviderEvents: []kernel.ResponseEventKind{kernel.EventResponseStarted, kernel.EventTextDelta, kernel.EventToolCallDelta, kernel.EventUsage, kernel.EventResponseComplete},
		RequiredEvents: []kernel.ResponseEventKind{kernel.EventTextDelta, kernel.EventToolCallDelta, kernel.EventResponseComplete},
	}
	writer := httptest.NewRecorder()
	session, err := renderer.Begin(context.Background(), options, writer)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []kernel.ResponseEvent{
		{Kind: kernel.EventResponseStarted, ResponseID: "resp_stream"},
		{Kind: kernel.EventTextDelta, Text: "partial"},
		{Kind: kernel.EventToolCallDelta, Index: 0, ToolCallID: "call_stream", ToolName: "lookup", ToolArguments: `{"id":7}`},
		{Kind: kernel.EventUsage, Usage: &kernel.UsageEvent{InputTokens: new(int64(4)), OutputTokens: new(int64(2))}},
		{Kind: kernel.EventResponseComplete},
	} {
		if err := session.Emit(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	if err := session.Finish(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	output := writer.Body.String()
	for _, expected := range []string{
		"event: response.created", "event: response.in_progress", "event: response.output_item.added",
		"event: response.output_text.delta", "event: response.function_call_arguments.delta",
		"event: response.output_text.done", "event: response.function_call_arguments.done", "event: response.completed",
		`"call_id":"call_stream"`, `"input_tokens":4`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("Responses SSE omitted %q: %s", expected, output)
		}
	}
	if strings.Contains(output, "[DONE]") || strings.Count(output, "event: response.completed") != 1 {
		t.Fatalf("Responses stream has incorrect terminal framing: %s", output)
	}
}

func TestOpenAIResponsesRendererDoesNotCompleteInterruptedStream(t *testing.T) {
	renderer := OpenAIResponses{}
	options := kernel.ResponseRenderContext{Status: 200, ClientFormat: normalize.FormatOpenAIResponses, ProviderFormat: normalize.FormatOpenAIChat, Streaming: true, ProviderStreaming: true}
	writer := httptest.NewRecorder()
	session, err := renderer.Begin(context.Background(), options, writer)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Emit(context.Background(), kernel.ResponseEvent{Kind: kernel.EventTextDelta, Text: "partial"}); err != nil {
		t.Fatal(err)
	}
	if err := session.Finish(context.Background(), context.Canceled); err == nil {
		t.Fatal("interrupted semantic Responses stream completed successfully")
	}
	if strings.Contains(writer.Body.String(), "event: response.completed") {
		t.Fatalf("interrupted stream emitted a completed response: %s", writer.Body.String())
	}
}
