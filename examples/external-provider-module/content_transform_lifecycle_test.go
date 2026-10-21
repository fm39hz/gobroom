package externalprovider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/fm39hz/gobroom/pkg/daemon"
	"github.com/fm39hz/gobroom/pkg/provider"
)

const contentTransformOptionsSchema = `{"type":"object","properties":{"suffix":{"type":"string","maxLength":80},"mode":{"type":"string","enum":["canonical","opaque"]}},"required":["suffix","mode"],"additionalProperties":false}`

var contentTransformOptionsRef = provider.Ref{Kind: "schema", ID: "fixture.content-transform-options", ContractVersion: 1}

type contentLifecycleTransform struct{}

func (contentLifecycleTransform) Definition() provider.TransformDefinition {
	return provider.TransformDefinition{
		Ref:                   provider.Ref{Kind: provider.RequestTransformKind, ID: "fixture.content-suffix", ContractVersion: 1},
		ImplementationVersion: "1", Label: "Append canonical content", Description: "Appends one configured user message.",
		OptionsSchemaRef: &contentTransformOptionsRef, Stage: provider.TransformBeforeRequirements,
		Effects: []provider.TransformEffect{provider.TransformInput},
	}
}

func (contentLifecycleTransform) Apply(_ context.Context, request *provider.NormalizedRequest, raw json.RawMessage) error {
	var options struct {
		Suffix string `json:"suffix"`
		Mode   string `json:"mode"`
	}
	if err := json.Unmarshal(raw, &options); err != nil {
		return err
	}
	if options.Mode == "opaque" {
		request.Messages = append(request.Messages, provider.Message{Role: "user", Content: "private", Metadata: map[string]any{"private": true}})
		return nil
	}
	request.Messages = append(request.Messages, provider.Message{Role: "user", Content: options.Suffix})
	return nil
}

type contentLifecycleModule struct{}

func (contentLifecycleModule) ModuleDescriptor() provider.ModuleRef {
	return provider.ModuleRef{ID: "fixture.content-transform", Version: "1", HostContractVersion: provider.RuntimeModuleHostContractVersion}
}

func (contentLifecycleModule) Register(registry *provider.RuntimeRegistry) error {
	return registry.RegisterRequestTransform(contentLifecycleTransform{}, json.RawMessage(contentTransformOptionsSchema))
}

func TestExternalContentTransformBindingLifecycle(t *testing.T) {
	root := t.TempDir()
	var calls atomic.Int32
	var wantSuffix atomic.Value
	wantSuffix.Store("")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read upstream request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var payload struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("decode upstream request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if suffix := wantSuffix.Load().(string); suffix != "" {
			if len(payload.Messages) != 2 || payload.Messages[1].Role != "user" || payload.Messages[1].Content != suffix {
				t.Errorf("transformed upstream messages=%s want appended user content %q", body, suffix)
			}
		} else if len(payload.Messages) != 1 {
			t.Errorf("unexpected upstream messages after binding removal: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-transform","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()

	config := daemon.Config{DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "daemon.sock"), HTTPEnabled: true, HTTPAddr: "127.0.0.1:0"}
	service := daemon.NewWithProviderModules(config, contentLifecycleModule{})
	start := func() {
		t.Helper()
		if err := service.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	stop := func() {
		t.Helper()
		if err := service.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	start()
	stopped := false
	defer func() {
		if !stopped {
			_ = service.Stop(context.Background())
		}
	}()

	rpc := func(method string, params map[string]any) json.RawMessage {
		t.Helper()
		response, err := daemon.CallIPC(context.Background(), config.IPCPath, daemon.IPCRequest{Method: method, Params: params})
		if err != nil || !response.OK {
			t.Fatalf("%s: response=%#v err=%v", method, response, err)
		}
		data, err := json.Marshal(response.Result)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	var node, connection struct {
		ID string `json:"ID"`
	}
	if err := json.Unmarshal(rpc("providers.create", map[string]any{"name": "Transform fixture", "prefix": "content-transform", "baseUrl": upstream.URL + "/v1", "protocol": "openai_chat", "definitionID": "openai-compatible-chat"}), &node); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rpc("connections.create", map[string]any{"providerNodeID": node.ID, "name": "Fixture account", "credentialType": "api_key", "secret": "fixture-key"}), &connection); err != nil {
		t.Fatal(err)
	}
	rpc("custom_models.upsert", map[string]any{"id": "content-transform-route", "providerNodeID": node.ID, "kind": "custom", "externalID": "fixture-model", "displayName": "Fixture model", "connectionIds": []string{connection.ID}})
	rpc("physical_models.upsert", map[string]any{"name": "content-transform-physical", "identity": map[string]any{"canonicalName": "fixture-model"}, "sources": []map[string]any{{"routeId": "content-transform-route", "fidelity": "exact"}}, "enabled": true})
	rpc("combo_models.upsert", map[string]any{"name": "publicCombo", "members": []map[string]any{{"kind": "physical", "id": "content-transform-physical"}}, "discoverable": true, "enabled": true})

	ref := contentLifecycleTransform{}.Definition().Ref
	binding := map[string]any{"id": "content-suffix", "transformRef": ref, "enabled": false, "scope": provider.TransformScope{Kind: provider.TransformScopeModel, ID: "publicCombo"}, "failureMode": provider.TransformFailClosed, "options": json.RawMessage(`{"suffix":"canonical-one","mode":"canonical"}`)}
	cli := os.Getenv("GOBROOM_TEST_CLI")
	runCLI := func(args ...string) []byte {
		t.Helper()
		return runDeviceRaceCLI(t, cli, config.IPCPath, args...)
	}
	writeCLIFile := func(value any) string {
		t.Helper()
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "transform-binding.json")
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	putBinding := func(value map[string]any) {
		t.Helper()
		if cli == "" {
			rpc("transform_bindings.upsert", value)
			return
		}
		var decoded provider.TransformBinding
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		runCLI("transforms", "upsert", "--file", writeCLIFile(decoded))
	}
	deleteBinding := func(id string) {
		t.Helper()
		if cli == "" {
			rpc("transform_bindings.delete", map[string]any{"id": id})
			return
		}
		runCLI("transforms", "delete", "--id", id)
	}
	list := func() []provider.TransformBinding {
		var result []provider.TransformBinding
		var encoded []byte
		if cli == "" {
			encoded = rpc("transform_bindings.list", nil)
		} else {
			encoded = runCLI("transforms", "list")
		}
		if err := json.Unmarshal(encoded, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	invoke := func(wantContent string) int {
		t.Helper()
		wantSuffix.Store(wantContent)
		response, err := http.Post("http://"+service.Addresses().HTTP+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"publicCombo","messages":[{"role":"user","content":"original"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, response.Body)
		return response.StatusCode
	}
	if got := len(list()); got != 0 {
		t.Fatalf("registered but unbound transform unexpectedly appears as a binding: %d", got)
	}
	if status := invoke(""); status != http.StatusOK || calls.Load() != 1 {
		t.Fatalf("inactive transform request status=%d calls=%d", status, calls.Load())
	}
	binding["enabled"] = true
	putBinding(binding)
	if status := invoke("canonical-one"); status != http.StatusOK || calls.Load() != 2 {
		t.Fatalf("enabled transform request status=%d calls=%d", status, calls.Load())
	}
	binding["options"] = json.RawMessage(`{"suffix":"canonical-two","mode":"canonical"}`)
	putBinding(binding)
	if status := invoke("canonical-two"); status != http.StatusOK || calls.Load() != 3 {
		t.Fatalf("updated options request status=%d calls=%d", status, calls.Load())
	}

	// Invalid opaque provenance must roll back transactionally and stop before upstream.
	binding["options"] = json.RawMessage(`{"suffix":"rejected","mode":"opaque"}`)
	putBinding(binding)
	if status := invoke("rejected"); status == http.StatusOK || calls.Load() != 3 {
		t.Fatalf("opaque provenance mutation was not fail closed: status=%d calls=%d", status, calls.Load())
	}
	binding["options"] = json.RawMessage(`{"suffix":"canonical-two","mode":"canonical"}`)
	putBinding(binding)

	stop()
	stopped = true
	service = daemon.NewWithProviderModules(config, contentLifecycleModule{})
	start()
	stopped = false
	if status := invoke("canonical-two"); status != http.StatusOK || calls.Load() != 4 {
		t.Fatalf("restart did not retain transform binding/options: status=%d calls=%d", status, calls.Load())
	}
	if len(list()) != 1 {
		t.Fatalf("restart did not retain transform binding: %s", rpc("transform_bindings.list", nil))
	}

	binding["enabled"] = false
	binding["options"] = json.RawMessage(`{"suffix":"canonical-two","mode":"canonical"}`)
	putBinding(binding)
	if status := invoke(""); status != http.StatusOK || calls.Load() != 5 {
		t.Fatalf("disabled transform request status=%d calls=%d", status, calls.Load())
	}
	deleteBinding("content-suffix")
	if len(list()) != 0 {
		t.Fatalf("deleted binding remains: %s", rpc("transform_bindings.list", nil))
	}
	if status := invoke(""); status != http.StatusOK || calls.Load() != 6 {
		t.Fatalf("deleted transform request status=%d calls=%d", status, calls.Load())
	}
}
