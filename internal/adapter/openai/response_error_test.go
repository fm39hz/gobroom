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
			if tc.name == "incomplete" && (providerUsage.InputTokens == nil || *providerUsage.InputTokens != 3 || providerUsage.OutputTokens == nil || *providerUsage.OutputTokens != 2) {
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
	if usage.InputTokens == nil || *usage.InputTokens != 7 || usage.OutputTokens == nil || *usage.OutputTokens != 4 {
		t.Fatalf("usage not preserved: %+v", usage)
	}
}

func TestOpenAIUsageJSONPresenceAndZero(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		usage                 string
		wantInput, wantOutput *int64
	}{
		{name: "missing", usage: `{}`, wantInput: nil, wantOutput: nil},
		{name: "null", usage: `{"input_tokens":null,"output_tokens":null}`, wantInput: nil, wantOutput: nil},
		{name: "zero", usage: `{"input_tokens":0,"output_tokens":0}`, wantInput: new(int64(0)), wantOutput: new(int64(0))},
		{name: "large exact integer", usage: `{"input_tokens":9007199254740993,"output_tokens":0}`, wantInput: new(int64(9007199254740993)), wantOutput: new(int64(0))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, streaming := range []bool{false, true} {
				body := `{"id":"resp","object":"response","status":"completed","output":[],"usage":` + tc.usage + `}`
				contentType := "application/json"
				if streaming {
					body = "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp\",\"usage\":" + tc.usage + "}}\n\n"
					contentType = "text/event-stream"
				}
				var got *kernel.UsageEvent
				err := decodeOpenAIResponse(context.Background(), kernel.UpstreamResponse{
					Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body)),
				}, normalize.FormatOpenAIResponses, func(event kernel.ResponseEvent) error {
					if event.Kind == kernel.EventUsage && event.Usage != nil {
						got = event.Usage
					}
					return nil
				}, kernel.StreamHooks{})
				if err != nil {
					t.Fatalf("streaming=%v: %v", streaming, err)
				}
				if got == nil || !sameOptionalInt(got.InputTokens, tc.wantInput) || !sameOptionalInt(got.OutputTokens, tc.wantOutput) {
					t.Fatalf("streaming=%v usage=%+v, want input=%v output=%v", streaming, got, tc.wantInput, tc.wantOutput)
				}
			}
		})
	}
}

func sameOptionalInt(got, want *int64) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	return *got == *want
}
