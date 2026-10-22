package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

type nativeResponsesMutationFixture struct{}
type responsesInputMutationFixture struct{}
type responsesToolHistoryMutationFixture struct{}
type responsesUnsupportedReasoningMutationFixture struct{}
type rewriteToolArgumentResponseTransform struct{}

func (rewriteToolArgumentResponseTransform) Definition() kernel.ResponseTransformDefinition {
	return kernel.ResponseTransformDefinition{Ref: extensions.Ref{Kind: kernel.ResponseTransformKind, ID: "fixture.rewrite-tool-arguments", ContractVersion: 1}, ImplementationVersion: "1", Label: "Rewrite tool arguments", Description: "Test semantic re-encoding while preserving call correlation.", Effects: []kernel.ResponseTransformEffect{kernel.ResponseEffectToolArguments}}
}

func (rewriteToolArgumentResponseTransform) ApplyResponse(_ context.Context, event kernel.ResponseEvent, _ json.RawMessage) (kernel.ResponseEvent, error) {
	if event.Kind == kernel.EventToolCallDelta {
		event.ToolArguments = `{"q":"transformed"}`
	}
	return event, nil
}

func (nativeResponsesMutationFixture) Definition() kernel.TransformDefinition {
	return kernel.TransformDefinition{Ref: extensions.Ref{Kind: kernel.RequestTransformKind, ID: "fixture.responses-native-options", ContractVersion: 1}, ImplementationVersion: "1", Label: "Responses native options", Description: "Change typed facets for native Responses egress.", Stage: kernel.TransformBeforeRequirements, Effects: []kernel.TransformEffect{kernel.TransformTools, kernel.TransformOptions, kernel.TransformThinking, kernel.TransformContinuity}}
}

func (nativeResponsesMutationFixture) Apply(_ context.Context, request *kernel.NormalizedRequest, _ json.RawMessage) error {
	request.Tools[0].Name = "new_tool"
	request.Tools[0].Function = map[string]any{"description": "updated"}
	request.ToolChoice.Mode, request.ToolChoice.Name = "tool", "new_tool"
	maxTokens, temperature, topP := 77, 0.6, 0.9
	request.Generation.MaxOutputTokens, request.Generation.Temperature, request.Generation.TopP = &maxTokens, &temperature, &topP
	request.Thinking = normalize.ThinkingIntent{Mode: "level", Effort: "high", Source: request.Thinking.Source}
	request.Continuity.PreviousResponse = "resp-new"
	return nil
}

func (responsesInputMutationFixture) Definition() kernel.TransformDefinition {
	return kernel.TransformDefinition{Ref: extensions.Ref{Kind: kernel.RequestTransformKind, ID: "fixture.responses-input", ContractVersion: 1}, ImplementationVersion: "1", Label: "Responses input transform", Description: "Update typed message and prompt text.", Stage: kernel.TransformBeforeRequirements, Effects: []kernel.TransformEffect{kernel.TransformInput, kernel.TransformPrompt}}
}

func (responsesInputMutationFixture) Apply(_ context.Context, request *kernel.NormalizedRequest, _ json.RawMessage) error {
	request.Prompt.Layers[0].Text = "new system policy"
	for index := range request.Messages {
		if request.Messages[index].Role == "user" {
			request.Messages[index].Content = "new user text"
		}
	}
	return nil
}

func (responsesToolHistoryMutationFixture) Definition() kernel.TransformDefinition {
	return kernel.TransformDefinition{Ref: extensions.Ref{Kind: kernel.RequestTransformKind, ID: "fixture.responses-tool-history", ContractVersion: 1}, ImplementationVersion: "1", Label: "Responses tool history", Description: "Change a canonical tool call and result.", Stage: kernel.TransformBeforeRequirements, Effects: []kernel.TransformEffect{kernel.TransformTools, kernel.TransformInput}}
}

func (responsesToolHistoryMutationFixture) Apply(_ context.Context, request *kernel.NormalizedRequest, _ json.RawMessage) error {
	for index := range request.Messages {
		message := &request.Messages[index]
		if len(message.ToolCalls) > 0 {
			message.ToolCalls[0].Arguments = map[string]any{"q": "updated"}
		}
		if message.Role == "tool" {
			message.Content = "updated result"
		}
	}
	return nil
}

func (responsesUnsupportedReasoningMutationFixture) Definition() kernel.TransformDefinition {
	return kernel.TransformDefinition{Ref: extensions.Ref{Kind: kernel.RequestTransformKind, ID: "fixture.responses-unsupported-reasoning", ContractVersion: 1}, ImplementationVersion: "1", Label: "Unsupported Responses reasoning mutation", Description: "Set a reasoning mode without a declared Responses mapping.", Stage: kernel.TransformBeforeRequirements, Effects: []kernel.TransformEffect{kernel.TransformThinking}}
}

func (responsesUnsupportedReasoningMutationFixture) Apply(_ context.Context, request *kernel.NormalizedRequest, _ json.RawMessage) error {
	request.Thinking = normalize.ThinkingIntent{Mode: "enabled", Source: "fixture"}
	return nil
}

func TestResponsesPrepareTranslatesChatReasoningIntent(t *testing.T) {
	request := normalize.Request{SourceFormat: normalize.FormatOpenAIChat, Raw: map[string]any{"messages": []any{}}, Thinking: normalize.ThinkingIntent{Mode: "level", Effort: "high"}}
	prepared, err := (Responses{}).Prepare(context.Background(), request, kernel.Route{ID: "route", BaseURL: "https://provider.test", ExternalModel: "gpt"}, kernel.Credential{})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.NewDecoder(prepared.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["reasoning_effort"] != nil {
		t.Fatalf("chat reasoning field leaked: %#v", body)
	}
	if reasoning, ok := body["reasoning"].(map[string]any); !ok || reasoning["effort"] != "high" {
		t.Fatalf("reasoning=%#v", body["reasoning"])
	}
}

func TestNativeResponsesEgressOverlaysTypedMutationsAndPreservesExtensions(t *testing.T) {
	parsed, err := normalize.Map("/v1/responses", http.Header{}, map[string]any{
		"model": "role", "input": []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "hello"}}}},
		"tools":             []any{map[string]any{"type": "function", "name": "old_tool", "description": "old", "parameters": map[string]any{"type": "object"}, "vendor_tool": "keep"}},
		"tool_choice":       map[string]any{"type": "function", "name": "old_tool", "vendor_choice": "keep"},
		"max_output_tokens": float64(16), "temperature": float64(0.2), "top_p": float64(0.3),
		"reasoning_effort": "low", "previous_response_id": "resp-old", "vendor_extension": map[string]any{"preserve": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	registry := kernel.NewRequestTransformRegistry()
	transform := nativeResponsesMutationFixture{}
	if err := registry.Register(transform); err != nil {
		t.Fatal(err)
	}
	if err := registry.ApplyScopes(context.Background(), &parsed.Request, []kernel.TransformBinding{{ID: "native-responses", TransformRef: transform.Definition().Ref, Enabled: true, Scope: kernel.TransformScope{Kind: kernel.TransformScopeDaemon}}}, kernel.TransformScope{Kind: kernel.TransformScopeDaemon}); err != nil {
		t.Fatal(err)
	}
	prepared, err := (Responses{}).Prepare(context.Background(), parsed.Request, kernel.Route{BaseURL: "https://provider.test/v1", ExternalModel: "target"}, kernel.Credential{})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.NewDecoder(prepared.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["max_output_tokens"] != float64(77) || body["temperature"] != float64(0.6) || body["top_p"] != float64(0.9) || body["previous_response_id"] != "resp-new" {
		t.Fatalf("Responses body retained stale typed fields: %#v", body)
	}
	if reasoning, ok := body["reasoning"].(map[string]any); !ok || reasoning["effort"] != "high" {
		t.Fatalf("Responses reasoning was not overlaid: %#v", body["reasoning"])
	}
	tools := body["tools"].([]any)
	tool := tools[0].(map[string]any)
	if tool["name"] != "new_tool" || tool["description"] != "updated" || tool["vendor_tool"] != "keep" {
		t.Fatalf("Responses tool overlay lost changed or opaque fields: %#v", tool)
	}
	choice := body["tool_choice"].(map[string]any)
	if choice["name"] != "new_tool" || choice["vendor_choice"] != "keep" {
		t.Fatalf("Responses tool choice overlay lost changed or opaque fields: %#v", choice)
	}
	if body["vendor_extension"].(map[string]any)["preserve"] != true {
		t.Fatalf("unrelated raw extension changed: %#v", body["vendor_extension"])
	}
}

func TestNativeResponsesCompatibilityRejectsUnsupportedOperationMutation(t *testing.T) {
	tests := []struct {
		name      string
		mutations normalize.RequestMutationSet
		facet     string
	}{
		{name: "operation payload", mutations: normalize.RequestMutationSet{OperationPayload: true}, facet: kernel.FacetWireRequest},
		{name: "modality payload", mutations: normalize.RequestMutationSet{Modalities: true}, facet: kernel.FacetWireRequest},
		{name: "unsupported stop sequence", mutations: normalize.RequestMutationSet{GenerationStopSequences: true}, facet: kernel.FacetGenerationOptions},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := normalize.Request{SourceFormat: normalize.FormatOpenAIResponses, Mutations: test.mutations}
			request.Generation.StopSequences = []string{"stop"}
			policy := kernel.CompatibilityPolicy{RequiredFacets: []string{test.facet}}
			plan := kernel.ComposeCompatibilityPlan([][]kernel.FacetMapping{openAIResponsesFacetReport(kernel.CompatibilityContext{Request: request, Policy: policy})}, policy)
			if plan.Supported {
				t.Fatalf("native Responses route was admitted without a %s overlay: %#v", test.name, plan)
			}
		})
	}
}

func TestNativeResponsesBudgetRequiresExactUnmodifiedSourceEvidence(t *testing.T) {
	request := normalize.Request{
		SourceFormat: normalize.FormatOpenAIResponses,
		Raw:          map[string]any{"reasoning": map[string]any{"max_output_tokens": float64(2048)}},
		Thinking:     normalize.ThinkingIntent{Mode: "budget", BudgetTokens: 2048, Source: "reasoning"},
	}
	policy := kernel.CompatibilityPolicy{RequiredFacets: []string{kernel.FacetReasoningBudget}}
	plan := kernel.ComposeCompatibilityPlan([][]kernel.FacetMapping{openAIResponsesFacetReport(kernel.CompatibilityContext{Request: request, Policy: policy})}, policy)
	if !plan.Supported {
		t.Fatalf("exact native reasoning budget should pass through: %#v", plan)
	}

	request.Mutations.Thinking = true
	plan = kernel.ComposeCompatibilityPlan([][]kernel.FacetMapping{openAIResponsesFacetReport(kernel.CompatibilityContext{Request: request, Policy: policy})}, policy)
	if plan.Supported {
		t.Fatalf("transformed native budget was admitted without a Responses mapping: %#v", plan)
	}
	for _, mapping := range plan.Mappings {
		if mapping.Facet == kernel.FacetReasoningBudget && mapping.Disposition != kernel.FacetUnsupported {
			t.Fatalf("transformed reasoning-budget disposition=%s, want unsupported: %#v", mapping.Disposition, mapping)
		}
	}
}

func TestKernelPreservesNativeResponsesJSONWithoutTransforms(t *testing.T) {
	wire := `{"id":"resp_native_1","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"exact"}]}]}`
	dispatches := 0
	composed := NewResponsesAdapter().(kernel.ComposedAdapter)
	composed.Transport = kernel.HTTPTransport{Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		dispatches++
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(wire)), Request: request}, nil
	})}}
	snapshot, err := kernel.BuildSnapshot(kernel.SnapshotInput{
		PublicModels: []kernel.PublicModel{{Name: "junior", TargetRef: "physical"}},
		Nodes:        []kernel.ModelNode{{ID: "physical", Kind: kernel.ModelPhysical, Strategy: kernel.StrategyFallback, Members: []kernel.MemberRef{{Kind: kernel.MemberRoute, ID: "route", Fidelity: kernel.FidelityExact}}}},
		Routes: []kernel.Route{{
			ID: "route", Enabled: true, Protocol: kernel.ProtocolOpenAIResponses, CredentialPresentation: kernel.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "}, BaseURL: "https://provider.test/v1", ExternalModel: "upstream-physical-model",
			OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"openai-responses"}}},
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
	engine.Adapters["openai-responses"] = composed
	parsed, err := normalize.Map("/v1/responses", http.Header{}, map[string]any{"model": "junior", "input": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	writer := httptest.NewRecorder()
	if err := engine.Execute(context.Background(), parsed.Request, kernel.Credential{Secret: "secret"}, writer); err != nil {
		t.Fatal(err)
	}
	if dispatches != 1 || writer.Body.String() != wire || writer.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("native Responses JSON was not preserved exactly: dispatches=%d content-type=%q body=%q", dispatches, writer.Header().Get("Content-Type"), writer.Body.String())
	}
}

func TestKernelRendersResponsesJSONItemsAsAnthropicToolUseBlocks(t *testing.T) {
	responseBody := `{"id":"resp_crosswire","object":"response","status":"completed","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"I found two results."}]},{"id":"fc_item_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"alpha\"}"},{"id":"fc_item_2","type":"function_call","call_id":"call_2","name":"lookup","arguments":"{\"q\":\"beta\"}"}],"usage":{"input_tokens":10,"output_tokens":5}}`
	composed := NewResponsesAdapter().(kernel.ComposedAdapter)
	composed.Transport = kernel.HTTPTransport{Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(responseBody)), Request: request}, nil
	})}}
	snapshot, err := kernel.BuildSnapshot(kernel.SnapshotInput{
		PublicModels: []kernel.PublicModel{{Name: "junior", TargetRef: "physical"}},
		Nodes:        []kernel.ModelNode{{ID: "physical", Kind: kernel.ModelPhysical, Strategy: kernel.StrategyFallback, Members: []kernel.MemberRef{{Kind: kernel.MemberRoute, ID: "route", Fidelity: kernel.FidelityExact}}}},
		Routes: []kernel.Route{{
			ID: "route", Enabled: true, Protocol: kernel.ProtocolOpenAIResponses, CredentialPresentation: kernel.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "}, BaseURL: "https://provider.test/v1", ExternalModel: "responses-model",
			Profile:           kernel.CapabilityProfile{kernel.CapabilityTools: {State: kernel.SupportNative}},
			OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"openai-responses"}}},
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
	engine.Adapters["openai-responses"] = composed
	parsed, err := normalize.JSON("/v1/messages", http.Header{"Anthropic-Version": []string{"2023-06-01"}}, []byte(`{"model":"junior","max_tokens":128,"messages":[{"role":"user","content":"look up alpha and beta"}],"tools":[{"name":"lookup","input_schema":{"type":"object"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	explanations, explainErr := engine.ExplainCompatibility(context.Background(), "junior", parsed.Request)
	if explainErr != nil {
		t.Fatal(explainErr)
	}
	if len(explanations) != 1 || !explanations[0].Compatible {
		plan := composed.PlanCompatibility(kernel.CompatibilityContext{Request: parsed.Request, Policy: kernel.CompatibilityPolicy{RequiredFacets: kernel.RequiredRequestFacets(parsed.Request)}})
		t.Fatalf("route compatibility=%#v direct plan=%#v", explanations, plan)
	}
	writer := httptest.NewRecorder()
	if err := engine.Execute(context.Background(), parsed.Request, kernel.Credential{Secret: "secret"}, writer); err != nil {
		t.Fatal(err)
	}
	var result struct {
		ID      string           `json:"id"`
		Type    string           `json:"type"`
		Stop    string           `json:"stop_reason"`
		Content []map[string]any `json:"content"`
		Usage   map[string]int64 `json:"usage"`
	}
	if err := json.Unmarshal(writer.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode Anthropic projection %q: %v", writer.Body.String(), err)
	}
	if result.ID != "resp_crosswire" || result.Type != "message" || result.Stop != "tool_use" || len(result.Content) != 3 || result.Content[0]["text"] != "I found two results." {
		t.Fatalf("Responses→Anthropic projection=%#v", result)
	}
	first, second := result.Content[1], result.Content[2]
	if first["type"] != "tool_use" || first["id"] != "call_1" || first["name"] != "lookup" || !reflect.DeepEqual(first["input"], map[string]any{"q": "alpha"}) || second["type"] != "tool_use" || second["id"] != "call_2" || second["name"] != "lookup" || !reflect.DeepEqual(second["input"], map[string]any{"q": "beta"}) {
		t.Fatalf("Responses tool items were not independently projected: %#v", result.Content)
	}
	if result.Usage["input_tokens"] != 10 || result.Usage["output_tokens"] != 5 {
		t.Fatalf("Responses usage projection=%#v", result.Usage)
	}
}

func TestKernelRestoresResponsesContinuityFromClientSession(t *testing.T) {
	var dispatches atomic.Int32
	composed := NewResponsesAdapter().(kernel.ComposedAdapter)
	composed.Transport = kernel.HTTPTransport{Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			return nil, err
		}
		call := dispatches.Add(1)
		switch call {
		case 1:
			if body["previous_response_id"] != nil {
				t.Errorf("first Responses call unexpectedly carried continuation: %#v", body["previous_response_id"])
			}
		case 2:
			if body["previous_response_id"] != "resp_previous" {
				t.Errorf("daemon session continuation was not restored into the next provider request: %#v", body)
			}
		default:
			t.Errorf("unexpected Responses dispatch number %d", call)
		}
		responseID := "resp_previous"
		text := "first turn"
		if call == 2 {
			responseID, text = "resp_next", "second turn"
		}
		responseBody, err := json.Marshal(map[string]any{
			"id": responseID, "object": "response", "status": "completed",
			"output": []any{map[string]any{"id": "msg_" + responseID, "type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text}}}},
		})
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(responseBody)), Request: request}, nil
	})}}
	sessionStoreRef := extensions.Ref{Kind: "session_store", ID: "responses-session", ContractVersion: 1}
	providerDefinitionRef := extensions.Ref{Kind: "provider-definition", ID: "openai", ContractVersion: 1}
	chatOperationRef := extensions.Ref{Kind: "operation", ID: string(normalize.OperationChatGenerate), ContractVersion: 1}
	snapshot, err := kernel.BuildSnapshot(kernel.SnapshotInput{
		PublicModels: []kernel.PublicModel{{Name: "junior", TargetRef: "physical"}},
		Nodes:        []kernel.ModelNode{{ID: "physical", Kind: kernel.ModelPhysical, Strategy: kernel.StrategyFallback, Members: []kernel.MemberRef{{Kind: kernel.MemberRoute, ID: "route", Fidelity: kernel.FidelityExact}}}},
		Routes: []kernel.Route{{
			ID: "route", Enabled: true, DefinitionID: "openai", DefinitionRef: providerDefinitionRef, SessionOperationRef: chatOperationRef, CredentialID: "account", HealthGeneration: kernel.RouteHealthGeneration{Connection: "principal-A"}, Protocol: kernel.ProtocolOpenAIResponses, CredentialPresentation: kernel.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "},
			BaseURL: "https://provider.test/v1", ExternalModel: "responses-model",
			OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{normalize.OperationChatGenerate: {
				ContractVersion: 1, AdapterIDs: []string{"openai-responses"}, SessionStoreRef: sessionStoreRef,
			}},
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
	engine.Adapters["openai-responses"] = composed
	engine.SessionStores[sessionStoreRef] = kernel.NewMemorySessionStore()
	for index, input := range []string{"first turn", "second turn"} {
		parsed, err := normalize.JSON("/v1/responses", http.Header{"X-Session-Id": []string{"client-session"}}, []byte(`{"model":"junior","input":"`+input+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		writer := httptest.NewRecorder()
		if err := engine.Execute(context.Background(), parsed.Request, kernel.Credential{Secret: "secret"}, writer); err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err := json.Unmarshal(writer.Body.Bytes(), &result); err != nil {
			t.Fatalf("decode Responses turn %d %q: %v", index+1, writer.Body.String(), err)
		}
		wantID := "resp_previous"
		if index == 1 {
			wantID = "resp_next"
		}
		if result["id"] != wantID || result["status"] != "completed" {
			t.Fatalf("Responses turn %d result=%#v", index+1, result)
		}
	}
	if dispatches.Load() != 2 {
		t.Fatalf("Responses continuity dispatch count=%d, want 2", dispatches.Load())
	}
}

func TestKernelMapsOpenAIChatRequestThroughResponsesAndBack(t *testing.T) {
	responseBody := `{"id":"resp_chat_crosswire","object":"response","status":"completed","output":[{"id":"msg_answer","type":"message","role":"assistant","content":[{"type":"output_text","text":"I found it."}]},{"id":"fc_answer","type":"function_call","call_id":"call_answer","name":"lookup","arguments":"{\"id\":7}"}],"usage":{"input_tokens":13,"output_tokens":4}}`
	composed := NewResponsesAdapter().(kernel.ComposedAdapter)
	composed.Transport = kernel.HTTPTransport{Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode Responses request: %v", err)
		} else {
			input, _ := body["input"].([]any)
			if body["model"] != "responses-physical" || body["messages"] != nil || body["max_output_tokens"] != float64(512) || body["temperature"] != float64(0.25) || body["top_p"] != float64(0.8) || body["tool_choice"] != "required" || len(input) != 4 {
				t.Errorf("OpenAI Chat request was not rebuilt as Responses input: %#v", body)
			}
			if reasoning, ok := body["reasoning"].(map[string]any); !ok || reasoning["effort"] != "high" {
				t.Errorf("reasoning effort was not translated: %#v", body["reasoning"])
			}
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(responseBody)), Request: request}, nil
	})}}
	snapshot, err := kernel.BuildSnapshot(kernel.SnapshotInput{
		PublicModels: []kernel.PublicModel{{Name: "junior", TargetRef: "physical"}},
		Nodes:        []kernel.ModelNode{{ID: "physical", Kind: kernel.ModelPhysical, Strategy: kernel.StrategyFallback, Members: []kernel.MemberRef{{Kind: kernel.MemberRoute, ID: "route", Fidelity: kernel.FidelityExact}}}},
		Routes: []kernel.Route{{
			ID: "route", Enabled: true, Protocol: kernel.ProtocolOpenAIResponses, CredentialPresentation: kernel.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "}, BaseURL: "https://provider.test/v1", ExternalModel: "responses-physical",
			Profile:           kernel.CapabilityProfile{kernel.CapabilityTools: {State: kernel.SupportNative}},
			OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"openai-responses"}}},
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
	engine.Adapters["openai-responses"] = composed
	parsed, err := normalize.JSON("/v1/chat/completions", http.Header{}, []byte(`{"model":"junior","stream":false,"max_tokens":512,"temperature":0.25,"top_p":0.8,"reasoning_effort":"high","tool_choice":"required","messages":[{"role":"system","content":"Be precise."},{"role":"user","content":"Look up item 7."},{"role":"assistant","content":null,"tool_calls":[{"id":"call_history","type":"function","function":{"name":"lookup","arguments":"{\"id\":7}"}}]},{"role":"tool","tool_call_id":"call_history","content":"{\"found\":true}"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{"id":{"type":"integer"}}}}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	explanations, err := engine.ExplainCompatibility(context.Background(), "junior", parsed.Request)
	if err != nil {
		t.Fatal(err)
	}
	if len(explanations) != 1 || !explanations[0].Compatible {
		t.Fatalf("OpenAI Chat→Responses compatibility=%#v", explanations)
	}
	writer := httptest.NewRecorder()
	if err := engine.Execute(context.Background(), parsed.Request, kernel.Credential{Secret: "secret"}, writer); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage map[string]int64 `json:"usage"`
	}
	if err := json.Unmarshal(writer.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode OpenAI Chat response %q: %v", writer.Body.String(), err)
	}
	if len(result.Choices) != 1 || result.Choices[0].Message.Content != "I found it." || result.Choices[0].FinishReason != "tool_calls" || len(result.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("Responses→OpenAI Chat result=%#v", result)
	}
	call := result.Choices[0].Message.ToolCalls[0]
	if call.ID != "call_answer" || call.Function.Name != "lookup" || call.Function.Arguments != `{"id":7}` || result.Usage["prompt_tokens"] != 13 || result.Usage["completion_tokens"] != 4 || result.Usage["total_tokens"] != 17 {
		t.Fatalf("Responses tool/usage projection: call=%#v usage=%#v", call, result.Usage)
	}
}

func TestResponsesJSONSemanticDecoderRejectsIncompleteStatus(t *testing.T) {
	composed := NewResponsesAdapter().(kernel.ComposedAdapter)
	response := kernel.UpstreamResponse{
		Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"id":"resp_incomplete","object":"response","status":"incomplete","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"partial"}]}]}`)),
	}
	writer := httptest.NewRecorder()
	err := composed.RenderResponse(context.Background(), response, writer, normalize.FormatAnthropic, kernel.StreamHooks{})
	if err == nil || !strings.Contains(err.Error(), `non-completed status "incomplete"`) {
		t.Fatalf("incomplete Responses JSON was treated as a completed semantic result: err=%v body=%s", err, writer.Body.String())
	}
	if writer.Body.Len() != 0 {
		t.Fatalf("incomplete Responses JSON leaked a successful client result: %s", writer.Body.String())
	}
}

func TestOpenAIChatRendererReencodesTransformedToolCallWithStableCorrelation(t *testing.T) {
	transform := rewriteToolArgumentResponseTransform{}
	registry := kernel.NewResponseTransformRegistry()
	if err := registry.Register(transform); err != nil {
		t.Fatal(err)
	}
	scope := kernel.TransformScope{Kind: kernel.TransformScopeDaemon}
	binding := kernel.TransformBinding{ID: "rewrite-tool-arguments", TransformRef: transform.Definition().Ref, Enabled: true, Scope: scope}
	if err := registry.ValidateBindings([]kernel.TransformBinding{binding}); err != nil {
		t.Fatal(err)
	}
	composed := NewAdapter().(kernel.ComposedAdapter)
	body := `{"id":"chatcmpl_tool","object":"chat.completion","model":"upstream","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call-stable-1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"original\"}"}}]},"finish_reason":"tool_calls"}]}`
	writer := httptest.NewRecorder()
	err := composed.RenderResponse(context.Background(), kernel.UpstreamResponse{
		Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)),
	}, writer, normalize.FormatOpenAIChat, kernel.StreamHooks{Streaming: true, TransformResponse: func(ctx context.Context, event kernel.ResponseEvent) (kernel.ResponseEvent, error) {
		return registry.ApplyScopes(ctx, event, []kernel.TransformBinding{binding}, scope)
	}})
	if err != nil {
		t.Fatal(err)
	}
	output := writer.Body.String()
	for _, expected := range []string{`"id":"call-stable-1"`, `"name":"lookup"`, `\"q\":\"transformed\"`, "data: [DONE]"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("transformed tool-call response missing %q: %s", expected, output)
		}
	}
	if strings.Contains(output, `\"q\":\"original\"`) {
		t.Fatalf("native upstream wire bypassed semantic tool transform: %s", output)
	}
}

func TestNativeResponsesReasoningMutationRequiresDeclaredModeMapping(t *testing.T) {
	for _, test := range []struct {
		name   string
		intent normalize.ThinkingIntent
		want   kernel.FacetDisposition
	}{
		{name: "effort", intent: normalize.ThinkingIntent{Mode: "level", Effort: "high"}, want: kernel.FacetTranslated},
		{name: "auto", intent: normalize.ThinkingIntent{Mode: "auto"}, want: kernel.FacetTranslated},
		{name: "enabled without budget", intent: normalize.ThinkingIntent{Mode: "enabled"}, want: kernel.FacetUnsupported},
		{name: "budget", intent: normalize.ThinkingIntent{Mode: "budget", BudgetTokens: 512}, want: kernel.FacetUnsupported},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := normalize.Request{SourceFormat: normalize.FormatOpenAIResponses, Thinking: test.intent, Mutations: normalize.RequestMutationSet{Thinking: true}}
			policy := kernel.CompatibilityPolicy{RequiredFacets: []string{kernel.FacetReasoningIntent}}
			plan := kernel.ComposeCompatibilityPlan([][]kernel.FacetMapping{openAIResponsesFacetReport(kernel.CompatibilityContext{Request: request, Policy: policy})}, policy)
			if len(plan.Mappings) != 1 || plan.Mappings[0].Disposition != test.want {
				t.Fatalf("reasoning mutation disposition=%#v want %s; plan=%#v", plan.Mappings, test.want, plan)
			}
		})
	}
}

func TestNativeResponsesOverlaysTransformedInputAndPromptPreservingItems(t *testing.T) {
	parsed, err := normalize.Map("/v1/responses", http.Header{}, map[string]any{
		"model": "role",
		"input": []any{
			map[string]any{"type": "message", "role": "system", "content": "old policy", "vendor_prompt": "keep"},
			map[string]any{"type": "message", "role": "user", "content": "old user", "vendor_message": "keep"},
			map[string]any{"type": "function_call", "call_id": "call-1", "name": "lookup", "arguments": "{}", "vendor_call": "keep"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	registry := kernel.NewRequestTransformRegistry()
	transform := responsesInputMutationFixture{}
	if err := registry.Register(transform); err != nil {
		t.Fatal(err)
	}
	if err := registry.ApplyScopes(context.Background(), &parsed.Request, []kernel.TransformBinding{{ID: "responses-input", TransformRef: transform.Definition().Ref, Enabled: true, Scope: kernel.TransformScope{Kind: kernel.TransformScopeDaemon}}}, kernel.TransformScope{Kind: kernel.TransformScopeDaemon}); err != nil {
		t.Fatal(err)
	}
	if !parsed.Request.Mutations.Messages || !parsed.Request.Mutations.Prompt {
		t.Fatalf("kernel mutation markers=%#v", parsed.Request.Mutations)
	}
	policy := kernel.CompatibilityPolicy{RequiredFacets: []string{kernel.FacetWireRequest, kernel.FacetPromptLayers}}
	plan := kernel.ComposeCompatibilityPlan([][]kernel.FacetMapping{openAIResponsesFacetReport(kernel.CompatibilityContext{Request: parsed.Request, Policy: policy})}, policy)
	if !plan.Supported {
		t.Fatalf("native Responses input/prompt overlay was not admitted: %#v", plan)
	}
	prepared, err := (Responses{}).Prepare(context.Background(), parsed.Request, kernel.Route{BaseURL: "https://provider.test/v1", ExternalModel: "target"}, kernel.Credential{})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.NewDecoder(prepared.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	input := body["input"].([]any)
	if len(input) != 3 {
		t.Fatalf("overlay changed Responses item count/order: %#v", input)
	}
	system := input[0].(map[string]any)
	user := input[1].(map[string]any)
	functionCall := input[2].(map[string]any)
	if system["content"] != "new system policy" || system["vendor_prompt"] != "keep" || user["content"] != "new user text" || user["vendor_message"] != "keep" || functionCall["vendor_call"] != "keep" || functionCall["arguments"] != "{}" {
		t.Fatalf("Responses overlay lost transformed or opaque fields: %#v", input)
	}
}

func TestNativeResponsesOverlaysTransformedToolCallAndResultItems(t *testing.T) {
	parsed, err := normalize.Map("/v1/responses", http.Header{}, map[string]any{
		"model": "role", "input": []any{
			map[string]any{"type": "message", "role": "user", "content": "call lookup"},
			map[string]any{"type": "function_call", "id": "item_1", "call_id": "call_1", "name": "lookup", "arguments": `{"q":"old"}`, "vendor_call": "keep"},
			map[string]any{"type": "function_call_output", "id": "item_2", "call_id": "call_1", "output": "old result", "vendor_result": "keep"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	registry := kernel.NewRequestTransformRegistry()
	transform := responsesToolHistoryMutationFixture{}
	if err := registry.Register(transform); err != nil {
		t.Fatal(err)
	}
	if err := registry.ApplyScopes(context.Background(), &parsed.Request, []kernel.TransformBinding{{ID: "tool-history", TransformRef: transform.Definition().Ref, Enabled: true, Scope: kernel.TransformScope{Kind: kernel.TransformScopeDaemon}}}, kernel.TransformScope{Kind: kernel.TransformScopeDaemon}); err != nil {
		t.Fatal(err)
	}
	if !parsed.Request.Mutations.ToolCalls || !parsed.Request.Mutations.Messages {
		t.Fatalf("tool-call mutation markers=%#v", parsed.Request.Mutations)
	}
	adapter := NewResponsesAdapter().(kernel.ComposedAdapter)
	policy := kernel.CompatibilityPolicy{RequiredFacets: kernel.RequiredRequestFacets(parsed.Request)}
	plan := adapter.PlanCompatibility(kernel.CompatibilityContext{Request: parsed.Request, Policy: policy})
	if !plan.Supported {
		t.Fatalf("native Responses tool history mutation was not admitted: %#v", plan)
	}
	prepared, err := (Responses{}).Prepare(context.Background(), parsed.Request, kernel.Route{BaseURL: "https://provider.test/v1", ExternalModel: "target"}, kernel.Credential{})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.NewDecoder(prepared.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	input := body["input"].([]any)
	call := input[1].(map[string]any)
	result := input[2].(map[string]any)
	if call["id"] != "item_1" || call["call_id"] != "call_1" || call["vendor_call"] != "keep" || call["arguments"] != `{"q":"updated"}` {
		t.Fatalf("Responses function_call overlay lost identity/metadata/arguments: %#v", call)
	}
	if result["id"] != "item_2" || result["call_id"] != "call_1" || result["vendor_result"] != "keep" || result["output"] != "updated result" {
		t.Fatalf("Responses function_call_output overlay lost identity/metadata/output: %#v", result)
	}
}

func TestNativeResponsesRejectsReorderedFunctionCallsWhenMessagesChange(t *testing.T) {
	parsed, err := normalize.Map("/v1/responses", http.Header{}, map[string]any{
		"model": "role",
		"input": []any{
			map[string]any{"type": "function_call", "call_id": "call-1", "name": "first", "arguments": `{"n":1}`},
			map[string]any{"type": "function_call", "call_id": "call-2", "name": "second", "arguments": `{"n":2}`},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed.Request.Mutations.Messages = true
	parsed.Request.Messages[0], parsed.Request.Messages[1] = parsed.Request.Messages[1], parsed.Request.Messages[0]
	_, err = overlayResponsesInput(parsed.Request.Raw["input"], parsed.Request)
	if err == nil || !strings.Contains(err.Error(), "message reorder cannot be safely mapped") {
		t.Fatalf("function_call reorder with a message mutation was silently ignored: %v", err)
	}
}

func TestNativeResponsesOverlayRejectsSourceMessageRoleChange(t *testing.T) {
	parsed, err := normalize.Map("/v1/responses", http.Header{}, map[string]any{
		"model": "role",
		"input": []any{map[string]any{"type": "message", "role": "user", "content": "text"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed.Request.Mutations.Messages = true
	parsed.Request.Messages[0].Role = "assistant"
	if _, err := overlayResponsesInput(parsed.Request.Raw["input"], parsed.Request); err == nil || !strings.Contains(err.Error(), "source message role is immutable") {
		t.Fatalf("source role change was not rejected: %v", err)
	}
}

func TestNativeResponsesOverlayRejectsFunctionCallRoleChange(t *testing.T) {
	parsed, err := normalize.Map("/v1/responses", http.Header{}, map[string]any{
		"model": "role",
		"input": []any{map[string]any{"type": "function_call", "call_id": "call-1", "name": "lookup", "arguments": "{}"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed.Request.Mutations.Messages = true
	parsed.Request.Messages[0].Role = "user"
	if _, err := overlayResponsesInput(parsed.Request.Raw["input"], parsed.Request); err == nil || !strings.Contains(err.Error(), "function_call items require the assistant role") {
		t.Fatalf("invalid function_call role was not rejected: %v", err)
	}
}

func TestNativeResponsesConvertsCanonicalNestedChatToolCallMetadata(t *testing.T) {
	call := normalize.ToolCall{
		ID: "call-1", Type: "function", Name: "lookup", Arguments: map[string]any{"q": "x"},
		Metadata: map[string]any{"id": "call-1", "type": "function", "function": map[string]any{"name": "lookup", "arguments": `{"q":"x"}`}},
	}
	item, err := nativeResponsesFunctionCallItem(call)
	if err != nil {
		t.Fatal(err)
	}
	if item["type"] != "function_call" || item["call_id"] != "call-1" || item["name"] != "lookup" || item["arguments"] != `{"q":"x"}` {
		t.Fatalf("Chat-style call was not normalized into Responses shape: %#v", item)
	}
	if _, nested := item["function"]; nested {
		t.Fatalf("Chat wrapper leaked into Responses item: %#v", item)
	}
}

func TestNativeResponsesRejectsOpaqueNestedChatToolCallMetadata(t *testing.T) {
	call := normalize.ToolCall{
		ID: "call-1", Type: "function", Name: "lookup", Arguments: map[string]any{"q": "x"},
		Metadata: map[string]any{"id": "call-1", "type": "function", "vendor_call": "opaque", "function": map[string]any{"name": "lookup", "arguments": `{"q":"x"}`}},
	}
	if _, err := nativeResponsesFunctionCallItem(call); err == nil || !strings.Contains(err.Error(), "opaque Chat-style tool-call field") {
		t.Fatalf("opaque Chat-style metadata was silently discarded: %v", err)
	}
}

func TestNativeResponsesRejectsUnmappedAddedMessageFields(t *testing.T) {
	tests := []struct {
		name    string
		message normalize.Message
	}{
		{name: "message name", message: normalize.Message{Role: "user", Name: "speaker", Content: "text"}},
		{name: "tool ID on user", message: normalize.Message{Role: "user", ToolCallID: "call-1", Content: "text"}},
		{name: "raw wire content", message: normalize.Message{Role: "user", Content: []any{map[string]any{"type": "vendor_block"}}}},
		{name: "untyped conversation object", message: normalize.Message{Role: "assistant", Content: map[string]any{"vendor": "opaque"}}},
		{name: "empty assistant", message: normalize.Message{Role: "assistant"}},
		{name: "user tool call", message: normalize.Message{Role: "user", Content: "text", ToolCalls: []normalize.ToolCall{{ID: "call-1", Name: "lookup"}}}},
		{name: "tool result name", message: normalize.Message{Role: "tool", Name: "lookup", ToolCallID: "call-1", Content: "result"}},
		{name: "tool result raw wire content", message: normalize.Message{Role: "tool", ToolCallID: "call-1", Content: []any{map[string]any{"type": "vendor_block"}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := kernel.NormalizedRequest{Messages: []normalize.Message{test.message}, Mutations: normalize.RequestMutationSet{Messages: true}}
			if _, err := overlayResponsesInput(nil, request); err == nil {
				t.Fatal("unrepresentable added message was accepted")
			}
		})
	}
}

func TestAnthropicMessagesRequestMapsToOpenAIResponsesFromTypedIR(t *testing.T) {
	parsed, err := normalize.JSON("/v1/messages", http.Header{}, []byte(`{
"model":"role","max_tokens":512,"temperature":0.2,"top_p":0.9,
"thinking":{"type":"disabled"},
"system":[{"type":"text","text":"Follow policy."}],
"tools":[{"name":"lookup","description":"Look up information","input_schema":{"type":"object","properties":{"q":{"type":"string"}}}}],
"tool_choice":{"type":"tool","name":"lookup","disable_parallel_tool_use":true},
"messages":[
 {"role":"user","content":[{"type":"text","text":"Find weather."},{"type":"image","source":{"type":"url","url":"https://example.test/weather.png"}}]},
 {"role":"assistant","content":[{"type":"text","text":"Checking."},{"type":"tool_use","id":"call_1","name":"lookup","input":{"q":"weather"}}]},
 {"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"Sunny."}]}
]}`))
	if err != nil {
		t.Fatal(err)
	}
	adapter := NewResponsesAdapter().(kernel.ComposedAdapter)
	plan := adapter.PlanCompatibility(kernel.CompatibilityContext{Request: parsed.Request, Policy: kernel.CompatibilityPolicy{RequiredFacets: kernel.RequiredRequestFacets(parsed.Request)}})
	if !plan.Supported {
		t.Fatalf("representable Anthropic request rejected: %#v", plan)
	}
	prepared, err := adapter.Prepare(context.Background(), parsed.Request, kernel.Route{ID: "route", BaseURL: "https://provider.test/v1", ExternalModel: "target"}, kernel.Credential{})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.NewDecoder(prepared.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["model"] != "target" || body["max_output_tokens"] != float64(512) || body["max_tokens"] != nil || body["messages"] != nil {
		t.Fatalf("Anthropic envelope leaked or fields were lost: %#v", body)
	}
	if reasoning, ok := body["reasoning"].(map[string]any); !ok || reasoning["effort"] != "none" {
		t.Fatalf("disabled reasoning=%#v", body["reasoning"])
	}
	input := body["input"].([]any)
	if len(input) != 5 {
		t.Fatalf("Responses input item order/count=%#v", input)
	}
	if input[0].(map[string]any)["role"] != "system" || input[1].(map[string]any)["role"] != "user" || input[2].(map[string]any)["type"] != "message" || input[3].(map[string]any)["type"] != "function_call" || input[4].(map[string]any)["type"] != "function_call_output" {
		t.Fatalf("Responses input item mapping/order=%#v", input)
	}
	assistantContent := input[2].(map[string]any)["content"].([]any)
	if assistantContent[0].(map[string]any)["type"] != "output_text" {
		t.Fatalf("assistant history must use the Responses output-text item: %#v", assistantContent)
	}
	userContent := input[1].(map[string]any)["content"].([]any)
	if userContent[0].(map[string]any)["type"] != "input_text" || userContent[1].(map[string]any)["type"] != "input_image" {
		t.Fatalf("Responses multimodal input=%#v", userContent)
	}
	choice, ok := body["tool_choice"].(map[string]any)
	if !ok || choice["type"] != "function" || choice["name"] != "lookup" || body["parallel_tool_calls"] != false {
		t.Fatalf("Responses tool policy=%#v", body)
	}
}

func TestAnthropicThinkingBudgetIsRejectedByResponsesBeforeEncoding(t *testing.T) {
	parsed, err := normalize.JSON("/v1/messages", http.Header{}, []byte(`{"model":"role","max_tokens":512,"thinking":{"type":"enabled","budget_tokens":2000},"messages":[{"role":"user","content":"think"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	adapter := NewResponsesAdapter().(kernel.ComposedAdapter)
	plan := adapter.PlanCompatibility(kernel.CompatibilityContext{Request: parsed.Request, Policy: kernel.CompatibilityPolicy{RequiredFacets: kernel.RequiredRequestFacets(parsed.Request)}})
	if plan.Supported || plan.Fidelity != kernel.FidelityUnsupported {
		t.Fatalf("Anthropic token budget was falsely treated as Responses reasoning effort: %#v", plan)
	}
}

func TestAnthropicEffortRouteRequiresSignedThinkingEgress(t *testing.T) {
	parsed, err := normalize.JSON("/v1/messages", http.Header{}, []byte(`{"model":"role","max_tokens":512,"thinking":{"type":"adaptive"},"output_config":{"effort":"high"},"messages":[{"role":"user","content":"think"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	adapter := NewResponsesAdapter().(kernel.ComposedAdapter)
	plan := adapter.PlanCompatibility(kernel.CompatibilityContext{Request: parsed.Request, Policy: kernel.CompatibilityPolicy{RequiredFacets: kernel.RequiredRequestFacets(parsed.Request)}})
	if plan.Supported || !strings.Contains(plan.Reason, "issuer signature") {
		t.Fatalf("Responses→Anthropic reasoning route was admitted without signed thinking egress: %#v", plan)
	}
}

func TestKernelExcludesOpenAIResponsesRouteForAnthropicThinkingBudgetBeforeDispatch(t *testing.T) {
	parsed, err := normalize.JSON("/v1/messages", http.Header{}, []byte(`{"model":"role","max_tokens":512,"thinking":{"type":"enabled","budget_tokens":2000},"messages":[{"role":"user","content":"think"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := kernel.BuildSnapshot(kernel.SnapshotInput{
		PublicModels: []kernel.PublicModel{{Name: "role", TargetRef: "physical"}},
		Nodes:        []kernel.ModelNode{{ID: "physical", Kind: kernel.ModelPhysical, Strategy: kernel.StrategyFallback, Members: []kernel.MemberRef{{Kind: kernel.MemberRoute, ID: "responses-route", Fidelity: kernel.FidelityExact}}}},
		Routes: []kernel.Route{{ID: "responses-route", Enabled: true, BaseURL: "http://provider.test/v1", ExternalModel: "model", OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{
			normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"openai-responses"}},
		}}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	dispatched := 0
	composed := NewResponsesAdapter().(kernel.ComposedAdapter)
	composed.Transport = kernel.HTTPTransport{Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		dispatched++
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}}
	gateway, err := kernel.New(snapshot, nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	gateway.Adapters["openai-responses"] = composed
	if err := gateway.Execute(context.Background(), parsed.Request, kernel.Credential{}, httptest.NewRecorder()); err == nil || dispatched != 0 {
		t.Fatalf("unrepresentable thinking budget reached Responses upstream: err=%v dispatches=%d", err, dispatched)
	}
}

func TestKernelExcludesTransformedUnsupportedResponsesReasoningBeforeDispatch(t *testing.T) {
	parsed, err := normalize.Map("/v1/responses", http.Header{}, map[string]any{
		"model": "role", "input": "think",
	})
	if err != nil {
		t.Fatal(err)
	}
	transform := responsesUnsupportedReasoningMutationFixture{}
	scope := kernel.TransformScope{Kind: kernel.TransformScopeDaemon}
	snapshot, err := kernel.BuildSnapshot(kernel.SnapshotInput{
		PublicModels: []kernel.PublicModel{{Name: "role", TargetRef: "physical"}},
		Nodes:        []kernel.ModelNode{{ID: "physical", Kind: kernel.ModelPhysical, Strategy: kernel.StrategyFallback, Members: []kernel.MemberRef{{Kind: kernel.MemberRoute, ID: "responses-route", Fidelity: kernel.FidelityExact}}}},
		Routes: []kernel.Route{{ID: "responses-route", Enabled: true, BaseURL: "http://provider.test/v1", ExternalModel: "model", OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{
			normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"openai-responses"}},
		}}},
		TransformBindings: []kernel.TransformBinding{{ID: "unsupported-reasoning", TransformRef: transform.Definition().Ref, Enabled: true, Scope: scope}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	var dispatched atomic.Int32
	composed := NewResponsesAdapter().(kernel.ComposedAdapter)
	composed.Transport = kernel.HTTPTransport{Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		dispatched.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}}
	gateway, err := kernel.New(snapshot, nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	if err := gateway.Transforms.Register(transform); err != nil {
		t.Fatal(err)
	}
	gateway.Adapters["openai-responses"] = composed
	if err := gateway.Execute(context.Background(), parsed.Request, kernel.Credential{}, httptest.NewRecorder()); err == nil || dispatched.Load() != 0 {
		t.Fatalf("unsupported transformed reasoning intent reached Responses upstream: err=%v dispatches=%d", err, dispatched.Load())
	}
}

func TestOpenAIResponsesDecoderPreservesToolIdentityForAnthropicEgress(t *testing.T) {
	composed, ok := NewResponsesAdapter().(kernel.ComposedAdapter)
	if !ok {
		t.Fatalf("Responses adapter type=%T", NewResponsesAdapter())
	}
	composed.Renderers[normalize.FormatAnthropic] = egress.AnthropicMessages{}
	body := "data: {\"type\":\"response.output_item.added\",\"response_id\":\"resp_tool\",\"item_id\":\"item_1\",\"item_type\":\"function_call\",\"item\":{\"id\":\"call_1\",\"call_id\":\"call_1\",\"name\":\"lookup\",\"arguments\":\"\"}}\n\n" +
		"data: {\"type\":\"response.function_call_arguments.delta\",\"response_id\":\"resp_tool\",\"item_id\":\"item_1\",\"delta\":\"{\\\"q\\\":\\\"weather\\\"}\"}\n\n" +
		"data: {\"type\":\"response.output_item.done\",\"response_id\":\"resp_tool\",\"item_id\":\"item_1\",\"status\":\"completed\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response_id\":\"resp_tool\",\"response\":{\"id\":\"resp_tool\",\"status\":\"completed\",\"usage\":{\"input_tokens\":9,\"output_tokens\":4}}}\n\n"
	response := kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
	writer := httptest.NewRecorder()
	var semantic bytes.Buffer
	var usage kernel.UsageEvent
	err := composed.RenderResponse(context.Background(), response, writer, normalize.FormatAnthropic, kernel.StreamHooks{Streaming: true, OnEvent: func(event kernel.ResponseEvent) {
		if event.Kind == kernel.EventToolCallDelta {
			_, _ = fmt.Fprintf(&semantic, "%s|%s|%s", event.ToolCallID, event.ToolName, event.ToolArguments)
		}
		if event.Kind == kernel.EventUsage && event.Usage != nil {
			usage = *event.Usage
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	output := writer.Body.String()
	for _, expected := range []string{"event: content_block_start", `"type":"tool_use"`, `"id":"call_1"`, `"name":"lookup"`, `"partial_json":"{\"q\":\"weather\"}"`, `"stop_reason":"tool_use"`, "event: message_stop"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("Anthropic semantic SSE missing %q: %s", expected, output)
		}
	}
	if !strings.Contains(semantic.String(), "call_1|lookup|") {
		t.Fatalf("Responses decoder did not preserve tool identity in IR: %q", semantic.String())
	}
	if usage.InputTokens == nil || *usage.InputTokens != 9 || usage.OutputTokens == nil || *usage.OutputTokens != 4 {
		t.Fatalf("Responses nested usage was lost: %#v", usage)
	}
}

func TestResponsesPrepareUsesSessionStoreContinuityWhenClientOmitsIt(t *testing.T) {
	request := normalize.Request{
		SourceFormat: normalize.FormatOpenAIChat,
		Raw:          map[string]any{"messages": []any{map[string]any{"role": "user", "content": "continue"}}},
		Continuity:   normalize.ContinuityState{PreviousResponse: "resp-from-session"},
	}
	prepared, err := (Responses{}).Prepare(context.Background(), request, kernel.Route{ID: "route", ExternalModel: "gpt"}, kernel.Credential{})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.NewDecoder(prepared.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["previous_response_id"] != "resp-from-session" {
		t.Fatalf("previous_response_id=%#v", body["previous_response_id"])
	}
}
