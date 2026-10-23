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

func TestAnthropicRawUsageCapturePreservesSelectedJSONAndSSEUsage(t *testing.T) {
	const usage = `{"input_tokens":9007199254740990,"output_tokens":4,"cache_read_input_tokens":2,"cache_creation_input_tokens":1,"output_tokens_details":{"thinking_tokens":3}}`
	for _, tc := range []struct {
		name, contentType, body string
	}{
		{"json", "application/json", `{"id":"msg_raw","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":` + usage + `}`},
		{"sse", "text/event-stream", "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":9007199254740990,\"cache_read_input_tokens\":2,\"cache_creation_input_tokens\":1}}}\n\n" +
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":4,\"output_tokens_details\":{\"thinking_tokens\":3}}}\n\n" +
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var provider, completed kernel.UsageEvent
			err := (Messages{}).DecodeResponse(context.Background(), kernel.UpstreamResponse{
				Status: http.StatusOK, Headers: http.Header{"Content-Type": {tc.contentType}}, Body: io.NopCloser(strings.NewReader(tc.body)),
			}, func(kernel.ResponseEvent) error { return nil }, kernel.StreamHooks{
				UsageCapture:    kernel.UsageCapturePolicy{Enabled: true, MaxBytes: 4096},
				ProviderIssuer:  extensions.Ref{Kind: "provider-definition", ID: "anthropic", ContractVersion: 1},
				OnProviderUsage: func(event kernel.UsageEvent) { provider = event },
				OnComplete:      func(event kernel.UsageEvent) { completed = event },
			})
			if err != nil {
				t.Fatal(err)
			}
			for label, event := range map[string]kernel.UsageEvent{"provider": provider, "complete": completed} {
				if event.RawUsage == nil || len(event.RawUsage.Value) == 0 || !json.Valid(event.RawUsage.Value) {
					t.Fatalf("%s callback lacks selected raw usage JSON: %#v", label, event.RawUsage)
				}
				if event.RawUsage.ProviderDefinition.ID != "anthropic" || event.RawUsage.API != "anthropic.messages" || event.InputTokens == nil || *event.InputTokens != 9007199254740993 {
					t.Fatalf("%s callback lost issuer or exact counter accounting: %+v", label, event)
				}
			}
			if tc.name == "json" && string(completed.RawUsage.Value) != usage {
				t.Fatalf("JSON raw usage differs from original selected object: %s", completed.RawUsage.Value)
			}
		})
	}
}

func TestAnthropicRawUsageCaptureRejectsUnrecognizedPrivateShape(t *testing.T) {
	const usage = `{"input_tokens":7,"output_tokens":3,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"provider_private":"private-marker","new_nested":{"safe-looking":1}}`
	body := `{"id":"msg_raw","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":` + usage + `}`
	var completed kernel.UsageEvent
	err := (Messages{}).DecodeResponse(context.Background(), kernel.UpstreamResponse{
		Status: http.StatusOK, Headers: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)),
	}, func(kernel.ResponseEvent) error { return nil }, kernel.StreamHooks{
		UsageCapture:   kernel.UsageCapturePolicy{Enabled: true, MaxBytes: 4096},
		ProviderIssuer: extensions.Ref{Kind: "provider-definition", ID: "anthropic", ContractVersion: 1},
		OnComplete:     func(event kernel.UsageEvent) { completed = event },
	})
	if err != nil {
		t.Fatal(err)
	}
	if completed.RawUsage != nil || completed.InputTokens == nil || *completed.InputTokens != 7 || completed.OutputTokens == nil || *completed.OutputTokens != 3 {
		t.Fatalf("unsafe raw shape affected common usage or was retained: %+v", completed)
	}
	if len(completed.AccountingIssues) == 0 {
		t.Fatal("unsafe raw usage shape was omitted without a diagnostic")
	}
	for _, issue := range completed.AccountingIssues {
		if issue.Code != "invalid_raw_usage" {
			t.Fatalf("unsafe raw issue contains a nonconstant code: %q", issue.Code)
		}
	}
	encoded, err := json.Marshal(completed.AccountingIssues)
	if err != nil || strings.Contains(string(encoded), "private-marker") || strings.Contains(string(encoded), "provider_private") {
		t.Fatalf("raw shape leaked through diagnostics: %s (%v)", encoded, err)
	}
}

func TestAnthropicRawUsageCaptureDisabledAddsNoRawOrIssue(t *testing.T) {
	body := `{"id":"msg_raw","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":7,"output_tokens":3,"provider_private":"private-marker"}}`
	var completed kernel.UsageEvent
	err := (Messages{}).DecodeResponse(context.Background(), kernel.UpstreamResponse{
		Status: http.StatusOK, Headers: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)),
	}, func(kernel.ResponseEvent) error { return nil }, kernel.StreamHooks{
		OnComplete: func(event kernel.UsageEvent) { completed = event },
	})
	if err != nil {
		t.Fatal(err)
	}
	if completed.RawUsage != nil || len(completed.AccountingIssues) != 0 {
		t.Fatalf("disabled capture retained bytes or emitted capture diagnostics: %+v", completed)
	}
}
