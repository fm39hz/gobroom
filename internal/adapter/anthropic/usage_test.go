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

func TestDecodeAnthropicUsageMapsDocumentedThinkingSubset(t *testing.T) {
	usage, err := decodeAnthropicUsage(json.RawMessage(`{"input_tokens":100,"cache_read_input_tokens":500,"cache_creation_input_tokens":400,"output_tokens":1000,"output_tokens_details":{"thinking_tokens":200}}`))
	if err != nil {
		t.Fatal(err)
	}
	if usage.InputTokens == nil || *usage.InputTokens != 1000 || usage.CachedReadTokens == nil || *usage.CachedReadTokens != 500 || usage.CachedWriteTokens == nil || *usage.CachedWriteTokens != 400 {
		t.Fatalf("cache components must be included in input exactly once: %#v", usage)
	}
	if usage.OutputTokens == nil || *usage.OutputTokens != 1000 || usage.ReasoningTokens == nil || *usage.ReasoningTokens != 200 {
		t.Fatalf("documented thinking tokens must remain an output subset: %#v", usage)
	}
	if usage.UpdateKind != kernel.UsageFullSnapshot {
		t.Fatalf("usage helper must return a full snapshot: %#v", usage)
	}
}

func TestDecodeAnthropicUsageIgnoresUndocumentedTopLevelReasoningTokens(t *testing.T) {
	usage, err := decodeAnthropicUsage(json.RawMessage(`{"input_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":9,"reasoning_tokens":7}`))
	if err != nil {
		t.Fatal(err)
	}
	if usage.InputTokens == nil || *usage.InputTokens != 0 || usage.OutputTokens == nil || *usage.OutputTokens != 9 || usage.ReasoningTokens != nil {
		t.Fatalf("unknown top-level reasoning alias must not affect documented counters: %#v", usage)
	}
}

func TestDecodeAnthropicUsageMalformedOptionalFieldsPreserveSiblings(t *testing.T) {
	usage, err := decodeAnthropicUsage(json.RawMessage(`{"input_tokens":1.5,"output_tokens":7,"output_tokens_details":{"thinking_tokens":"private-value"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if usage.InputTokens != nil || usage.OutputTokens == nil || *usage.OutputTokens != 7 || usage.ReasoningTokens != nil {
		t.Fatalf("invalid counters must be unknown without poisoning valid siblings: %#v", usage)
	}
	if len(usage.AccountingIssues) == 0 || usage.AccountingIssues[0].Code != "invalid_usage_counter" {
		t.Fatalf("invalid optional counters need a sanitized issue: %#v", usage.AccountingIssues)
	}
}

func TestAnthropicThinkingUsageInJSONAndFinalSSEDelta(t *testing.T) {
	const jsonBody = `{"id":"msg_thinking","type":"message","role":"assistant","content":[{"type":"text","text":"answer"}],"stop_reason":"end_turn","usage":{"input_tokens":100,"cache_read_input_tokens":500,"cache_creation_input_tokens":400,"output_tokens":1000,"output_tokens_details":{"thinking_tokens":200}}}`
	const sseBody = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":100,\"cache_read_input_tokens\":500,\"cache_creation_input_tokens\":400}}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1000,\"output_tokens_details\":{\"thinking_tokens\":200}}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	for _, test := range []struct {
		name, contentType, body string
	}{
		{"json", "application/json", jsonBody},
		{"sse", "text/event-stream", sseBody},
	} {
		t.Run(test.name, func(t *testing.T) {
			var got kernel.UsageEvent
			err := NewAdapter().RenderResponse(context.Background(), kernel.UpstreamResponse{
				Status: http.StatusOK, Headers: http.Header{"Content-Type": {test.contentType}}, Body: io.NopCloser(strings.NewReader(test.body)),
			}, httptest.NewRecorder(), normalize.FormatOpenAIChat, kernel.StreamHooks{OnComplete: func(event kernel.UsageEvent) { got = event }})
			if err != nil {
				t.Fatal(err)
			}
			if got.InputTokens == nil || *got.InputTokens != 1000 || got.OutputTokens == nil || *got.OutputTokens != 1000 || got.ReasoningTokens == nil || *got.ReasoningTokens != 200 {
				t.Fatalf("JSON and final message_delta must map canonical thinking usage: %#v", got)
			}
		})
	}
}

func TestAnthropicUsageJSONMalformedDetailsRetractEarlierThinking(t *testing.T) {
	initial, err := decodeAnthropicUsageFrame([]byte(`{"usage":{"input_tokens":100,"cache_read_input_tokens":900,"cache_creation_input_tokens":0,"output_tokens":40,"output_tokens_details":{"thinking_tokens":20}}}`))
	if err != nil {
		t.Fatal(err)
	}
	state := (anthropicUsageState{}).apply(initial.EventUsage)
	if !anthropicCountIs(state.snapshot().ReasoningTokens, 20) {
		t.Fatal("initial JSON usage did not establish thinking count 20")
	}
	correction, err := decodeAnthropicUsageFrame([]byte(`{"usage":{"output_tokens":80,"output_tokens_details":"bad"}}`))
	if err != nil {
		t.Fatal(err)
	}
	assertAnthropicMalformedDetailsCorrection(t, state.apply(correction.EventUsage).snapshot())
}

func TestAnthropicUsageSSEMalformedDetailsRetractEarlierThinking(t *testing.T) {
	body := "data: " + `{"type":"message_start","message":{"id":"msg_correction","usage":{"input_tokens":100,"cache_read_input_tokens":900,"cache_creation_input_tokens":0,"output_tokens":40,"output_tokens_details":{"thinking_tokens":20}}}}` + "\n\n" +
		"data: " + `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":80,"output_tokens_details":"bad"}}` + "\n\n" +
		"data: " + `{"type":"message_stop"}` + "\n\n"
	var providerSnapshots, clientSnapshots []kernel.UsageEvent
	var completed kernel.UsageEvent
	completeCalls := 0
	err := (Messages{}).DecodeResponse(context.Background(), kernel.UpstreamResponse{
		Status: http.StatusOK, Headers: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body)),
	}, func(event kernel.ResponseEvent) error {
		if event.Usage != nil {
			clientSnapshots = append(clientSnapshots, event.Usage.Clone())
		}
		return nil
	}, kernel.StreamHooks{
		OnProviderUsage: func(usage kernel.UsageEvent) { providerSnapshots = append(providerSnapshots, usage.Clone()) },
		OnComplete: func(usage kernel.UsageEvent) {
			completed = usage.Clone()
			completeCalls++
		},
	})
	if err != nil || completeCalls != 1 {
		t.Fatalf("malformed optional details failed successful completion: err=%v completions=%d", err, completeCalls)
	}
	for name, snapshots := range map[string][]kernel.UsageEvent{"provider": providerSnapshots, "client": clientSnapshots} {
		if len(snapshots) < 2 || !anthropicCountIs(snapshots[0].ReasoningTokens, 20) {
			t.Fatalf("%s snapshots did not establish thinking before correction: %#v", name, snapshots)
		}
		assertAnthropicMalformedDetailsCorrection(t, snapshots[len(snapshots)-1])
	}
	assertAnthropicMalformedDetailsCorrection(t, completed)
}

func assertAnthropicMalformedDetailsCorrection(t *testing.T, usage kernel.UsageEvent) {
	t.Helper()
	if usage.ReasoningTokens != nil {
		t.Errorf("malformed whole details retained stale thinking count: %d", *usage.ReasoningTokens)
	}
	if !anthropicCountIs(usage.InputTokens, 1000) || !anthropicCountIs(usage.CachedReadTokens, 900) || !anthropicCountIs(usage.CachedWriteTokens, 0) || !anthropicCountIs(usage.OutputTokens, 80) {
		t.Errorf("thinking correction changed valid input/cache/output siblings: %#v", usage)
	}
	if usage.UpdateKind != kernel.UsageFullSnapshot || usage.Status != "ok" {
		t.Errorf("correction must remain a successful full snapshot: %#v", usage)
	}
	if len(usage.AccountingIssues) != 1 || usage.AccountingIssues[0].Code != "invalid_usage_counter" {
		t.Errorf("malformed details must retain a sanitized accounting issue: %#v", usage.AccountingIssues)
	}
}
