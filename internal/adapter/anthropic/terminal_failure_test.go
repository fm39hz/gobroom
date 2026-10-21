package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/kernel"
)

func TestAnthropicTerminalErrorEvidence(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"before text", `data: {"type":"error","error":{"type":"overloaded_error","message":"busy","private":"do-not-leak"}}` + "\n\n" + `data: {"type":"message_stop"}` + "\n\n"},
		{"after text and usage", "data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"partial\"}}\n\n" +
			"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":7}}\n\n" +
			`data: {"type":"error","error":{"type":"overloaded_error","message":"busy","private":"do-not-leak"}}` + "\n\n" + `data: {"type":"message_stop"}` + "\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			var gotUsage kernel.UsageEvent
			completed := false
			err := (Messages{}).DecodeResponse(context.Background(), kernel.UpstreamResponse{
				Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(tc.body)),
			}, func(event kernel.ResponseEvent) error {
				if event.Kind == kernel.EventRawFrame {
					out.Write(event.Raw)
				}
				return nil
			}, kernel.StreamHooks{
				OnProviderUsage: func(usage kernel.UsageEvent) { gotUsage = usage },
				OnComplete:      func(kernel.UsageEvent) { completed = true },
			})
			var evidence *kernel.ProviderResponseError
			if !errors.As(err, &evidence) {
				t.Fatalf("DecodeResponse error = %v, want ProviderResponseError", err)
			}
			if strings.Contains(out.String(), "do-not-leak") || strings.Contains(out.String(), `"type":"error"`) {
				t.Fatalf("raw provider error leaked through passthrough: %s", out.String())
			}
			var got struct {
				Error map[string]json.RawMessage `json:"error"`
			}
			if err := json.Unmarshal(evidence.Data(), &got); err != nil || string(got.Error["private"]) != `"do-not-leak"` {
				t.Fatalf("error evidence did not preserve nested provider object: %s (%v)", evidence.Data(), err)
			}
			if completed {
				t.Fatal("terminal error invoked OnComplete")
			}
			wantOutput := int64(0)
			if tc.name == "after text and usage" {
				wantOutput = 7
			}
			if gotUsage.OutputTokens != wantOutput {
				t.Fatalf("reported usage = %+v, want output tokens %d", gotUsage, wantOutput)
			}
		})
	}
}

func TestAnthropicJSONTerminalErrorAndStreamCompletion(t *testing.T) {
	t.Run("error object without outer type", func(t *testing.T) {
		err := (Messages{}).DecodeResponse(context.Background(), kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"type":"api_error","message":"private-detail"}}`))}, func(kernel.ResponseEvent) error { return nil }, kernel.StreamHooks{})
		var evidence *kernel.ProviderResponseError
		if !errors.As(err, &evidence) || strings.Contains(err.Error(), "private-detail") {
			t.Fatalf("untyped error object was not intercepted: %v", err)
		}
	})
	t.Run("known input usage survives incomplete stream", func(t *testing.T) {
		var usage kernel.UsageEvent
		body := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"partial\",\"usage\":{\"input_tokens\":9,\"output_tokens\":0}}}\n\n"
		err := (Messages{}).DecodeResponse(context.Background(), kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}, func(kernel.ResponseEvent) error { return nil }, kernel.StreamHooks{OnProviderUsage: func(event kernel.UsageEvent) { usage = event }})
		if !errors.Is(err, io.ErrUnexpectedEOF) || usage.InputTokens != 9 {
			t.Fatalf("incomplete stream lost usage: err=%v usage=%#v", err, usage)
		}
	})
	t.Run("2xx JSON error", func(t *testing.T) {
		var completed bool
		err := (Messages{}).DecodeResponse(context.Background(), kernel.UpstreamResponse{
			Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"api_error","message":"failed","private":"secret"}}`)),
		}, func(kernel.ResponseEvent) error { return nil }, kernel.StreamHooks{OnComplete: func(kernel.UsageEvent) { completed = true }})
		var evidence *kernel.ProviderResponseError
		if !errors.As(err, &evidence) || completed || strings.Contains(err.Error(), "secret") {
			t.Fatalf("DecodeResponse error = %v, complete = %v", err, completed)
		}
	})

	t.Run("healthy exact raw frame and stop", func(t *testing.T) {
		const frame = "data: {\"type\":\"message_stop\"}\n\n"
		var raw strings.Builder
		var completed bool
		err := (Messages{}).DecodeResponse(context.Background(), kernel.UpstreamResponse{
			Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(frame)),
		}, func(event kernel.ResponseEvent) error {
			if event.Kind == kernel.EventRawFrame {
				raw.Write(event.Raw)
			}
			return nil
		}, kernel.StreamHooks{OnComplete: func(kernel.UsageEvent) { completed = true }})
		if err != nil || raw.String() != frame || !completed {
			t.Fatalf("DecodeResponse err=%v raw=%q complete=%v", err, raw.String(), completed)
		}
	})

	t.Run("incomplete stream fails closed", func(t *testing.T) {
		var completed bool
		err := (Messages{}).DecodeResponse(context.Background(), kernel.UpstreamResponse{
			Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"message_start\"}\n\n")),
		}, func(kernel.ResponseEvent) error { return nil }, kernel.StreamHooks{OnComplete: func(kernel.UsageEvent) { completed = true }})
		if err == nil || completed {
			t.Fatalf("DecodeResponse error = %v, complete = %v; want incomplete stream failure", err, completed)
		}
	})

}
