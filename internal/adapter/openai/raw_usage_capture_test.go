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

func TestOpenAIRawUsageCapturePreservesSelectedJSONAndSSEUsage(t *testing.T) {
	const usage = `{"input_tokens":9007199254740993,"output_tokens":4,"input_tokens_details":{"cached_tokens":2},"output_tokens_details":{"reasoning_tokens":1},"cost":9007199254740993.0123456789,"currency":"EUR"}`
	for _, tc := range []struct {
		name, contentType, body string
	}{
		{"json", "application/json", `{"id":"resp_raw","object":"response","status":"completed","output":[],"usage":` + usage + `}`},
		{"sse", "text/event-stream", "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_raw\",\"status\":\"completed\",\"output\":[],\"usage\":" + usage + "}}\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var observed, completed kernel.UsageEvent
			err := decodeOpenAIResponse(context.Background(), kernel.UpstreamResponse{
				Status: http.StatusOK, Headers: http.Header{"Content-Type": {tc.contentType}}, Body: io.NopCloser(strings.NewReader(tc.body)),
			}, normalize.FormatOpenAIResponses, func(event kernel.ResponseEvent) error {
				if event.Kind == kernel.EventUsage && event.Usage != nil {
					observed = event.Usage.Clone()
				}
				return nil
			}, kernel.StreamHooks{
				UsageCapture:   kernel.UsageCapturePolicy{Enabled: true, MaxBytes: 4096},
				ProviderIssuer: extensions.Ref{Kind: "provider-definition", ID: "openai", ContractVersion: 1},
				OnComplete:     func(event kernel.UsageEvent) { completed = event },
			})
			if err != nil {
				t.Fatal(err)
			}
			for label, event := range map[string]kernel.UsageEvent{"usage event": observed, "complete": completed} {
				if event.RawUsage == nil || !json.Valid(event.RawUsage.Value) || string(event.RawUsage.Value) != usage {
					t.Fatalf("%s callback lost original selected usage JSON: %#v", label, event.RawUsage)
				}
				if event.RawUsage.ProviderDefinition.ID != "openai" || event.RawUsage.API != "openai.responses" || event.InputTokens == nil || *event.InputTokens != 9007199254740993 {
					t.Fatalf("%s callback lost issuer or exact counter accounting: %+v", label, event)
				}
			}
		})
	}
}

func TestOpenAIChatRawUsageCaptureUsesChatAPIIdentity(t *testing.T) {
	const usage = `{"prompt_tokens":9007199254740993,"completion_tokens":4,"total_tokens":9007199254740997,"prompt_tokens_details":{"cached_tokens":2,"cache_write_tokens":1},"completion_tokens_details":{"reasoning_tokens":3},"cost":0.125,"currency":"USD"}`
	body := `{"id":"chat_raw","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":` + usage + `}`
	var completed kernel.UsageEvent
	err := decodeOpenAIResponse(context.Background(), kernel.UpstreamResponse{
		Status: http.StatusOK, Headers: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)),
	}, normalize.FormatOpenAIChat, func(kernel.ResponseEvent) error { return nil }, kernel.StreamHooks{
		UsageCapture:   kernel.UsageCapturePolicy{Enabled: true, MaxBytes: 4096},
		ProviderIssuer: extensions.Ref{Kind: "provider-definition", ID: "openai", ContractVersion: 1},
		OnComplete:     func(event kernel.UsageEvent) { completed = event },
	})
	if err != nil {
		t.Fatal(err)
	}
	if completed.RawUsage == nil || string(completed.RawUsage.Value) != usage || completed.RawUsage.API != "openai.chat" {
		t.Fatalf("OpenAI Chat capture lost original usage or canonical API identity: %#v", completed.RawUsage)
	}
	if completed.InputTokens == nil || *completed.InputTokens != 9007199254740993 || completed.OutputTokens == nil || *completed.OutputTokens != 4 {
		t.Fatalf("OpenAI Chat capture changed common counters: %+v", completed)
	}
}

func TestOpenAIRawUsageCaptureRejectsUnrecognizedPrivateShape(t *testing.T) {
	const usage = `{"input_tokens":7,"output_tokens":3,"provider_private":"private-marker","new_nested":{"safe-looking":1}}`
	body := `{"id":"resp_raw","object":"response","status":"completed","output":[],"usage":` + usage + `}`
	var completed kernel.UsageEvent
	err := decodeOpenAIResponse(context.Background(), kernel.UpstreamResponse{
		Status: http.StatusOK, Headers: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)),
	}, normalize.FormatOpenAIResponses, func(kernel.ResponseEvent) error { return nil }, kernel.StreamHooks{
		UsageCapture:   kernel.UsageCapturePolicy{Enabled: true, MaxBytes: 4096},
		ProviderIssuer: extensions.Ref{Kind: "provider-definition", ID: "openai", ContractVersion: 1},
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

func TestOpenAIRawUsageCaptureDisabledAddsNoRawOrIssue(t *testing.T) {
	body := `{"id":"resp_raw","object":"response","status":"completed","output":[],"usage":{"input_tokens":7,"output_tokens":3,"provider_private":"private-marker"}}`
	var completed kernel.UsageEvent
	err := decodeOpenAIResponse(context.Background(), kernel.UpstreamResponse{
		Status: http.StatusOK, Headers: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)),
	}, normalize.FormatOpenAIResponses, func(kernel.ResponseEvent) error { return nil }, kernel.StreamHooks{
		OnComplete: func(event kernel.UsageEvent) { completed = event },
	})
	if err != nil {
		t.Fatal(err)
	}
	if completed.RawUsage != nil || len(completed.AccountingIssues) != 0 {
		t.Fatalf("disabled capture retained bytes or emitted capture diagnostics: %+v", completed)
	}
}
