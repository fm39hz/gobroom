package anthropic

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

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

type nativeAnthropicRequestTransform struct{}
type anthropicRoundTripFunc func(*http.Request) (*http.Response, error)

func (f anthropicRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func (nativeAnthropicRequestTransform) Definition() kernel.TransformDefinition {
	return kernel.TransformDefinition{Ref: extensions.Ref{Kind: kernel.RequestTransformKind, ID: "fixture.native-anthropic", ContractVersion: 1}, ImplementationVersion: "1", Label: "Native Anthropic fixture", Description: "Mutate typed native Anthropic facets.", Stage: kernel.TransformBeforeRequirements, Effects: []kernel.TransformEffect{kernel.TransformPrompt, kernel.TransformInput, kernel.TransformTools, kernel.TransformThinking, kernel.TransformOptions}}
}

func (nativeAnthropicRequestTransform) Apply(_ context.Context, request *kernel.NormalizedRequest, _ json.RawMessage) error {
	request.Prompt.Layers[0].Parts[0].Text = "new system policy"
	for index := range request.Messages {
		message := &request.Messages[index]
		switch message.Role {
		case "user":
			if message.ToolCallID != "" {
				message.Content = "new result"
			} else if len(message.ToolCalls) > 0 {
				message.Content = "new assistant text"
				message.ToolCalls[0].Arguments.(map[string]any)["q"] = "updated"
			} else {
				message.Content = "new user text"
			}
		case "assistant":
			message.Content = "new assistant text"
			message.ToolCalls[0].Arguments.(map[string]any)["q"] = "updated"
		case "tool":
			message.Content = "new result"
		}
	}
	request.Tools[0].Name = "lookup_v2"
	request.Tools[0].Function["name"] = "lookup_v2"
	request.Tools[0].Function["description"] = "updated tool"
	request.ToolChoice.Name = "lookup_v2"
	maxTokens, temperature, topP := 2048, 0.6, 0.9
	request.Generation.MaxOutputTokens, request.Generation.Temperature, request.Generation.TopP = &maxTokens, &temperature, &topP
	request.Generation.StopSequences = []string{"new stop"}
	request.Thinking.BudgetTokens = 2048
	return nil
}

func TestAnthropicToolAndTextEventsBecomeOpenAIChunks(t *testing.T) {
	body := strings.NewReader("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":12}}}\n\ndata: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_1\",\"name\":\"search\"}}\n\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"partial_json\":\"{}\"}}\n\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"done\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":4}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
	recorder := httptest.NewRecorder()
	var usage kernel.UsageEvent
	var events []kernel.ResponseEvent
	if err := NewAdapter().RenderResponse(context.Background(), kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(body)}, recorder, normalize.FormatOpenAIChat, kernel.StreamHooks{Streaming: true, OnEvent: func(event kernel.ResponseEvent) { events = append(events, event) }, OnComplete: func(event kernel.UsageEvent) { usage = event }}); err != nil {
		t.Fatal(err)
	}
	output := recorder.Body.String()
	if !strings.Contains(output, "tool_calls") || !strings.Contains(output, "done") || !strings.Contains(output, "tool_calls") {
		t.Fatalf("output=%s", output)
	}
	if usage.InputTokens != nil || !anthropicCountIs(usage.OutputTokens, 4) {
		t.Fatalf("usage=%#v", usage)
	}
	seenText, seenTool, seenUsage, seenStart, seenEnd := false, false, false, false, false
	for _, event := range events {
		seenText = seenText || event.Kind == kernel.EventTextDelta
		seenTool = seenTool || event.Kind == kernel.EventToolCallDelta
		seenUsage = seenUsage || event.Kind == kernel.EventUsage
		seenStart = seenStart || event.Kind == kernel.EventContentBlockStart
		seenEnd = seenEnd || event.Kind == kernel.EventContentBlockEnd
	}
	if !seenText || !seenTool || !seenUsage || !seenStart || !seenEnd {
		t.Fatalf("canonical events=%#v", events)
	}
}

func TestReasoningBudgetRaisesAnthropicOutputLimitOnlyWithNamedLossApproval(t *testing.T) {
	maxOutput := 512
	temperature, topP := 0.4, 0.8
	request := normalize.Request{
		Model: "role", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1,
		SourceFormat: normalize.FormatOpenAIChat, Messages: []normalize.Message{{Role: "user", Content: "transcribe"}},
		Thinking:   normalize.ThinkingIntent{Mode: "budget", BudgetTokens: 2048},
		Generation: normalize.GenerationOptions{MaxOutputTokens: &maxOutput, Temperature: &temperature, TopP: &topP, StopSequences: []string{"DONE"}},
	}
	adapter := NewAdapter().(kernel.ComposedAdapter)
	policy := kernel.CompatibilityPolicy{RequiredFacets: kernel.RequiredRequestFacets(request)}
	plan := adapter.PlanCompatibility(kernel.CompatibilityContext{Request: request, Policy: policy})
	if plan.Supported {
		t.Fatalf("output limit below reasoning budget was admitted without a loss grant: %#v", plan)
	}
	var loss *kernel.LossRecord
	ungrantedReason := ""
	for _, mapping := range plan.Mappings {
		if mapping.Facet == kernel.FacetReasoningBudget && len(mapping.Losses) == 1 {
			loss = &mapping.Losses[0]
			ungrantedReason = mapping.Reason
		}
	}
	if loss == nil || loss.ID != "anthropic.reasoning-budget-raises-max-output" || loss.Requested != float64(512) || loss.Effective != float64(3072) || !strings.Contains(ungrantedReason, "not permitted") {
		if loss == nil {
			t.Fatalf("reasoning-budget/output-limit mismatch lacks named loss evidence: %#v", plan.Mappings)
		}
		t.Fatalf("reasoning-budget/output-limit loss fields: id=%q requested=%T(%#v) effective=%T(%#v) reason=%q", loss.ID, loss.Requested, loss.Requested, loss.Effective, loss.Effective, ungrantedReason)
	}
	policy.AllowedLosses = []string{loss.ID}
	plan = adapter.PlanCompatibility(kernel.CompatibilityContext{Request: request, Policy: policy})
	if !plan.Supported {
		t.Fatalf("explicitly granted Anthropic budget loss was not admitted: %#v", plan)
	}
	request.UnsupportedFacets = []string{kernel.FacetReasoningIntent}
	plan = adapter.PlanCompatibility(kernel.CompatibilityContext{Request: request, Policy: kernel.CompatibilityPolicy{RequiredFacets: kernel.RequiredRequestFacets(request)}})
	if plan.Supported {
		t.Fatalf("Anthropic codec reclaimed a parser-rejected reasoning facet: %#v", plan)
	}
	request.UnsupportedFacets = nil
	prepared, err := (Messages{}).Prepare(context.Background(), request, kernel.Route{BaseURL: "https://provider.test/v1", ExternalModel: "claude-test"}, kernel.Credential{})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.NewDecoder(prepared.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	thinking, _ := body["thinking"].(map[string]any)
	if thinking["budget_tokens"] != float64(2048) || body["max_tokens"] != float64(3072) || body["temperature"] != 0.4 || body["top_p"] != 0.8 || !reflect.DeepEqual(body["stop_sequences"], []any{"DONE"}) {
		t.Fatalf("Anthropic payload did not match admitted requested/effective values: %#v", body)
	}
}

func TestKernelAppliesOnlyApprovedAnthropicReasoningBudgetLossToWire(t *testing.T) {
	const lossID = "anthropic.reasoning-budget-raises-max-output"
	makeKernel := func(allow bool, dispatches *atomic.Int32, observed *int) *kernel.Kernel {
		policy := kernel.LossPolicy{}
		ceiling := kernel.LossPolicyCeiling{AllowOnly: true}
		if allow {
			policy.Allow = []string{lossID}
			ceiling.Allow = []string{lossID}
		}
		snapshot, err := kernel.BuildSnapshot(kernel.SnapshotInput{
			LossCeiling:  ceiling,
			PublicModels: []kernel.PublicModel{{Name: "role", TargetRef: "physical"}},
			Nodes: []kernel.ModelNode{{
				ID: "physical", Kind: kernel.ModelPhysical, Strategy: kernel.StrategyFallback,
				Members: []kernel.MemberRef{{Kind: kernel.MemberRoute, ID: "route", Fidelity: kernel.FidelityExact}}, LossPolicy: policy,
			}},
			Routes: []kernel.Route{{
				ID: "route", Enabled: true, Protocol: kernel.ProtocolAnthropic, CredentialPresentation: kernel.CredentialPresentation{Header: "x-api-key"}, BaseURL: "https://provider.test/v1", ExternalModel: "claude-model",
				OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"anthropic-messages"}}},
			}},
		}, 1)
		if err != nil {
			t.Fatal(err)
		}
		engine, err := kernel.New(snapshot, nil, 4)
		if err != nil {
			t.Fatal(err)
		}
		composed := NewAdapter().(kernel.ComposedAdapter)
		composed.Transport = kernel.HTTPTransport{Client: &http.Client{Transport: anthropicRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			dispatches.Add(1)
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode Anthropic request: %v", err)
			} else if value, ok := body["max_tokens"].(float64); !ok || int(value) != 3072 {
				t.Errorf("approved reasoning-budget loss was not applied on wire: max_tokens=%#v body=%#v", body["max_tokens"], body)
			} else {
				*observed = int(value)
			}
			responseBody := `{"id":"msg_loss","type":"message","role":"assistant","model":"claude-model","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":4,"output_tokens":2}}`
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(responseBody)), Request: request}, nil
		})}}
		engine.Adapters["anthropic-messages"] = composed
		return engine
	}
	maxOutput := 512
	request := kernel.NormalizedRequest{
		Model: "role", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1,
		SourceFormat: normalize.FormatOpenAIChat, Messages: []normalize.Message{{Role: "user", Content: "reason carefully"}},
		Thinking:   normalize.ThinkingIntent{Mode: "budget", BudgetTokens: 2048},
		Generation: normalize.GenerationOptions{MaxOutputTokens: &maxOutput},
	}

	var deniedDispatches atomic.Int32
	deniedObserved := 0
	denied := makeKernel(false, &deniedDispatches, &deniedObserved)
	err := denied.Execute(context.Background(), request, kernel.Credential{}, httptest.NewRecorder())
	denied.Close()
	if err == nil || deniedDispatches.Load() != 0 {
		t.Fatalf("unapproved reasoning-budget loss reached provider: err=%v dispatches=%d", err, deniedDispatches.Load())
	}

	var dispatches atomic.Int32
	observedMaxTokens := 0
	allowed := makeKernel(true, &dispatches, &observedMaxTokens)
	writer := httptest.NewRecorder()
	if err := allowed.Execute(context.Background(), request, kernel.Credential{}, writer); err != nil {
		allowed.Close()
		t.Fatal(err)
	}
	if dispatches.Load() != 1 || observedMaxTokens != 3072 {
		allowed.Close()
		t.Fatalf("approved loss wire execution: dispatches=%d max_tokens=%d", dispatches.Load(), observedMaxTokens)
	}
	select {
	case event := <-allowed.Events:
		validLoss := false
		if len(event.CompatibilityLosses) == 1 {
			requested, requestedOK := lossInteger(event.CompatibilityLosses[0].Requested)
			effective, effectiveOK := lossInteger(event.CompatibilityLosses[0].Effective)
			validLoss = event.CompatibilityLosses[0].ID == lossID && requestedOK && effectiveOK && requested == 512 && effective == 3072
		}
		if event.CompatibilityFidelity != kernel.FidelityLossy || !validLoss {
			allowed.Close()
			t.Fatalf("usage does not attest the applied wire loss: %#v", event)
		}
	default:
		allowed.Close()
		t.Fatal("successful execution omitted compatibility usage")
	}
	allowed.Close()
}

func TestReasoningBudgetPreservesSufficientExplicitOutputLimit(t *testing.T) {
	parsed, err := normalize.JSON("/v1/chat/completions", http.Header{}, []byte(`{"model":"role","messages":[{"role":"user","content":"hello"}],"reasoning":{"max_output_tokens":2048},"max_tokens":4096}`))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := (Messages{}).Prepare(t.Context(), parsed.Request, kernel.Route{ExternalModel: "claude", CredentialPresentation: kernel.CredentialPresentation{Header: "x-api-key"}}, kernel.Credential{Secret: "fixture-key"})
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		MaxTokens int `json:"max_tokens"`
	}
	if err := json.NewDecoder(prepared.Body).Decode(&wire); err != nil {
		t.Fatal(err)
	}
	if wire.MaxTokens != 4096 {
		t.Fatalf("sufficient explicit limit changed without a named loss: %d", wire.MaxTokens)
	}
}

func TestKernelMapsOpenAIResponsesThroughAnthropicMessagesAndBack(t *testing.T) {
	responseBody := `{"id":"msg_responses_crosswire","type":"message","role":"assistant","model":"claude-physical","content":[{"type":"text","text":"I found it."},{"type":"tool_use","id":"call_anthropic","name":"lookup","input":{"found":true}}],"stop_reason":"tool_use","usage":{"input_tokens":9,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":4}}`
	var dispatches atomic.Int32
	composed := NewAdapter().(kernel.ComposedAdapter)
	composed.Transport = kernel.HTTPTransport{Client: &http.Client{Transport: anthropicRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		dispatches.Add(1)
		if request.URL.String() != "https://provider.test/v1/messages" || request.Header.Get("x-api-key") != "anthropic-key" {
			t.Errorf("Anthropic request target/auth: url=%q x-api-key=%q", request.URL.String(), request.Header.Get("x-api-key"))
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode Anthropic request: %v", err)
		} else {
			messages, _ := body["messages"].([]any)
			tools, _ := body["tools"].([]any)
			system, _ := body["system"].([]any)
			choice, _ := body["tool_choice"].(map[string]any)
			var content []any
			if len(messages) == 1 {
				if message, ok := messages[0].(map[string]any); ok {
					content, _ = message["content"].([]any)
				}
			}
			var source map[string]any
			if len(content) > 1 {
				if image, ok := content[1].(map[string]any); ok {
					source, _ = image["source"].(map[string]any)
				}
			}
			if body["model"] != "claude-physical" || body["input"] != nil || len(messages) != 1 || len(content) != 2 || content[0].(map[string]any)["text"] != "Find item 7." || content[1].(map[string]any)["type"] != "image" || source["type"] != "base64" || source["media_type"] != "image/png" || source["data"] != "aGVsbG8=" || len(tools) != 1 || len(system) != 1 || body["max_tokens"] != float64(512) || choice["type"] != "any" {
				t.Errorf("Responses request was not rebuilt as Anthropic Messages: %#v", body)
			}
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(responseBody)), Request: request}, nil
	})}}
	snapshot, err := kernel.BuildSnapshot(kernel.SnapshotInput{
		PublicModels: []kernel.PublicModel{{Name: "junior", TargetRef: "physical"}},
		Nodes:        []kernel.ModelNode{{ID: "physical", Kind: kernel.ModelPhysical, Strategy: kernel.StrategyFallback, Members: []kernel.MemberRef{{Kind: kernel.MemberRoute, ID: "route", Fidelity: kernel.FidelityExact}}}},
		Routes: []kernel.Route{{
			ID: "route", Enabled: true, Protocol: kernel.ProtocolAnthropic, CredentialPresentation: kernel.CredentialPresentation{Header: "x-api-key"}, BaseURL: "https://provider.test/v1", ExternalModel: "claude-physical",
			Profile: kernel.CapabilityProfile{
				kernel.CapabilityTools:  {State: kernel.SupportNative},
				kernel.CapabilityVision: {State: kernel.SupportNative},
			},
			OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"anthropic-messages"}}},
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
	engine.Adapters["anthropic-messages"] = composed
	requestBody := `{"model":"junior","instructions":"Use tools when needed.","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Find item 7."},{"type":"input_image","image_url":"data:image/png;base64,aGVsbG8="}]}],"tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"id":{"type":"integer"}}}}],"tool_choice":"required","max_output_tokens":512}`
	parsed, err := normalize.JSON("/v1/responses", http.Header{}, []byte(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	explanations, err := engine.ExplainCompatibility(context.Background(), "junior", parsed.Request)
	if err != nil {
		t.Fatal(err)
	}
	if len(explanations) != 1 || !explanations[0].Compatible {
		t.Fatalf("Responses→Anthropic compatibility=%#v", explanations)
	}
	writer := httptest.NewRecorder()
	if err := engine.Execute(context.Background(), parsed.Request, kernel.Credential{Secret: "anthropic-key"}, writer); err != nil {
		t.Fatal(err)
	}
	var result struct {
		ID     string           `json:"id"`
		Object string           `json:"object"`
		Status string           `json:"status"`
		Output []map[string]any `json:"output"`
		Usage  *struct {
			InputTokens        *int64 `json:"input_tokens"`
			OutputTokens       *int64 `json:"output_tokens"`
			TotalTokens        *int64 `json:"total_tokens"`
			InputTokensDetails *struct {
				CachedTokens     *int64 `json:"cached_tokens"`
				CacheWriteTokens *int64 `json:"cache_write_tokens"`
			} `json:"input_tokens_details"`
			OutputTokensDetails *struct {
				ReasoningTokens *int64 `json:"reasoning_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(writer.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode Responses projection %q: %v", writer.Body.String(), err)
	}
	if dispatches.Load() != 1 || !strings.HasPrefix(result.ID, "resp_gobroom_") || result.Object != "response" || result.Status != "completed" || len(result.Output) != 2 {
		t.Fatalf("Anthropic→Responses result=%#v dispatches=%d", result, dispatches.Load())
	}
	if result.Output[0]["type"] != "message" || result.Output[1]["type"] != "function_call" || result.Output[1]["call_id"] != "call_anthropic" || result.Usage == nil || !anthropicCountIs(result.Usage.InputTokens, 9) || !anthropicCountIs(result.Usage.OutputTokens, 4) {
		t.Fatalf("Anthropic semantic output/usage=%#v usage=%#v", result.Output, result.Usage)
	}
	if result.Usage.InputTokensDetails == nil || !anthropicCountIs(result.Usage.InputTokensDetails.CachedTokens, 0) || !anthropicCountIs(result.Usage.InputTokensDetails.CacheWriteTokens, 0) {
		t.Fatalf("Anthropic projected usage lost reported cache zeros: %#v", result.Usage)
	}
	continuity, err := normalize.JSON("/v1/responses", http.Header{}, []byte(`{"model":"junior","previous_response_id":"resp_previous","input":"continue"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Execute(context.Background(), continuity.Request, kernel.Credential{Secret: "anthropic-key"}, httptest.NewRecorder()); !errors.Is(err, kernel.ErrNoRoute) || dispatches.Load() != 1 {
		t.Fatalf("Responses continuity was silently dropped on Anthropic route: err=%v dispatches=%d", err, dispatches.Load())
	}
	for _, unsupported := range []string{
		`{"model":"junior","input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,aGVsbG8=","detail":"high"}]}]}`,
		`{"model":"junior","input":[{"type":"message","role":"user","content":[{"type":"input_image","file_id":"file_123"}]}]}`,
	} {
		unsupportedRequest, err := normalize.JSON("/v1/responses", http.Header{}, []byte(unsupported))
		if err != nil {
			t.Fatal(err)
		}
		if err := engine.Execute(context.Background(), unsupportedRequest.Request, kernel.Credential{Secret: "anthropic-key"}, httptest.NewRecorder()); !errors.Is(err, kernel.ErrNoRoute) || dispatches.Load() != 1 {
			t.Fatalf("unmapped Responses image semantics reached Anthropic: request=%s err=%v calls=%d", unsupported, err, dispatches.Load())
		}
	}
}

func TestAnthropicPassthroughJSONPreservesBodyAndReportsUsage(t *testing.T) {
	body := `{"id":"msg_live","type":"message","model":"claude-opus-4-8","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn","usage":{"input_tokens":10370,"output_tokens":4}}`
	recorder := httptest.NewRecorder()
	var usage kernel.UsageEvent
	err := NewAdapter().RenderResponse(context.Background(), kernel.UpstreamResponse{
		Status:  http.StatusOK,
		Headers: http.Header{"Content-Type": []string{"application/json"}},
		Body:    io.NopCloser(strings.NewReader(body)),
	}, recorder, normalize.FormatAnthropic, kernel.StreamHooks{OnComplete: func(event kernel.UsageEvent) { usage = event }})
	if err != nil {
		t.Fatal(err)
	}
	if recorder.Body.String() != body {
		t.Fatalf("Anthropic passthrough body changed: %s", recorder.Body.String())
	}
	if usage.Status != "ok" || usage.InputTokens != nil || !anthropicCountIs(usage.OutputTokens, 4) {
		t.Fatalf("usage=%#v", usage)
	}
}

func TestAnthropicPassthroughStreamReportsUsageWithoutChangingEvents(t *testing.T) {
	body := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":21}}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":7}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	recorder := httptest.NewRecorder()
	var usage kernel.UsageEvent
	err := NewAdapter().RenderResponse(context.Background(), kernel.UpstreamResponse{
		Status:  http.StatusOK,
		Headers: http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:    io.NopCloser(strings.NewReader(body)),
	}, recorder, normalize.FormatAnthropic, kernel.StreamHooks{Streaming: true, OnComplete: func(event kernel.UsageEvent) { usage = event }})
	if err != nil {
		t.Fatal(err)
	}
	if recorder.Body.String() != body {
		t.Fatalf("Anthropic SSE passthrough body changed: %s", recorder.Body.String())
	}
	if usage.Status != "ok" || usage.InputTokens != nil || !anthropicCountIs(usage.OutputTokens, 7) {
		t.Fatalf("usage=%#v", usage)
	}
}

func anthropicCountIs(value *int64, want int64) bool {
	return value != nil && *value == want
}

func TestAnthropicUsageFramePreservesMissingFieldsAndExplicitZero(t *testing.T) {
	state := anthropicUsageState{InputTokens: new(int64(7)), OutputTokens: new(int64(4)), CachedReadTokens: new(int64(1)), CachedWriteTokens: new(int64(2))}
	frame, err := decodeAnthropicUsageFrame([]byte(`{"usage":{"input_tokens":0}}`))
	if err != nil {
		t.Fatal(err)
	}
	usage := state.apply(frame.EventUsage).snapshot()
	if !anthropicCountIs(usage.InputTokens, 3) || !anthropicCountIs(usage.OutputTokens, 4) {
		t.Fatalf("partial usage update=%#v", usage)
	}
}

func TestAnthropicCacheInputTotalIncludesReadAndCreationOnce(t *testing.T) {
	body := `{"id":"msg_cache","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"cache_read_input_tokens":8,"cache_creation_input_tokens":4,"output_tokens":1}}`
	var completed kernel.UsageEvent
	err := NewAdapter().RenderResponse(context.Background(), kernel.UpstreamResponse{
		Status: http.StatusOK, Headers: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)),
	}, httptest.NewRecorder(), normalize.FormatOpenAIChat, kernel.StreamHooks{OnComplete: func(event kernel.UsageEvent) { completed = event }})
	if err != nil {
		t.Fatal(err)
	}
	if !anthropicCountIs(completed.InputTokens, 14) || !anthropicCountIs(completed.CachedReadTokens, 8) || !anthropicCountIs(completed.CachedWriteTokens, 4) {
		t.Fatalf("Anthropic input must include uncached, cache-read and cache-creation tokens exactly once: %#v", completed)
	}
}

func TestAnthropicSSEPartialUsageSnapshotsRetainCacheComponents(t *testing.T) {
	body := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":2,\"cache_read_input_tokens\":8}}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"cache_creation_input_tokens\":4,\"output_tokens\":1}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	var completed kernel.UsageEvent
	err := NewAdapter().RenderResponse(context.Background(), kernel.UpstreamResponse{
		Status: http.StatusOK, Headers: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body)),
	}, httptest.NewRecorder(), normalize.FormatOpenAIChat, kernel.StreamHooks{Streaming: true, OnComplete: func(event kernel.UsageEvent) { completed = event }})
	if err != nil {
		t.Fatal(err)
	}
	if !anthropicCountIs(completed.InputTokens, 14) || !anthropicCountIs(completed.CachedReadTokens, 8) || !anthropicCountIs(completed.CachedWriteTokens, 4) {
		t.Fatalf("partial Anthropic frames must produce one coherent total snapshot: %#v", completed)
	}
}

func TestAnthropicUsageInvalidCountIsUnknownWithIssue(t *testing.T) {
	frame, err := decodeAnthropicUsageFrame([]byte(`{"usage":{"input_tokens":1.5,"output_tokens":7}}`))
	if err != nil {
		t.Fatal(err)
	}
	usage := (anthropicUsageState{}).apply(frame.EventUsage).snapshot()
	if usage.InputTokens != nil || !anthropicCountIs(usage.OutputTokens, 7) || len(usage.AccountingIssues) != 1 || usage.AccountingIssues[0].Code != "invalid_usage_counter" {
		t.Fatalf("fractional count must be unknown with an issue, preserving valid siblings: %#v", usage)
	}
}

func TestAnthropicDecoderEmitsThinkingSignatureAsCanonicalEvent(t *testing.T) {
	body := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_signed\",\"usage\":{\"input_tokens\":3}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\",\"signature\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"reasoning\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"signed-by-provider\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":4}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	var seenThinking, seenSignature bool
	response := kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
	if err := NewAdapter().RenderResponse(context.Background(), response, httptest.NewRecorder(), normalize.FormatOpenAIChat, kernel.StreamHooks{Streaming: true, OnEvent: func(event kernel.ResponseEvent) {
		seenThinking = seenThinking || event.Kind == kernel.EventThinkingDelta && event.Text == "reasoning" && event.Index == 0
		seenSignature = seenSignature || event.Kind == kernel.EventThinkingSignature && event.Signature == "signed-by-provider" && event.Index == 0
	}}); err != nil {
		t.Fatal(err)
	}
	if !seenThinking || !seenSignature {
		t.Fatalf("Anthropic decoder lost thinking/signature events: thinking=%v signature=%v", seenThinking, seenSignature)
	}
}

func TestAnthropicPrepareTranslatesReasoningWithoutMetadataShim(t *testing.T) {
	request := normalize.Request{SourceFormat: normalize.FormatOpenAIChat, Raw: map[string]any{"messages": []any{}}, Thinking: normalize.ThinkingIntent{Mode: "level", Effort: "high"}}
	prepared, err := (Messages{}).Prepare(context.Background(), request, kernel.Route{ID: "route", BaseURL: "https://provider.test", ExternalModel: "claude"}, kernel.Credential{})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.NewDecoder(prepared.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["metadata"]; ok {
		t.Fatalf("metadata shim remains: %#v", body)
	}
	thinking, ok := body["thinking"].(map[string]any)
	if !ok || thinking["type"] != "adaptive" {
		t.Fatalf("thinking=%#v", body["thinking"])
	}
	output, ok := body["output_config"].(map[string]any)
	if !ok || output["effort"] != "high" {
		t.Fatalf("output_config=%#v", body["output_config"])
	}
}

func TestAnthropicRequestMapsPromptPlanToTopLevelSystem(t *testing.T) {
	request := normalize.Request{SourceFormat: normalize.FormatOpenAIChat, Prompt: normalize.PromptPlan{Layers: []normalize.PromptLayer{{Origin: normalize.PromptInline, Role: "system", Text: "policy"}}}, Messages: []normalize.Message{{Role: "user", Content: "hello"}}}
	prepared, err := (Messages{}).Prepare(context.Background(), request, kernel.Route{BaseURL: "https://provider.test", ExternalModel: "claude"}, kernel.Credential{})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.NewDecoder(prepared.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	system, ok := body["system"].([]any)
	if !ok || len(system) != 1 || system[0].(map[string]any)["text"] != "policy" {
		t.Fatalf("system=%#v", body["system"])
	}
}

func TestNativeAnthropicEgressOverlaysTypedFacetsAndPreservesBlocks(t *testing.T) {
	parsed, err := normalize.JSON("/v1/messages", http.Header{}, []byte(`{
"model":"role","max_tokens":512,"temperature":0.2,"top_p":0.3,"stop_sequences":["old stop"],
"thinking":{"type":"enabled","budget_tokens":1024},
"system":[{"type":"text","text":"old system","cache_control":{"type":"ephemeral"}}],
"tools":[{"name":"lookup","description":"old tool","input_schema":{"type":"object"},"vendor_tool":"keep"}],
"tool_choice":{"type":"tool","name":"lookup","disable_parallel_tool_use":true,"vendor_choice":"keep"},
"messages":[
 {"role":"user","content":[{"type":"text","text":"old user","cache_control":{"type":"ephemeral"}}]},
 {"role":"assistant","content":[{"type":"text","text":"old assistant"},{"type":"tool_use","id":"call_1","name":"lookup","input":{"q":"old"}}]},
 {"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"old result"}]},
 {"role":"assistant","content":[{"type":"vendor_custom_block","payload":{"opaque":"keep"}}]}
]}`))
	if err != nil {
		t.Fatal(err)
	}
	registry := kernel.NewRequestTransformRegistry()
	transform := nativeAnthropicRequestTransform{}
	if err := registry.Register(transform); err != nil {
		t.Fatal(err)
	}
	if err := registry.ApplyScopes(context.Background(), &parsed.Request, []kernel.TransformBinding{{ID: "native-anthropic", TransformRef: transform.Definition().Ref, Enabled: true, Scope: kernel.TransformScope{Kind: kernel.TransformScopeDaemon}}}, kernel.TransformScope{Kind: kernel.TransformScopeDaemon}); err != nil {
		t.Fatal(err)
	}
	adapter := NewAdapter().(kernel.ComposedAdapter)
	policy := kernel.CompatibilityPolicy{RequiredFacets: kernel.RequiredRequestFacets(parsed.Request)}
	plan := adapter.PlanCompatibility(kernel.CompatibilityContext{Request: parsed.Request, Policy: policy})
	if !plan.Supported {
		t.Fatalf("native Anthropic transform overlay was not admitted: %#v", plan)
	}
	prepared, err := (Messages{}).Prepare(context.Background(), parsed.Request, kernel.Route{BaseURL: "https://provider.test/v1", ExternalModel: "claude"}, kernel.Credential{})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.NewDecoder(prepared.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["max_tokens"] != float64(2048) || body["temperature"] != float64(0.6) || body["top_p"] != float64(0.9) || !reflect.DeepEqual(body["stop_sequences"], []any{"new stop"}) {
		t.Fatalf("Anthropic generation fields were stale: %#v", body)
	}
	if thinking, ok := body["thinking"].(map[string]any); !ok || thinking["type"] != "enabled" || thinking["budget_tokens"] != float64(2048) {
		t.Fatalf("Anthropic thinking was stale: %#v", body["thinking"])
	}
	system := body["system"].([]any)[0].(map[string]any)
	if system["text"] != "new system policy" || system["cache_control"].(map[string]any)["type"] != "ephemeral" {
		t.Fatalf("Anthropic system block lost transformed/opaque fields: %#v", system)
	}
	tools := body["tools"].([]any)
	if tools[0].(map[string]any)["description"] != "updated tool" || tools[0].(map[string]any)["vendor_tool"] != "keep" {
		t.Fatalf("Anthropic tool overlay lost metadata: %#v", tools)
	}
	choice := body["tool_choice"].(map[string]any)
	if choice["name"] != "lookup_v2" || choice["vendor_choice"] != "keep" {
		t.Fatalf("Anthropic tool-choice overlay lost metadata: %#v", choice)
	}
	messages := body["messages"].([]any)
	if len(messages) != 4 || messages[3].(map[string]any)["content"].([]any)[0].(map[string]any)["type"] != "vendor_custom_block" {
		t.Fatalf("opaque unsupported message block was not forwarded unchanged: %#v", messages)
	}
	userContent := messages[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	assistantContent := messages[1].(map[string]any)["content"].([]any)
	toolUse := assistantContent[1].(map[string]any)
	toolResult := messages[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if userContent["text"] != "new user text" || userContent["cache_control"].(map[string]any)["type"] != "ephemeral" || assistantContent[0].(map[string]any)["text"] != "new assistant text" || toolUse["input"].(map[string]any)["q"] != "updated" || toolResult["content"] != "new result" {
		t.Fatalf("Anthropic message block overlay lost content/order/provenance: %#v", messages)
	}
}

func TestNativeAnthropicOverlayRejectsReorderedSourceMessages(t *testing.T) {
	parsed, err := normalize.JSON("/v1/messages", http.Header{}, []byte(`{"model":"role","max_tokens":32,"messages":[{"role":"user","content":"first"},{"role":"assistant","content":"second"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	messages := parsed.Request.Messages
	messages[0], messages[1] = messages[1], messages[0]
	_, err = overlayAnthropicMessages(parsed.Request.Raw["messages"], messages)
	if err == nil || !strings.Contains(err.Error(), "message reorder cannot be safely overlaid") {
		t.Fatalf("reordered Anthropic source messages were not rejected explicitly: %v", err)
	}
}

func TestNativeAnthropicOverlayAppendsTypedMessagesAndToolResults(t *testing.T) {
	parsed, err := normalize.JSON("/v1/messages", http.Header{}, []byte(`{"model":"role","max_tokens":32,"messages":[{"role":"user","content":[{"type":"text","text":"keep"},{"type":"vendor_custom_block","payload":{"opaque":"keep"}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	messages := append([]normalize.Message(nil), parsed.Request.Messages...)
	messages = append(messages,
		normalize.Message{Role: "assistant", Content: "new assistant text", ToolCalls: []normalize.ToolCall{{ID: "call-new", Type: "function", Name: "lookup", Arguments: map[string]any{"q": "x"}}}},
		normalize.Message{Role: "tool", ToolCallID: "call-new", Content: "new tool result"},
	)
	updated, err := overlayAnthropicMessages(parsed.Request.Raw["messages"], messages)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated) != 3 {
		t.Fatalf("message count=%d want original plus two additions: %#v", len(updated), updated)
	}
	original := updated[0].(map[string]any)
	blocks := original["content"].([]any)
	if blocks[1].(map[string]any)["type"] != "vendor_custom_block" {
		t.Fatalf("opaque source block moved or changed: %#v", original)
	}
	assistant := updated[1].(map[string]any)
	assistantBlocks := assistant["content"].([]any)
	if assistant["role"] != "assistant" || assistantBlocks[0].(map[string]any)["text"] != "new assistant text" || assistantBlocks[1].(map[string]any)["type"] != "tool_use" {
		t.Fatalf("new assistant/tool-use message was not projected: %#v", assistant)
	}
	toolResult := updated[2].(map[string]any)
	resultBlock := toolResult["content"].([]any)[0].(map[string]any)
	if toolResult["role"] != "user" || resultBlock["type"] != "tool_result" || resultBlock["tool_use_id"] != "call-new" || resultBlock["content"] != "new tool result" {
		t.Fatalf("new tool result was not projected into Anthropic user content: %#v", toolResult)
	}
}

func TestNativeAnthropicOverlayRejectsUnrepresentableAddedContent(t *testing.T) {
	parsed, err := normalize.JSON("/v1/messages", http.Header{}, []byte(`{"model":"role","max_tokens":32,"messages":[{"role":"user","content":"original"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	messages := append([]normalize.Message(nil), parsed.Request.Messages...)
	messages = append(messages, normalize.Message{Role: "assistant", Content: []normalize.ContentPart{{Type: "thinking", Text: "unsigned"}}})
	if _, err := overlayAnthropicMessages(parsed.Request.Raw["messages"], messages); err == nil || !strings.Contains(err.Error(), "no native insertion mapping") {
		t.Fatalf("unrepresentable added thinking content was not rejected: %v", err)
	}
}

func TestNativeAnthropicAddedMessageRejectsUnmappedFields(t *testing.T) {
	tests := []struct {
		name    string
		message normalize.Message
	}{
		{name: "tool ID on user", message: normalize.Message{Role: "user", ToolCallID: "call-1", Content: "text"}},
		{name: "empty assistant", message: normalize.Message{Role: "assistant"}},
		{name: "assistant tool ID", message: normalize.Message{Role: "assistant", ToolCallID: "call-1", Content: "text"}},
		{name: "user tool call", message: normalize.Message{Role: "user", Content: "text", ToolCalls: []normalize.ToolCall{{ID: "call-1", Name: "lookup"}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := nativeAnthropicAddedMessage(test.message); err == nil {
				t.Fatal("unrepresentable added message was accepted")
			}
		})
	}
}

func TestNativeAnthropicOverlayRejectsAdditionsInsertedBetweenSourceMessages(t *testing.T) {
	parsed, err := normalize.JSON("/v1/messages", http.Header{}, []byte(`{"model":"role","max_tokens":32,"messages":[{"role":"user","content":"first"},{"role":"assistant","content":"second"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	messages := []normalize.Message{parsed.Request.Messages[0], {Role: "assistant", Content: "inserted"}, parsed.Request.Messages[1]}
	if _, err := overlayAnthropicMessages(parsed.Request.Raw["messages"], messages); err == nil || !strings.Contains(err.Error(), "must form a suffix") {
		t.Fatalf("middle insertion was not rejected explicitly: %v", err)
	}
}

func TestNativeAnthropicOverlayRejectsSourceMessageRoleChange(t *testing.T) {
	parsed, err := normalize.JSON("/v1/messages", http.Header{}, []byte(`{"model":"role","max_tokens":32,"messages":[{"role":"user","content":"text"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	messages := append([]normalize.Message(nil), parsed.Request.Messages...)
	messages[0].Role = "assistant"
	if _, err := overlayAnthropicMessages(parsed.Request.Raw["messages"], messages); err == nil || !strings.Contains(err.Error(), "source message role is immutable") {
		t.Fatalf("source role change was not rejected: %v", err)
	}
}

func TestNativeAnthropicOverlayRejectsToolHistoryRoleMutation(t *testing.T) {
	parsed, err := normalize.JSON("/v1/messages", http.Header{}, []byte(`{"model":"role","max_tokens":32,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call-1","name":"lookup","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":"ok"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	messages := append([]normalize.Message(nil), parsed.Request.Messages...)
	messages[0].Role = "user"
	if _, err := overlayAnthropicMessages(parsed.Request.Raw["messages"], messages); err == nil || !strings.Contains(err.Error(), "source message role is immutable") {
		t.Fatalf("role change that invalidates tool_use history was not rejected: %v", err)
	}
}
