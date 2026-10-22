package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/kernel"
)

func TestAnthropicOptionalUsageFaultPreservesAnswerJSONAndSSE(t *testing.T) {
	var constantIssue kernel.UsageIssue
	for _, wire := range []string{"json", "sse"} {
		for _, invalid := range []struct {
			name, value string
		}{
			{"string", `"private-input-count-47"`},
			{"fraction", `1.25`},
			{"negative", `-3`},
		} {
			t.Run(wire+"/"+invalid.name, func(t *testing.T) {
				usageJSON := `{"input_tokens":` + invalid.value + `,"output_tokens":7}`
				body := `{"id":"optional-usage","type":"message","content":[{"type":"text","text":"valid answer"}],"stop_reason":"end_turn","usage":` + usageJSON + `}`
				contentType := "application/json"
				if wire == "sse" {
					contentType = "text/event-stream"
					body = "data: " + `{"type":"message_start","message":{"id":"optional-usage","usage":` + usageJSON + `}}` + "\n\n" +
						"data: " + `{"type":"content_block_delta","delta":{"text":"valid answer"}}` + "\n\n" +
						"data: " + `{"type":"message_stop"}` + "\n\n"
				}
				var answer strings.Builder
				var completed kernel.UsageEvent
				completeCalls, errorCalls := 0, 0
				emit := func(event kernel.ResponseEvent) error {
					if event.Kind == kernel.EventTextDelta {
						answer.WriteString(event.Text)
					}
					if event.Kind == kernel.EventResponseError {
						errorCalls++
					}
					return nil
				}
				err := (Messages{}).DecodeResponse(context.Background(), kernel.UpstreamResponse{
					Status: http.StatusOK, Headers: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body)),
				}, emit, kernel.StreamHooks{
					OnComplete: func(usage kernel.UsageEvent) {
						completed = usage
						completeCalls++
					},
					OnError: func(error) { errorCalls++ },
				})
				if err != nil || completeCalls != 1 || errorCalls != 0 || answer.String() != "valid answer" {
					t.Fatalf("optional usage fault failed valid response: err=%v complete=%d errors=%d answer=%q", err, completeCalls, errorCalls, answer.String())
				}
				if completed.Status != "ok" || completed.InputTokens != nil || completed.OutputTokens == nil || *completed.OutputTokens != 7 {
					t.Fatalf("optional usage fault lost valid accounting: %#v", completed)
				}
				if len(completed.AccountingIssues) != 1 || completed.AccountingIssues[0].Code == "" {
					t.Fatalf("want one sanitized accounting issue: %#v", completed.AccountingIssues)
				}
				issue := completed.AccountingIssues[0]
				if constantIssue.Code == "" {
					constantIssue = issue
				} else if issue != constantIssue {
					t.Fatalf("accounting issue varies with private value: %#v vs %#v", issue, constantIssue)
				}
				evidence, marshalErr := json.Marshal(completed.AccountingIssues)
				if marshalErr != nil || strings.Contains(string(evidence), "private-input-count-47") || strings.Contains(string(evidence), invalid.value) {
					t.Fatalf("accounting issue contains raw invalid value: %s (%v)", evidence, marshalErr)
				}
			})
		}
	}
}

func TestAnthropicNativeUsageSnapshotIsCanonicalFullObservation(t *testing.T) {
	state := anthropicUsageState{}
	for _, wire := range []string{
		`{"input_tokens":2,"cache_read_input_tokens":8}`,
		`{"cache_creation_input_tokens":4,"output_tokens":1}`,
	} {
		frame, err := decodeAnthropicUsageFrame([]byte(`{"usage":` + wire + `}`))
		if err != nil {
			t.Fatal(err)
		}
		state = state.apply(frame.EventUsage)
	}
	usage := state.snapshot()
	if !anthropicCountIs(usage.InputTokens, 14) || !anthropicCountIs(usage.OutputTokens, 1) ||
		!anthropicCountIs(usage.CachedReadTokens, 8) || !anthropicCountIs(usage.CachedWriteTokens, 4) {
		t.Fatalf("Anthropic snapshot must contain canonical totals once: %#v", usage)
	}
	if usage.UpdateKind != kernel.UsageFullSnapshot {
		t.Fatalf("Anthropic native usage must be marked as a full snapshot, got kind=%q", usage.UpdateKind)
	}
}
