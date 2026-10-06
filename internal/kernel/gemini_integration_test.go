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
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/provider"
)

func TestGeminiRuntimeBindingExecutesOpenAIChatToolRoundTrip(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listener unavailable in test environment: %v", err)
	}
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			} else if call, _ := parts[0].(map[string]any); call["thoughtSignature"] != "opaque-sig" {
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
	snapshot, err := kernel.BuildSnapshot(kernel.SnapshotInput{
		PublicModels: []kernel.PublicModel{{Name: "assistant-role", TargetRef: "assistant-role"}},
		Routes:       []kernel.Route{{ID: "gemini:gemini-3-flash", NodeID: "gemini-node", DefinitionID: "gemini", Protocol: kernel.ProtocolGemini, OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{normalize.OperationChatGenerate: {AdapterIDs: []string{binding.AdapterID}}}, BaseURL: upstream.URL + "/v1beta", ExternalModel: "gemini-3-flash", CredentialID: "gemini-connection", Profile: profile, Enabled: true}},
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

	request, err := normalize.Map("/v1/chat/completions", http.Header{}, map[string]any{
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
	recorder := httptest.NewRecorder()
	err = gateway.Execute(context.Background(), request.Request, kernel.Credential{ConnectionID: "gemini-connection", Secret: "integration-key"}, recorder)
	if err != nil {
		t.Fatal(err)
	}
	response := recorder.Body.String()
	if recorder.Code != http.StatusOK || !strings.Contains(response, `"content":"Here is the result."`) || !strings.Contains(response, `"thoughtSignature":"next-sig"`) || !strings.Contains(response, "data: [DONE]") {
		t.Fatalf("status=%d translated stream=%s", recorder.Code, response)
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
		Routes:       []kernel.Route{{ID: "gemini:model", NodeID: "gemini", DefinitionID: "gemini", Protocol: kernel.ProtocolGemini, OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{normalize.OperationChatGenerate: {AdapterIDs: []string{binding.AdapterID}}}, BaseURL: upstream.URL + "/v1beta", ExternalModel: "gemini-model", Enabled: true}},
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
		Routes:       []kernel.Route{{ID: "gemini:model", NodeID: "gemini", DefinitionID: "gemini", Protocol: kernel.ProtocolGemini, OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{normalize.OperationChatGenerate: {AdapterIDs: []string{binding.AdapterID}}}, BaseURL: upstream.URL + "/v1beta", ExternalModel: "gemini-flash", Enabled: true}},
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
			{ID: "route:limited", NodeID: "gemini", DefinitionID: "gemini", Protocol: kernel.ProtocolGemini, OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{normalize.OperationChatGenerate: {AdapterIDs: []string{binding.AdapterID}}}, BaseURL: baseURL + "/v1beta", ExternalModel: "limited", Enabled: true},
			{ID: "route:healthy", NodeID: "gemini", DefinitionID: "gemini", Protocol: kernel.ProtocolGemini, OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{normalize.OperationChatGenerate: {AdapterIDs: []string{binding.AdapterID}}}, BaseURL: baseURL + "/v1beta", ExternalModel: "healthy", Enabled: true},
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
