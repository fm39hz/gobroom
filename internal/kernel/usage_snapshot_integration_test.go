package kernel_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/provider"
)

func TestGeminiNativeSnapshotInvalidationReachesClientAndKernelTerminal(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listener unavailable in test environment: %v", err)
	}
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/gemini-flash:streamGenerateContent" || r.URL.Query().Get("alt") != "sse" {
			t.Errorf("upstream target=%s?%s", r.URL.Path, r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w,
			"data: {\"usageMetadata\":{\"promptTokenCount\":11,\"candidatesTokenCount\":6,\"totalTokenCount\":17}}\n\n"+
				"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"done\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"candidatesTokenCount\":7}}\n\n")
	}))
	upstream.Listener = listener
	upstream.Start()
	defer upstream.Close()

	registry, err := provider.NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	binding, err := provider.NewRuntimeBindingBuilder(registry).Build(provider.ProviderDefinitionRef("gemini", 1), provider.OperationChat)
	if err != nil {
		t.Fatal(err)
	}
	definition := extensions.Ref{Kind: "provider-definition", ID: "gemini", ContractVersion: 1}
	route := kernel.Route{
		ID: "gemini:model", NodeID: "gemini", DefinitionID: "gemini", DefinitionRef: definition,
		Protocol: kernel.ProtocolGemini, CredentialPresentation: kernel.CredentialPresentation{Header: "x-goog-api-key"},
		OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{
			normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{binding.AdapterID}},
		},
		Profile: kernel.CapabilityProfile{
			kernel.CapabilityTools: {State: kernel.SupportNative},
		},
		BaseURL: upstream.URL + "/v1beta", ExternalModel: "gemini-flash", Enabled: true,
	}
	snapshot, err := kernel.BuildSnapshot(kernel.SnapshotInput{
		PublicModels: []kernel.PublicModel{{Name: "role", TargetRef: "role"}},
		Routes:       []kernel.Route{route},
		Nodes:        []kernel.ModelNode{{ID: "role", Kind: kernel.ModelPhysical, Members: []kernel.MemberRef{{Kind: kernel.MemberRoute, ID: route.ID, Fidelity: kernel.FidelityExact}}}},
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
		"stream_options": map[string]any{"include_usage": true},
		"messages":       []any{map[string]any{"role": "user", "content": "answer briefly"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	if err := gateway.Execute(context.Background(), request.Request, kernel.Credential{Secret: "test-key"}, recorder); err != nil {
		t.Fatal(err)
	}
	clientBody := recorder.Body.String()
	if recorder.Code != http.StatusOK || !strings.Contains(clientBody, "data: [DONE]") {
		t.Fatalf("status=%d client stream=%s", recorder.Code, clientBody)
	}
	var terminalUsage map[string]any
	for _, line := range strings.Split(clientBody, "\n") {
		if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
			continue
		}
		var frame struct {
			Choices []json.RawMessage `json:"choices"`
			Usage   map[string]any    `json:"usage"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil {
			t.Fatal(err)
		}
		if frame.Usage != nil {
			terminalUsage = frame.Usage
		}
	}
	if terminalUsage == nil {
		t.Fatalf("stream omitted requested usage projection: %s", clientBody)
	}
	if _, exists := terminalUsage["completion_tokens"]; exists {
		t.Errorf("client terminal retained stale output: %#v", terminalUsage)
	}
	if _, exists := terminalUsage["total_tokens"]; exists {
		t.Errorf("client terminal retained stale total: %#v", terminalUsage)
	}
	select {
	case terminal := <-gateway.Events:
		if terminal.OutputTokens != nil || terminal.OutputTokensPerSecond != nil {
			t.Fatalf("kernel terminal retained stale output or derived throughput: %#v", terminal)
		}
		if terminal.InputTokens == nil || *terminal.InputTokens != 11 {
			t.Fatalf("kernel terminal lost known input: %#v", terminal)
		}
	case <-time.After(time.Second):
		t.Fatal("kernel did not emit terminal Gemini usage")
	}
}

func TestGeminiChatStreamUsageOptionReachesClientWithoutUpstreamOption(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listener unavailable in test environment: %v", err)
	}
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/gemini-flash:streamGenerateContent" || r.URL.Query().Get("alt") != "sse" {
			t.Errorf("upstream target=%s?%s", r.URL.Path, r.URL.RawQuery)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode Gemini request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if _, forwarded := body["stream_options"]; forwarded {
			t.Errorf("Chat transport option forwarded to Gemini: %#v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"done\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":11,\"candidatesTokenCount\":6,\"thoughtsTokenCount\":0}}\n\n")
	}))
	upstream.Listener = listener
	upstream.Start()
	defer upstream.Close()

	registry, err := provider.NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	binding, err := provider.NewRuntimeBindingBuilder(registry).Build(provider.ProviderDefinitionRef("gemini", 1), provider.OperationChat)
	if err != nil {
		t.Fatal(err)
	}
	route := kernel.Route{
		ID: "gemini:model", NodeID: "gemini", DefinitionID: "gemini",
		DefinitionRef: extensions.Ref{Kind: "provider-definition", ID: "gemini", ContractVersion: 1},
		Protocol:      kernel.ProtocolGemini, CredentialPresentation: kernel.CredentialPresentation{Header: "x-goog-api-key"},
		OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{
			normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{binding.AdapterID}},
		},
		Profile: kernel.CapabilityProfile{kernel.CapabilityTools: {State: kernel.SupportNative}},
		BaseURL: upstream.URL + "/v1beta", ExternalModel: "gemini-flash", Enabled: true,
	}
	snapshot, err := kernel.BuildSnapshot(kernel.SnapshotInput{
		PublicModels: []kernel.PublicModel{{Name: "role", TargetRef: "role"}},
		Routes:       []kernel.Route{route},
		Nodes:        []kernel.ModelNode{{ID: "role", Kind: kernel.ModelPhysical, Members: []kernel.MemberRef{{Kind: kernel.MemberRoute, ID: route.ID, Fidelity: kernel.FidelityExact}}}},
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
		"stream_options": map[string]any{"include_usage": true},
		"messages":       []any{map[string]any{"role": "user", "content": "answer briefly"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !request.Request.Transport.IncludeUsage {
		t.Fatal("normalizer lost the requested usage transport hint")
	}
	recorder := httptest.NewRecorder()
	if err := gateway.Execute(context.Background(), request.Request, kernel.Credential{Secret: "test-key"}, recorder); err != nil {
		t.Fatal(err)
	}
	var usage map[string]any
	for _, line := range strings.Split(recorder.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
			continue
		}
		var frame struct {
			Usage map[string]any `json:"usage"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil {
			t.Fatal(err)
		}
		if frame.Usage != nil {
			usage = frame.Usage
		}
	}
	if recorder.Code != http.StatusOK || usage == nil || usage["prompt_tokens"] != float64(11) || usage["completion_tokens"] != float64(6) {
		t.Fatalf("Chat usage option did not reach client: status=%d usage=%#v body=%s", recorder.Code, usage, recorder.Body.String())
	}
}
