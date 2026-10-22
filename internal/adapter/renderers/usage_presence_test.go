package renderers

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

func TestOpenAIChatJSONUsagePresence(t *testing.T) {
	options := kernel.ResponseRenderContext{Status: 200, ClientFormat: normalize.FormatOpenAIChat, ProviderFormat: normalize.FormatOpenAIResponses}
	writer := httptest.NewRecorder()
	session, err := (OpenAIChat{}).Begin(context.Background(), options, writer)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Emit(context.Background(), kernel.ResponseEvent{Kind: kernel.EventUsage, Usage: &kernel.UsageEvent{InputTokens: new(int64(0))}}); err != nil {
		t.Fatal(err)
	}
	if err := session.Finish(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(writer.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	usage := body["usage"].(map[string]any)
	if got, ok := usage["prompt_tokens"]; !ok || got != float64(0) {
		t.Fatalf("explicit zero missing: %#v", usage)
	}
	if _, ok := usage["completion_tokens"]; ok {
		t.Fatalf("unknown completion count emitted: %#v", usage)
	}
	if _, ok := usage["total_tokens"]; ok {
		t.Fatalf("total derived with unknown component: %#v", usage)
	}
}

func TestOpenAIChatSSEUsagePresence(t *testing.T) {
	options := kernel.ResponseRenderContext{Status: 200, ClientFormat: normalize.FormatOpenAIChat, ProviderFormat: normalize.FormatOpenAIResponses, Streaming: true, ProviderStreaming: true, IncludeUsage: true}
	writer := httptest.NewRecorder()
	session, err := (OpenAIChat{}).Begin(context.Background(), options, writer)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Emit(context.Background(), kernel.ResponseEvent{Kind: kernel.EventUsage, Usage: &kernel.UsageEvent{InputTokens: new(int64(2)), OutputTokens: new(int64(0))}}); err != nil {
		t.Fatal(err)
	}
	if err := session.Finish(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	frames := decodeChatUsageFrames(t, writer.Body.String())
	var found bool
	for _, frame := range frames {
		if frame.Usage == nil {
			continue
		}
		found = true
		if frame.Usage.PromptTokens != 2 || frame.Usage.CompletionTokens != 0 || frame.Usage.TotalTokens != 2 {
			t.Fatalf("wrong known usage values: %#v", frame.Usage)
		}
	}
	if !found || !strings.Contains(writer.Body.String(), `"total_tokens":2`) {
		t.Fatalf("usage SSE frame missing known total: %s", writer.Body.String())
	}
}
