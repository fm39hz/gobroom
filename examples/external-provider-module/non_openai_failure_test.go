package externalprovider

import (
	"context"
	"encoding/json"
	"fmt"
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

type nonOpenAIFixtureMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Only the error envelope follows the provider wire format. The reason used to
// request continuity recovery is fixture-specific, not a builtin provider rule.
func TestNonOpenAIProviderFailuresReachConfiguredClassifier(t *testing.T) {
	for _, fixture := range []struct {
		name, definition, protocol, basePath, reasonPath, healthy, errorJSON string
		partial                                                              []string
	}{
		{
			name: "anthropic", definition: "anthropic-messages", protocol: "anthropic", basePath: "/v1", reasonPath: "/error/details/reason",
			healthy:   `{"type":"message","id":"msg_ok","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`,
			errorJSON: `{"type":"error","error":{"type":"invalid_request_error","message":"private-provider-marker","details":{"reason":"fixture_continuity_reset"}}}`,
			partial: []string{
				`{"type":"message_start","message":{"id":"msg_partial","type":"message","role":"assistant","content":[],"usage":{"input_tokens":7,"output_tokens":0}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial-text"}}`,
				`{"type":"message_delta","delta":{},"usage":{"output_tokens":2}}`,
			},
		},
		{
			name: "gemini", definition: "gemini", protocol: "gemini", basePath: "/v1beta", reasonPath: "/error/details/0/reason",
			healthy:   `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1}}`,
			errorJSON: `{"error":{"code":400,"status":"FAILED_PRECONDITION","message":"private-provider-marker","details":[{"reason":"fixture_continuity_reset"}]}}`,
			partial:   []string{`{"candidates":[{"content":{"parts":[{"text":"partial-text"}]}}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":2}}`},
		},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			var mode, calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch mode.Swap(0) {
				case 1:
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, fixture.errorJSON)
				case 2:
					w.Header().Set("Content-Type", "text/event-stream")
					for _, event := range fixture.partial {
						_, _ = fmt.Fprintf(w, "data: %s\n\n", event)
					}
					if flusher, ok := w.(http.Flusher); ok {
						flusher.Flush()
					}
					_, _ = fmt.Fprintf(w, "data: %s\n\n", fixture.errorJSON)
					// The decoder must stop at the error, not accept a later stop.
					if fixture.name == "anthropic" {
						_, _ = io.WriteString(w, "data: {\"type\":\"message_stop\"}\n\n")
					} else {
						_, _ = io.WriteString(w, "data: [DONE]\n\n")
					}
				default:
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, fixture.healthy)
				}
			}))
			defer upstream.Close()
			root := t.TempDir()
			manifests := filepath.Join(root, "providers")
			if err := os.Mkdir(manifests, 0o700); err != nil {
				t.Fatal(err)
			}
			runtime, err := provider.NewRuntimeRegistry()
			if err != nil {
				t.Fatal(err)
			}
			definition, ok := runtime.Primitives.Definition(provider.ProviderDefinitionRef(fixture.definition, 1))
			if !ok {
				t.Fatal("missing builtin definition")
			}
			definition.ID = "fixture-error-" + fixture.name
			for operation, binding := range definition.Operations {
				if binding.RequestCodec.ID == "" {
					continue
				}
				binding.ErrorClassifier = provider.PrimitiveRef{Kind: provider.PrimitiveErrorClassifier, ID: "http-json", ContractVersion: 1}
				binding.ErrorClassifierOptions = provider.ErrorClassifierOptions{HTTPJSON: &provider.HTTPJSONErrorClassifierOptions{
					ResponseContinuityResetSignals: []provider.HTTPJSONErrorSignal{{Fields: []provider.HTTPJSONFieldMatch{{Path: fixture.reasonPath, Equals: []string{"fixture_continuity_reset"}}}}},
				}}
				definition.Operations[operation] = binding
			}
			encoded, err := provider.EncodeManifest(definition)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(manifests, "fixture.json"), encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			config := daemon.Config{DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "daemon.sock"), ProviderManifestDir: manifests, HTTPEnabled: true, HTTPAddr: "127.0.0.1:0"}
			service := daemon.New(config)
			if err := service.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = service.Stop(context.Background()) }()
			rpc := func(method string, params map[string]any) json.RawMessage {
				t.Helper()
				response, err := daemon.CallIPC(context.Background(), config.IPCPath, daemon.IPCRequest{Method: method, Params: params})
				if err != nil || !response.OK {
					t.Fatalf("%s err=%v response=%#v", method, err, response)
				}
				data, err := json.Marshal(response.Result)
				if err != nil {
					t.Fatal(err)
				}
				return data
			}
			var node, connection struct{ ID string }
			if err := json.Unmarshal(rpc("providers.create", map[string]any{"name": "fixture", "prefix": "f", "baseUrl": upstream.URL + fixture.basePath, "protocol": fixture.protocol, "definitionID": definition.ID}), &node); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(rpc("connections.create", map[string]any{"providerNodeID": node.ID, "name": "fixture", "credentialType": "api_key", "secret": "fixture-key"}), &connection); err != nil {
				t.Fatal(err)
			}
			rpc("custom_models.upsert", map[string]any{"id": "route", "providerNodeID": node.ID, "kind": "custom", "externalID": "fixture-model", "displayName": "fixture", "connectionIds": []string{connection.ID}})
			rpc("physical_models.upsert", map[string]any{"name": "physical", "identity": map[string]any{"canonicalName": "fixture-model"}, "sources": []map[string]any{{"routeId": "route", "fidelity": "exact"}}, "enabled": true})
			rpc("combo_models.upsert", map[string]any{"name": "role", "members": []map[string]any{{"kind": "physical", "id": "physical"}}, "discoverable": true, "enabled": true})
			call := func(stream bool) (int, []byte) {
				t.Helper()
				body, err := json.Marshal(struct {
					Model    string                    `json:"model"`
					Messages []nonOpenAIFixtureMessage `json:"messages"`
					Stream   bool                      `json:"stream"`
				}{Model: "role", Messages: []nonOpenAIFixtureMessage{{Role: "user", Content: "hello"}}, Stream: stream})
				if err != nil {
					t.Fatal(err)
				}
				request, err := http.NewRequest(http.MethodPost, "http://"+service.Addresses().HTTP+"/v1/chat/completions", strings.NewReader(string(body)))
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Content-Type", "application/json")
				response, err := http.DefaultClient.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				data, err := io.ReadAll(io.LimitReader(response.Body, 128<<10))
				if err != nil {
					t.Fatal(err)
				}
				return response.StatusCode, data
			}
			want := provider.ClientFailureForError(provider.ErrContinuityRestartRequired)
			mode.Store(1)
			status, body := call(false)
			var envelope struct {
				Error provider.ClientFailure `json:"error"`
			}
			if err := json.Unmarshal(body, &envelope); err != nil || status != http.StatusConflict || envelope.Error != want {
				t.Fatalf("JSON classifier projection status=%d body=%s err=%v", status, body, err)
			}
			mode.Store(2)
			status, body = call(true)
			if status != http.StatusOK || !strings.Contains(string(body), "partial-text") || strings.Contains(string(body), "private-provider-marker") || strings.Contains(string(body), "[DONE]") {
				t.Fatalf("stream projection status=%d body=%s", status, body)
			}
			failures := 0
			for _, line := range strings.Split(string(body), "\n") {
				data, ok := strings.CutPrefix(line, "data:")
				if !ok {
					continue
				}
				var frame struct {
					Error provider.ClientFailure `json:"error"`
				}
				if err := json.Unmarshal([]byte(data), &frame); err != nil {
					t.Fatal(err)
				}
				if frame.Error.Code != "" {
					failures++
					if frame.Error != want {
						t.Fatalf("stream descriptor=%#v", frame.Error)
					}
				}
			}
			if failures != 1 || calls.Load() != 2 {
				t.Fatalf("terminal frames=%d upstream calls=%d", failures, calls.Load())
			}
			if cli := os.Getenv("GOBROOM_TEST_CLI"); cli != "" {
				runDeviceRaceCLI(t, cli, config.IPCPath, "status")
			}
			if status, body := call(false); status != http.StatusOK {
				t.Fatalf("request-scoped failure suppressed provider: %d %s", status, body)
			}
			if err := service.Stop(context.Background()); err != nil {
				t.Fatal(err)
			}
			service = daemon.New(config)
			if err := service.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			var usages []struct {
				Status                    string
				InputTokens, OutputTokens int64
			}
			if err := json.Unmarshal(rpc("usage.list", map[string]any{"limit": 100}), &usages); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, usage := range usages {
				if usage.Status == "error" && usage.InputTokens == 7 && usage.OutputTokens == 2 {
					found = true
				}
			}
			if !found {
				t.Fatalf("failed usage not retained through restart: %#v", usages)
			}
		})
	}
}
