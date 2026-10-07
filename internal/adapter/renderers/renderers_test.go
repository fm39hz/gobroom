package renderers

import (
	"context"
	"encoding/json"
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

func TestAnthropicMessagesRendererProjectsCanonicalTextAndTools(t *testing.T) {
	renderer := AnthropicMessages{}
	options := kernel.ResponseRenderContext{
		Status: 200, ClientFormat: normalize.FormatAnthropic, ProviderFormat: normalize.FormatOpenAIChat,
		Streaming: true, ProviderStreaming: true,
		ProviderEvents: []kernel.ResponseEventKind{kernel.EventResponseStarted, kernel.EventContentBlockStart, kernel.EventContentBlockEnd, kernel.EventTextDelta, kernel.EventToolCallDelta, kernel.EventUsage, kernel.EventResponseComplete},
		RequiredEvents: []kernel.ResponseEventKind{kernel.EventTextDelta, kernel.EventToolCallDelta, kernel.EventResponseComplete},
	}
	plan := renderer.SupportsResponse(options)
	if !plan.Supported || plan.Fidelity != kernel.FidelityTranslated {
		t.Fatalf("semantic Anthropic renderer did not admit text/tool events: %#v", plan)
	}
	writer := httptest.NewRecorder()
	session, err := renderer.Begin(context.Background(), options, writer)
	if err != nil {
		t.Fatal(err)
	}
	events := []kernel.ResponseEvent{
		{Kind: kernel.EventResponseStarted, ResponseID: "response-1"},
		{Kind: kernel.EventTextDelta, Text: "hello"},
		{Kind: kernel.EventToolCallDelta, Index: 0, ToolCallID: "call-1", ToolName: "lookup", ToolArguments: `{"q":`},
		{Kind: kernel.EventToolCallDelta, Index: 0, ToolArguments: `"weather"}`},
		{Kind: kernel.EventContentBlockEnd, StopReason: "tool_calls"},
		{Kind: kernel.EventResponseComplete},
		{Kind: kernel.EventUsage, Usage: &kernel.UsageEvent{InputTokens: 7, OutputTokens: 5}},
	}
	for _, event := range events {
		if err := session.Emit(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	if err := session.Finish(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	output := writer.Body.String()
	for _, expected := range []string{"event: message_start", "event: content_block_start", `"type":"tool_use"`, `"id":"call-1"`, `"name":"lookup"`, `"type":"input_json_delta"`, `"stop_reason":"tool_use"`, `"output_tokens":5`, "event: message_stop"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("Anthropic SSE missing %q: %s", expected, output)
		}
	}
}

func TestAnthropicMessagesRendererBuildsNonStreamingMessage(t *testing.T) {
	renderer := AnthropicMessages{}
	options := kernel.ResponseRenderContext{
		Status: 200, ClientFormat: normalize.FormatAnthropic, ProviderFormat: normalize.FormatOpenAIChat,
		ProviderEvents: []kernel.ResponseEventKind{kernel.EventTextDelta, kernel.EventToolCallDelta, kernel.EventContentBlockEnd, kernel.EventUsage, kernel.EventResponseComplete},
		RequiredEvents: []kernel.ResponseEventKind{kernel.EventTextDelta, kernel.EventResponseComplete},
	}
	writer := httptest.NewRecorder()
	session, err := renderer.Begin(context.Background(), options, writer)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []kernel.ResponseEvent{
		{Kind: kernel.EventTextDelta, Text: "answer"},
		{Kind: kernel.EventToolCallDelta, ToolCallID: "call-2", ToolName: "search", ToolArguments: `{"term":"go"}`},
		{Kind: kernel.EventContentBlockEnd, StopReason: "tool_calls"},
		{Kind: kernel.EventResponseComplete},
	} {
		if err := session.Emit(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	if err := session.Finish(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	var message struct {
		Type    string           `json:"type"`
		Stop    string           `json:"stop_reason"`
		Content []map[string]any `json:"content"`
	}
	if err := json.Unmarshal(writer.Body.Bytes(), &message); err != nil {
		t.Fatal(err)
	}
	if message.Type != "message" || message.Stop != "tool_use" || len(message.Content) != 2 || message.Content[0]["text"] != "answer" {
		t.Fatalf("non-stream Anthropic message=%#v", message)
	}
	input, ok := message.Content[1]["input"].(map[string]any)
	if !ok || input["term"] != "go" {
		t.Fatalf("tool input was not decoded as an object: %#v", message.Content[1])
	}
}

func TestAnthropicSemanticThinkingRequiresAndPreservesIssuerSignature(t *testing.T) {
	renderer := AnthropicMessages{}
	options := kernel.ResponseRenderContext{
		Status: 200, ClientFormat: normalize.FormatAnthropic, ProviderFormat: normalize.FormatOpenAIChat,
		RequiredEvents: []kernel.ResponseEventKind{kernel.EventThinkingDelta, kernel.EventResponseComplete},
		ProviderEvents: []kernel.ResponseEventKind{kernel.EventContentBlockStart, kernel.EventContentBlockEnd, kernel.EventThinkingDelta, kernel.EventResponseComplete},
	}
	if plan := renderer.SupportsResponse(options); plan.Supported {
		t.Fatalf("thinking was admitted without a signature contract: %#v", plan)
	}
	options.ProviderEvents = append(options.ProviderEvents, kernel.EventThinkingSignature)
	if plan := renderer.SupportsResponse(options); !plan.Supported {
		t.Fatalf("signed thinking path was rejected: %#v", plan)
	}
	options.SemanticTransformActive = true
	if plan := renderer.SupportsResponse(options); plan.Supported {
		t.Fatalf("thinking signature was admitted across semantic transforms: %#v", plan)
	}
	options.SemanticTransformActive = false
	writer := httptest.NewRecorder()
	session, err := renderer.Begin(context.Background(), options, writer)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []kernel.ResponseEvent{
		{Kind: kernel.EventContentBlockStart, Index: 0, BlockType: "thinking"},
		{Kind: kernel.EventThinkingDelta, Index: 0, Text: "private reasoning"},
		{Kind: kernel.EventThinkingSignature, Index: 0, Signature: "signed-by-issuer"},
		{Kind: kernel.EventContentBlockEnd, Index: 0},
		{Kind: kernel.EventResponseComplete},
	} {
		if err := session.Emit(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	if err := session.Finish(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	var message struct {
		Content []map[string]any `json:"content"`
	}
	if err := json.Unmarshal(writer.Body.Bytes(), &message); err != nil {
		t.Fatal(err)
	}
	if len(message.Content) != 1 || message.Content[0]["type"] != "thinking" || message.Content[0]["signature"] != "signed-by-issuer" {
		t.Fatalf("thinking signature was not preserved: %#v", message.Content)
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
