package anthropic

import (
	"context"
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
	adapter := Messages{}
	if err := adapter.TranslateStream(context.Background(), kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(body)}, recorder, normalize.FormatOpenAIChat, kernel.StreamHooks{OnComplete: func(event kernel.UsageEvent) { usage = event }}); err != nil {
		t.Fatal(err)
	}
	output := recorder.Body.String()
	if !strings.Contains(output, "tool_calls") || !strings.Contains(output, "done") || !strings.Contains(output, "tool_calls") {
		t.Fatalf("output=%s", output)
	}
	if usage.InputTokens != 12 || usage.OutputTokens != 4 {
		t.Fatalf("usage=%#v", usage)
	}
}
