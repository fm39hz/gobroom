package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	egress "github.com/fm39hz/gobroom/internal/adapter/renderers"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

func TestResponsesPrepareTranslatesChatReasoningIntent(t *testing.T) {
	request := normalize.Request{SourceFormat: normalize.FormatOpenAIChat, Raw: map[string]any{"messages": []any{}}, Thinking: normalize.ThinkingIntent{Mode: "level", Effort: "high"}}
	prepared, err := (Responses{}).Prepare(context.Background(), request, kernel.Route{ID: "route", BaseURL: "https://provider.test", ExternalModel: "gpt"}, kernel.Credential{})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.NewDecoder(prepared.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["reasoning_effort"] != nil {
		t.Fatalf("chat reasoning field leaked: %#v", body)
	}
	if reasoning, ok := body["reasoning"].(map[string]any); !ok || reasoning["effort"] != "high" {
		t.Fatalf("reasoning=%#v", body["reasoning"])
	}
}

func TestOpenAIResponsesDecoderPreservesToolIdentityForAnthropicEgress(t *testing.T) {
	composed, ok := NewResponsesAdapter().(kernel.ComposedAdapter)
	if !ok {
		t.Fatalf("Responses adapter type=%T", NewResponsesAdapter())
	}
	composed.Renderers[normalize.FormatAnthropic] = egress.AnthropicMessages{}
	body := "data: {\"type\":\"response.output_item.added\",\"response_id\":\"resp_tool\",\"item_id\":\"item_1\",\"item_type\":\"function_call\",\"item\":{\"id\":\"call_1\",\"call_id\":\"call_1\",\"name\":\"lookup\",\"arguments\":\"\"}}\n\n" +
		"data: {\"type\":\"response.function_call_arguments.delta\",\"response_id\":\"resp_tool\",\"item_id\":\"item_1\",\"delta\":\"{\\\"q\\\":\\\"weather\\\"}\"}\n\n" +
		"data: {\"type\":\"response.output_item.done\",\"response_id\":\"resp_tool\",\"item_id\":\"item_1\",\"status\":\"completed\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response_id\":\"resp_tool\",\"response\":{\"id\":\"resp_tool\",\"status\":\"completed\"}}\n\n"
	response := kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
	writer := httptest.NewRecorder()
	var semantic bytes.Buffer
	err := composed.RenderResponse(context.Background(), response, writer, normalize.FormatAnthropic, kernel.StreamHooks{Streaming: true, OnEvent: func(event kernel.ResponseEvent) {
		if event.Kind == kernel.EventToolCallDelta {
			_, _ = fmt.Fprintf(&semantic, "%s|%s|%s", event.ToolCallID, event.ToolName, event.ToolArguments)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	output := writer.Body.String()
	for _, expected := range []string{"event: content_block_start", `"type":"tool_use"`, `"id":"call_1"`, `"name":"lookup"`, `"partial_json":"{\"q\":\"weather\"}"`, `"stop_reason":"tool_use"`, "event: message_stop"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("Anthropic semantic SSE missing %q: %s", expected, output)
		}
	}
	if !strings.Contains(semantic.String(), "call_1|lookup|") {
		t.Fatalf("Responses decoder did not preserve tool identity in IR: %q", semantic.String())
	}
}

func TestResponsesPrepareUsesSessionStoreContinuityWhenClientOmitsIt(t *testing.T) {
	request := normalize.Request{
		SourceFormat: normalize.FormatOpenAIChat,
		Raw:          map[string]any{"messages": []any{map[string]any{"role": "user", "content": "continue"}}},
		Continuity:   normalize.ContinuityState{PreviousResponse: "resp-from-session"},
	}
	prepared, err := (Responses{}).Prepare(context.Background(), request, kernel.Route{ID: "route", ExternalModel: "gpt"}, kernel.Credential{})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.NewDecoder(prepared.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["previous_response_id"] != "resp-from-session" {
		t.Fatalf("previous_response_id=%#v", body["previous_response_id"])
	}
}
