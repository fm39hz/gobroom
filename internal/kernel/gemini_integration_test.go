package kernel_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/provider"
)

func TestGeminiRuntimeBindingExecutesOpenAIChatToolRoundTrip(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listener unavailable in test environment: %v", err)
	}
	var upstreamCalls atomic.Int32
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1beta/models/gemini-3-flash:streamGenerateContent" || r.URL.Query().Get("alt") != "sse" {
			t.Errorf("upstream target=%s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		if got := r.Header.Get("x-goog-api-key"); got != "integration-key" {
			t.Errorf("x-goog-api-key=%q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode Gemini request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if _, ok := body["messages"]; ok {
			t.Errorf("OpenAI messages leaked into Gemini request: %#v", body)
		}
		if _, ok := body["systemInstruction"]; !ok {
			t.Errorf("system instruction missing: %#v", body)
		}
		contents, ok := body["contents"].([]any)
		if !ok || len(contents) != 3 {
			t.Errorf("Gemini contents=%#v", body["contents"])
		} else {
			modelTurn, _ := contents[1].(map[string]any)
			parts, _ := modelTurn["parts"].([]any)
			if len(parts) != 1 {
				t.Errorf("model function-call parts=%#v", parts)
			} else if call, _ := parts[0].(map[string]any); call["thoughtSignature"] != "opaque-sig" && call["thoughtSignature"] != "next-sig" {
				t.Errorf("tool thought signature not carried upstream: %#v", call)
			}
			toolTurn, _ := contents[2].(map[string]any)
			toolParts, _ := toolTurn["parts"].([]any)
			if len(toolParts) != 1 {
				t.Errorf("function-response parts=%#v", toolParts)
			} else if part, _ := toolParts[0].(map[string]any); part["functionResponse"] == nil {
				t.Errorf("OpenAI tool result was not encoded as functionResponse: %#v", part)
			}
		}
		if _, ok := body["tools"]; !ok {
			t.Errorf("Gemini function declarations missing: %#v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w,
			"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Here is the result.\"}]}}]}\n\n"+
				"data: {\"candidates\":[{\"content\":{\"parts\":[{\"functionCall\":{\"name\":\"lookup\",\"args\":{\"id\":7}},\"thoughtSignature\":\"next-sig\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":11,\"candidatesTokenCount\":6}}\n\n")
	}))
	upstream.Listener = listener
	upstream.Start()
	defer upstream.Close()

	registry, err := provider.NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	binding, err := provider.NewRuntimeBindingBuilder(registry).Build("gemini", provider.OperationChat)
	if err != nil {
		t.Fatal(err)
	}
	profile := kernel.CapabilityProfile{
		kernel.CapabilityTools: {State: kernel.SupportNative},
	}
	route := kernel.Route{ID: "gemini:gemini-3-flash", NodeID: "gemini-node", DefinitionID: "gemini", DefinitionRef: extensions.Ref{Kind: "provider-definition", ID: "gemini", ContractVersion: 1}, Protocol: kernel.ProtocolGemini, CredentialPresentation: kernel.CredentialPresentation{Header: "x-goog-api-key"}, OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{binding.AdapterID}, SessionStoreRef: binding.SessionStoreRef}}, BaseURL: upstream.URL + "/v1beta", ExternalModel: "gemini-3-flash", CredentialID: "gemini-connection", Profile: profile, Enabled: true}
	snapshot, err := kernel.BuildSnapshot(kernel.SnapshotInput{
		PublicModels: []kernel.PublicModel{{Name: "assistant-role", TargetRef: "assistant-role"}},
		Routes:       []kernel.Route{route},
		Nodes:        []kernel.ModelNode{{ID: "assistant-role", Kind: kernel.ModelPhysical, Members: []kernel.MemberRef{{Kind: kernel.MemberRoute, ID: "gemini:gemini-3-flash", Fidelity: kernel.FidelityExact}}}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := kernel.New(snapshot, kernel.AlwaysOpenGate{}, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	gateway.Adapters[binding.AdapterID] = binding.Adapter
	if binding.SessionStore == nil || binding.SessionStoreRef.ID == "" {
		t.Fatal("Gemini binding did not compose a session store for signature provenance")
	}
	gateway.SessionStores[binding.SessionStoreRef] = binding.SessionStore
	sessionID := "gemini-signature-session"
	requestHeaders := make(http.Header)
	requestHeaders.Set("x-session-id", sessionID)
	request, err := normalize.Map("/v1/chat/completions", requestHeaders, map[string]any{
		"model": "assistant-role", "stream": true,
		"messages": []any{
			map[string]any{"role": "system", "content": "Be precise."},
			map[string]any{"role": "user", "content": "Look up item 7."},
			map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{
				"id": "call-1", "type": "function", "thoughtSignature": "opaque-sig",
				"function": map[string]any{"name": "lookup", "arguments": `{"id":7}`},
			}}},
			map[string]any{"role": "tool", "tool_call_id": "call-1", "content": `{"found":true}`},
		},
		"tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "lookup", "description": "look up item", "parameters": map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "integer"}}}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := gateway.Execute(context.Background(), request.Request, kernel.Credential{ConnectionID: "gemini-connection", Secret: "integration-key"}, httptest.NewRecorder()); !errors.Is(err, kernel.ErrNoRoute) {
		t.Fatalf("client-echoed opaque signature was accepted without session issuer proof: %v", err)
	}
	if upstreamCalls.Load() != 0 {
		t.Fatalf("unattested signature reached upstream: calls=%d", upstreamCalls.Load())
	}
	priorProof, err := kernel.NewSignatureProof("opaque-sig", route.DefinitionRef)
	if err != nil {
		t.Fatal(err)
	}
	wrongIssuer := extensions.Ref{Kind: "provider-definition", ID: "other-provider", ContractVersion: 1}
	wrongProof, err := kernel.NewSignatureProof("opaque-sig", wrongIssuer)
	if err != nil {
		t.Fatal(err)
	}
	if err := binding.SessionStore.Save(context.Background(), route, sessionID, kernel.SessionState{Signatures: []kernel.SignatureProof{wrongProof}}); err != nil {
		t.Fatal(err)
	}
	if err := gateway.Execute(context.Background(), request.Request, kernel.Credential{ConnectionID: "gemini-connection", Secret: "integration-key"}, httptest.NewRecorder()); !errors.Is(err, kernel.ErrNoRoute) {
		t.Fatalf("wrong-issuer session proof was accepted: %v", err)
	}
	if upstreamCalls.Load() != 0 {
		t.Fatalf("wrong-issuer proof reached upstream: calls=%d", upstreamCalls.Load())
	}
	if err := binding.SessionStore.Save(context.Background(), route, sessionID, kernel.SessionState{Signatures: []kernel.SignatureProof{priorProof}}); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	err = gateway.Execute(context.Background(), request.Request, kernel.Credential{ConnectionID: "gemini-connection", Secret: "integration-key"}, recorder)
	if err != nil {
		t.Fatal(err)
	}
	response := recorder.Body.String()
	if recorder.Code != http.StatusOK || !strings.Contains(response, `"content":"Here is the result."`) || !strings.Contains(response, `"thoughtSignature":"next-sig"`) || !strings.Contains(response, "data: [DONE]") {
		t.Fatalf("status=%d translated stream=%s", recorder.Code, response)
	}
	if upstreamCalls.Load() != 1 {
		t.Fatalf("session-attested signature upstream calls=%d, want 1", upstreamCalls.Load())
	}
	saved, ok, err := binding.SessionStore.Load(context.Background(), route, sessionID)
	if err != nil || !ok {
		t.Fatalf("session signature state load: ok=%v err=%v", ok, err)
	}
	nextProof, err := kernel.NewSignatureProof("next-sig", route.DefinitionRef)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSignatureProof(saved.Signatures, nextProof) {
		t.Fatalf("Gemini response signature was not persisted for this issuer: %#v", saved.Signatures)
	}
	followUp := normalize.CloneRequest(request.Request)
	followUp.Messages[1].ToolCalls[0].Signature = "next-sig"
	followUpRecorder := httptest.NewRecorder()
	if err := gateway.Execute(context.Background(), followUp, kernel.Credential{ConnectionID: "gemini-connection", Secret: "integration-key"}, followUpRecorder); err != nil {
		t.Fatalf("same-session signature replay was rejected: %v", err)
	}
	select {
	case usage := <-gateway.Events:
		if usage.Status != "ok" || usage.InputTokens != 11 || usage.OutputTokens != 6 || usage.ExternalModel != "gemini-3-flash" {
			t.Fatalf("kernel usage event=%#v", usage)
		}
	case <-time.After(time.Second):
		t.Fatal("kernel did not emit completed Gemini usage")
	}
}

func TestGeminiRuntimeBindingMapsOpenAIResponsesThroughGeminiAndBack(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listener unavailable in test environment: %v", err)
	}
	var upstreamCalls atomic.Int32
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1beta/models/gemini-3-flash:generateContent" || r.URL.RawQuery != "" {
			t.Errorf("upstream target=%s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		if r.Header.Get("x-goog-api-key") != "integration-key" {
			t.Errorf("x-goog-api-key=%q", r.Header.Get("x-goog-api-key"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode Gemini request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if body["messages"] != nil || body["input"] != nil || body["instructions"] != nil {
			t.Errorf("Responses wire leaked into Gemini request: %#v", body)
		}
		instruction, _ := body["systemInstruction"].(map[string]any)
		instructionParts, _ := instruction["parts"].([]any)
		contents, _ := body["contents"].([]any)
		tools, _ := body["tools"].([]any)
		config, _ := body["toolConfig"].(map[string]any)
		calling, _ := config["functionCallingConfig"].(map[string]any)
		generation, _ := body["generationConfig"].(map[string]any)
		thinking, _ := generation["thinkingConfig"].(map[string]any)
		if len(instructionParts) != 1 || len(contents) != 3 || len(tools) != 1 || calling["mode"] != "ANY" || body["temperature"] != nil || generation["temperature"] != 0.2 || generation["topP"] != 0.7 || generation["maxOutputTokens"] != float64(4096) || thinking["thinkingBudget"] != float64(2048) {
			t.Errorf("typed Responses request was not composed as Gemini request: %#v", body)
		}
		userTurn, _ := contents[0].(map[string]any)
		userParts, _ := userTurn["parts"].([]any)
		if len(userParts) != 2 || userParts[0].(map[string]any)["text"] != "Find item 7." {
			t.Errorf("Responses input message mapping=%#v", contents[0])
		} else if image, ok := userParts[1].(map[string]any)["inlineData"].(map[string]any); !ok || image["mimeType"] != "image/png" || image["data"] != "aGVsbG8=" {
			t.Errorf("Responses image was not translated to Gemini inlineData: %#v", userParts[1])
		}
		modelTurn, _ := contents[1].(map[string]any)
		modelParts, _ := modelTurn["parts"].([]any)
		if len(modelParts) != 1 || modelParts[0].(map[string]any)["functionCall"] == nil {
			t.Errorf("Responses function call history mapping=%#v", contents[1])
		}
		toolTurn, _ := contents[2].(map[string]any)
		toolParts, _ := toolTurn["parts"].([]any)
		if len(toolParts) != 1 || toolParts[0].(map[string]any)["functionResponse"] == nil {
			t.Errorf("Responses function result history mapping=%#v", contents[2])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"Found it."},{"functionCall":{"name":"lookup","args":{"found":true}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":14,"candidatesTokenCount":4}}`)
	}))
	upstream.Listener = listener
	upstream.Start()
	defer upstream.Close()

	registry, err := provider.NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	binding, err := provider.NewRuntimeBindingBuilder(registry).Build("gemini", provider.OperationChat)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := kernel.BuildSnapshot(kernel.SnapshotInput{
		PublicModels: []kernel.PublicModel{{Name: "junior", TargetRef: "physical"}},
		Nodes:        []kernel.ModelNode{{ID: "physical", Kind: kernel.ModelPhysical, Strategy: kernel.StrategyFallback, Members: []kernel.MemberRef{{Kind: kernel.MemberRoute, ID: "gemini-route", Fidelity: kernel.FidelityExact}}}},
		Routes: []kernel.Route{{
			ID: "gemini-route", NodeID: "gemini-node", DefinitionID: "gemini",
			DefinitionRef: extensions.Ref{Kind: "provider-definition", ID: "gemini", ContractVersion: 1},
			Protocol:      kernel.ProtocolGemini, CredentialPresentation: kernel.CredentialPresentation{Header: "x-goog-api-key"}, BaseURL: upstream.URL + "/v1beta", ExternalModel: "gemini-3-flash", CredentialID: "gemini-connection",
			Profile: kernel.CapabilityProfile{
				kernel.CapabilityTools:  {State: kernel.SupportNative},
				kernel.CapabilityVision: {State: kernel.SupportNative},
			}, Enabled: true,
			OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{normalize.OperationChatGenerate: {
				ContractVersion: 1, AdapterIDs: []string{binding.AdapterID}, SessionStoreRef: binding.SessionStoreRef,
			}},
		}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := kernel.New(snapshot, kernel.AlwaysOpenGate{}, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	engine.Adapters[binding.AdapterID] = binding.Adapter
	engine.SessionStores[binding.SessionStoreRef] = binding.SessionStore
	requestBody := `{"model":"junior","instructions":"Be precise.","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Find item 7."},{"type":"input_image","image_url":"data:image/png;base64,aGVsbG8="}]},{"type":"function_call","call_id":"call_prior","name":"lookup","arguments":"{\"id\":7}"},{"type":"function_call_output","call_id":"call_prior","output":"{\"found\":true}"}],"tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"id":{"type":"integer"}}}}],"tool_choice":"required","max_output_tokens":4096,"temperature":0.2,"top_p":0.7,"reasoning":{"max_output_tokens":2048}}`
	parsed, err := normalize.JSON("/v1/responses", http.Header{}, []byte(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	explanations, err := engine.ExplainCompatibility(context.Background(), "junior", parsed.Request)
	if err != nil {
		t.Fatal(err)
	}
	if len(explanations) != 1 || !explanations[0].Compatible {
		t.Fatalf("Responses→Gemini compatibility=%#v", explanations)
	}
	writer := httptest.NewRecorder()
	if err := engine.Execute(context.Background(), parsed.Request, kernel.Credential{ConnectionID: "gemini-connection", Secret: "integration-key"}, writer); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Object string           `json:"object"`
		Status string           `json:"status"`
		Output []map[string]any `json:"output"`
		Usage  map[string]int64 `json:"usage"`
	}
	if err := json.Unmarshal(writer.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode OpenAI Responses projection %q: %v", writer.Body.String(), err)
	}
	if upstreamCalls.Load() != 1 || result.Object != "response" || result.Status != "completed" || len(result.Output) != 2 || result.Output[0]["type"] != "message" || result.Output[1]["type"] != "function_call" || result.Output[1]["name"] != "lookup" || result.Usage["input_tokens"] != 14 || result.Usage["output_tokens"] != 4 {
		t.Fatalf("Gemini→Responses result=%#v upstream calls=%d", result, upstreamCalls.Load())
	}
	for _, unsupported := range []string{
		`{"model":"junior","previous_response_id":"resp_previous","input":"continue"}`,
		`{"model":"junior","store":true,"input":"do not persist"}`,
		`{"model":"junior","input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"https://images.example/image.png"}]}]}`,
	} {
		unsupportedRequest, err := normalize.JSON("/v1/responses", http.Header{}, []byte(unsupported))
		if err != nil {
			t.Fatal(err)
		}
		if err := engine.Execute(context.Background(), unsupportedRequest.Request, kernel.Credential{ConnectionID: "gemini-connection", Secret: "integration-key"}, httptest.NewRecorder()); !errors.Is(err, kernel.ErrNoRoute) || upstreamCalls.Load() != 1 {
			t.Fatalf("unmapped Responses semantic reached Gemini: request=%s err=%v calls=%d", unsupported, err, upstreamCalls.Load())
		}
	}
}

func containsSignatureProof(proofs []kernel.SignatureProof, expected kernel.SignatureProof) bool {
	for _, proof := range proofs {
		if proof == expected {
			return true
		}
	}
	return false
}

func TestGeminiRuntimeBindingPropagatesRequestCancellation(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listener unavailable in test environment: %v", err)
	}
	upstreamStarted := make(chan struct{})
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(upstreamStarted)
		<-r.Context().Done()
	}))
	upstream.Listener = listener
	upstream.Start()
	defer upstream.Close()

	registry, err := provider.NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	binding, err := provider.NewRuntimeBindingBuilder(registry).Build("gemini", provider.OperationChat)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := kernel.BuildSnapshot(kernel.SnapshotInput{
		PublicModels: []kernel.PublicModel{{Name: "role", TargetRef: "role"}},
		Routes:       []kernel.Route{{ID: "gemini:model", NodeID: "gemini", DefinitionID: "gemini", DefinitionRef: extensions.Ref{Kind: "provider-definition", ID: "gemini", ContractVersion: 1}, Protocol: kernel.ProtocolGemini, CredentialPresentation: kernel.CredentialPresentation{Header: "x-goog-api-key"}, OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{binding.AdapterID}}}, BaseURL: upstream.URL + "/v1beta", ExternalModel: "gemini-model", Enabled: true}},
		Nodes:        []kernel.ModelNode{{ID: "role", Kind: kernel.ModelPhysical, Members: []kernel.MemberRef{{Kind: kernel.MemberRoute, ID: "gemini:model", Fidelity: kernel.FidelityExact}}}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := kernel.New(snapshot, kernel.AlwaysOpenGate{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	gateway.Adapters[binding.AdapterID] = binding.Adapter
	request, err := normalize.Map("/v1/chat/completions", http.Header{}, map[string]any{
		"model": "role", "stream": true,
		"messages": []any{map[string]any{"role": "user", "content": "wait"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- gateway.Execute(ctx, request.Request, kernel.Credential{Secret: "test-key"}, httptest.NewRecorder())
	}()
	select {
	case <-upstreamStarted:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("Gemini upstream did not start")
	}
	cancel()
	select {
	case err := <-result:
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "context canceled") {
			t.Fatalf("cancellation was not propagated: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("kernel stayed blocked after request cancellation")
	}
}

func TestGeminiRuntimeBindingTranslatesNonStreamingTextCompletion(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listener unavailable in test environment: %v", err)
	}
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/gemini-flash:generateContent" || r.URL.Query().Get("alt") != "" {
			t.Errorf("non-stream target=%s?%s", r.URL.Path, r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"plain response"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":3}}`)
	}))
	upstream.Listener = listener
	upstream.Start()
	defer upstream.Close()

	registry, err := provider.NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	binding, err := provider.NewRuntimeBindingBuilder(registry).Build("gemini", provider.OperationChat)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := kernel.BuildSnapshot(kernel.SnapshotInput{
		PublicModels: []kernel.PublicModel{{Name: "role", TargetRef: "role"}},
		Routes:       []kernel.Route{{ID: "gemini:model", NodeID: "gemini", DefinitionID: "gemini", DefinitionRef: extensions.Ref{Kind: "provider-definition", ID: "gemini", ContractVersion: 1}, Protocol: kernel.ProtocolGemini, CredentialPresentation: kernel.CredentialPresentation{Header: "x-goog-api-key"}, OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{binding.AdapterID}}}, BaseURL: upstream.URL + "/v1beta", ExternalModel: "gemini-flash", Enabled: true}},
		Nodes:        []kernel.ModelNode{{ID: "role", Kind: kernel.ModelPhysical, Members: []kernel.MemberRef{{Kind: kernel.MemberRoute, ID: "gemini:model", Fidelity: kernel.FidelityExact}}}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := kernel.New(snapshot, kernel.AlwaysOpenGate{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	gateway.Adapters[binding.AdapterID] = binding.Adapter
	request, err := normalize.Map("/v1/chat/completions", http.Header{}, map[string]any{
		"model": "role", "stream": false,
		"messages": []any{map[string]any{"role": "user", "content": "answer briefly"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	if err := gateway.Execute(context.Background(), request.Request, kernel.Credential{Secret: "test-key"}, recorder); err != nil {
		t.Fatal(err)
	}
	var output openAIResponseFixture
	if err := json.Unmarshal(recorder.Body.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if output.Choices[0].Message.Content != "plain response" || output.Choices[0].FinishReason != "stop" {
		t.Fatalf("non-stream response=%s", recorder.Body.String())
	}
	select {
	case usage := <-gateway.Events:
		if usage.InputTokens != 8 || usage.OutputTokens != 3 {
			t.Fatalf("usage=%#v", usage)
		}
	case <-time.After(time.Second):
		t.Fatal("kernel did not emit non-stream usage")
	}
}

type openAIResponseFixture struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

func newGeminiFallbackGateway(t *testing.T, baseURL string, stream bool) (*kernel.Kernel, normalize.Request) {
	t.Helper()
	registry, err := provider.NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	binding, err := provider.NewRuntimeBindingBuilder(registry).Build("gemini", provider.OperationChat)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := kernel.BuildSnapshot(kernel.SnapshotInput{
		PublicModels: []kernel.PublicModel{{Name: "role", TargetRef: "role"}},
		Routes: []kernel.Route{
			{ID: "route:limited", NodeID: "gemini", DefinitionID: "gemini", DefinitionRef: extensions.Ref{Kind: "provider-definition", ID: "gemini", ContractVersion: 1}, Protocol: kernel.ProtocolGemini, CredentialPresentation: kernel.CredentialPresentation{Header: "x-goog-api-key"}, OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{binding.AdapterID}}}, BaseURL: baseURL + "/v1beta", ExternalModel: "limited", Enabled: true},
			{ID: "route:healthy", NodeID: "gemini", DefinitionID: "gemini", DefinitionRef: extensions.Ref{Kind: "provider-definition", ID: "gemini", ContractVersion: 1}, Protocol: kernel.ProtocolGemini, CredentialPresentation: kernel.CredentialPresentation{Header: "x-goog-api-key"}, OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{binding.AdapterID}}}, BaseURL: baseURL + "/v1beta", ExternalModel: "healthy", Enabled: true},
		},
		Nodes: []kernel.ModelNode{
			{ID: "physical-limited", Kind: kernel.ModelPhysical, Members: []kernel.MemberRef{{Kind: kernel.MemberRoute, ID: "route:limited", Fidelity: kernel.FidelityExact}}},
			{ID: "physical-healthy", Kind: kernel.ModelPhysical, Members: []kernel.MemberRef{{Kind: kernel.MemberRoute, ID: "route:healthy", Fidelity: kernel.FidelityExact}}},
			{ID: "role", Kind: kernel.ModelCombo, Strategy: kernel.StrategyFallback, Members: []kernel.MemberRef{{Kind: kernel.MemberModel, ID: "physical-limited"}, {Kind: kernel.MemberModel, ID: "physical-healthy"}}},
		},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := kernel.New(snapshot, kernel.AlwaysOpenGate{}, 4)
	if err != nil {
		t.Fatal(err)
	}
	gateway.Adapters[binding.AdapterID] = binding.Adapter
	request, err := normalize.Map("/v1/chat/completions", http.Header{}, map[string]any{
		"model": "role", "stream": stream,
		"messages": []any{map[string]any{"role": "user", "content": "answer"}},
	})
	if err != nil {
		gateway.Close()
		t.Fatal(err)
	}
	return gateway, request.Request
}

func TestGeminiRateLimitBeforeResponseCommitFallsBackToNextPhysicalModel(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listener unavailable in test environment: %v", err)
	}
	var limitedCalls, healthyCalls int
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1beta/models/limited:generateContent":
			limitedCalls++
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"rate limit"}}`)
		case "/v1beta/models/healthy:generateContent":
			healthyCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"fallback answer"}]},"finishReason":"STOP"}]}`)
		default:
			t.Errorf("unexpected upstream path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	upstream.Listener = listener
	upstream.Start()
	defer upstream.Close()

	gateway, request := newGeminiFallbackGateway(t, upstream.URL, false)
	defer gateway.Close()
	recorder := httptest.NewRecorder()
	if err := gateway.Execute(context.Background(), request, kernel.Credential{Secret: "test-key"}, recorder); err != nil {
		t.Fatal(err)
	}
	if limitedCalls != 1 || healthyCalls != 1 {
		t.Fatalf("calls limited=%d healthy=%d; expected fallback before commitment", limitedCalls, healthyCalls)
	}
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "fallback answer") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestGeminiAuthAndTerminalErrorsFollowFallbackPolicy(t *testing.T) {
	tests := []struct {
		name           string
		status         int
		wantFallback   bool
		wantErrorMatch string
	}{
		{name: "unauthorized continues to alternate route", status: http.StatusUnauthorized, wantFallback: true},
		{name: "invalid request is terminal", status: http.StatusBadRequest, wantErrorMatch: "upstream status 400"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Skipf("loopback listener unavailable in test environment: %v", err)
			}
			limitedCalls, healthyCalls := 0, 0
			upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1beta/models/limited:generateContent":
					limitedCalls++
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(test.status)
					_, _ = io.WriteString(w, `{"error":{"message":"upstream failure"}}`)
				case "/v1beta/models/healthy:generateContent":
					healthyCalls++
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"alternate route"}]},"finishReason":"STOP"}]}`)
				default:
					t.Errorf("unexpected upstream path %q", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			upstream.Listener = listener
			upstream.Start()
			defer upstream.Close()

			gateway, request := newGeminiFallbackGateway(t, upstream.URL, false)
			defer gateway.Close()
			recorder := httptest.NewRecorder()
			err = gateway.Execute(context.Background(), request, kernel.Credential{Secret: "test-key"}, recorder)
			if test.wantFallback {
				if err != nil || healthyCalls != 1 || !strings.Contains(recorder.Body.String(), "alternate route") {
					t.Fatalf("fallback err=%v calls limited=%d healthy=%d body=%s", err, limitedCalls, healthyCalls, recorder.Body.String())
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErrorMatch) || healthyCalls != 0 {
				t.Fatalf("terminal result err=%v calls limited=%d healthy=%d", err, limitedCalls, healthyCalls)
			}
		})
	}
}

func TestGeminiStreamFailureAfterCommitDoesNotFallback(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listener unavailable in test environment: %v", err)
	}
	var limitedCalls, healthyCalls int
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1beta/models/limited:streamGenerateContent":
			limitedCalls++
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"partial output\"}]}}]}\n\n")
		case "/v1beta/models/healthy:streamGenerateContent":
			healthyCalls++
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"must not appear\"}]},\"finishReason\":\"STOP\"}]}\n\n")
		default:
			t.Errorf("unexpected upstream path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	upstream.Listener = listener
	upstream.Start()
	defer upstream.Close()

	gateway, request := newGeminiFallbackGateway(t, upstream.URL, true)
	defer gateway.Close()
	recorder := httptest.NewRecorder()
	err = gateway.Execute(context.Background(), request, kernel.Credential{Secret: "test-key"}, recorder)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected truncated stream error, got %v", err)
	}
	if limitedCalls != 1 || healthyCalls != 0 {
		t.Fatalf("calls limited=%d healthy=%d; route switched after response commit", limitedCalls, healthyCalls)
	}
	if !strings.Contains(recorder.Body.String(), "partial output") || strings.Contains(recorder.Body.String(), "data: [DONE]") {
		t.Fatalf("partial stream should remain visible but incomplete: %s", recorder.Body.String())
	}
}
