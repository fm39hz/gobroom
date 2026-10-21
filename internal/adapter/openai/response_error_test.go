package openai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

func TestResponsesProviderErrorsStopBeforeRawFrameAndDone(t *testing.T) {
	for _, tc := range []struct{ name, data string }{
		{"error", `{"type":"error","code":"private_code","message":"private detail"}`},
		{"failed", `{"type":"response.failed","response":{"error":{"code":"private_code","message":"private detail"}}}`},
		{"incomplete", `{"type":"response.incomplete","response":{"usage":{"input_tokens":3,"output_tokens":2}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "data: " + tc.data + "\n\ndata: [DONE]\n\n"
			var raws []string
			var complete int
			var providerUsage kernel.UsageEvent
			err := decodeOpenAIResponse(context.Background(), kernel.UpstreamResponse{
				Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body)),
			}, normalize.FormatOpenAIResponses, func(event kernel.ResponseEvent) error {
				if event.Kind == kernel.EventRawFrame {
					raws = append(raws, string(event.Raw))
				}
				if event.Kind == kernel.EventResponseComplete {
					complete++
				}
				return nil
			}, kernel.StreamHooks{OnProviderUsage: func(usage kernel.UsageEvent) { providerUsage = usage }})
			var typed *kernel.ProviderResponseError
			if err == nil || tc.name != "incomplete" && !errors.As(err, &typed) {
				t.Fatalf("expected provider response error; got %T %v", err, err)
			}
			if complete != 0 {
				t.Fatalf("failed response emitted completion %d times", complete)
			}
			if strings.Contains(strings.Join(raws, ""), "private detail") || strings.Contains(strings.Join(raws, ""), "private_code") {
				t.Fatalf("private error payload leaked in raw frames: %q", raws)
			}
			if tc.name == "incomplete" && (providerUsage.InputTokens != 3 || providerUsage.OutputTokens != 2) {
				t.Fatalf("error response usage not preserved: %+v", providerUsage)
			}
		})
	}
}

func TestOpenAIHealthySSEWireRemainsExact(t *testing.T) {
	body := "event: message\r\ndata: {\"choices\":[]}\r\n\r\ndata: [DONE]\r\n\r\n"
	var raws []string
	err := decodeOpenAIResponse(context.Background(), kernel.UpstreamResponse{
		Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body)),
	}, normalize.FormatOpenAIChat, func(event kernel.ResponseEvent) error {
		if event.Kind == kernel.EventRawFrame {
			raws = append(raws, string(event.Raw))
		}
		return nil
	}, kernel.StreamHooks{})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(raws, ""); got != body {
		t.Fatalf("healthy raw wire changed:\n got %q\nwant %q", got, body)
	}
}

func TestFailedJSONProviderErrorKeepsUsage(t *testing.T) {
	body := `{"status":"failed","error":{"code":"private_code","message":"private detail","usage":{"input_tokens":7,"output_tokens":4}}}`
	var usage kernel.UsageEvent
	err := decodeOpenAIResponse(context.Background(), kernel.UpstreamResponse{
		Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)),
	}, normalize.FormatOpenAIResponses, func(kernel.ResponseEvent) error { return nil }, kernel.StreamHooks{OnProviderUsage: func(event kernel.UsageEvent) { usage = event }})
	var typed *kernel.ProviderResponseError
	if !errors.As(err, &typed) {
		t.Fatalf("expected typed provider response error; got %T %v", err, err)
	}
	if usage.InputTokens != 7 || usage.OutputTokens != 4 {
		t.Fatalf("usage not preserved: %+v", usage)
	}
}
