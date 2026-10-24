package externalprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/pkg/daemon"
	"github.com/fm39hz/gobroom/pkg/provider"
)

// Both cases load the same module, receive the same upstream headers/body, and
// build the same graph. Only the provider's usage-source binding changes.
func newUsageProxyBenchmarkFixture(tb testing.TB, extension bool) *proxyBenchmarkFixture {
	tb.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Stream bool `json:"stream"`
		}
		if r.URL.Path != "/v1/chat/completions" || json.NewDecoder(r.Body).Decode(&request) != nil {
			http.Error(w, "invalid fixture request", http.StatusBadRequest)
			return
		}
		w.Header().Set("X-Search-Units", searchUnitsLexeme)
		w.Header().Set("X-Search-Marker", searchPrivateMark)
		if request.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: {\"id\":\"usage-bench\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":%q},\"finish_reason\":null}]}\n\n", benchmarkReply)
			_, _ = io.WriteString(w, "data: {\"id\":\"usage-bench\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			w.(http.Flusher).Flush()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"usage-bench","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}]}`, benchmarkReply)
	}))
	tb.Cleanup(upstream.Close)
	root := tb.TempDir()
	manifestDir := filepath.Join(root, "providers")
	if err := os.Mkdir(manifestDir, 0o700); err != nil {
		tb.Fatal(err)
	}
	runtime, err := provider.NewRuntimeRegistry()
	if err != nil {
		tb.Fatal(err)
	}
	definition, ok := runtime.Primitives.Definition(provider.ProviderDefinitionRef("openai-compatible-chat", 1))
	if !ok {
		tb.Fatal("missing fixture base definition")
	}
	definition.ID = "benchmark-paired-usage"
	if extension {
		binding := definition.Operations[provider.OperationChat]
		binding.UsageSource = provider.PrimitiveRef{Kind: provider.PrimitiveUsageSource, ID: searchSourceID, ContractVersion: 1}
		definition.Operations[provider.OperationChat] = binding
	}
	manifest, err := provider.EncodeManifest(definition)
	if err != nil {
		tb.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "provider.json"), manifest, 0o600); err != nil {
		tb.Fatal(err)
	}
	policy := daemon.DefaultUsagePolicy()
	policy.AllowManagementDetails, policy.AllowPrivateDetails = true, true
	config := daemon.Config{DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "daemon.sock"), HTTPEnabled: true, HTTPAddr: "127.0.0.1:0", ProviderManifestDir: manifestDir, UsagePolicy: &policy}
	service := daemon.NewWithProviderModules(config, SearchUsageModule{})
	if err := service.Start(context.Background()); err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() {
		if err := service.Stop(context.Background()); err != nil {
			tb.Error(err)
		}
	})
	rpc := func(method string, params map[string]any) json.RawMessage {
		tb.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		response, err := daemon.CallIPC(ctx, config.IPCPath, daemon.IPCRequest{Method: method, Params: params})
		if err != nil || !response.OK {
			tb.Fatalf("fixture %s: %v / %s", method, err, response.Error)
		}
		encoded, err := json.Marshal(response.Result)
		if err != nil {
			tb.Fatal(err)
		}
		return encoded
	}
	var node, connection struct{ ID string }
	if err := json.Unmarshal(rpc("providers.create", map[string]any{"name": "Paired usage", "prefix": "paired", "baseUrl": upstream.URL + "/v1", "protocol": "openai_chat", "definitionID": definition.ID}), &node); err != nil {
		tb.Fatal(err)
	}
	if err := json.Unmarshal(rpc("connections.create", map[string]any{"providerNodeID": node.ID, "name": "Fixture", "credentialType": "api_key", "secret": "benchmark-only"}), &connection); err != nil {
		tb.Fatal(err)
	}
	rpc("custom_models.upsert", map[string]any{"id": "paired-route", "providerNodeID": node.ID, "kind": "custom", "externalID": "fixture", "displayName": "Fixture", "connectionIds": []string{connection.ID}})
	rpc("physical_models.upsert", map[string]any{"name": "paired-physical", "identity": map[string]any{"canonicalName": "fixture"}, "sources": []map[string]any{{"routeId": "paired-route", "fidelity": "exact"}}, "enabled": true})
	rpc("combo_models.upsert", map[string]any{"name": "paired-public", "members": []map[string]any{{"kind": "physical", "id": "paired-physical"}}, "enabled": true, "discoverable": true})
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{MaxIdleConns: 32, MaxIdleConnsPerHost: 32}}
	tb.Cleanup(client.CloseIdleConnections)
	return &proxyBenchmarkFixture{client: client, ipcPath: config.IPCPath, proxyURL: "http://" + service.Addresses().HTTP + "/v1/chat/completions",
		jsonBody: []byte(`{"model":"paired-public","messages":[{"role":"user","content":"Reply briefly."}],"max_tokens":16}`),
		sseBody:  []byte(`{"model":"paired-public","messages":[{"role":"user","content":"Reply briefly."}],"max_tokens":16,"stream":true}`)}
}

func TestUsageProxyBenchmarkFixturePersistsOnlyEnabledExtension(t *testing.T) {
	for _, extension := range []bool{false, true} {
		t.Run(fmt.Sprintf("extension-%t", extension), func(t *testing.T) {
			fixture := newUsageProxyBenchmarkFixture(t, extension)
			for _, body := range [][]byte{fixture.jsonBody, fixture.sseBody} {
				response, err := fixture.client.Post(fixture.proxyURL, "application/json", bytes.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil || response.StatusCode != http.StatusOK || !bytes.Contains(data, []byte(benchmarkReply)) || bytes.Contains(data, []byte(searchPrivateMark)) || bytes.Contains(data, []byte(searchUnitsLexeme)) {
					t.Fatalf("fixture answer/leak failure: %d %s / %v", response.StatusCode, data, err)
				}
			}
			deadline := time.Now().Add(5 * time.Second)
			for fixture.usageStatus(t).Persisted != 2 {
				if time.Now().After(deadline) {
					t.Fatal("paired fixture accounting did not persist")
				}
				time.Sleep(time.Millisecond)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			response, err := daemon.CallIPC(ctx, fixture.ipcPath, daemon.IPCRequest{Method: "usage.list"})
			if err != nil || !response.OK {
				t.Fatalf("fixture list: %v / %s", err, response.Error)
			}
			encoded, _ := json.Marshal(response.Result)
			var rows []struct{ ID int64 }
			if err := json.Unmarshal(encoded, &rows); err != nil || len(rows) != 2 {
				t.Fatalf("fixture rows: %s / %v", encoded, err)
			}
			for _, row := range rows {
				response, err := daemon.CallIPC(ctx, fixture.ipcPath, daemon.IPCRequest{Method: "usage.detail", Params: map[string]any{"id": row.ID, "includeExtensions": true, "includePrivate": true}})
				if err != nil || !response.OK {
					t.Fatalf("fixture detail: %v / %s", err, response.Error)
				}
				encoded, _ := json.Marshal(response.Result)
				var detail struct {
					Extensions []struct {
						Value struct {
							Units  json.Number `json:"units"`
							Marker string      `json:"marker"`
						} `json:"value"`
					} `json:"extensions"`
				}
				if err := json.Unmarshal(encoded, &detail); err != nil {
					t.Fatal(err)
				}
				want := 0
				if extension {
					want = 1
				}
				if len(detail.Extensions) != want {
					t.Fatalf("fixture did not exercise requested path: %s", encoded)
				}
				if extension && (detail.Extensions[0].Value.Units.String() != searchUnitsLexeme || detail.Extensions[0].Value.Marker != searchPrivateMark) {
					t.Fatalf("fixture extension lost precision/value: %s", encoded)
				}
			}
		})
	}
}

func BenchmarkUsageProxy(b *testing.B) {
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.Cleanup(func() { slog.SetDefault(previousLogger) })
	for _, extension := range []bool{false, true} {
		b.Run(fmt.Sprintf("extension-%t", extension), func(b *testing.B) {
			fixture := newUsageProxyBenchmarkFixture(b, extension)
			for _, mode := range []struct {
				name   string
				body   []byte
				stream bool
			}{{"JSON", fixture.jsonBody, false}, {"SSE", fixture.sseBody, true}} {
				b.Run(mode.name, func(b *testing.B) {
					before := fixture.usageStatus(b)
					b.ReportAllocs()
					b.ResetTimer()
					for range b.N {
						fixture.request(b, fixture.proxyURL, mode.body, mode.stream)
					}
					b.StopTimer()
					fixture.reportUsage(b, before, true)
				})
			}
		})
	}
}
