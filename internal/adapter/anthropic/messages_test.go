package anthropic

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

func TestAnthropicToolAndTextEventsBecomeOpenAIChunks(t *testing.T) {
	body := strings.NewReader("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":12}}}\n\ndata: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_1\",\"name\":\"search\"}}\n\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"partial_json\":\"{}\"}}\n\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"done\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":4}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
	recorder := httptest.NewRecorder()
	var usage kernel.UsageEvent
	var events []kernel.ResponseEvent
	adapter := Messages{}
	if err := adapter.TranslateStream(context.Background(), kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(body)}, recorder, normalize.FormatOpenAIChat, kernel.StreamHooks{OnEvent: func(event kernel.ResponseEvent) { events = append(events, event) }, OnComplete: func(event kernel.UsageEvent) { usage = event }}); err != nil {
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
