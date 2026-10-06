package renderers

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

func TestOpenAIChatRendererPlansRequiredSemanticEvents(t *testing.T) {
	base := kernel.ResponseRenderContext{
		ClientFormat: normalize.FormatOpenAIChat, ProviderFormat: normalize.FormatOpenAIChat,
		Streaming: true, ProviderStreaming: true,
		ProviderEvents: []kernel.ResponseEventKind{kernel.EventRawFrame, kernel.EventTextDelta, kernel.EventToolCallDelta, kernel.EventResponseComplete},
		RequiredEvents: []kernel.ResponseEventKind{kernel.EventTextDelta, kernel.EventToolCallDelta, kernel.EventResponseComplete},
	}
	plan := (OpenAIChat{}).SupportsResponse(base)
	if !plan.Supported || plan.Fidelity != kernel.FidelityNative {
		t.Fatalf("native event path was not admitted: %#v", plan)
	}

	base.RequiredEvents = append(base.RequiredEvents, kernel.EventThinkingDelta)
	plan = (OpenAIChat{}).SupportsResponse(base)
	if plan.Supported || plan.Fidelity != kernel.FidelityUnsupported {
		t.Fatalf("renderer admitted a required event missing from decoder contract: %#v", plan)
	}
}

func TestActiveSemanticTransformDisablesOpenAIWirePassthrough(t *testing.T) {
	options := kernel.ResponseRenderContext{
		Status: 200, ClientFormat: normalize.FormatOpenAIChat, ProviderFormat: normalize.FormatOpenAIChat,
		Streaming: true, ProviderStreaming: true, SemanticTransformActive: true,
		ProviderEvents: []kernel.ResponseEventKind{kernel.EventRawFrame, kernel.EventTextDelta, kernel.EventResponseComplete},
		RequiredEvents: []kernel.ResponseEventKind{kernel.EventTextDelta, kernel.EventResponseComplete},
	}
	plan := (OpenAIChat{}).SupportsResponse(options)
	if !plan.Supported || plan.Fidelity != kernel.FidelityTranslated {
		t.Fatalf("semantic path was not selected when a transform is active: %#v", plan)
	}

	writer := httptest.NewRecorder()
	session, err := (OpenAIChat{}).Begin(context.Background(), options, writer)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Emit(context.Background(), kernel.ResponseEvent{Kind: kernel.EventRawFrame, WireFormat: normalize.FormatOpenAIChat, Raw: []byte("original-wire")}); err != nil {
		t.Fatal(err)
	}
	if err := session.Emit(context.Background(), kernel.ResponseEvent{Kind: kernel.EventTextDelta, Text: "transformed"}); err != nil {
		t.Fatal(err)
	}
	if err := session.Emit(context.Background(), kernel.ResponseEvent{Kind: kernel.EventResponseComplete}); err != nil {
		t.Fatal(err)
	}
	if err := session.Finish(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(writer.Body.String(), "original-wire") || !strings.Contains(writer.Body.String(), "transformed") {
		t.Fatalf("native frames bypassed the active semantic transform: %q", writer.Body.String())
	}
}

func TestWirePassthroughRejectsSemanticResponseTransform(t *testing.T) {
	options := kernel.ResponseRenderContext{
		ClientFormat: normalize.FormatAnthropic, ProviderFormat: normalize.FormatAnthropic,
		Streaming: true, ProviderStreaming: true, SemanticTransformActive: true,
		ProviderEvents: []kernel.ResponseEventKind{kernel.EventRawFrame, kernel.EventTextDelta, kernel.EventResponseComplete},
		RequiredEvents: []kernel.ResponseEventKind{kernel.EventTextDelta, kernel.EventResponseComplete},
	}
	plan := (WirePassthrough{Format: normalize.FormatAnthropic}).SupportsResponse(options)
	if plan.Supported || plan.Fidelity != kernel.FidelityUnsupported {
		t.Fatalf("wire passthrough accepted an active semantic transform: %#v", plan)
	}
}
