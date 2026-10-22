package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/store"
)

func TestDaemonUsagePresenceAcceptance(t *testing.T) {
	runUsagePresenceFixture(t, false)
}

// Keep the installed wrapper acceptance separately named so an unset optional
// CLI path cannot make the daemon and persistence acceptance look skipped.
func TestInstalledCLIUsagePresence(t *testing.T) {
	if os.Getenv("GOBROOM_TEST_CLI") == "" {
		t.Skip("GOBROOM_TEST_CLI is unset; daemon IPC acceptance runs independently")
	}
	runUsagePresenceFixture(t, true)
}

func runUsagePresenceFixture(t *testing.T, installedCLI bool) {
	t.Helper()
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var request map[string]any
		if json.Unmarshal(body, &request) != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		call := calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if call == 1 {
			_, _ = io.WriteString(w, `{"id":"fixture-explicit","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"fixture-omitted","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()

	root := t.TempDir()
	config := Config{DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "run", "daemon.sock"), HTTPEnabled: true, HTTPAddr: "127.0.0.1:0"}
	d := New(config)
	if err := d.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Stop(context.Background()) }()

	rpc := func(method string, params map[string]any) json.RawMessage {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		result, err := CallIPC(ctx, config.IPCPath, IPCRequest{Method: method, Params: params})
		if err != nil {
			t.Fatalf("IPC %s: %v", method, err)
		}
		data, err := json.Marshal(result.Result)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	var node store.ProviderNode
	if err := json.Unmarshal(rpc("providers.create", map[string]any{"name": "Presence fixture", "prefix": "presence-fixture", "baseUrl": upstream.URL + "/v1", "protocol": "openai_chat", "definitionID": "openai-compatible-chat"}), &node); err != nil {
		t.Fatal(err)
	}
	var connection store.ConnectionRecord
	if err := json.Unmarshal(rpc("connections.create", map[string]any{"providerNodeID": node.ID, "name": "Fixture account", "credentialType": "api_key", "secret": "presence-fixture-secret"}), &connection); err != nil {
		t.Fatal(err)
	}
	rpc("custom_models.upsert", map[string]any{"id": "presence-route", "providerNodeID": node.ID, "kind": "custom", "externalID": "fixture", "displayName": "Fixture", "connectionIds": []string{connection.ID}})
	rpc("physical_models.upsert", map[string]any{"name": "presence-physical", "identity": map[string]any{"canonicalName": "fixture"}, "sources": []map[string]any{{"routeId": "presence-route", "fidelity": "exact"}}, "enabled": true})
	rpc("combo_models.upsert", map[string]any{"name": "presence-role", "members": []map[string]any{{"kind": "physical", "id": "presence-physical"}}, "discoverable": true, "enabled": true})

	client := &http.Client{Timeout: 5 * time.Second}
	for range 2 {
		payload, _ := json.Marshal(map[string]any{"model": "presence-role", "messages": []map[string]string{{"role": "user", "content": "hello"}}})
		response, err := client.Post("http://"+d.Addresses().HTTP+"/v1/chat/completions", "application/json", bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		responseBody, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr != nil || response.StatusCode != http.StatusOK || !bytes.Contains(responseBody, []byte(`"content":"ok"`)) {
			t.Fatalf("inference response: status=%d body=%s err=%v", response.StatusCode, responseBody, readErr)
		}
	}
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelWait()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for d.usageWorker.Snapshot().Persisted < 2 {
		select {
		case <-waitCtx.Done():
			t.Fatalf("usage writes were not committed: %#v", d.usageWorker.Snapshot())
		case <-ticker.C:
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("upstream calls=%d, want 2", calls.Load())
	}

	var rows []store.UsageRecord
	if err := json.Unmarshal(rpc("usage.list", map[string]any{"limit": 10}), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("usage rows=%d, want 2", len(rows))
	}
	// Events are returned newest first; both usage counters must retain presence.
	if rows[0].InputTokens != nil || rows[0].OutputTokens != nil {
		t.Fatalf("omitted usage became known: %#v", rows[0])
	}
	if rows[1].InputTokens == nil || *rows[1].InputTokens != 0 || rows[1].OutputTokens == nil || *rows[1].OutputTokens != 0 {
		t.Fatalf("explicit zero was lost: %#v", rows[1])
	}
	var summaries []store.UsageSummary
	if err := json.Unmarshal(rpc("usage.summary", nil), &summaries); err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 {
		t.Fatalf("daily summaries=%d, want 1", len(summaries))
	}
	summary := summaries[0]
	if summary.Attempts != 2 || summary.Input.Sum != 0 || summary.Input.Reported != 1 || summary.Output.Sum != 0 || summary.Output.Reported != 1 {
		t.Fatalf("daily usage coverage is inaccurate: %#v", summary)
	}

	if installedCLI {
		for _, command := range []string{"status", "usage", "usage-summary"} {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			output, err := exec.CommandContext(ctx, os.Getenv("GOBROOM_TEST_CLI"), "--ipc", config.IPCPath, command).CombinedOutput()
			cancel()
			if err != nil {
				t.Fatalf("installed CLI %s: %v (%s)", command, err, output)
			}
			if bytes.Contains(output, []byte("0x")) || bytes.Contains(output, []byte("presence-fixture-secret")) {
				t.Fatalf("CLI %s leaked pointer or secret data: %s", command, output)
			}
			if command == "usage" && (!bytes.Contains(output, []byte(`"inputTokens":null`)) || !bytes.Contains(output, []byte(`"inputTokens":0`))) {
				t.Fatalf("CLI usage omitted presence distinction: %s", output)
			}
			if command == "usage-summary" && (!bytes.Contains(output, []byte(`"attempts":2`)) || !bytes.Contains(output, []byte(`"reported":1`))) {
				t.Fatalf("CLI summary lacks coverage: %s", output)
			}
		}
	}

	// IPC payloads themselves must be safe to display and must not expose Go
	// pointer formatting or the fixture credential.
	for _, output := range [][]byte{rpc("status", nil), rpc("usage.list", map[string]any{"limit": 10}), rpc("usage.summary", nil)} {
		if bytes.Contains(output, []byte("0x")) || strings.Contains(string(output), "presence-fixture-secret") {
			t.Fatalf("unsafe IPC payload: %s", output)
		}
	}
}
