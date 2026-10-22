package gemini

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

func TestGeminiOptionalUsageFaultPreservesAnswerJSONAndSSE(t *testing.T) {
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
				body := `{"candidates":[{"content":{"parts":[{"text":"valid answer"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":` + invalid.value + `,"candidatesTokenCount":7,"thoughtsTokenCount":0}}`
				contentType := "application/json"
				if wire == "sse" {
					contentType = "text/event-stream"
					body = "data: " + body + "\n\n"
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
				err := (Gemini{}).DecodeResponse(context.Background(), kernel.UpstreamResponse{
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

func TestGeminiNativeSnapshotsInvalidatePreviouslyProvenOutput(t *testing.T) {
	body := "data: {\"usageMetadata\":{\"promptTokenCount\":11,\"candidatesTokenCount\":6,\"totalTokenCount\":17}}\n\n" +
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"done\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"candidatesTokenCount\":7}}\n\n"
	var providerSnapshots, clientSnapshots []kernel.UsageEvent
	var completed kernel.UsageEvent
	err := (Gemini{}).DecodeResponse(context.Background(), kernel.UpstreamResponse{
		Status: http.StatusOK, Headers: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body)),
	}, func(event kernel.ResponseEvent) error {
		if event.Kind == kernel.EventUsage && event.Usage != nil {
			clientSnapshots = append(clientSnapshots, event.Usage.Clone())
		}
		return nil
	}, kernel.StreamHooks{
		OnProviderUsage: func(event kernel.UsageEvent) { providerSnapshots = append(providerSnapshots, event.Clone()) },
		OnComplete:      func(event kernel.UsageEvent) { completed = event },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(providerSnapshots) != 2 || len(clientSnapshots) != 2 {
		t.Fatalf("provider snapshots=%#v client snapshots=%#v", providerSnapshots, clientSnapshots)
	}
	for name, snapshots := range map[string][]kernel.UsageEvent{"provider": providerSnapshots, "client": clientSnapshots} {
		latest := snapshots[len(snapshots)-1]
		if latest.OutputTokens != nil {
			t.Errorf("%s snapshot retained stale proven output %d: %#v", name, *latest.OutputTokens, latest)
		}
		if latest.UpdateKind != kernel.UsageFullSnapshot {
			t.Errorf("%s native observation kind=%q, want snapshot", name, latest.UpdateKind)
		}
	}
	if completed.OutputTokens != nil {
		t.Fatalf("completion retained stale output: %#v", completed)
	}
}

func TestGeminiRequestCodecRejectsUnrecognizedRawStreamOptions(t *testing.T) {
	mapped, err := normalize.Map("/v1/chat/completions", http.Header{}, map[string]any{
		"model": "gemini-flash", "stream": true,
		"stream_options": map[string]any{"include_usage": true},
		"messages":       []any{map[string]any{"role": "user", "content": "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []any{
		map[string]any{"include_usage": true, "unknown_option": true},
		map[string]any{"include_usage": "true"},
	} {
		request := normalize.CloneRequest(mapped.Request)
		request.Raw["stream_options"] = raw
		if _, err := geminiRequest(request); err == nil {
			t.Errorf("Gemini codec accepted unsupported raw stream_options: %#v", raw)
		}
	}
}
