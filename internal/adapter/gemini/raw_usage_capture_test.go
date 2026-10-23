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

func TestGeminiRawUsageCapturePreservesSelectedJSONAndSSEUsage(t *testing.T) {
	const usage = `{"promptTokenCount":9007199254740993,"candidatesTokenCount":4,"totalTokenCount":9007199254740997,"cachedContentTokenCount":2,"thoughtsTokenCount":1}`
	for _, tc := range []struct {
		name, contentType, body string
	}{
		{"json", "application/json", `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":` + usage + `}`},
		{"sse", "text/event-stream", "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":" + usage + "}\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var provider, completed kernel.UsageEvent
			err := (Gemini{}).DecodeResponse(context.Background(), kernel.UpstreamResponse{
				Status: http.StatusOK, Headers: http.Header{"Content-Type": {tc.contentType}}, Body: io.NopCloser(strings.NewReader(tc.body)),
			}, func(kernel.ResponseEvent) error { return nil }, kernel.StreamHooks{
				UsageCapture:    kernel.UsageCapturePolicy{Enabled: true, MaxBytes: 4096},
				ProviderIssuer:  extensions.Ref{Kind: "provider-definition", ID: "gemini", ContractVersion: 1},
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
				if event.RawUsage.ProviderDefinition.ID != "gemini" || event.RawUsage.API != "gemini.generate-content" || event.InputTokens == nil || *event.InputTokens != 9007199254740993 {
					t.Fatalf("%s callback lost issuer or exact counter accounting: %+v", label, event)
				}
			}
			if tc.name == "json" && string(completed.RawUsage.Value) != usage {
				t.Fatalf("JSON raw usage differs from original selected object: %s", completed.RawUsage.Value)
			}
		})
	}
}

func TestGeminiRawUsageCaptureRejectsUnrecognizedPrivateShape(t *testing.T) {
	const usage = `{"promptTokenCount":7,"candidatesTokenCount":3,"totalTokenCount":10,"providerPrivate":"private-marker","newNested":{"safeLooking":1}}`
	body := `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":` + usage + `}`
	var completed kernel.UsageEvent
	err := (Gemini{}).DecodeResponse(context.Background(), kernel.UpstreamResponse{
		Status: http.StatusOK, Headers: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)),
	}, func(kernel.ResponseEvent) error { return nil }, kernel.StreamHooks{
		UsageCapture:   kernel.UsageCapturePolicy{Enabled: true, MaxBytes: 4096},
		ProviderIssuer: extensions.Ref{Kind: "provider-definition", ID: "gemini", ContractVersion: 1},
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
	if err != nil || strings.Contains(string(encoded), "private-marker") || strings.Contains(string(encoded), "providerPrivate") {
		t.Fatalf("raw shape leaked through diagnostics: %s (%v)", encoded, err)
	}
}

func TestGeminiRawUsageCaptureDisabledAddsNoRawOrIssue(t *testing.T) {
	body := `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3,"totalTokenCount":10,"providerPrivate":"private-marker"}}`
	var completed kernel.UsageEvent
	err := (Gemini{}).DecodeResponse(context.Background(), kernel.UpstreamResponse{
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
