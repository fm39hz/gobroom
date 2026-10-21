package renderers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

func TestCommittedSemanticStreamsProjectTerminalErrorOnce(t *testing.T) {
	tests := []struct {
		name                 string
		renderer             kernel.ResponseRenderer
		client               normalize.Format
		prior                kernel.ResponseEvent
		wantError, forbidden string
	}{
		{"chat", OpenAIChat{}, normalize.FormatOpenAIChat, kernel.ResponseEvent{Kind: kernel.EventTextDelta, Text: "before"}, `"type":"upstream_error"`, "[DONE]"},
		{"responses", OpenAIResponses{}, normalize.FormatOpenAIResponses, kernel.ResponseEvent{Kind: kernel.EventTextDelta, Text: "before"}, `event: response.failed`, "response.completed"},
		{"anthropic", AnthropicMessages{}, normalize.FormatAnthropic, kernel.ResponseEvent{Kind: kernel.EventTextDelta, Text: "before"}, `event: error`, "event: message_stop"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			options := kernel.ResponseRenderContext{Status: 200, ClientFormat: tt.client, ProviderFormat: normalize.FormatOpenAIChat, Streaming: true, ProviderStreaming: true, SemanticTransformActive: true}
			writer := httptest.NewRecorder()
			session, err := tt.renderer.Begin(context.Background(), options, writer)
			if err != nil {
				t.Fatal(err)
			}
			if err := session.Emit(context.Background(), tt.prior); err != nil {
				t.Fatal(err)
			}
			failure := kernel.ResponseEvent{Kind: kernel.EventResponseError, Error: "upstream said \"no\"\\again"}
			if err := session.Emit(context.Background(), failure); err != nil {
				t.Fatal(err)
			}
			if err := session.Emit(context.Background(), failure); err != nil {
				t.Fatal(err)
			}
			if err := session.Finish(context.Background(), nil); err == nil {
				t.Fatal("Finish accepted terminal response error")
			}
			body := writer.Body.String()
			if !strings.Contains(body, "before") || !strings.Contains(body, tt.wantError) {
				t.Fatalf("prior data or protocol error missing: %s", body)
			}
			if strings.Count(body, tt.wantError) != 1 {
				t.Fatalf("error projected more than once: %s", body)
			}
			if strings.Contains(body, tt.forbidden) {
				t.Fatalf("success completion emitted after error: %s", body)
			}
			for _, line := range strings.Split(body, "\n") {
				if strings.HasPrefix(line, "data: {") && strings.Contains(line, "error") {
					var decoded any
					if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &decoded); err != nil {
						t.Fatalf("invalid JSON error frame %q: %v", line, err)
					}
				}
			}
		})
	}
}

func TestTerminalErrorBeforeCommitWritesNothing(t *testing.T) {
	options := kernel.ResponseRenderContext{Status: 200, ClientFormat: normalize.FormatOpenAIChat, ProviderFormat: normalize.FormatOpenAIChat, Streaming: true, ProviderStreaming: true}
	writer := httptest.NewRecorder()
	session, err := (OpenAIChat{}).Begin(context.Background(), options, writer)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Emit(context.Background(), kernel.ResponseEvent{Kind: kernel.EventResponseError, Error: "failed"}); err != nil {
		t.Fatal(err)
	}
	if err := session.Finish(context.Background(), nil); err == nil {
		t.Fatal("Finish accepted terminal response error")
	}
	if writer.Body.Len() != 0 {
		t.Fatalf("precommit error wrote a response: %q", writer.Body.String())
	}
}

func TestNativePassthroughProjectsTerminalErrorOnce(t *testing.T) {
	tests := []struct {
		name     string
		renderer kernel.ResponseRenderer
		format   normalize.Format
		want     string
	}{
		{"chat", OpenAIChat{}, normalize.FormatOpenAIChat, `"type":"upstream_error"`},
		{"responses", OpenAIResponses{}, normalize.FormatOpenAIResponses, `event: response.failed`},
		{"anthropic", AnthropicMessages{}, normalize.FormatAnthropic, `event: error`},
		{"wire responses", WirePassthrough{Format: normalize.FormatOpenAIResponses}, normalize.FormatOpenAIResponses, `event: response.failed`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			options := kernel.ResponseRenderContext{Status: 200, ClientFormat: tt.format, ProviderFormat: tt.format, Streaming: true, ProviderStreaming: true}
			writer := httptest.NewRecorder()
			session, err := tt.renderer.Begin(context.Background(), options, writer)
			if err != nil {
				t.Fatal(err)
			}
			if err := session.Emit(context.Background(), kernel.ResponseEvent{Kind: kernel.EventRawFrame, WireFormat: tt.format, Raw: []byte("data: prior\n\n")}); err != nil {
				t.Fatal(err)
			}
			event := kernel.ResponseEvent{Kind: kernel.EventResponseError, Error: "failed"}
			if err := session.Emit(context.Background(), event); err != nil {
				t.Fatal(err)
			}
			if err := session.Emit(context.Background(), event); err != nil {
				t.Fatal(err)
			}
			if err := session.Finish(context.Background(), nil); err == nil {
				t.Fatal("Finish accepted terminal response error")
			}
			body := writer.Body.String()
			if !strings.Contains(body, "prior") || strings.Count(body, tt.want) != 1 {
				t.Fatalf("unexpected passthrough error stream: %s", body)
			}
			if strings.Contains(body, "[DONE]") || strings.Contains(body, "response.completed") || strings.Contains(body, "message_stop") {
				t.Fatalf("success completion emitted after error: %s", body)
			}
		})
	}
}

func TestWirePassthroughHealthyBytesRemainExact(t *testing.T) {
	for _, format := range []normalize.Format{normalize.FormatOpenAIChat, normalize.FormatOpenAIResponses, normalize.FormatAnthropic} {
		t.Run(string(format), func(t *testing.T) {
			options := kernel.ResponseRenderContext{Status: 200, ClientFormat: format, ProviderFormat: format, Streaming: true, ProviderStreaming: true}
			writer := httptest.NewRecorder()
			session, err := (WirePassthrough{Format: format}).Begin(context.Background(), options, writer)
			if err != nil {
				t.Fatal(err)
			}
			raw := []byte("data: {\"ok\":true}\n\n")
			if err := session.Emit(context.Background(), kernel.ResponseEvent{Kind: kernel.EventRawFrame, WireFormat: format, Raw: raw}); err != nil {
				t.Fatal(err)
			}
			if err := session.Finish(context.Background(), nil); err != nil {
				t.Fatal(err)
			}
			if got := writer.Body.String(); got != string(raw) {
				t.Fatalf("raw passthrough changed: %q", got)
			}
		})
	}
}

func TestFinishDecodeErrorRemainsFailure(t *testing.T) {
	options := kernel.ResponseRenderContext{Status: 200, ClientFormat: normalize.FormatOpenAIChat, ProviderFormat: normalize.FormatOpenAIChat, Streaming: true, ProviderStreaming: true}
	writer := httptest.NewRecorder()
	session, err := (OpenAIChat{}).Begin(context.Background(), options, writer)
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("decode failed")
	if got := session.Finish(context.Background(), want); !errors.Is(got, want) {
		t.Fatalf("Finish error = %v, want %v", got, want)
	}
}
