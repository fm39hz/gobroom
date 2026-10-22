package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

func TestOpenAIIssueOnlyUsagePreservesProviderError(t *testing.T) {
	body := `{"status":"failed","error":{"code":"private-code","message":"private-marker","usage":{"input_tokens":"private-marker"}}}`
	var received []kernel.UsageEvent
	err := decodeOpenAIResponse(context.Background(), kernel.UpstreamResponse{
		Status:  http.StatusOK,
		Headers: http.Header{"Content-Type": []string{"application/json"}},
		Body:    io.NopCloser(strings.NewReader(body)),
	}, normalize.FormatOpenAIResponses, func(kernel.ResponseEvent) error { return nil }, kernel.StreamHooks{
		OnProviderUsage: func(usage kernel.UsageEvent) { received = append(received, usage) },
	})
	var providerError *kernel.ProviderResponseError
	if !errors.As(err, &providerError) {
		t.Fatalf("expected original provider error classification, got %T", err)
	}
	if len(received) != 1 {
		t.Fatalf("expected issue-only provider usage callback, got %d", len(received))
	}
	usage := received[0]
	if usage.InputTokens != nil || usage.OutputTokens != nil || len(usage.AccountingIssues) == 0 {
		t.Fatalf("expected unknown counters with accounting issue: %+v", usage)
	}
	for _, issue := range usage.AccountingIssues {
		if issue.Code != "invalid_usage_counter" {
			t.Fatalf("unexpected accounting issue code: %q", issue.Code)
		}
	}
	encoded, marshalErr := json.Marshal(usage.AccountingIssues)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), "cannot unmarshal") {
		t.Fatalf("issue-only evidence contains private provider data: %s", encoded)
	}
}

func TestOpenAIResponsesTerminalUsageIsFullAccumulatedSnapshot(t *testing.T) {
	body := "data: {\"type\":\"response.output_item.added\",\"usage\":{\"input_tokens\":11}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_usage\",\"status\":\"completed\",\"usage\":{\"output_tokens\":6}}}\n\n"
	var final kernel.UsageEvent
	err := decodeOpenAIResponse(context.Background(), kernel.UpstreamResponse{
		Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body)),
	}, normalize.FormatOpenAIResponses, func(kernel.ResponseEvent) error { return nil }, kernel.StreamHooks{
		OnComplete: func(usage kernel.UsageEvent) { final = usage },
	})
	if err != nil {
		t.Fatal(err)
	}
	if final.UpdateKind != kernel.UsageFullSnapshot {
		t.Fatalf("terminal accumulated usage must be a full snapshot, got update kind %q", final.UpdateKind)
	}
	if final.InputTokens == nil || *final.InputTokens != 11 || final.OutputTokens == nil || *final.OutputTokens != 6 {
		t.Fatalf("terminal snapshot must retain accumulated known counters exactly once: %+v", final)
	}
	if final.CachedReadTokens != nil || final.CachedWriteTokens != nil || final.ReasoningTokens != nil {
		t.Fatalf("terminal snapshot fabricated unreported counters: %+v", final)
	}
}

func TestOpenAIOptionalInvalidCounterPreservesAnswerAndValidUsage(t *testing.T) {
	for _, wire := range []string{"json", "sse"} {
		for _, counter := range []struct {
			name string
			raw  string
		}{
			{name: "string", raw: `"private-marker"`},
			{name: "negative", raw: `-1`},
			{name: "fraction", raw: `1.5`},
		} {
			t.Run(wire+"/"+counter.name, func(t *testing.T) {
				const answer = "valid answer private-body-marker"
				usageJSON := `{"input_tokens":` + counter.raw + `,"output_tokens":7}`
				body := `{"object":"response","id":"resp_accounting","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"` + answer + `"}]}],"usage":` + usageJSON + `}`
				contentType := "application/json"
				if wire == "sse" {
					contentType = "text/event-stream"
					body = "data: {\"type\":\"response.output_text.delta\",\"response_id\":\"resp_accounting\",\"delta\":\"" + answer + "\"}\n\n" +
						"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_accounting\",\"status\":\"completed\",\"usage\":" + usageJSON + "}}\n\n"
				}
				var text strings.Builder
				var usages []kernel.UsageEvent
				completed, onComplete := 0, 0
				err := decodeOpenAIResponse(context.Background(), kernel.UpstreamResponse{
					Status:  http.StatusOK,
					Headers: http.Header{"Content-Type": []string{contentType}},
					Body:    io.NopCloser(strings.NewReader(body)),
				}, normalize.FormatOpenAIResponses, func(event kernel.ResponseEvent) error {
					if event.Kind == kernel.EventTextDelta {
						text.WriteString(event.Text)
					}
					if event.Kind == kernel.EventResponseComplete {
						completed++
					}
					if event.Usage != nil {
						usages = append(usages, *event.Usage)
					}
					return nil
				}, kernel.StreamHooks{OnComplete: func(usage kernel.UsageEvent) {
					onComplete++
					if usage.Status != "ok" {
						t.Errorf("optional accounting failure changed completion status: %q", usage.Status)
					}
					usages = append(usages, usage)
				}})
				if err != nil {
					t.Fatalf("optional accounting failure rejected valid answer: %v", err)
				}
				if text.String() != answer || completed == 0 || onComplete != 1 {
					t.Fatalf("answer did not complete: text=%q completed=%d onComplete=%d", text.String(), completed, onComplete)
				}
				if len(usages) < 2 {
					t.Fatalf("expected usage event and completion usage, got %d", len(usages))
				}
				for _, usage := range usages {
					if usage.InputTokens != nil || usage.OutputTokens == nil || *usage.OutputTokens != 7 {
						t.Fatalf("invalid input must be unknown and valid output preserved: %+v", usage)
					}
					if len(usage.AccountingIssues) == 0 {
						t.Fatal("invalid optional counter did not produce accounting evidence")
					}
					for _, issue := range usage.AccountingIssues {
						if issue.Code != "invalid_usage_counter" {
							t.Errorf("expected constant sanitized issue code, got %q", issue.Code)
						}
					}
					encoded, marshalErr := json.Marshal(usage.AccountingIssues)
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					for _, private := range []string{"private-marker", "private-body-marker", "cannot unmarshal", "usage counter must be", body} {
						if strings.Contains(string(encoded), private) {
							t.Fatalf("accounting issue contains provider body or decoder error detail: %s", encoded)
						}
					}
				}
			})
		}
	}
}
