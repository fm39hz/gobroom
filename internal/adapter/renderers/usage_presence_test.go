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

func TestOpenAIChatUsageDetailsAndOverflow(t *testing.T) {
	max := int64(^uint64(0) >> 1)
	usage := &kernel.UsageEvent{
		InputTokens: new(max), OutputTokens: new(int64(1)), CachedReadTokens: new(int64(3)),
		CachedWriteTokens: new(int64(2)), ReasoningTokens: new(int64(4)),
	}
	got := chatUsage(usage)
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	inputDetails, inputOK := wire["prompt_tokens_details"].(map[string]any)
	outputDetails, outputOK := wire["completion_tokens_details"].(map[string]any)
	if !inputOK || inputDetails["cached_tokens"] != float64(3) || inputDetails["cache_write_tokens"] != float64(2) || !outputOK || outputDetails["reasoning_tokens"] != float64(4) {
		t.Fatalf("known cache and reasoning details missing: %#v", wire)
	}
	if _, exists := wire["cache_creation_input_tokens"]; exists {
		t.Fatalf("OpenAI usage must not emit the Anthropic cache-write alias: %#v", wire)
	}
	if _, exists := wire["total_tokens"]; exists {
		t.Fatalf("overflowing total must be unknown: %#v", wire)
	}
}

func TestOpenAIResponsesUsageDetailsAndOverflow(t *testing.T) {
	max := int64(^uint64(0) >> 1)
	usage := &kernel.UsageEvent{
		InputTokens: new(max), OutputTokens: new(int64(1)), CachedReadTokens: new(int64(3)),
		CachedWriteTokens: new(int64(2)), ReasoningTokens: new(int64(4)),
	}
	got := responsesUsage(usage)
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	inputDetails, inputOK := wire["input_tokens_details"].(map[string]any)
	outputDetails, outputOK := wire["output_tokens_details"].(map[string]any)
	if !inputOK || inputDetails["cached_tokens"] != float64(3) || inputDetails["cache_write_tokens"] != float64(2) || !outputOK || outputDetails["reasoning_tokens"] != float64(4) {
		t.Fatalf("known cache and reasoning details missing: %#v", wire)
	}
	if _, exists := wire["cache_creation_input_tokens"]; exists {
		t.Fatalf("OpenAI usage must not emit the Anthropic cache-write alias: %#v", wire)
	}
	if _, exists := wire["total_tokens"]; exists {
		t.Fatalf("overflowing total must be unknown: %#v", wire)
	}
}

func TestOpenAIUsageCacheWriteOfficialPathsPresence(t *testing.T) {
	for _, protocol := range []struct {
		name, detailsKey string
		project          func(*kernel.UsageEvent) map[string]any
	}{
		{name: "Chat", detailsKey: "prompt_tokens_details", project: chatUsage},
		{name: "Responses", detailsKey: "input_tokens_details", project: responsesUsage},
	} {
		for _, tc := range []struct {
			name  string
			write *int64
		}{
			{name: "known zero", write: new(int64(0))},
			{name: "missing", write: nil},
		} {
			t.Run(protocol.name+"/"+tc.name, func(t *testing.T) {
				// With no cached-read counter, a known cache-write zero must still
				// cause the input details object to be emitted.
				encoded, err := json.Marshal(protocol.project(&kernel.UsageEvent{CachedWriteTokens: tc.write}))
				if err != nil {
					t.Fatal(err)
				}
				var wire map[string]json.RawMessage
				if err := json.Unmarshal(encoded, &wire); err != nil {
					t.Fatal(err)
				}
				if _, exists := wire["cache_creation_input_tokens"]; exists {
					t.Fatalf("OpenAI usage emitted the Anthropic alias: %s", encoded)
				}
				detailsRaw, exists := wire[protocol.detailsKey]
				if tc.write == nil {
					if exists {
						t.Fatalf("unknown input details must stay absent: %s", encoded)
					}
					return
				}
				if !exists {
					t.Fatalf("known-zero cache write lost its details object: %s", encoded)
				}
				var details struct {
					CacheWriteTokens *int64 `json:"cache_write_tokens"`
					CachedTokens     *int64 `json:"cached_tokens"`
				}
				if err := json.Unmarshal(detailsRaw, &details); err != nil {
					t.Fatal(err)
				}
				if details.CacheWriteTokens == nil || *details.CacheWriteTokens != 0 || details.CachedTokens != nil {
					t.Fatalf("known-zero write or unknown read changed: %s", encoded)
				}
			})
		}
		t.Run(protocol.name+"/missing write with known read", func(t *testing.T) {
			encoded, err := json.Marshal(protocol.project(&kernel.UsageEvent{CachedReadTokens: new(int64(3))}))
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]map[string]int64
			if err := json.Unmarshal(encoded, &wire); err != nil {
				t.Fatal(err)
			}
			details := wire[protocol.detailsKey]
			if _, exists := details["cache_write_tokens"]; exists {
				t.Fatalf("missing write fabricated in populated details: %s", encoded)
			}
			if got, exists := details["cached_tokens"]; !exists || got != 3 {
				t.Fatalf("known cache read missing: %s", encoded)
			}
		})
	}
}

func TestOpenAIChatRendererMergesUsageSnapshots(t *testing.T) {
	options := kernel.ResponseRenderContext{Status: 200, ClientFormat: normalize.FormatOpenAIChat, ProviderFormat: normalize.FormatOpenAIResponses}
	writer := httptest.NewRecorder()
	session, err := (OpenAIChat{}).Begin(context.Background(), options, writer)
	if err != nil {
		t.Fatal(err)
	}
	for _, usage := range []*kernel.UsageEvent{{InputTokens: new(int64(100))}, {OutputTokens: new(int64(4))}} {
		if err := session.Emit(context.Background(), kernel.ResponseEvent{Kind: kernel.EventUsage, Usage: usage}); err != nil {
			t.Fatal(err)
		}
	}
	if err := session.Finish(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(writer.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	usage := body["usage"].(map[string]any)
	if usage["prompt_tokens"] != float64(100) || usage["completion_tokens"] != float64(4) || usage["total_tokens"] != float64(104) {
		t.Fatalf("renderer replaced earlier partial usage: %#v", usage)
	}
}

func TestOpenAIChatSSERendererMergesUsageSnapshots(t *testing.T) {
	options := kernel.ResponseRenderContext{Status: 200, ClientFormat: normalize.FormatOpenAIChat, ProviderFormat: normalize.FormatOpenAIResponses, Streaming: true, ProviderStreaming: true, IncludeUsage: true}
	writer := httptest.NewRecorder()
	session, err := (OpenAIChat{}).Begin(context.Background(), options, writer)
	if err != nil {
		t.Fatal(err)
	}
	for _, usage := range []*kernel.UsageEvent{{InputTokens: new(int64(100))}, {OutputTokens: new(int64(4))}} {
		if err := session.Emit(context.Background(), kernel.ResponseEvent{Kind: kernel.EventUsage, Usage: usage}); err != nil {
			t.Fatal(err)
		}
	}
	if err := session.Finish(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	frames := decodeChatUsageFrames(t, writer.Body.String())
	found := false
	for _, frame := range frames {
		if frame.Usage == nil {
			continue
		}
		found = true
		if frame.Usage.PromptTokens != 100 || frame.Usage.CompletionTokens != 4 || frame.Usage.TotalTokens != 104 {
			t.Fatalf("SSE renderer replaced earlier partial usage: %#v", frame.Usage)
		}
	}
	if !found {
		t.Fatalf("usage frame missing from SSE: %s", writer.Body.String())
	}
}

func TestOpenAIResponsesRendererMergesUsageSnapshots(t *testing.T) {
	options := kernel.ResponseRenderContext{Status: 200, ClientFormat: normalize.FormatOpenAIResponses, ProviderFormat: normalize.FormatOpenAIChat}
	writer := httptest.NewRecorder()
	session, err := (OpenAIResponses{}).Begin(context.Background(), options, writer)
	if err != nil {
		t.Fatal(err)
	}
	for _, usage := range []*kernel.UsageEvent{{InputTokens: new(int64(100))}, {OutputTokens: new(int64(4))}} {
		if err := session.Emit(context.Background(), kernel.ResponseEvent{Kind: kernel.EventUsage, Usage: usage}); err != nil {
			t.Fatal(err)
		}
	}
	if err := session.Emit(context.Background(), kernel.ResponseEvent{Kind: kernel.EventResponseComplete}); err != nil {
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
	if usage["input_tokens"] != float64(100) || usage["output_tokens"] != float64(4) || usage["total_tokens"] != float64(104) {
		t.Fatalf("renderer replaced earlier partial usage: %#v", usage)
	}
}

func TestOpenAIRenderersFullSnapshotClearsUnknownUsage(t *testing.T) {
	tests := []struct {
		name   string
		format normalize.Format
		begin  func(kernel.ResponseRenderContext, *httptest.ResponseRecorder) (kernel.ResponseRenderSession, error)
	}{
		{name: "Chat", format: normalize.FormatOpenAIChat, begin: func(options kernel.ResponseRenderContext, writer *httptest.ResponseRecorder) (kernel.ResponseRenderSession, error) {
			return (OpenAIChat{}).Begin(context.Background(), options, writer)
		}},
		{name: "Responses", format: normalize.FormatOpenAIResponses, begin: func(options kernel.ResponseRenderContext, writer *httptest.ResponseRecorder) (kernel.ResponseRenderSession, error) {
			return (OpenAIResponses{}).Begin(context.Background(), options, writer)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			writer := httptest.NewRecorder()
			options := kernel.ResponseRenderContext{
				Status: 200, ClientFormat: tc.format, ProviderFormat: normalize.FormatOpenAIResponses,
				SemanticTransformActive: true,
			}
			session, err := tc.begin(options, writer)
			if err != nil {
				t.Fatal(err)
			}
			known := &kernel.UsageEvent{UpdateKind: kernel.UsageFullSnapshot, InputTokens: new(int64(11)), OutputTokens: new(int64(6))}
			unknown := &kernel.UsageEvent{UpdateKind: kernel.UsageFullSnapshot}
			for _, usage := range []*kernel.UsageEvent{known, unknown} {
				if err := session.Emit(context.Background(), kernel.ResponseEvent{Kind: kernel.EventUsage, Usage: usage}); err != nil {
					t.Fatal(err)
				}
			}
			if err := session.Emit(context.Background(), kernel.ResponseEvent{Kind: kernel.EventResponseComplete}); err != nil {
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
			for _, field := range []string{"input_tokens", "output_tokens", "prompt_tokens", "completion_tokens", "total_tokens"} {
				if _, exists := usage[field]; exists {
					t.Fatalf("full unknown snapshot retained or fabricated %s: %#v", field, usage)
				}
			}
		})
	}
}

func TestAnthropicRendererMergesPartialUsageInJSONAndSSE(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "json"
		if streaming {
			name = "sse"
		}
		t.Run(name, func(t *testing.T) {
			writer := httptest.NewRecorder()
			options := kernel.ResponseRenderContext{
				Status: 200, ClientFormat: normalize.FormatAnthropic, ProviderFormat: normalize.FormatOpenAIChat,
				Streaming: streaming, ProviderStreaming: streaming,
			}
			session, err := (AnthropicMessages{}).Begin(context.Background(), options, writer)
			if err != nil {
				t.Fatal(err)
			}
			for _, usage := range []*kernel.UsageEvent{{InputTokens: new(int64(100))}, {OutputTokens: new(int64(4))}} {
				if err := session.Emit(context.Background(), kernel.ResponseEvent{Kind: kernel.EventUsage, Usage: usage}); err != nil {
					t.Fatal(err)
				}
			}
			if err := session.Finish(context.Background(), nil); err != nil {
				t.Fatal(err)
			}
			if !streaming {
				var body struct {
					Usage struct {
						Input  int64 `json:"input_tokens"`
						Output int64 `json:"output_tokens"`
					} `json:"usage"`
				}
				if err := json.Unmarshal(writer.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body.Usage.Input != 100 || body.Usage.Output != 4 {
					t.Fatalf("Anthropic JSON lost partial usage fields: %+v", body.Usage)
				}
				return
			}
			output := writer.Body.String()
			if !strings.Contains(output, `"input_tokens":100`) || !strings.Contains(output, `"output_tokens":4`) {
				t.Fatalf("Anthropic SSE lost partial usage fields: %s", output)
			}
		})
	}
}
