package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/adapter/usagewire"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

func TestOpenAIUsageSnapshotOwnsReportedChargeAcrossCallbacks(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body string
	}{
		{name: "JSON", contentType: "application/json", body: `{"object":"response","id":"r","status":"completed","output":[],"usage":{"cost":0.125}}`},
		{name: "SSE", contentType: "text/event-stream", body: "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"usage\":{\"cost\":0.125}}}\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var completed kernel.UsageEvent
			usageEvents := 0
			err := decodeOpenAIResponse(context.Background(), kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": {tc.contentType}}, Body: io.NopCloser(strings.NewReader(tc.body))}, normalize.FormatOpenAIResponses, func(event kernel.ResponseEvent) error {
				if event.Kind == kernel.EventUsage && event.Usage != nil && len(event.Usage.Extensions) > 0 {
					usageEvents++
					event.Usage.Extensions[0].Value[0] = 'X'
				}
				return nil
			}, kernel.StreamHooks{OnComplete: func(event kernel.UsageEvent) { completed = event }})
			if err != nil {
				t.Fatal(err)
			}
			if usageEvents == 0 {
				t.Fatal("expected a usage event to exercise callback mutation")
			}
			if len(completed.Extensions) != 1 {
				t.Fatalf("charge disappeared: %#v", completed)
			}
			var charge usagewire.ReportedCharge
			if err := json.Unmarshal(completed.Extensions[0].Value, &charge); err != nil || charge.Amount != "0.125" {
				t.Fatalf("client observation mutated original accounting: %q error=%v", completed.Extensions[0].Value, err)
			}
		})
	}
}
