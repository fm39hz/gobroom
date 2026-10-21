package renderers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

type chatUsageFrame struct {
	Choices []struct {
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
		TotalTokens      int64 `json:"total_tokens"`
	} `json:"usage"`
}

func decodeChatUsageFrames(t *testing.T, body string) []chatUsageFrame {
	t.Helper()
	var frames []chatUsageFrame
	for _, block := range strings.Split(body, "\n\n") {
		if !strings.HasPrefix(block, "data: ") {
			continue
		}
		data := strings.TrimPrefix(block, "data: ")
		if data == "[DONE]" {
			continue
		}
		var frame chatUsageFrame
		if err := json.Unmarshal([]byte(data), &frame); err != nil {
			t.Fatalf("decode Chat SSE frame %q: %v", data, err)
		}
		frames = append(frames, frame)
	}
	return frames
}

func TestOpenAIChatSemanticStreamIncludesUsageOnlyChunk(t *testing.T) {
	options := kernel.ResponseRenderContext{
		Status: 200, ClientFormat: normalize.FormatOpenAIChat, ProviderFormat: normalize.FormatOpenAIResponses,
		Streaming: true, ProviderStreaming: true, IncludeUsage: true,
		ProviderEvents: []kernel.ResponseEventKind{kernel.EventTextDelta, kernel.EventUsage, kernel.EventResponseComplete},
		RequiredEvents: []kernel.ResponseEventKind{kernel.EventTextDelta, kernel.EventResponseComplete},
	}
	writer := httptest.NewRecorder()
	session, err := (OpenAIChat{}).Begin(context.Background(), options, writer)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []kernel.ResponseEvent{
		{Kind: kernel.EventTextDelta, Text: "answer"},
		{Kind: kernel.EventUsage, Usage: &kernel.UsageEvent{InputTokens: 11, OutputTokens: 7}},
		{Kind: kernel.EventContentBlockEnd, StopReason: "completed"},
		{Kind: kernel.EventResponseComplete},
	} {
		if err := session.Emit(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	if err := session.Finish(context.Background(), nil); err != nil {
		t.Fatal(err)
	}

	frames := decodeChatUsageFrames(t, writer.Body.String())
	usageIndexes, finishIndexes := []int{}, []int{}
	for i, frame := range frames {
		if frame.Usage != nil {
			usageIndexes = append(usageIndexes, i)
			if len(frame.Choices) != 0 {
				t.Fatalf("usage frame must have choices: [], got %#v", frame)
			}
			if frame.Usage.PromptTokens != 11 || frame.Usage.CompletionTokens != 7 || frame.Usage.TotalTokens != 18 {
				t.Fatalf("incorrect usage counters: %#v", frame.Usage)
			}
		}
		for _, choice := range frame.Choices {
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				finishIndexes = append(finishIndexes, i)
			}
		}
	}
	if len(usageIndexes) != 1 || len(finishIndexes) != 1 || usageIndexes[0] <= finishIndexes[0] {
		t.Fatalf("expected exactly one usage chunk after finish choice, usage=%v finish=%v frames=%#v", usageIndexes, finishIndexes, frames)
	}
	body := writer.Body.String()
	if strings.Index(body, `"usage"`) > strings.Index(body, "data: [DONE]") || !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("usage must precede terminal [DONE]: %s", body)
	}
}

func TestOpenAIChatSemanticStreamUsageOptOutAndMissingUsage(t *testing.T) {
	for _, tt := range []struct {
		name      string
		include   bool
		emitUsage bool
	}{
		{name: "opt out", include: false, emitUsage: true},
		{name: "missing usage event", include: true, emitUsage: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			options := kernel.ResponseRenderContext{
				Status: 200, ClientFormat: normalize.FormatOpenAIChat, ProviderFormat: normalize.FormatOpenAIResponses,
				Streaming: true, ProviderStreaming: true, IncludeUsage: tt.include,
				ProviderEvents: []kernel.ResponseEventKind{kernel.EventTextDelta, kernel.EventUsage, kernel.EventResponseComplete},
				RequiredEvents: []kernel.ResponseEventKind{kernel.EventTextDelta, kernel.EventResponseComplete},
			}
			writer := httptest.NewRecorder()
			session, err := (OpenAIChat{}).Begin(context.Background(), options, writer)
			if err != nil {
				t.Fatal(err)
			}
			if err := session.Emit(context.Background(), kernel.ResponseEvent{Kind: kernel.EventTextDelta, Text: "answer"}); err != nil {
				t.Fatal(err)
			}
			if tt.emitUsage {
				if err := session.Emit(context.Background(), kernel.ResponseEvent{Kind: kernel.EventUsage, Usage: &kernel.UsageEvent{InputTokens: 3, OutputTokens: 2}}); err != nil {
					t.Fatal(err)
				}
			}
			if err := session.Finish(context.Background(), nil); err != nil {
				t.Fatal(err)
			}
			for _, frame := range decodeChatUsageFrames(t, writer.Body.String()) {
				if frame.Usage != nil {
					t.Fatalf("unexpected fabricated or opted-out usage frame: %#v", frame)
				}
			}
		})
	}
}

func TestOpenAIChatFailedFinishDoesNotEmitUsageOrDone(t *testing.T) {
	options := kernel.ResponseRenderContext{
		Status: 200, ClientFormat: normalize.FormatOpenAIChat, ProviderFormat: normalize.FormatOpenAIResponses,
		Streaming: true, ProviderStreaming: true, IncludeUsage: true,
	}
	writer := httptest.NewRecorder()
	session, err := (OpenAIChat{}).Begin(context.Background(), options, writer)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Emit(context.Background(), kernel.ResponseEvent{Kind: kernel.EventUsage, Usage: &kernel.UsageEvent{InputTokens: 9, OutputTokens: 4}}); err != nil {
		t.Fatal(err)
	}
	if err := session.Finish(context.Background(), errors.New("decode failed")); err == nil {
		t.Fatal("Finish accepted a decode failure")
	}
	if body := writer.Body.String(); strings.Contains(body, `"usage"`) || strings.Contains(body, "data: [DONE]") {
		t.Fatalf("failed Finish emitted successful usage or terminal framing: %s", body)
	}
}

func TestOpenAIChatNativePassthroughPreservesBytesWithIncludeUsage(t *testing.T) {
	wire := "data: {\"choices\":[{\"delta\":{},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n"
	options := kernel.ResponseRenderContext{
		Status: 200, Headers: http.Header{"Content-Type": []string{"text/event-stream"}},
		ClientFormat: normalize.FormatOpenAIChat, ProviderFormat: normalize.FormatOpenAIChat,
		Streaming: true, ProviderStreaming: true, IncludeUsage: true,
		ProviderEvents: []kernel.ResponseEventKind{kernel.EventRawFrame},
	}
	writer := httptest.NewRecorder()
	session, err := (OpenAIChat{}).Begin(context.Background(), options, writer)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Emit(context.Background(), kernel.ResponseEvent{Kind: kernel.EventRawFrame, WireFormat: normalize.FormatOpenAIChat, Raw: []byte(wire)}); err != nil {
		t.Fatal(err)
	}
	if err := session.Finish(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if writer.Body.String() != wire {
		t.Fatalf("native Chat passthrough changed bytes: got %q want %q", writer.Body.String(), wire)
	}
}
