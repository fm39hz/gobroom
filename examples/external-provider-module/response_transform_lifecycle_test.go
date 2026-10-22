package externalprovider

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/pkg/daemon"
	"github.com/fm39hz/gobroom/pkg/provider"
)

const responseLifecycleOptionsSchema = `{"type":"object","properties":{"suffix":{"type":"string","minLength":1,"maxLength":80},"usageAdjustment":{"type":"integer","minimum":0,"maximum":1000}},"required":["suffix","usageAdjustment"],"additionalProperties":false}`

var responseLifecycleOptionsRef = provider.Ref{Kind: provider.SchemaKind, ID: "fixture.response-transform-options", ContractVersion: 1}

type responseLifecycleTransform struct{}

func (responseLifecycleTransform) Definition() provider.ResponseTransformDefinition {
	return provider.ResponseTransformDefinition{
		Ref:                   provider.Ref{Kind: provider.ResponseTransformKind, ID: "fixture.response-text-suffix", ContractVersion: 1},
		ImplementationVersion: "1",
		Label:                 "Append canonical response text",
		Description:           "Appends configured text to canonical response events.",
		OptionsSchemaRef:      &responseLifecycleOptionsRef,
		Effects:               []provider.ResponseTransformEffect{provider.ResponseEffectText, provider.ResponseEffectUsage},
	}
}

func (responseLifecycleTransform) ApplyResponse(_ context.Context, event provider.ResponseEvent, raw json.RawMessage) (provider.ResponseEvent, error) {
	var options struct {
		Suffix          string `json:"suffix"`
		UsageAdjustment int64  `json:"usageAdjustment"`
	}
	if err := json.Unmarshal(raw, &options); err != nil {
		return event, err
	}
	switch event.Kind {
	case provider.EventTextDelta:
		event.Text += options.Suffix
	case provider.EventUsage:
		if event.Usage != nil {
			if event.Usage.OutputTokens != nil {
				event.Usage.OutputTokens = new(*event.Usage.OutputTokens + options.UsageAdjustment)
			}
		}
	}
	return event, nil
}

type responseLifecycleModule struct{}

func (responseLifecycleModule) ModuleDescriptor() provider.ModuleRef {
	return provider.ModuleRef{ID: "fixture.response-transform", Version: "1", HostContractVersion: provider.RuntimeModuleHostContractVersion}
}

func (responseLifecycleModule) Register(registry *provider.RuntimeRegistry) error {
	return registry.RegisterResponseTransform(responseLifecycleTransform{}, json.RawMessage(responseLifecycleOptionsSchema))
}

func TestExternalResponseTransformBindingLifecycle(t *testing.T) {
	root := t.TempDir()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode upstream request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if request.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-response-transform\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"provider\"},\"finish_reason\":null}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-response-transform\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":3,\"total_tokens\":10}}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-response-transform","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"provider"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`)
	}))
	defer upstream.Close()

	config := daemon.Config{DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "daemon.sock"), HTTPEnabled: true, HTTPAddr: "127.0.0.1:0"}
	newDaemon := func() *daemon.Daemon {
		service := daemon.NewWithProviderModules(config, responseLifecycleModule{})
		if err := service.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		return service
	}
	service := newDaemon()
	defer func() { _ = service.Stop(context.Background()) }()
	client := &http.Client{Timeout: 5 * time.Second}

	rpc := func(method string, params map[string]any) json.RawMessage {
		t.Helper()
		response, err := daemon.CallIPC(context.Background(), config.IPCPath, daemon.IPCRequest{Method: method, Params: params})
		if err != nil || !response.OK {
			t.Fatalf("%s: response=%#v err=%v", method, response, err)
		}
		encoded, err := json.Marshal(response.Result)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	var node, connection struct{ ID string }
	if err := json.Unmarshal(rpc("providers.create", map[string]any{
		"name": "Response transform fixture", "prefix": "response-transform", "baseUrl": upstream.URL + "/v1",
		"protocol": "openai_chat", "definitionID": "openai-compatible-chat",
	}), &node); err != nil || node.ID == "" {
		t.Fatalf("provider node=%#v err=%v", node, err)
	}
	if err := json.Unmarshal(rpc("connections.create", map[string]any{
		"providerNodeID": node.ID, "name": "Fixture account", "credentialType": "api_key", "secret": "fixture-key",
	}), &connection); err != nil || connection.ID == "" {
		t.Fatalf("connection=%#v err=%v", connection, err)
	}
	rpc("custom_models.upsert", map[string]any{"id": "response-transform-route", "providerNodeID": node.ID, "kind": "custom", "externalID": "fixture-model", "displayName": "Fixture model", "connectionIds": []string{connection.ID}})
	rpc("physical_models.upsert", map[string]any{"name": "response-transform-physical", "identity": map[string]any{"canonicalName": "fixture-model"}, "sources": []map[string]any{{"routeId": "response-transform-route", "fidelity": "exact"}}, "enabled": true})
	rpc("combo_models.upsert", map[string]any{"name": "response-transform-model", "members": []map[string]any{{"kind": "physical", "id": "response-transform-physical"}}, "discoverable": true, "enabled": true})

	cli := os.Getenv("GOBROOM_TEST_CLI")
	runCLI := func(args ...string) []byte { t.Helper(); return runDeviceRaceCLI(t, cli, config.IPCPath, args...) }
	writeBindingFile := func(value provider.TransformBinding) string {
		t.Helper()
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "response-transform-binding.json")
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	putBinding := func(binding provider.TransformBinding) {
		t.Helper()
		if cli != "" {
			runCLI("transforms", "upsert", "--file", writeBindingFile(binding))
			return
		}
		rpc("transform_bindings.upsert", map[string]any{
			"id": binding.ID, "transformRef": binding.TransformRef, "enabled": binding.Enabled,
			"scope": binding.Scope, "failureMode": binding.FailureMode, "options": binding.Options,
		})
	}
	listBindings := func() []provider.TransformBinding {
		t.Helper()
		var encoded []byte
		if cli != "" {
			encoded = runCLI("transforms", "list")
		} else {
			encoded = rpc("transform_bindings.list", nil)
		}
		var bindings []provider.TransformBinding
		if err := json.Unmarshal(encoded, &bindings); err != nil {
			t.Fatal(err)
		}
		return bindings
	}
	deleteBinding := func(id string) {
		t.Helper()
		if cli != "" {
			runCLI("transforms", "delete", "--id", id)
			return
		}
		rpc("transform_bindings.delete", map[string]any{"id": id})
	}

	ref := responseLifecycleTransform{}.Definition().Ref
	binding := provider.TransformBinding{
		ID: "response-text-suffix", TransformRef: ref, Enabled: true,
		Scope:       provider.TransformScope{Kind: provider.TransformScopeModel, ID: "response-transform-model"},
		FailureMode: provider.TransformFailClosed, Options: json.RawMessage(`{"suffix":"-one","usageAdjustment":100}`),
	}
	if len(listBindings()) != 0 {
		t.Fatal("registered but unbound response transform unexpectedly has a binding")
	}
	call := func(stream bool) (int, []byte) {
		t.Helper()
		payload := map[string]any{"model": "response-transform-model", "messages": []map[string]string{{"role": "user", "content": "hello"}}, "stream": stream}
		if stream {
			payload["stream_options"] = map[string]any{"include_usage": true}
		}
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		request, err := http.NewRequest(http.MethodPost, "http://"+service.Addresses().HTTP+"/v1/chat/completions", strings.NewReader(string(body)))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, data
	}
	assertJSONText := func(want string, wantOutputTokens int64) {
		t.Helper()
		status, body := call(false)
		var result struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
			Usage struct {
				PromptTokens     int64 `json:"prompt_tokens"`
				CompletionTokens int64 `json:"completion_tokens"`
				TotalTokens      int64 `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatalf("decode JSON response %s: %v", body, err)
		}
		if status != http.StatusOK || len(result.Choices) != 1 || result.Choices[0].Message.Content != want || result.Usage.PromptTokens != 7 || result.Usage.CompletionTokens != wantOutputTokens || result.Usage.TotalTokens != 7+wantOutputTokens {
			t.Fatalf("JSON response status=%d body=%s; wanted canonical text %q and output tokens %d", status, body, want, wantOutputTokens)
		}
	}
	assertSSEText := func(want string, wantOutputTokens int64) {
		t.Helper()
		status, body := call(true)
		var text strings.Builder
		done := false
		usageSeen := false
		scanner := bufio.NewScanner(strings.NewReader(string(body)))
		for scanner.Scan() {
			payload, ok := strings.CutPrefix(scanner.Text(), "data: ")
			if !ok {
				continue
			}
			if payload == "[DONE]" {
				done = true
				continue
			}
			var frame struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
				} `json:"choices"`
				Usage *struct {
					PromptTokens     int64 `json:"prompt_tokens"`
					CompletionTokens int64 `json:"completion_tokens"`
					TotalTokens      int64 `json:"total_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal([]byte(payload), &frame); err != nil {
				t.Fatalf("decode client SSE frame %q: %v", payload, err)
			}
			for _, choice := range frame.Choices {
				text.WriteString(choice.Delta.Content)
			}
			if frame.Usage != nil {
				usageSeen = true
				if frame.Usage.PromptTokens != 7 || frame.Usage.CompletionTokens != wantOutputTokens || frame.Usage.TotalTokens != 7+wantOutputTokens {
					t.Fatalf("client SSE usage=%#v; wanted output token count %d", frame.Usage, wantOutputTokens)
				}
			}
		}
		if err := scanner.Err(); err != nil {
			t.Fatal(err)
		}
		if status != http.StatusOK || !done || !usageSeen || text.String() != want {
			t.Fatalf("SSE response status=%d text=%q done=%v body=%s; wanted %q", status, text.String(), done, body, want)
		}
	}
	assertJSONText("provider", 3)
	assertSSEText("provider", 3)

	putBinding(binding)
	assertJSONText("provider-one", 103)
	assertSSEText("provider-one", 103)

	// A schema-invalid replacement must leave the active, valid options in place.
	invalid := binding
	invalid.Options = json.RawMessage(`{"suffix":"invalid","unknown":true}`)
	response, err := daemon.CallIPC(context.Background(), config.IPCPath, daemon.IPCRequest{Method: "transform_bindings.upsert", Params: mustJSONParams(t, invalid)})
	if err == nil || response.OK {
		t.Fatalf("schema-invalid upsert did not return both IPC failure signals: response=%#v err=%v", response, err)
	}
	assertJSONText("provider-one", 103)
	assertSSEText("provider-one", 103)

	binding.Options = json.RawMessage(`{"suffix":"-two","usageAdjustment":100}`)
	putBinding(binding)
	assertJSONText("provider-two", 103)
	assertSSEText("provider-two", 103)

	if err := service.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	service = newDaemon()
	if len(listBindings()) != 1 {
		t.Fatalf("restart did not retain response transform binding: %s", rpc("transform_bindings.list", nil))
	}
	assertJSONText("provider-two", 103)
	assertSSEText("provider-two", 103)

	binding.Enabled = false
	putBinding(binding)
	assertJSONText("provider", 3)
	assertSSEText("provider", 3)
	deleteBinding(binding.ID)
	if len(listBindings()) != 0 {
		t.Fatalf("deleted response transform binding remains: %s", rpc("transform_bindings.list", nil))
	}
	assertJSONText("provider", 3)
	assertSSEText("provider", 3)
	if err := service.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	service = newDaemon()

	var usages []struct {
		InputTokens  int64 `json:"inputTokens"`
		OutputTokens int64 `json:"outputTokens"`
	}
	if err := json.Unmarshal(rpc("usage.list", map[string]any{"limit": 100}), &usages); err != nil {
		t.Fatal(err)
	}
	if len(usages) != 14 {
		t.Fatalf("provider usage count=%d want 14", len(usages))
	}
	for _, usage := range usages {
		if usage.InputTokens != 7 || usage.OutputTokens != 3 {
			t.Fatalf("provider usage was changed by client response transform: %#v", usage)
		}
	}
}
