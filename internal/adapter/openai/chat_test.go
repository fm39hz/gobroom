package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	egress "github.com/fm39hz/gobroom/internal/adapter/renderers"
	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

type nativeChatRequestTransform struct{}
type appendCanonicalChatMessageTransform struct{}

func (appendCanonicalChatMessageTransform) Definition() kernel.TransformDefinition {
	return kernel.TransformDefinition{Ref: extensions.Ref{Kind: kernel.RequestTransformKind, ID: "fixture.append-chat-image", ContractVersion: 1}, ImplementationVersion: "1", Label: "Append canonical image message", Description: "Exercise typed Chat egress for an appended IR message.", Stage: kernel.TransformBeforeRequirements, Effects: []kernel.TransformEffect{kernel.TransformInput}}
}

func (appendCanonicalChatMessageTransform) Apply(_ context.Context, request *kernel.NormalizedRequest, _ json.RawMessage) error {
	request.Messages = append(request.Messages, normalize.Message{Role: "user", Content: []normalize.ContentPart{{Type: "text", Text: "inspect"}, {Type: "image", URL: "https://example.test/appended.png"}}})
	return nil
}

func (nativeChatRequestTransform) Definition() kernel.TransformDefinition {
	return kernel.TransformDefinition{Ref: extensions.Ref{Kind: kernel.RequestTransformKind, ID: "fixture.native-chat-options", ContractVersion: 1}, ImplementationVersion: "1", Label: "Native Chat options", Description: "Change typed options and tools for native egress.", Stage: kernel.TransformBeforeRequirements, Effects: []kernel.TransformEffect{kernel.TransformTools, kernel.TransformOptions}}
}

func (nativeChatRequestTransform) Apply(_ context.Context, request *kernel.NormalizedRequest, _ json.RawMessage) error {
	maxTokens, temperature, topP := 64, 0.7, 0.8
	request.Generation.MaxOutputTokens = &maxTokens
	request.Generation.Temperature = &temperature
	request.Generation.TopP = &topP
	request.Generation.StopSequences = []string{"done"}
	toolMetadata := request.Tools[0].Metadata
	request.Tools = []normalize.Tool{{Type: "function", Name: "lookup", Function: map[string]any{"name": "lookup", "parameters": map[string]any{"type": "object"}}, Metadata: toolMetadata}}
	choiceMetadata := request.ToolChoice.Metadata
	request.ToolChoice = normalize.ToolChoice{Mode: "tool", Name: "lookup", Set: true, Metadata: choiceMetadata}
	return nil
}

func TestChatAdapterForwardsAndPassthroughsSSE(t *testing.T) {
	adapter := NewAdapter()
	request := normalize.Request{Model: "public", SourceFormat: normalize.FormatOpenAIChat, Stream: true, Raw: map[string]any{"model": "public", "messages": []any{map[string]any{"role": "user", "content": "hi"}}}}
	route := kernel.Route{ID: "route:test", BaseURL: "https://provider.example/v1", ExternalModel: "upstream-model", Enabled: true, CredentialPresentation: kernel.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "}}
	prepared, err := adapter.Prepare(context.Background(), request, route, kernel.Credential{Secret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.URL != "https://provider.example/v1/chat/completions" || prepared.Headers.Get("Authorization") != "Bearer secret" {
		t.Fatalf("prepared=%#v", prepared)
	}
	response := kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))}
	recorder := httptest.NewRecorder()
	if err := adapter.RenderResponse(context.Background(), response, recorder, normalize.FormatOpenAIChat, kernel.StreamHooks{Streaming: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(recorder.Body.String(), "data:") {
		t.Fatalf("body=%q", recorder.Body.String())
	}
}

func TestKernelPreservesNativeChatSSEWithoutTransforms(t *testing.T) {
	wire := "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\r\n\r\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\r\n\r\n" +
		"data: [DONE]\r\n\r\n"
	var dispatches atomic.Int32
	composed := NewAdapter().(kernel.ComposedAdapter)
	composed.Transport = kernel.HTTPTransport{Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		dispatches.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire)), Request: request}, nil
	})}}
	snapshot, err := kernel.BuildSnapshot(kernel.SnapshotInput{
		PublicModels: []kernel.PublicModel{{Name: "junior", TargetRef: "physical"}},
		Nodes:        []kernel.ModelNode{{ID: "physical", Kind: kernel.ModelPhysical, Strategy: kernel.StrategyFallback, Members: []kernel.MemberRef{{Kind: kernel.MemberRoute, ID: "route", Fidelity: kernel.FidelityExact}}}},
		Routes: []kernel.Route{{
			ID: "route", Enabled: true, Protocol: kernel.ProtocolOpenAIChat, CredentialPresentation: kernel.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "}, BaseURL: "https://provider.test/v1", ExternalModel: "upstream-physical-model",
			OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"openai-chat"}}},
		}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := kernel.New(snapshot, nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	engine.Adapters["openai-chat"] = composed
	parsed, err := normalize.JSON("/v1/chat/completions", http.Header{}, []byte(`{"model":"junior","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	writer := httptest.NewRecorder()
	if err := engine.Execute(context.Background(), parsed.Request, kernel.Credential{Secret: "secret"}, writer); err != nil {
		t.Fatal(err)
	}
	if dispatches.Load() != 1 || writer.Body.String() != wire || writer.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("native response was not preserved exactly: dispatches=%d content-type=%q body=%q", dispatches.Load(), writer.Header().Get("Content-Type"), writer.Body.String())
	}
}

func TestOpenAIChatDecoderRendersAnthropicMessagesFromSemanticEvents(t *testing.T) {
	composed, ok := NewAdapter().(kernel.ComposedAdapter)
	if !ok {
		t.Fatalf("OpenAI adapter type=%T", NewAdapter())
	}
	composed.Renderers[normalize.FormatAnthropic] = egress.AnthropicMessages{}
	body := `{"id":"chatcmpl_semantic","object":"chat.completion","model":"upstream","choices":[{"index":0,"message":{"role":"assistant","content":"Checking now.","tool_calls":[{"id":"call_123","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"weather\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":7,"completion_tokens":5,"total_tokens":12}}`
	response := kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
	writer := httptest.NewRecorder()
	var observed []kernel.ResponseEvent
	if err := composed.RenderResponse(context.Background(), response, writer, normalize.FormatAnthropic, kernel.StreamHooks{OnEvent: func(event kernel.ResponseEvent) { observed = append(observed, event) }}); err != nil {
		t.Fatal(err)
	}
	var message struct {
		ID      string           `json:"id"`
		Type    string           `json:"type"`
		Stop    string           `json:"stop_reason"`
		Usage   map[string]int64 `json:"usage"`
		Content []map[string]any `json:"content"`
	}
	if err := json.Unmarshal(writer.Body.Bytes(), &message); err != nil {
		t.Fatalf("decode Anthropic semantic response %q: %v", writer.Body.String(), err)
	}
	if message.ID != "chatcmpl_semantic" || message.Type != "message" || message.Stop != "tool_use" || len(message.Content) != 2 || message.Content[0]["text"] != "Checking now." {
		t.Fatalf("Anthropic response projection=%#v", message)
	}
	toolInput, ok := message.Content[1]["input"].(map[string]any)
	if !ok || toolInput["q"] != "weather" || message.Content[1]["id"] != "call_123" || message.Content[1]["name"] != "lookup" {
		t.Fatalf("Anthropic tool_use projection=%#v", message.Content[1])
	}
	if message.Usage["input_tokens"] != 7 || message.Usage["output_tokens"] != 5 {
		t.Fatalf("Anthropic usage projection=%#v", message.Usage)
	}
	seenText, seenTool := false, false
	for _, event := range observed {
		seenText = seenText || event.Kind == kernel.EventTextDelta
		seenTool = seenTool || event.Kind == kernel.EventToolCallDelta
	}
	if !seenText || !seenTool {
		t.Fatalf("OpenAI decoder did not produce canonical text/tool events: %#v", observed)
	}
}

func TestKernelRunsAnthropicMessagesThroughOpenAIChatAndBack(t *testing.T) {
	upstreamBody := `{"id":"chatcmpl_roundtrip","object":"chat.completion","model":"upstream-model","choices":[{"index":0,"message":{"role":"assistant","content":"Checking now.","tool_calls":[{"id":"call_roundtrip","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"weather\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":7,"completion_tokens":5,"total_tokens":12}}`
	var dispatched atomic.Int32
	composed := NewAdapter().(kernel.ComposedAdapter)
	composed.Transport = kernel.HTTPTransport{Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		dispatched.Add(1)
		if request.URL.String() != "https://provider.test/v1/chat/completions" {
			t.Errorf("upstream URL=%q", request.URL.String())
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode upstream request: %v", err)
		} else {
			messages, _ := body["messages"].([]any)
			if body["model"] != "physical-model" || len(messages) != 1 || messages[0].(map[string]any)["content"] != "Find the weather" {
				t.Errorf("OpenAI Chat request body=%#v", body)
			}
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(upstreamBody)), Request: request}, nil
	})}}
	snapshot, err := kernel.BuildSnapshot(kernel.SnapshotInput{
		PublicModels: []kernel.PublicModel{{Name: "junior", TargetRef: "physical"}},
		Nodes:        []kernel.ModelNode{{ID: "physical", Kind: kernel.ModelPhysical, Strategy: kernel.StrategyFallback, Members: []kernel.MemberRef{{Kind: kernel.MemberRoute, ID: "route", Fidelity: kernel.FidelityExact}}}},
		Routes: []kernel.Route{{
			ID: "route", Enabled: true, Protocol: kernel.ProtocolOpenAIChat, CredentialPresentation: kernel.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "}, BaseURL: "https://provider.test/v1", ExternalModel: "physical-model",
			Profile:           kernel.CapabilityProfile{kernel.CapabilityTools: {State: kernel.SupportNative}},
			OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"openai-chat"}}},
		}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := kernel.New(snapshot, nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	engine.Adapters["openai-chat"] = composed
	parsed, err := normalize.JSON("/v1/messages", http.Header{"Anthropic-Version": []string{"2023-06-01"}}, []byte(`{"model":"junior","max_tokens":128,"messages":[{"role":"user","content":"Find the weather"}],"tools":[{"name":"lookup","input_schema":{"type":"object","properties":{"q":{"type":"string"}}}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	explanations, explainErr := engine.ExplainCompatibility(context.Background(), "junior", parsed.Request)
	if explainErr != nil {
		t.Fatal(explainErr)
	}
	if len(explanations) != 1 || !explanations[0].Compatible {
		t.Fatalf("route compatibility explanations=%#v", explanations)
	}
	writer := httptest.NewRecorder()
	if err := engine.Execute(context.Background(), parsed.Request, kernel.Credential{Secret: "secret"}, writer); err != nil {
		t.Fatal(err)
	}
	if dispatched.Load() != 1 || writer.Code != http.StatusOK || !strings.Contains(writer.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("dispatches=%d status=%d content-type=%q body=%s", dispatched.Load(), writer.Code, writer.Header().Get("Content-Type"), writer.Body.String())
	}
	var message struct {
		ID      string           `json:"id"`
		Type    string           `json:"type"`
		Model   string           `json:"model"`
		Stop    string           `json:"stop_reason"`
		Usage   map[string]int64 `json:"usage"`
		Content []map[string]any `json:"content"`
	}
	if err := json.Unmarshal(writer.Body.Bytes(), &message); err != nil {
		t.Fatalf("decode Anthropic response %q: %v", writer.Body.String(), err)
	}
	if message.ID != "chatcmpl_roundtrip" || message.Type != "message" || message.Model != "junior" || message.Stop != "tool_use" || len(message.Content) != 2 {
		t.Fatalf("Anthropic response envelope=%#v", message)
	}
	if message.Content[0]["text"] != "Checking now." || message.Content[1]["type"] != "tool_use" || message.Content[1]["id"] != "call_roundtrip" || message.Content[1]["name"] != "lookup" {
		t.Fatalf("Anthropic semantic content=%#v", message.Content)
	}
	if input, ok := message.Content[1]["input"].(map[string]any); !ok || input["q"] != "weather" || message.Usage["input_tokens"] != 7 || message.Usage["output_tokens"] != 5 {
		t.Fatalf("Anthropic tool/usage projection=%#v usage=%#v", message.Content[1], message.Usage)
	}
}

func TestKernelRejectsMissingClientRendererBeforeDispatch(t *testing.T) {
	var dispatches atomic.Int32
	composed := NewAdapter().(kernel.ComposedAdapter)
	composed.Renderers = map[normalize.Format]kernel.ResponseRenderer{}
	composed.Transport = kernel.HTTPTransport{Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		dispatches.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: request}, nil
	})}}
	snapshot, err := kernel.BuildSnapshot(kernel.SnapshotInput{
		PublicModels: []kernel.PublicModel{{Name: "junior", TargetRef: "physical"}},
		Nodes:        []kernel.ModelNode{{ID: "physical", Kind: kernel.ModelPhysical, Strategy: kernel.StrategyFallback, Members: []kernel.MemberRef{{Kind: kernel.MemberRoute, ID: "route", Fidelity: kernel.FidelityExact}}}},
		Routes: []kernel.Route{{
			ID: "route", Enabled: true, Protocol: kernel.ProtocolOpenAIChat, CredentialPresentation: kernel.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "}, BaseURL: "https://provider.test/v1", ExternalModel: "physical-model",
			OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"openai-chat"}}},
		}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := kernel.New(snapshot, nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	engine.Adapters["openai-chat"] = composed
	parsed, err := normalize.JSON("/v1/messages", http.Header{"Anthropic-Version": []string{"2023-06-01"}}, []byte(`{"model":"junior","max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	err = engine.Execute(context.Background(), parsed.Request, kernel.Credential{Secret: "secret"}, httptest.NewRecorder())
	if err == nil || !strings.Contains(err.Error(), "no usable route") || dispatches.Load() != 0 {
		t.Fatalf("missing client renderer must reject before provider dispatch: err=%v dispatches=%d", err, dispatches.Load())
	}
}

func TestKernelStreamsInterleavedToolLifecycleToAnthropicClient(t *testing.T) {
	upstreamWire := strings.Join([]string{
		`data: {"id":"chatcmpl_tools","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_alpha","type":"function"}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_tools","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"lookup","arguments":"{\"q\":"}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_tools","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_beta","type":"function"}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_tools","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{\"x\":1}"}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_tools","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"name":"second_tool"}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_tools","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"weather\"}"}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_tools","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
		"",
	}, "\n\n")
	var dispatched atomic.Int32
	composed := NewAdapter().(kernel.ComposedAdapter)
	composed.Transport = kernel.HTTPTransport{Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		dispatched.Add(1)
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode provider request: %v", err)
		} else if body["stream"] != true {
			t.Errorf("streaming intent was lost upstream: %#v", body)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(upstreamWire)), Request: request}, nil
	})}}
	snapshot, err := kernel.BuildSnapshot(kernel.SnapshotInput{
		PublicModels: []kernel.PublicModel{{Name: "tools", TargetRef: "physical"}},
		Nodes:        []kernel.ModelNode{{ID: "physical", Kind: kernel.ModelPhysical, Strategy: kernel.StrategyFallback, Members: []kernel.MemberRef{{Kind: kernel.MemberRoute, ID: "route", Fidelity: kernel.FidelityExact}}}},
		Routes: []kernel.Route{{
			ID: "route", Enabled: true, Protocol: kernel.ProtocolOpenAIChat, CredentialPresentation: kernel.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "}, BaseURL: "https://provider.test/v1", ExternalModel: "physical-tools",
			Profile:           kernel.CapabilityProfile{kernel.CapabilityTools: {State: kernel.SupportNative}},
			OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"openai-chat"}}},
		}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := kernel.New(snapshot, nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	engine.Adapters["openai-chat"] = composed
	parsed, err := normalize.JSON("/v1/messages", http.Header{"Anthropic-Version": []string{"2023-06-01"}}, []byte(`{"model":"tools","max_tokens":128,"stream":true,"messages":[{"role":"user","content":"find weather"}],"tools":[{"name":"lookup","input_schema":{"type":"object"}},{"name":"second_tool","input_schema":{"type":"object"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	writer := httptest.NewRecorder()
	if err := engine.Execute(context.Background(), parsed.Request, kernel.Credential{Secret: "secret"}, writer); err != nil {
		t.Fatal(err)
	}
	output := writer.Body.String()
	if dispatched.Load() != 1 || !strings.Contains(writer.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("dispatches=%d content-type=%q output=%s", dispatched.Load(), writer.Header().Get("Content-Type"), output)
	}
	for _, expected := range []string{
		`"id":"call_alpha"`, `"name":"lookup"`, `"id":"call_beta"`, `"name":"second_tool"`,
		`"partial_json":"{\"q\":"`, `"partial_json":"\"weather\"}"`, `"partial_json":"{\"x\":1}"`,
		`"stop_reason":"tool_use"`, "event: message_stop",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("Anthropic stream omitted %q: %s", expected, output)
		}
	}
	if strings.Count(output, "event: content_block_start") != 2 {
		t.Fatalf("interleaved deltas did not produce two tool blocks: %s", output)
	}
}

func TestKernelRejectsIncompleteToolIdentityWithoutSuccessfulStreamEnd(t *testing.T) {
	upstreamWire := strings.Join([]string{
		`data: {"id":"chatcmpl_incomplete","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_without_name","type":"function"}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_incomplete","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
		"",
	}, "\n\n")
	composed := NewAdapter().(kernel.ComposedAdapter)
	composed.Transport = kernel.HTTPTransport{Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(upstreamWire)), Request: request}, nil
	})}}
	snapshot, err := kernel.BuildSnapshot(kernel.SnapshotInput{
		PublicModels: []kernel.PublicModel{{Name: "role", TargetRef: "physical"}},
		Nodes:        []kernel.ModelNode{{ID: "physical", Kind: kernel.ModelPhysical, Strategy: kernel.StrategyFallback, Members: []kernel.MemberRef{{Kind: kernel.MemberRoute, ID: "route", Fidelity: kernel.FidelityExact}}}},
		Routes: []kernel.Route{{
			ID: "route", Enabled: true, Protocol: kernel.ProtocolOpenAIChat, CredentialPresentation: kernel.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "}, BaseURL: "https://provider.test/v1", ExternalModel: "model",
			OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"openai-chat"}}},
		}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := kernel.New(snapshot, nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	engine.Adapters["openai-chat"] = composed
	parsed, err := normalize.JSON("/v1/messages", http.Header{"Anthropic-Version": []string{"2023-06-01"}}, []byte(`{"model":"role","max_tokens":128,"stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	writer := httptest.NewRecorder()
	err = engine.Execute(context.Background(), parsed.Request, kernel.Credential{Secret: "secret"}, writer)
	if err == nil || !strings.Contains(err.Error(), "ID and name") {
		t.Fatalf("malformed upstream tool identity was not rejected: err=%v output=%s", err, writer.Body.String())
	}
	if strings.Contains(writer.Body.String(), "event: message_stop") {
		t.Fatalf("malformed tool call received a successful terminal event: %s", writer.Body.String())
	}
}

func TestKernelInterruptedCrossProtocolStreamStaysIncompleteAndDoesNotFallback(t *testing.T) {
	var primaryCalls, backupCalls atomic.Int32
	composed := NewAdapter().(kernel.ComposedAdapter)
	composed.Transport = kernel.HTTPTransport{Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "primary.test" {
			primaryCalls.Add(1)
			partial := `data: {"id":"chatcmpl_partial","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"partial answer"},"finish_reason":null}]}` + "\n\n"
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(partial)), Request: request}, nil
		}
		if request.URL.Host == "backup.test" {
			backupCalls.Add(1)
			complete := `data: {"id":"chatcmpl_backup","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"must not be sent"},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(complete)), Request: request}, nil
		}
		t.Errorf("unexpected upstream host %q", request.URL.Host)
		return nil, errors.New("unexpected upstream host")
	})}}
	operationBinding := map[normalize.Operation]kernel.RouteOperationBinding{
		normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"openai-chat"}},
	}
	snapshot, err := kernel.BuildSnapshot(kernel.SnapshotInput{
		PublicModels: []kernel.PublicModel{{Name: "role", TargetRef: "physical"}},
		Nodes: []kernel.ModelNode{{ID: "physical", Kind: kernel.ModelPhysical, Strategy: kernel.StrategyFallback, Members: []kernel.MemberRef{
			{Kind: kernel.MemberRoute, ID: "primary", Fidelity: kernel.FidelityExact},
			{Kind: kernel.MemberRoute, ID: "backup", Fidelity: kernel.FidelityExact},
		}}},
		Routes: []kernel.Route{
			{ID: "primary", Enabled: true, Protocol: kernel.ProtocolOpenAIChat, CredentialPresentation: kernel.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "}, BaseURL: "https://primary.test/v1", ExternalModel: "model", OperationBindings: operationBinding},
			{ID: "backup", Enabled: true, Protocol: kernel.ProtocolOpenAIChat, CredentialPresentation: kernel.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "}, BaseURL: "https://backup.test/v1", ExternalModel: "model", OperationBindings: operationBinding},
		},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := kernel.New(snapshot, nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	engine.Adapters["openai-chat"] = composed
	parsed, err := normalize.JSON("/v1/messages", http.Header{"Anthropic-Version": []string{"2023-06-01"}}, []byte(`{"model":"role","max_tokens":128,"stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	writer := httptest.NewRecorder()
	err = engine.Execute(context.Background(), parsed.Request, kernel.Credential{Secret: "secret"}, writer)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated cross-protocol stream error=%v", err)
	}
	output := writer.Body.String()
	if primaryCalls.Load() != 1 || backupCalls.Load() != 0 {
		t.Fatalf("stream failure after client output must not fallback: primary=%d backup=%d", primaryCalls.Load(), backupCalls.Load())
	}
	if !strings.Contains(output, "partial answer") || strings.Contains(output, "message_stop") || strings.Contains(output, "message_delta") || strings.Contains(output, "must not be sent") {
		t.Fatalf("partial Anthropic stream was falsely finalized or replaced: %s", output)
	}
}

func TestKernelMapsResponsesIngressThroughOpenAIChatAndBack(t *testing.T) {
	responseBody := `{"id":"chatcmpl_responses_ingress","object":"chat.completion","model":"chat-model","choices":[{"index":0,"message":{"role":"assistant","content":"Done.","tool_calls":[{"id":"call_final","type":"function","function":{"name":"lookup","arguments":"{\"found\":true}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15}}`
	var dispatches atomic.Int32
	composed := NewAdapter().(kernel.ComposedAdapter)
	composed.Transport = kernel.HTTPTransport{Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		dispatches.Add(1)
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode OpenAI Chat request: %v", err)
		} else {
			messages, _ := body["messages"].([]any)
			if body["model"] != "chat-physical" || body["input"] != nil || body["instructions"] != nil || len(messages) != 4 || body["max_tokens"] != float64(512) || body["temperature"] != 0.25 || body["top_p"] != 0.8 {
				t.Errorf("Responses input was not rebuilt as Chat request: %#v", body)
			}
			if messages, ok := body["messages"].([]any); ok {
				if messages[0].(map[string]any)["role"] != "developer" || messages[1].(map[string]any)["content"] == nil || messages[2].(map[string]any)["tool_calls"] == nil || messages[3].(map[string]any)["tool_call_id"] != "call_prior" {
					t.Errorf("typed Responses prompt/history mapping=%#v", messages)
				}
				parts, _ := messages[1].(map[string]any)["content"].([]any)
				var imageURL map[string]any
				if len(parts) > 1 {
					if imagePart, ok := parts[1].(map[string]any); ok {
						imageURL, _ = imagePart["image_url"].(map[string]any)
					}
				}
				if len(parts) != 2 || imageURL["url"] != "https://images.example/item.png" || imageURL["detail"] != "high" {
					t.Errorf("Responses input image was not translated to Chat vision content: %#v", messages[1])
				}
			}
			if choice, ok := body["tool_choice"].(map[string]any); !ok || choice["type"] != "function" || choice["function"].(map[string]any)["name"] != "lookup" {
				t.Errorf("Responses function tool choice was not mapped to Chat: %#v", body["tool_choice"])
			}
			if body["reasoning_effort"] != "high" {
				t.Errorf("Responses reasoning effort was not mapped to Chat: %#v", body["reasoning_effort"])
			}
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(responseBody)), Request: request}, nil
	})}}
	snapshot, err := kernel.BuildSnapshot(kernel.SnapshotInput{
		PublicModels: []kernel.PublicModel{{Name: "junior", TargetRef: "physical"}},
		Nodes:        []kernel.ModelNode{{ID: "physical", Kind: kernel.ModelPhysical, Strategy: kernel.StrategyFallback, Members: []kernel.MemberRef{{Kind: kernel.MemberRoute, ID: "route", Fidelity: kernel.FidelityExact}}}},
		Routes: []kernel.Route{{
			ID: "route", Enabled: true, Protocol: kernel.ProtocolOpenAIChat, CredentialPresentation: kernel.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "}, BaseURL: "https://provider.test/v1", ExternalModel: "chat-physical",
			Profile: kernel.CapabilityProfile{
				kernel.CapabilityTools:  {State: kernel.SupportNative},
				kernel.CapabilityVision: {State: kernel.SupportNative},
			},
			OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"openai-chat"}}},
		}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := kernel.New(snapshot, nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	engine.Adapters["openai-chat"] = composed
	requestBody := `{"model":"junior","stream":false,"instructions":"Be precise.","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Find item 7."},{"type":"input_image","image_url":"https://images.example/item.png","detail":"high"}]},{"type":"function_call","call_id":"call_prior","name":"lookup","arguments":"{\"id\":7}"},{"type":"function_call_output","call_id":"call_prior","output":"{\"found\":true}"}],"tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"id":{"type":"integer"}}}}],"tool_choice":{"type":"function","name":"lookup"},"max_output_tokens":512,"temperature":0.25,"top_p":0.8,"reasoning":{"effort":"high"}}`
	parsed, err := normalize.JSON("/v1/responses", http.Header{}, []byte(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	explanations, err := engine.ExplainCompatibility(context.Background(), "junior", parsed.Request)
	if err != nil {
		t.Fatal(err)
	}
	if len(explanations) != 1 || !explanations[0].Compatible {
		t.Fatalf("Responses→Chat compatibility=%#v", explanations)
	}
	writer := httptest.NewRecorder()
	if err := engine.Execute(context.Background(), parsed.Request, kernel.Credential{Secret: "secret"}, writer); err != nil {
		t.Fatal(err)
	}
	var result struct {
		ID     string           `json:"id"`
		Object string           `json:"object"`
		Status string           `json:"status"`
		Output []map[string]any `json:"output"`
		Usage  map[string]int64 `json:"usage"`
	}
	if err := json.Unmarshal(writer.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode OpenAI Responses projection %q: %v", writer.Body.String(), err)
	}
	if !strings.HasPrefix(result.ID, "resp_gobroom_") || result.Object != "response" || result.Status != "completed" || len(result.Output) != 2 || dispatches.Load() != 1 {
		t.Fatalf("Chat→Responses result=%#v dispatches=%d", result, dispatches.Load())
	}
	if result.Output[0]["type"] != "message" || result.Output[1]["type"] != "function_call" || result.Output[1]["call_id"] != "call_final" || result.Usage["input_tokens"] != 12 || result.Usage["output_tokens"] != 3 {
		t.Fatalf("Chat→Responses output/usage=%#v usage=%#v", result.Output, result.Usage)
	}

	withContinuity := `{"model":"junior","previous_response_id":"resp_prev","input":"continue"}`
	continuityRequest, err := normalize.JSON("/v1/responses", http.Header{}, []byte(withContinuity))
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Execute(context.Background(), continuityRequest.Request, kernel.Credential{Secret: "secret"}, httptest.NewRecorder()); !errors.Is(err, kernel.ErrNoRoute) || dispatches.Load() != 1 {
		t.Fatalf("Responses continuity was silently lost on Chat route: err=%v dispatches=%d", err, dispatches.Load())
	}
	for _, unsupported := range []string{
		`{"model":"junior","store":true,"input":"do not persist"}`,
		`{"model":"junior","input":[{"type":"message","role":"user","content":[{"type":"input_image","file_id":"file_123"}]}]}`,
		`{"model":"junior","reasoning":{"effort":"high","summary":"detailed"},"input":"unsupported reasoning summary"}`,
		`{"model":"junior","tool_choice":{"type":"file_search"},"input":"unsupported choice"}`,
		`{"model":"junior","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"citation","annotations":[{"type":"url_citation"}]}]}]}`,
	} {
		unsupportedRequest, err := normalize.JSON("/v1/responses", http.Header{}, []byte(unsupported))
		if err != nil {
			t.Fatal(err)
		}
		if err := engine.Execute(context.Background(), unsupportedRequest.Request, kernel.Credential{Secret: "secret"}, httptest.NewRecorder()); !errors.Is(err, kernel.ErrNoRoute) || dispatches.Load() != 1 {
			t.Fatalf("unmapped Responses field was not rejected before Chat dispatch: request=%s err=%v calls=%d", unsupported, err, dispatches.Load())
		}
	}
}

func TestKernelStreamsResponsesIngressThroughOpenAIChatAndBack(t *testing.T) {
	upstreamWire := strings.Join([]string{
		`data: {"id":"chatcmpl_stream","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Searching now."},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_stream","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_stream","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"tea\"}"}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_stream","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":6,"completion_tokens":3}}`,
		`data: [DONE]`,
		"",
	}, "\n\n")
	composed := NewAdapter().(kernel.ComposedAdapter)
	composed.Transport = kernel.HTTPTransport{Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode OpenAI Chat stream request: %v", err)
		} else if body["stream"] != true || body["input"] != nil || body["messages"] == nil {
			t.Errorf("Responses stream request was not translated: %#v", body)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(upstreamWire)), Request: request}, nil
	})}}
	snapshot, err := kernel.BuildSnapshot(kernel.SnapshotInput{
		PublicModels: []kernel.PublicModel{{Name: "junior", TargetRef: "physical"}},
		Nodes:        []kernel.ModelNode{{ID: "physical", Kind: kernel.ModelPhysical, Strategy: kernel.StrategyFallback, Members: []kernel.MemberRef{{Kind: kernel.MemberRoute, ID: "route", Fidelity: kernel.FidelityExact}}}},
		Routes: []kernel.Route{{
			ID: "route", Enabled: true, Protocol: kernel.ProtocolOpenAIChat, CredentialPresentation: kernel.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "}, BaseURL: "https://provider.test/v1", ExternalModel: "chat-physical",
			Profile:           kernel.CapabilityProfile{kernel.CapabilityTools: {State: kernel.SupportNative}},
			OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"openai-chat"}}},
		}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := kernel.New(snapshot, nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	engine.Adapters["openai-chat"] = composed
	parsed, err := normalize.JSON("/v1/responses", http.Header{}, []byte(`{"model":"junior","stream":true,"input":"Search for tea.","tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	writer := httptest.NewRecorder()
	if err := engine.Execute(context.Background(), parsed.Request, kernel.Credential{Secret: "secret"}, writer); err != nil {
		t.Fatal(err)
	}
	output := writer.Body.String()
	if !strings.Contains(writer.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("Responses stream content type=%q output=%s", writer.Header().Get("Content-Type"), output)
	}
	for _, expected := range []string{
		"event: response.created", "event: response.output_text.delta", "Searching now.",
		"event: response.output_item.added", `"call_id":"call_stream"`,
		"event: response.function_call_arguments.delta", "event: response.function_call_arguments.done",
		"event: response.completed", `"input_tokens":6`, `"output_tokens":3`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("Responses stream omitted %q: %s", expected, output)
		}
	}
	if strings.Contains(output, "[DONE]") || strings.Contains(output, "chatcmpl_stream") || !strings.Contains(output, `"id":"resp_gobroom_`) {
		t.Fatalf("Responses stream leaked Chat framing/identity or used the wrong terminal: %s", output)
	}
}

func TestKernelInterruptedChatProviderResponsesStreamDoesNotCompleteOrFallback(t *testing.T) {
	var primaryCalls, backupCalls atomic.Int32
	composed := NewAdapter().(kernel.ComposedAdapter)
	composed.Transport = kernel.HTTPTransport{Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "primary.test" {
			primaryCalls.Add(1)
			partial := `data: {"id":"chatcmpl_incomplete","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"partial Responses answer"},"finish_reason":null}]}` + "\n\n"
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(partial)), Request: request}, nil
		}
		if request.URL.Host == "backup.test" {
			backupCalls.Add(1)
			complete := `data: {"id":"chatcmpl_backup","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"fallback must not appear"},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(complete)), Request: request}, nil
		}
		t.Errorf("unexpected upstream host %q", request.URL.Host)
		return nil, errors.New("unexpected upstream host")
	})}}
	operationBinding := map[normalize.Operation]kernel.RouteOperationBinding{
		normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"openai-chat"}},
	}
	snapshot, err := kernel.BuildSnapshot(kernel.SnapshotInput{
		PublicModels: []kernel.PublicModel{{Name: "role", TargetRef: "physical"}},
		Nodes: []kernel.ModelNode{{ID: "physical", Kind: kernel.ModelPhysical, Strategy: kernel.StrategyFallback, Members: []kernel.MemberRef{
			{Kind: kernel.MemberRoute, ID: "primary", Fidelity: kernel.FidelityExact},
			{Kind: kernel.MemberRoute, ID: "backup", Fidelity: kernel.FidelityExact},
		}}},
		Routes: []kernel.Route{
			{ID: "primary", Enabled: true, Protocol: kernel.ProtocolOpenAIChat, CredentialPresentation: kernel.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "}, BaseURL: "https://primary.test/v1", ExternalModel: "model", OperationBindings: operationBinding},
			{ID: "backup", Enabled: true, Protocol: kernel.ProtocolOpenAIChat, CredentialPresentation: kernel.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "}, BaseURL: "https://backup.test/v1", ExternalModel: "model", OperationBindings: operationBinding},
		},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := kernel.New(snapshot, nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	engine.Adapters["openai-chat"] = composed
	parsed, err := normalize.JSON("/v1/responses", http.Header{}, []byte(`{"model":"role","stream":true,"input":"answer"}`))
	if err != nil {
		t.Fatal(err)
	}
	writer := httptest.NewRecorder()
	err = engine.Execute(context.Background(), parsed.Request, kernel.Credential{Secret: "secret"}, writer)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated Chat→Responses stream error=%v", err)
	}
	output := writer.Body.String()
	if primaryCalls.Load() != 1 || backupCalls.Load() != 0 {
		t.Fatalf("partial Responses output must not fallback: primary=%d backup=%d", primaryCalls.Load(), backupCalls.Load())
	}
	if !strings.Contains(output, "event: response.output_text.delta") || !strings.Contains(output, "partial Responses answer") || strings.Contains(output, "event: response.completed") || strings.Contains(output, "fallback must not appear") {
		t.Fatalf("truncated Responses stream was falsely completed or replaced: %s", output)
	}
}

func TestChatRequestReassemblesPromptPlanAtWireBoundary(t *testing.T) {
	request := normalize.Request{Model: "public", SourceFormat: normalize.FormatOpenAIChat, Prompt: normalize.PromptPlan{Layers: []normalize.PromptLayer{{Origin: normalize.PromptHarness, Role: "system", Text: "policy"}}}, Messages: []normalize.Message{{Role: "user", Content: "hello"}}}
	prepared, err := (Chat{}).Prepare(context.Background(), request, kernel.Route{BaseURL: "https://provider.test/v1", ExternalModel: "m"}, kernel.Credential{})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.NewDecoder(prepared.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	messages, _ := body["messages"].([]any)
	if len(messages) != 2 || messages[0].(map[string]any)["role"] != "system" || messages[1].(map[string]any)["role"] != "user" {
		t.Fatalf("wire messages=%#v", messages)
	}
}

func TestNativeChatMessageEncodesCanonicalTextAndImageParts(t *testing.T) {
	message := normalize.Message{Role: "user", Content: []normalize.ContentPart{
		{Type: "text", Text: "describe this"},
		{Type: "image", URL: "https://example.test/image.png"},
		{Type: "image", MediaType: "image/png", Data: "aGVsbG8="},
	}}
	encoded, err := nativeChatMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	parts, ok := encoded["content"].([]any)
	if !ok || len(parts) != 3 {
		t.Fatalf("content=%#v", encoded["content"])
	}
	text := parts[0].(map[string]any)
	urlImage := parts[1].(map[string]any)
	dataImage := parts[2].(map[string]any)
	if text["type"] != "text" || text["text"] != "describe this" {
		t.Fatalf("text part=%#v", text)
	}
	if urlImage["type"] != "image_url" || urlImage["image_url"].(map[string]any)["url"] != "https://example.test/image.png" {
		t.Fatalf("URL image part=%#v", urlImage)
	}
	if dataImage["type"] != "image_url" || dataImage["image_url"].(map[string]any)["url"] != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("base64 image part=%#v", dataImage)
	}
}

func TestChatPrepareEncodesAddedCanonicalTypedMessage(t *testing.T) {
	source := map[string]any{"role": "user", "content": "original"}
	request := normalize.Request{
		Model: "public", SourceFormat: normalize.FormatOpenAIChat,
		Raw:       map[string]any{"model": "public", "messages": []any{source}},
		Messages:  []normalize.Message{{Role: "user", Content: "original", Metadata: source}, {Role: "user", Content: []normalize.ContentPart{{Type: "text", Text: "added"}, {Type: "image", URL: "https://example.test/new.png"}}}},
		Mutations: normalize.RequestMutationSet{Messages: true},
	}
	prepared, err := (Chat{}).Prepare(context.Background(), request, kernel.Route{BaseURL: "https://provider.test/v1", ExternalModel: "m"}, kernel.Credential{})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.NewDecoder(prepared.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	messages := body["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("message count=%d want 2: %#v", len(messages), messages)
	}
	added := messages[1].(map[string]any)
	content := added["content"].([]any)
	if added["role"] != "user" || content[0].(map[string]any)["text"] != "added" || content[1].(map[string]any)["type"] != "image_url" {
		t.Fatalf("added canonical message was not encoded as Chat content: %#v", messages)
	}
}

func TestOpenAIChatBudgetRequiresExactNativeSourceEvidence(t *testing.T) {
	request := normalize.Request{
		Model: "native", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1,
		SourceFormat: normalize.FormatOpenAIChat, Messages: []normalize.Message{{Role: "user", Content: "think carefully"}},
		Thinking: normalize.ThinkingIntent{Mode: "budget", BudgetTokens: 512},
		Raw:      map[string]any{"thinking": map[string]any{"budget_tokens": float64(512)}},
	}
	plan := NewAdapter().PlanCompatibility(kernel.CompatibilityContext{Request: request, Policy: kernel.CompatibilityPolicy{RequiredFacets: kernel.RequiredRequestFacets(request)}})
	if !plan.Supported {
		t.Fatalf("exact native budget was not preserved: %#v", plan)
	}
	for _, mapping := range plan.Mappings {
		if mapping.Facet == kernel.FacetReasoningBudget && mapping.Disposition == kernel.FacetPreserved {
			request.Raw = nil
			plan = NewAdapter().PlanCompatibility(kernel.CompatibilityContext{Request: request, Policy: kernel.CompatibilityPolicy{RequiredFacets: kernel.RequiredRequestFacets(request)}})
			if plan.Supported {
				t.Fatalf("synthesized budget was silently assumed native: %#v", plan)
			}
			return
		}
	}
	t.Fatalf("native reasoning-budget facet was not declared: %#v", plan.Mappings)
}

func TestNativeChatReasoningMutationRequiresDeclaredModeMapping(t *testing.T) {
	for _, test := range []struct {
		name   string
		intent normalize.ThinkingIntent
		want   kernel.FacetDisposition
	}{
		{name: "effort", intent: normalize.ThinkingIntent{Mode: "level", Effort: "high"}, want: kernel.FacetTranslated},
		{name: "enabled without budget", intent: normalize.ThinkingIntent{Mode: "enabled"}, want: kernel.FacetUnsupported},
		{name: "budget without exact raw evidence", intent: normalize.ThinkingIntent{Mode: "budget", BudgetTokens: 512}, want: kernel.FacetUnsupported},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := normalize.Request{SourceFormat: normalize.FormatOpenAIChat, Thinking: test.intent, Mutations: normalize.RequestMutationSet{Thinking: true}}
			policy := kernel.CompatibilityPolicy{RequiredFacets: []string{kernel.FacetReasoningIntent}}
			plan := kernel.ComposeCompatibilityPlan([][]kernel.FacetMapping{openAIChatFacetReport(kernel.CompatibilityContext{Request: request, Policy: policy}, normalize.FormatOpenAIChat)}, policy)
			if len(plan.Mappings) != 1 || plan.Mappings[0].Disposition != test.want {
				t.Fatalf("reasoning mutation disposition=%#v want %s; plan=%#v", plan.Mappings, test.want, plan)
			}
		})
	}
}

func TestMalformedAnthropicReasoningCannotBeReclaimedByOpenAIChatCodec(t *testing.T) {
	parsed, err := normalize.JSON("/v1/messages", http.Header{}, []byte(`{"model":"junior","max_tokens":512,"thinking":{"type":"enabled","budget_tokens":2000},"output_config":{"effort":"high"},"messages":[{"role":"user","content":"think"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Request.UnsupportedFacets) == 0 {
		t.Fatal("malformed incompatible Anthropic reasoning fields were not retained as unsupported")
	}
	plan := NewAdapter().PlanCompatibility(kernel.CompatibilityContext{Request: parsed.Request, Policy: kernel.CompatibilityPolicy{RequiredFacets: kernel.RequiredRequestFacets(parsed.Request)}})
	if plan.Supported {
		t.Fatalf("OpenAI Chat codec reclaimed a parser-rejected Anthropic reasoning facet: %#v", plan)
	}
}

func TestParserUnsupportedAnthropicToolChoiceCannotBeReclaimedByOpenAIChatCodec(t *testing.T) {
	parsed, err := normalize.JSON("/v1/messages", http.Header{}, []byte(`{"model":"junior","max_tokens":512,"tool_choice":"vendor_mode","messages":[{"role":"user","content":"search"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !hasUnsupportedFacet(parsed.Request, kernel.FacetToolChoice) {
		t.Fatalf("parser did not retain unknown tool-choice semantics: %#v", parsed.Request.UnsupportedFacets)
	}
	plan := NewAdapter().PlanCompatibility(kernel.CompatibilityContext{Request: parsed.Request, Policy: kernel.CompatibilityPolicy{RequiredFacets: kernel.RequiredRequestFacets(parsed.Request)}})
	if plan.Supported {
		t.Fatalf("codec claimed a mapping for parser-unsupported tool choice: %#v", plan)
	}
}

func TestChatTransformRegistryToWireEncodesCanonicalMessageSuffix(t *testing.T) {
	parsed, err := normalize.JSON("/v1/chat/completions", http.Header{}, []byte(`{"model":"public","messages":[{"role":"user","content":"original"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	transform := appendCanonicalChatMessageTransform{}
	registry := kernel.NewRequestTransformRegistry()
	if err := registry.Register(transform); err != nil {
		t.Fatal(err)
	}
	binding := kernel.TransformBinding{ID: "append-image", TransformRef: transform.Definition().Ref, Enabled: true, Scope: kernel.TransformScope{Kind: kernel.TransformScopeDaemon}}
	if err := registry.ApplyScopes(context.Background(), &parsed.Request, []kernel.TransformBinding{binding}, kernel.TransformScope{Kind: kernel.TransformScopeDaemon}); err != nil {
		t.Fatal(err)
	}
	if !parsed.Request.Mutations.Messages {
		t.Fatal("kernel did not mark the appended message facet")
	}
	prepared, err := (Chat{}).Prepare(context.Background(), parsed.Request, kernel.Route{BaseURL: "https://provider.test/v1", ExternalModel: "m"}, kernel.Credential{})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.NewDecoder(prepared.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	messages := body["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("messages=%#v", messages)
	}
	added := messages[1].(map[string]any)
	content := added["content"].([]any)
	if added["role"] != "user" || content[0].(map[string]any)["text"] != "inspect" || content[1].(map[string]any)["image_url"].(map[string]any)["url"] != "https://example.test/appended.png" {
		t.Fatalf("canonical suffix was not mapped to OpenAI Chat wire: %#v", added)
	}
}

func TestNativeChatMessageRejectsUnmappedCanonicalContentParts(t *testing.T) {
	_, err := nativeChatMessage(normalize.Message{Role: "assistant", Content: []normalize.ContentPart{{Type: "thinking", Text: "private"}}})
	if err == nil || !strings.Contains(err.Error(), "has no native message mapping") {
		t.Fatalf("unmapped content part was accepted: %v", err)
	}
}

func TestNativeChatMessageRejectsUnmappedAddedMessageFields(t *testing.T) {
	tests := []struct {
		name    string
		message normalize.Message
	}{
		{name: "tool ID on user", message: normalize.Message{Role: "user", ToolCallID: "call-1", Content: "text"}},
		{name: "raw wire content", message: normalize.Message{Role: "user", Content: []any{map[string]any{"type": "vendor_block"}}}},
		{name: "untyped conversation object", message: normalize.Message{Role: "user", Content: map[string]any{"vendor": "opaque"}}},
		{name: "empty assistant", message: normalize.Message{Role: "assistant"}},
		{name: "user tool call", message: normalize.Message{Role: "user", Content: "text", ToolCalls: []normalize.ToolCall{{ID: "call-1", Name: "lookup"}}}},
		{name: "tool missing ID", message: normalize.Message{Role: "tool", Content: "result"}},
		{name: "unknown role", message: normalize.Message{Role: "function", Content: "result"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := nativeChatMessage(test.message); err == nil {
				t.Fatal("unrepresentable added message was accepted")
			}
		})
	}
}

func TestNativeChatEgressOverlaysOnlyTransformedTypedFacets(t *testing.T) {
	maxTokens, temperature, topP := 12, 0.2, 0.3
	request := normalize.Request{
		Model: "public", SourceFormat: normalize.FormatOpenAIChat,
		Raw: map[string]any{
			"model": "public", "messages": []any{map[string]any{"role": "user", "content": "original"}},
			"tools":       []any{map[string]any{"type": "function", "function": map[string]any{"name": "old"}, "vendor_tool": "preserve"}},
			"tool_choice": map[string]any{"type": "function", "function": map[string]any{"name": "old", "vendor_function": "preserve"}, "vendor_choice": "preserve"},
			"max_tokens":  float64(12), "temperature": float64(0.2), "top_p": float64(0.3), "stop": "old-stop",
			"vendor_extension": map[string]any{"preserve": true},
		},
		Messages:   []normalize.Message{{Role: "user", Content: "compressed", Metadata: map[string]any{"role": "user", "content": "original", "vendor_message": "preserve"}}},
		Tools:      []normalize.Tool{{Type: "function", Name: "old", Function: map[string]any{"name": "old", "parameters": map[string]any{"type": "object"}}, Metadata: map[string]any{"type": "function", "vendor_tool": "preserve", "function": map[string]any{"name": "old"}}}},
		ToolChoice: normalize.ToolChoice{Mode: "tool", Name: "old", Set: true, Metadata: map[string]any{"vendor_choice": "preserve", "function": map[string]any{"vendor_function": "preserve"}}},
		Generation: normalize.GenerationOptions{MaxOutputTokens: &maxTokens, Temperature: &temperature, TopP: &topP, StopSequences: []string{"old-stop"}},
	}
	registry := kernel.NewRequestTransformRegistry()
	transform := nativeChatRequestTransform{}
	if err := registry.Register(transform); err != nil {
		t.Fatal(err)
	}
	if err := registry.ApplyScopes(context.Background(), &request, []kernel.TransformBinding{{ID: "native-options", TransformRef: transform.Definition().Ref, Enabled: true, Scope: kernel.TransformScope{Kind: kernel.TransformScopeDaemon}}}, kernel.TransformScope{Kind: kernel.TransformScopeDaemon}); err != nil {
		t.Fatal(err)
	}
	if !request.Mutations.ToolChoice || !request.Mutations.Tools || !request.Mutations.GenerationMaxOutput || !request.Mutations.GenerationTemperature || !request.Mutations.GenerationTopP || !request.Mutations.GenerationStopSequences {
		t.Fatalf("kernel did not mark transformed facets: %#v", request.Mutations)
	}
	prepared, err := (Chat{}).Prepare(context.Background(), request, kernel.Route{BaseURL: "https://provider.test/v1", ExternalModel: "upstream"}, kernel.Credential{})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.NewDecoder(prepared.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["max_tokens"] != float64(64) || body["temperature"] != float64(0.7) || body["top_p"] != float64(0.8) || !reflect.DeepEqual(body["stop"], []any{"done"}) {
		t.Fatalf("generation options used stale raw values: %#v", body)
	}
	choice, ok := body["tool_choice"].(map[string]any)
	if !ok || choice["type"] != "function" || choice["function"].(map[string]any)["name"] != "lookup" {
		t.Fatalf("tool choice used stale raw value: %#v", body["tool_choice"])
	}
	tools, _ := body["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["function"].(map[string]any)["name"] != "lookup" {
		t.Fatalf("tool definitions used stale raw values: %#v", body["tools"])
	}
	if body["vendor_extension"].(map[string]any)["preserve"] != true {
		t.Fatalf("untouched opaque source extension was lost: %#v", body)
	}
	message := body["messages"].([]any)[0].(map[string]any)
	if message["vendor_message"] != "preserve" {
		t.Fatalf("untouched message metadata was lost: %#v", message)
	}
	if tools[0].(map[string]any)["vendor_tool"] != "preserve" {
		t.Fatalf("untouched tool metadata was lost: %#v", tools[0])
	}
	if choice["vendor_choice"] != "preserve" || choice["function"].(map[string]any)["vendor_function"] != "preserve" {
		t.Fatalf("untouched tool-choice metadata was lost: %#v", choice)
	}
}

func TestChatAdapterEmitsCanonicalSSEEvents(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"hello\",\"tool_calls\":[{\"id\":\"call-1\",\"function\":{\"name\":\"search\",\"arguments\":\"{}\"}}]}}]}\n\ndata: {\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}\n\ndata: [DONE]\n\n"
	var events []kernel.ResponseEvent
	recorder := httptest.NewRecorder()
	response := kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
	err := NewAdapter().RenderResponse(context.Background(), response, recorder, normalize.FormatOpenAIChat, kernel.StreamHooks{Streaming: true, OnEvent: func(event kernel.ResponseEvent) { events = append(events, event) }})
	if err != nil {
		t.Fatal(err)
	}
	seenText, seenTool, seenUsage := false, false, false
	for _, event := range events {
		seenText = seenText || event.Kind == kernel.EventTextDelta
		seenTool = seenTool || event.Kind == kernel.EventToolCallDelta
		seenUsage = seenUsage || event.Kind == kernel.EventUsage
	}
	if !seenText || !seenTool || !seenUsage {
		t.Fatalf("events=%#v", events)
	}
	if !strings.Contains(recorder.Body.String(), "[DONE]") {
		t.Fatalf("passthrough=%q", recorder.Body.String())
	}
}

func TestChatAdapterReportsMalformedSSE(t *testing.T) {
	var got kernel.ResponseEvent
	var streamErr error
	recorder := httptest.NewRecorder()
	response := kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {bad}\n\n"))}
	err := NewAdapter().RenderResponse(context.Background(), response, recorder, normalize.FormatOpenAIChat, kernel.StreamHooks{Streaming: true, OnEvent: func(event kernel.ResponseEvent) { got = event }, OnError: func(err error) { streamErr = err }})
	if err == nil || streamErr == nil || got.Kind != kernel.EventResponseError {
		t.Fatalf("err=%v streamErr=%v event=%#v", err, streamErr, got)
	}
}

func TestResponsesEventsKeepContinuityIdentity(t *testing.T) {
	body := "data: {\"type\":\"response.output_text.delta\",\"response_id\":\"resp_1\",\"item_id\":\"msg_1\",\"delta\":\"hi\"}\n\ndata: {\"type\":\"response.completed\",\"response_id\":\"resp_1\",\"response\":{\"status\":\"completed\"}}\n\n"
	var event kernel.ResponseEvent
	recorder := httptest.NewRecorder()
	response := kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
	if err := NewResponsesAdapter().RenderResponse(context.Background(), response, recorder, normalize.FormatOpenAIResponses, kernel.StreamHooks{Streaming: true, OnEvent: func(got kernel.ResponseEvent) {
		if got.Kind == kernel.EventTextDelta {
			event = got
		}
	}}); err != nil {
		t.Fatal(err)
	}
	if event.ResponseID != "resp_1" || event.ItemID != "msg_1" || event.ContentType != "output_text" {
		t.Fatalf("event=%#v", event)
	}
}

func TestResponsesJSONExposesResponseIDForSessionContinuity(t *testing.T) {
	body := `{"id":"resp_42","object":"response","status":"completed"}`
	var responseID string
	response := kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
	if err := NewResponsesAdapter().RenderResponse(context.Background(), response, httptest.NewRecorder(), normalize.FormatOpenAIResponses, kernel.StreamHooks{OnEvent: func(event kernel.ResponseEvent) {
		if event.Kind == kernel.EventResponseComplete {
			responseID = event.ResponseID
		}
	}}); err != nil {
		t.Fatal(err)
	}
	if responseID != "resp_42" {
		t.Fatalf("response ID for session continuity=%q", responseID)
	}
}

func TestResponsesEventsKeepOutputItemBoundaries(t *testing.T) {
	body := "data: {\"type\":\"response.output_item.added\",\"response_id\":\"resp_1\",\"item_id\":\"item_1\",\"item_type\":\"message\"}\n\ndata: {\"type\":\"response.output_item.done\",\"response_id\":\"resp_1\",\"item_id\":\"item_1\",\"status\":\"completed\"}\n\ndata: {\"type\":\"response.completed\",\"response_id\":\"resp_1\",\"response\":{\"status\":\"completed\"}}\n\n"
	var events []kernel.ResponseEvent
	recorder := httptest.NewRecorder()
	response := kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
	if err := NewResponsesAdapter().RenderResponse(context.Background(), response, recorder, normalize.FormatOpenAIResponses, kernel.StreamHooks{Streaming: true, OnEvent: func(event kernel.ResponseEvent) {
		if event.Kind != kernel.EventRawFrame {
			events = append(events, event)
		}
	}}); err != nil {
		t.Fatal(err)
	}
	startIndex, endIndex, completeIndex := -1, -1, -1
	for index, event := range events {
		switch event.Kind {
		case kernel.EventContentBlockStart:
			startIndex = index
		case kernel.EventContentBlockEnd:
			endIndex = index
		case kernel.EventResponseComplete:
			completeIndex = index
		}
	}
	if len(events) < 4 || events[0].Kind != kernel.EventResponseStarted || startIndex < 0 || endIndex <= startIndex || completeIndex <= endIndex {
		t.Fatalf("events=%#v", events)
	}
}

func TestChatJSONRendererPreservesBodyAndEmitsText(t *testing.T) {
	body := `{"id":"chat","choices":[{"message":{"content":"hello"},"finish_reason":"stop"}],"usage":{"input_tokens":2,"output_tokens":1}}`
	var events []kernel.ResponseEvent
	recorder := httptest.NewRecorder()
	response := kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
	if err := NewAdapter().RenderResponse(context.Background(), response, recorder, normalize.FormatOpenAIChat, kernel.StreamHooks{OnEvent: func(event kernel.ResponseEvent) { events = append(events, event) }}); err != nil {
		t.Fatal(err)
	}
	if recorder.Body.String() != body {
		t.Fatalf("body changed: %q", recorder.Body.String())
	}
	if len(events) < 2 || events[1].Kind != kernel.EventTextDelta || events[1].Text != "hello" {
		t.Fatalf("events=%#v", events)
	}
}

func TestAnthropicMessagesRequestMapsToOpenAIChatFromTypedIR(t *testing.T) {
	parsed, err := normalize.JSON("/v1/messages", http.Header{}, []byte(`{
"model":"junior","max_tokens":512,"temperature":0.2,"top_p":0.9,"stop_sequences":["stop"],
"system":[{"type":"text","text":"Be concise."}],
"tools":[{"name":"lookup","description":"Search","input_schema":{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}}],
"tool_choice":{"type":"tool","name":"lookup"},
"messages":[{"role":"user","content":[{"type":"text","text":"Check weather."},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]},
{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"lookup","input":{"q":"weather"}}]},
{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"Sunny."}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	composed := NewAdapter().(kernel.ComposedAdapter)
	plan := composed.PlanCompatibility(kernel.CompatibilityContext{Request: parsed.Request, Policy: kernel.CompatibilityPolicy{RequiredFacets: kernel.RequiredRequestFacets(parsed.Request)}})
	if !plan.Supported || plan.Fidelity != kernel.FidelityTranslated {
		t.Fatalf("representable Anthropic request was not admitted: %#v", plan)
	}
	route := kernel.Route{ID: "openai-route", BaseURL: "https://provider.test/v1", ExternalModel: "upstream-model", Enabled: true, CredentialPresentation: kernel.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "}}
	prepared, err := composed.Prepare(context.Background(), parsed.Request, route, kernel.Credential{Secret: "credential"})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.NewDecoder(prepared.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["model"] != "upstream-model" || body["stream"] != false || body["max_tokens"] != float64(512) || body["temperature"] != 0.2 || body["top_p"] != 0.9 {
		t.Fatalf("translated OpenAI request options=%#v", body)
	}
	if stop, ok := body["stop"].([]any); !ok || len(stop) != 1 || stop[0] != "stop" {
		t.Fatalf("translated stop sequences=%#v", body["stop"])
	}
	if choice, ok := body["tool_choice"].(map[string]any); !ok || choice["type"] != "function" || choice["function"].(map[string]any)["name"] != "lookup" {
		t.Fatalf("translated tool choice=%#v", body["tool_choice"])
	}
	messages, ok := body["messages"].([]any)
	if !ok || len(messages) != 4 {
		t.Fatalf("translated messages=%#v", body["messages"])
	}
	if messages[0].(map[string]any)["role"] != "system" || messages[0].(map[string]any)["content"] != "Be concise." {
		t.Fatalf("translated system prompt=%#v", messages[0])
	}
	userParts := messages[1].(map[string]any)["content"].([]any)
	imageURL := userParts[1].(map[string]any)["image_url"].(map[string]any)["url"]
	if imageURL != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("translated Anthropic image source=%#v", userParts[1])
	}
	assistant := messages[2].(map[string]any)
	calls := assistant["tool_calls"].([]any)
	function := calls[0].(map[string]any)["function"].(map[string]any)
	if function["name"] != "lookup" || function["arguments"] != `{"q":"weather"}` {
		t.Fatalf("translated tool call=%#v", calls[0])
	}
	toolResult := messages[3].(map[string]any)
	if toolResult["role"] != "tool" || toolResult["tool_call_id"] != "call_1" || toolResult["content"] != "Sunny." {
		t.Fatalf("translated tool result=%#v", toolResult)
	}
}

func TestAnthropicThinkingIntentIsRejectedByOpenAIChatCompatibilityBeforeEncoding(t *testing.T) {
	parsed, err := normalize.JSON("/v1/messages", http.Header{}, []byte(`{"model":"junior","max_tokens":512,"thinking":{"type":"enabled","budget_tokens":2000},"messages":[{"role":"user","content":"think"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	composed := NewAdapter().(kernel.ComposedAdapter)
	plan := composed.PlanCompatibility(kernel.CompatibilityContext{Request: parsed.Request, Policy: kernel.CompatibilityPolicy{RequiredFacets: kernel.RequiredRequestFacets(parsed.Request)}})
	if plan.Supported || plan.Fidelity != kernel.FidelityUnsupported {
		t.Fatalf("unmapped Anthropic thinking budget was admitted to OpenAI Chat: %#v", plan)
	}
}

func TestKernelExcludesOpenAIChatRouteForAnthropicThinkingBeforeDispatch(t *testing.T) {
	parsed, err := normalize.JSON("/v1/messages", http.Header{}, []byte(`{"model":"role","max_tokens":1024,"thinking":{"type":"enabled","budget_tokens":2048},"messages":[{"role":"user","content":"reason carefully"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := kernel.BuildSnapshot(kernel.SnapshotInput{
		PublicModels: []kernel.PublicModel{{Name: "role", TargetRef: "physical"}},
		Nodes:        []kernel.ModelNode{{ID: "physical", Kind: kernel.ModelPhysical, Strategy: kernel.StrategyFallback, Members: []kernel.MemberRef{{Kind: kernel.MemberRoute, ID: "openai-route", Fidelity: kernel.FidelityExact}}}},
		Routes: []kernel.Route{{ID: "openai-route", Enabled: true, BaseURL: "http://provider.test/v1", ExternalModel: "model", OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{
			normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"openai-chat"}},
		}}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	var dispatched atomic.Int32
	composed := NewAdapter().(kernel.ComposedAdapter)
	composed.Transport = kernel.HTTPTransport{Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		dispatched.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}}
	gateway, err := kernel.New(snapshot, nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	gateway.Adapters["openai-chat"] = composed
	writer := httptest.NewRecorder()
	err = gateway.Execute(context.Background(), parsed.Request, kernel.Credential{}, writer)
	if err == nil || dispatched.Load() != 0 {
		t.Fatalf("unmapped Anthropic thinking intent reached OpenAI Chat: err=%v dispatches=%d", err, dispatched.Load())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }
