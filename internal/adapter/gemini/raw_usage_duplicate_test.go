package gemini

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
)

func TestGeminiRawUsageCaptureRejectsDuplicateMembers(t *testing.T) {
	for _, tc := range []struct {
		name, body string
	}{
		{"top usage counter", `{"candidates":[{"content":{"parts":[{"text":"answer"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":"private-marker","promptTokenCount":7,"candidatesTokenCount":3,"thoughtsTokenCount":0,"totalTokenCount":10}}`},
		{"nested answer member", `{"candidates":[{"content":{"parts":[{"text":"private-marker","text":"answer"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3,"thoughtsTokenCount":0,"totalTokenCount":10}}`},
		{"duplicate capture leaf", `{"candidates":[{"content":{"parts":[{"text":"answer"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":"private-marker"},"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3,"thoughtsTokenCount":0,"totalTokenCount":10}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var observations []kernel.UsageEvent
			var answer strings.Builder
			completed := false
			emit := func(event kernel.ResponseEvent) error {
				if event.Kind == kernel.EventTextDelta {
					answer.WriteString(event.Text)
				}
				return nil
			}
			hooks := kernel.StreamHooks{
				UsageCapture:    kernel.UsageCapturePolicy{Enabled: true, MaxBytes: 4096},
				ProviderIssuer:  extensions.Ref{Kind: "provider-definition", ID: "gemini", ContractVersion: 1},
				OnProviderUsage: func(event kernel.UsageEvent) { observations = append(observations, event.Clone()) },
				OnComplete: func(event kernel.UsageEvent) {
					completed = true
					observations = append(observations, event.Clone())
				},
			}
			response := kernel.UpstreamResponse{
				Status:  http.StatusOK,
				Headers: http.Header{"Content-Type": {"application/json"}},
				Body:    io.NopCloser(strings.NewReader(tc.body)),
			}
			if err := (Gemini{}).DecodeResponse(context.Background(), response, emit, hooks); err != nil {
				t.Fatal(err)
			}
			if !completed || answer.String() != "answer" || len(observations) == 0 {
				t.Fatal("duplicate rejection changed ordinary answer decoding or completion")
			}
			for _, event := range observations {
				if event.InputTokens == nil || *event.InputTokens != 7 || event.OutputTokens == nil || *event.OutputTokens != 3 {
					t.Fatalf("duplicate rejection changed ordinary counters: %+v", event)
				}
				if event.RawUsage != nil {
					t.Fatal("raw usage retained duplicate-member evidence")
				}
				if len(event.AccountingIssues) == 0 {
					t.Fatal("duplicate raw capture was omitted without a diagnostic")
				}
				for _, issue := range event.AccountingIssues {
					if issue.Code != "invalid_raw_usage" {
						t.Fatalf("duplicate capture issue was not constant: %q", issue.Code)
					}
				}
				encoded, err := json.Marshal(event.AccountingIssues)
				if err != nil || strings.Contains(string(encoded), "private-marker") {
					t.Fatal("duplicate-member diagnostics disclosed provider text")
				}
			}
		})
	}
}
