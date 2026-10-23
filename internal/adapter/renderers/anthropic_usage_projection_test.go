package renderers

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

type anthropicUsageProjection struct {
	InputTokens              *int64 `json:"input_tokens"`
	CacheReadInputTokens     *int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
	OutputTokens             *int64 `json:"output_tokens"`
	OutputTokensDetails      *struct {
		ThinkingTokens *int64 `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
	ReasoningTokens *int64 `json:"reasoning_tokens"`
}

func TestAnthropicUsageProjectionUsesDisjointInputAndKnownCache(t *testing.T) {
	usage := kernel.UsageEvent{
		InputTokens: new(int64(1000)), OutputTokens: new(int64(30)),
		CachedReadTokens: new(int64(900)), CachedWriteTokens: new(int64(50)),
		ReasoningTokens: new(int64(20)),
	}
	got := anthropicUsage(usage)
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var projection anthropicUsageProjection
	if err := json.Unmarshal(encoded, &projection); err != nil {
		t.Fatal(err)
	}
	if projection.InputTokens == nil || *projection.InputTokens != 50 ||
		projection.CacheReadInputTokens == nil || *projection.CacheReadInputTokens != 900 ||
		projection.CacheCreationInputTokens == nil || *projection.CacheCreationInputTokens != 50 {
		t.Fatalf("wrong Anthropic input/cache projection: %s", encoded)
	}
	if projection.OutputTokens == nil || *projection.OutputTokens != 30 ||
		projection.OutputTokensDetails == nil || projection.OutputTokensDetails.ThinkingTokens == nil ||
		*projection.OutputTokensDetails.ThinkingTokens != 20 {
		t.Fatalf("wrong Anthropic output/thinking projection: %s", encoded)
	}
	if projection.ReasoningTokens != nil {
		t.Fatalf("invented top-level reasoning_tokens: %s", encoded)
	}
}

func TestAnthropicUsageProjectionPreservesZeroAndUnknownInput(t *testing.T) {
	t.Run("known explicit zeros", func(t *testing.T) {
		got := anthropicUsage(kernel.UsageEvent{
			InputTokens: new(int64(0)), OutputTokens: new(int64(0)),
			CachedReadTokens: new(int64(0)), CachedWriteTokens: new(int64(0)), ReasoningTokens: new(int64(0)),
		})
		encoded, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		var projection anthropicUsageProjection
		if err := json.Unmarshal(encoded, &projection); err != nil {
			t.Fatal(err)
		}
		if projection.InputTokens == nil || *projection.InputTokens != 0 || projection.CacheReadInputTokens == nil || *projection.CacheReadInputTokens != 0 ||
			projection.CacheCreationInputTokens == nil || *projection.CacheCreationInputTokens != 0 || projection.OutputTokens == nil || *projection.OutputTokens != 0 ||
			projection.OutputTokensDetails == nil || projection.OutputTokensDetails.ThinkingTokens == nil || *projection.OutputTokensDetails.ThinkingTokens != 0 {
			t.Fatalf("explicit zero was lost: %s", encoded)
		}
	})

	t.Run("unknown cache write", func(t *testing.T) {
		got := anthropicUsage(kernel.UsageEvent{InputTokens: new(int64(1000)), CachedReadTokens: new(int64(900))})
		encoded, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		var projection anthropicUsageProjection
		if err := json.Unmarshal(encoded, &projection); err != nil {
			t.Fatal(err)
		}
		if projection.InputTokens != nil {
			t.Fatalf("derived uncached input with unknown cache-write count: %s", encoded)
		}
		if projection.CacheReadInputTokens == nil || *projection.CacheReadInputTokens != 900 || projection.CacheCreationInputTokens != nil {
			t.Fatalf("known cache read or unknown cache write projected incorrectly: %s", encoded)
		}
	})
}

func TestAnthropicUsageProjectionDoesNotDiscloseExtensionValues(t *testing.T) {
	usage := kernel.UsageEvent{
		InputTokens: new(int64(2)),
		Extensions:  []extensions.UsageValue{{TypeRef: extensions.Ref{Kind: "usage-value", ID: "private"}, Value: json.RawMessage(`{"secret":"do-not-project"}`)}},
	}
	encoded, err := json.Marshal(anthropicUsage(usage))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "do-not-project") || strings.Contains(string(encoded), "secret") {
		t.Fatalf("client usage projection disclosed extension payload: %s", encoded)
	}
}

func TestAnthropicUsageProjectionInJSONAndFinalSSEDelta(t *testing.T) {
	usage := &kernel.UsageEvent{
		InputTokens: new(int64(1000)), OutputTokens: new(int64(30)),
		CachedReadTokens: new(int64(900)), CachedWriteTokens: new(int64(50)), ReasoningTokens: new(int64(20)),
	}
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
			if err := session.Emit(context.Background(), kernel.ResponseEvent{Kind: kernel.EventUsage, Usage: usage}); err != nil {
				t.Fatal(err)
			}
			if err := session.Finish(context.Background(), nil); err != nil {
				t.Fatal(err)
			}
			output := writer.Body.String()
			if streaming {
				if !strings.Contains(output, "event: message_delta") {
					t.Fatalf("final usage delta missing: %s", output)
				}
				output = strings.Split(output, "event: message_delta\n")[1]
				output = strings.SplitN(output, "\n\n", 2)[0]
				output = strings.TrimSpace(strings.TrimPrefix(output, "data:"))
			}
			var envelope struct {
				Usage anthropicUsageProjection `json:"usage"`
			}
			if err := json.Unmarshal([]byte(output), &envelope); err != nil {
				t.Fatalf("decode projected usage: %v; payload=%s", err, output)
			}
			if envelope.Usage.InputTokens == nil || *envelope.Usage.InputTokens != 50 ||
				envelope.Usage.CacheReadInputTokens == nil || *envelope.Usage.CacheReadInputTokens != 900 ||
				envelope.Usage.CacheCreationInputTokens == nil || *envelope.Usage.CacheCreationInputTokens != 50 ||
				envelope.Usage.OutputTokens == nil || *envelope.Usage.OutputTokens != 30 ||
				envelope.Usage.OutputTokensDetails == nil || envelope.Usage.OutputTokensDetails.ThinkingTokens == nil || *envelope.Usage.OutputTokensDetails.ThinkingTokens != 20 {
				t.Fatalf("JSON/final SSE delta lost canonical usage: %s", output)
			}
		})
	}
}
