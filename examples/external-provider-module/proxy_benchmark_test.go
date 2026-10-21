package externalprovider

import (
	"bufio"
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
	"strings"
	"sync"
	"testing"

	"github.com/fm39hz/gobroom/pkg/daemon"
	"github.com/fm39hz/gobroom/pkg/provider"
)

const benchmarkReply = "A deterministic local benchmark reply with bounded output."

type proxyBenchmarkFixture struct {
	upstreamURL string
	proxyURL    string
	client      *http.Client
	jsonBody    []byte
	sseBody     []byte
	ipcPath     string
}

func newProxyBenchmarkFixture(tb testing.TB) *proxyBenchmarkFixture {
	tb.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		var request struct {
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if request.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-bench\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":%q},\"finish_reason\":null}]}\n\n", benchmarkReply)
			_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-bench\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"chatcmpl-bench","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}]}`, benchmarkReply)
	}))
	tb.Cleanup(upstream.Close)

	root := tb.TempDir()
	runtime, err := provider.NewRuntimeRegistry()
	if err != nil {
		tb.Fatal(err)
	}
	definition, ok := runtime.Primitives.Definition(provider.ProviderDefinitionRef("openai-compatible-chat", 1))
	if !ok {
		tb.Fatal("builtin openai-compatible-chat definition missing")
	}
	config := daemon.Config{
		DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "daemon.sock"),
		HTTPEnabled: true, HTTPAddr: "127.0.0.1:0",
	}
	service := daemon.New(config)
	if err := service.Start(context.Background()); err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = service.Stop(context.Background()) })
	rpc := func(method string, params map[string]any) json.RawMessage {
		tb.Helper()
		response, err := daemon.CallIPC(context.Background(), config.IPCPath, daemon.IPCRequest{Method: method, Params: params})
		if err != nil || !response.OK {
			tb.Fatalf("IPC %s: response=%#v err=%v", method, response, err)
		}
		data, err := json.Marshal(response.Result)
		if err != nil {
			tb.Fatal(err)
		}
		return data
	}
	var node, connection struct{ ID string }
	if err := json.Unmarshal(rpc("providers.create", map[string]any{
		"name": "Benchmark local", "prefix": "bench", "baseUrl": upstream.URL + "/v1",
		"protocol": "openai_chat", "definitionID": definition.ID,
	}), &node); err != nil {
		tb.Fatal(err)
	}
	if err := json.Unmarshal(rpc("connections.create", map[string]any{
		"providerNodeID": node.ID, "name": "Benchmark local", "credentialType": "api_key", "secret": "benchmark-only",
	}), &connection); err != nil {
		tb.Fatal(err)
	}
	_ = rpc("custom_models.upsert", map[string]any{
		"id": "benchmark-route", "providerNodeID": node.ID, "kind": "custom", "externalID": "local-chat",
		"displayName": "Local benchmark", "connectionIds": []string{connection.ID},
	})
	_ = rpc("physical_models.upsert", map[string]any{
		"name": "benchmark-physical", "identity": map[string]any{"canonicalName": "local-chat"},
		"sources": []map[string]any{{"routeId": "benchmark-route", "fidelity": "exact"}}, "enabled": true,
	})
	_ = rpc("combo_models.upsert", map[string]any{
		"name": "benchmark-public", "members": []map[string]any{{"kind": "physical", "id": "benchmark-physical"}},
		"discoverable": true, "enabled": true,
	})
	address := service.Addresses().HTTP
	client := &http.Client{Transport: &http.Transport{MaxIdleConns: 32, MaxIdleConnsPerHost: 32}}
	tb.Cleanup(func() { client.CloseIdleConnections() })
	return &proxyBenchmarkFixture{
		ipcPath:     config.IPCPath,
		upstreamURL: upstream.URL + "/v1/chat/completions",
		proxyURL:    "http://" + address + "/v1/chat/completions", client: client,
		jsonBody: []byte(`{"model":"benchmark-public","messages":[{"role":"user","content":"Reply briefly."}],"max_tokens":16}`),
		sseBody:  []byte(`{"model":"benchmark-public","messages":[{"role":"user","content":"Reply briefly."}],"max_tokens":16,"stream":true}`),
	}
}

func (f *proxyBenchmarkFixture) request(tb testing.TB, target string, body []byte, stream bool) {
	tb.Helper()
	if err := f.doRequest(target, body, stream); err != nil {
		tb.Fatal(err)
	}
}

func TestProxyBenchmarkFixtureJSONSSEAndConcurrency(t *testing.T) {
	fixture := newProxyBenchmarkFixture(t)
	fixture.request(t, fixture.upstreamURL, fixture.jsonBody, false)
	fixture.request(t, fixture.upstreamURL, fixture.sseBody, true)
	fixture.request(t, fixture.proxyURL, fixture.jsonBody, false)
	fixture.request(t, fixture.proxyURL, fixture.sseBody, true)
	var wg sync.WaitGroup
	errCh := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body, stream := fixture.jsonBody, false
			if i%2 != 0 {
				body, stream = fixture.sseBody, true
			}
			if err := fixture.doRequest(fixture.proxyURL, body, stream); err != nil {
				errCh <- err
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	if cli := os.Getenv("GOBROOM_TEST_CLI"); cli != "" {
		runDeviceRaceCLI(t, cli, fixture.ipcPath, "status")
	}
}

func (f *proxyBenchmarkFixture) doRequest(target string, body []byte, stream bool) error {
	req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return fmt.Errorf("status=%d body=%s", resp.StatusCode, data)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if !stream {
		var result struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(data, &result); err != nil || len(result.Choices) != 1 || result.Choices[0].Message.Content != benchmarkReply {
			return fmt.Errorf("invalid JSON completion %q: %v", data, err)
		}
		return nil
	}
	var content strings.Builder
	done := false
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 1024), 64<<10)
	for scanner.Scan() {
		payload, found := strings.CutPrefix(scanner.Text(), "data: ")
		if !found {
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
		}
		if err := json.Unmarshal([]byte(payload), &frame); err != nil {
			return fmt.Errorf("invalid SSE frame: %w", err)
		}
		for _, choice := range frame.Choices {
			content.WriteString(choice.Delta.Content)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if !done || content.String() != benchmarkReply {
		return fmt.Errorf("incomplete SSE response %q", data)
	}
	return nil
}

func BenchmarkOpenAIChatProxy(b *testing.B) {
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.Cleanup(func() { slog.SetDefault(previousLogger) })
	fixture := newProxyBenchmarkFixture(b)
	for _, mode := range []struct {
		name   string
		body   []byte
		stream bool
	}{{"JSON", fixture.jsonBody, false}, {"SSE", fixture.sseBody, true}} {
		for _, target := range []struct{ name, url string }{{"Direct", fixture.upstreamURL}, {"Proxy", fixture.proxyURL}} {
			b.Run(mode.name+"/"+target.name, func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					fixture.request(b, target.url, mode.body, mode.stream)
				}
			})
			b.Run(mode.name+"/"+target.name+"Parallel", func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						if err := fixture.doRequest(target.url, mode.body, mode.stream); err != nil {
							b.Error(err)
							return
						}
					}
				})
			})
		}
	}
}
