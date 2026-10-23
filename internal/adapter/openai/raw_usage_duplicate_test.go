package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

func TestOpenAIRawUsageCaptureRejectsDuplicateMembers(t *testing.T) {
	for _, tc := range []struct {
		name, body string
	}{
		{"top usage counter", `{"id":"resp_duplicate","object":"response","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"answer"}]}],` + `"usage":{"input_tokens":"private-marker","input_tokens":7,"output_tokens":3}}`},
		{"nested usage detail", `{"id":"resp_duplicate","object":"response","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"answer"}]}],` + `"usage":{"input_tokens":7,"output_tokens":3,"input_tokens_details":{"cached_tokens":"private-marker","cached_tokens":2}}}`},
		{"duplicate capture leaf", `{"id":"resp_duplicate","object":"response","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"answer"}]}],` + `"usage":{"input_tokens":"private-marker","output_tokens":3},"usage":{"input_tokens":7,"output_tokens":3}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var observations []kernel.UsageEvent
			var answer strings.Builder
			completed := false
			emit := func(event kernel.ResponseEvent) error {
				if event.Kind == kernel.EventTextDelta {
					answer.WriteString(event.Text)
				}
				if event.Kind == kernel.EventUsage && event.Usage != nil {
					observations = append(observations, event.Usage.Clone())
				}
				return nil
			}
			hooks := kernel.StreamHooks{
				UsageCapture:   kernel.UsageCapturePolicy{Enabled: true, MaxBytes: 4096},
				ProviderIssuer: extensions.Ref{Kind: "provider-definition", ID: "openai", ContractVersion: 1},
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
			if err := decodeOpenAIResponse(context.Background(), response, normalize.FormatOpenAIResponses, emit, hooks); err != nil {
				t.Fatal(err)
			}
			if !completed || answer.String() != "answer" || len(observations) == 0 {
				t.Fatal("duplicate rejection changed ordinary answer decoding or completion")
			}
			for _, event := range observations {
				if event.InputTokens == nil || *event.InputTokens != 7 || event.OutputTokens == nil || *event.OutputTokens != 3 {
					t.Fatalf("duplicate rejection changed ordinary counters: %+v", event)
				}
				if tc.name == "nested usage detail" && (event.CachedReadTokens == nil || *event.CachedReadTokens != 2) {
					t.Fatal("duplicate rejection changed the ordinary cache counter")
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
