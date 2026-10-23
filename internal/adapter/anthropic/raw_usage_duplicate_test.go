package anthropic

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

func TestAnthropicRawUsageCaptureRejectsDuplicateMembers(t *testing.T) {
	for _, tc := range []struct {
		name, body string
	}{
		{"top usage counter", `{"id":"msg_duplicate","type":"message","role":"assistant","content":[{"type":"text","text":"answer"}],"stop_reason":"end_turn",` + `"usage":{"input_tokens":"private-marker","input_tokens":7,"output_tokens":3,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}`},
		{"nested usage detail", `{"id":"msg_duplicate","type":"message","role":"assistant","content":[{"type":"text","text":"answer"}],"stop_reason":"end_turn",` + `"usage":{"input_tokens":7,"output_tokens":3,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens_details":{"thinking_tokens":"private-marker","thinking_tokens":2}}}`},
		{"duplicate capture leaf", `{"id":"msg_duplicate","type":"message","role":"assistant","content":[{"type":"text","text":"answer"}],"stop_reason":"end_turn",` + `"usage":{"input_tokens":"private-marker","output_tokens":3},"usage":{"input_tokens":7,"output_tokens":3,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}`},
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
				ProviderIssuer:  extensions.Ref{Kind: "provider-definition", ID: "anthropic", ContractVersion: 1},
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
			if err := (Messages{}).DecodeResponse(context.Background(), response, emit, hooks); err != nil {
				t.Fatal(err)
			}
			if !completed || answer.String() != "answer" || len(observations) == 0 {
				t.Fatal("duplicate rejection changed ordinary answer decoding or completion")
			}
			for _, event := range observations {
				if event.InputTokens == nil || *event.InputTokens != 7 || event.OutputTokens == nil || *event.OutputTokens != 3 {
					t.Fatalf("duplicate rejection changed ordinary counters: %+v", event)
				}
				if tc.name == "nested usage detail" && (event.ReasoningTokens == nil || *event.ReasoningTokens != 2) {
					t.Fatal("duplicate rejection changed the ordinary thinking counter")
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
