package gemini

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

func TestGeminiTerminalErrorsStopJSONAndSSE(t *testing.T) {
	errorJSON := `"error":{"code":400,"status":"INVALID_ARGUMENT","message":"private message","details":[{"@type":"type.googleapis.com/example","reason":"private detail"}]}`
	for _, tc := range []struct {
		name, contentType, body string
	}{
		{"json with candidates before error", "application/json", `{"candidates":[{"content":{"parts":[{"text":"before"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":4,"thoughtsTokenCount":0},` + errorJSON + `}`},
		{"json error without candidates", "application/json", `{"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":4,"thoughtsTokenCount":0},` + errorJSON + `}`},
		{"sse error before text", "text/event-stream", "data: {" + errorJSON + `,"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":4,"thoughtsTokenCount":0}}` + "\n\ndata: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"after\"}]},\"finishReason\":\"STOP\"}]}\n\ndata: [DONE]\n\n"},
		{"sse error after text", "text/event-stream", "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"before\"}]}}]}\n\ndata: {" + errorJSON + `,"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":4,"thoughtsTokenCount":0}}` + "\n\ndata: [DONE]\n\n"},
		{"sse accumulated usage", "text/event-stream", "data: {\"usageMetadata\":{\"promptTokenCount\":11,\"candidatesTokenCount\":4,\"thoughtsTokenCount\":0}}\n\ndata: {" + errorJSON + "}\n\ndata: [DONE]\n\n"},
		{"sse usage survives later content without metadata", "text/event-stream", "data: {\"usageMetadata\":{\"promptTokenCount\":11,\"candidatesTokenCount\":4,\"thoughtsTokenCount\":0}}\n\ndata: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"before\"}]}}]}\n\ndata: {" + errorJSON + "}\n\ndata: [DONE]\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var events []kernel.ResponseEvent
			var providerUsage kernel.UsageEvent
			var completeCalls int
			err := (Gemini{}).DecodeResponse(context.Background(), kernel.UpstreamResponse{
				Status:  http.StatusOK,
				Headers: http.Header{"Content-Type": []string{tc.contentType}},
				Body:    io.NopCloser(strings.NewReader(tc.body)),
			}, func(event kernel.ResponseEvent) error {
				events = append(events, event)
				return nil
			}, kernel.StreamHooks{
				OnEvent:         func(event kernel.ResponseEvent) { events = append(events, event) },
				OnProviderUsage: func(event kernel.UsageEvent) { providerUsage = event },
				OnComplete:      func(kernel.UsageEvent) { completeCalls++ },
			})
			var typed *kernel.ProviderResponseError
			if !errors.As(err, &typed) {
				t.Fatalf("error=%T %v, want *kernel.ProviderResponseError", err, err)
			}
			var envelope struct {
				Error struct {
					Code    int               `json:"code"`
					Details []json.RawMessage `json:"details"`
				} `json:"error"`
			}
			if decodeErr := json.Unmarshal(typed.Data(), &envelope); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			if envelope.Error.Code != 400 || len(envelope.Error.Details) != 1 || !strings.Contains(string(envelope.Error.Details[0]), "private detail") {
				t.Fatalf("provider error evidence lost fields: %s", typed.Data())
			}
			if !geminiCountIs(providerUsage.InputTokens, 11) || !geminiCountIs(providerUsage.OutputTokens, 4) {
				t.Fatalf("provider usage=%+v", providerUsage)
			}
			if err.Error() != "provider reported a terminal response error" {
				t.Fatalf("provider details escaped typed error string: %q", err.Error())
			}
			if completeCalls != 0 {
				t.Fatalf("completion called %d times", completeCalls)
			}
			for _, event := range events {
				if event.Kind == kernel.EventResponseComplete {
					t.Fatal("terminal error was followed by response completion")
				}
				if event.Kind == kernel.EventResponseError && strings.Contains(strings.Join([]string{event.Error.Message, string(event.Error.Code), event.Error.Type}, " "), "private") {
					t.Fatalf("provider error leaked to client event: %+v", event.Error)
				}
			}
		})
	}
}

func TestGeminiHealthyResponseStillCompletes(t *testing.T) {
	body := `{"candidates":[{"content":{"parts":[{"text":"healthy"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"thoughtsTokenCount":0}}`
	var completeCalls int
	err := (Gemini{}).DecodeResponse(context.Background(), kernel.UpstreamResponse{
		Status:  http.StatusOK,
		Headers: http.Header{"Content-Type": []string{"application/json"}},
		Body:    io.NopCloser(strings.NewReader(body)),
	}, func(kernel.ResponseEvent) error { return nil }, kernel.StreamHooks{
		OnComplete: func(usage kernel.UsageEvent) {
			completeCalls++
			if !geminiCountIs(usage.InputTokens, 3) || !geminiCountIs(usage.OutputTokens, 2) {
				t.Errorf("usage=%+v", usage)
			}
		},
	})
	if err != nil || completeCalls != 1 {
		t.Fatalf("healthy response err=%v complete calls=%d", err, completeCalls)
	}
}
