package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/fm39hz/gobroom/internal/usage"
)

func TestDaemonUsageWriteFailureIsVisibleWithoutFailingInference(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"fixture","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
	}))
	defer upstream.Close()
	root := t.TempDir()
	config := Config{DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "run", "daemon.sock"), HTTPEnabled: true, HTTPAddr: "127.0.0.1:0", HTTPControl: true, HTTPControlAddr: "127.0.0.1:0"}
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
			t.Fatal(err)
		}
		data, err := json.Marshal(result.Result)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	var node store.ProviderNode
	if err := json.Unmarshal(rpc("providers.create", map[string]any{"name": "Usage fixture", "prefix": "usage-fixture", "baseUrl": upstream.URL + "/v1", "protocol": "openai_chat", "definitionID": "openai-compatible-chat"}), &node); err != nil {
		t.Fatal(err)
	}
	var connection store.ConnectionRecord
	if err := json.Unmarshal(rpc("connections.create", map[string]any{"providerNodeID": node.ID, "name": "Fixture account", "credentialType": "api_key", "secret": "fixture-key"}), &connection); err != nil {
		t.Fatal(err)
	}
	rpc("custom_models.upsert", map[string]any{"id": "usage-route", "providerNodeID": node.ID, "kind": "custom", "externalID": "fixture", "displayName": "Fixture", "connectionIds": []string{connection.ID}})
	rpc("physical_models.upsert", map[string]any{"name": "usage-physical", "identity": map[string]any{"canonicalName": "fixture"}, "sources": []map[string]any{{"routeId": "usage-route", "fidelity": "exact"}}, "enabled": true})
	rpc("combo_models.upsert", map[string]any{"name": "usage-role", "members": []map[string]any{{"kind": "physical", "id": "usage-physical"}}, "discoverable": true, "enabled": true})
	if _, err := d.store.DB.Exec(`CREATE TRIGGER reject_usage BEFORE INSERT ON usage_events BEGIN SELECT RAISE(FAIL,'private-usage-write-marker'); END`); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	call := func() {
		t.Helper()
		response, err := client.Post("http://"+d.Addresses().HTTP+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"usage-role","messages":[{"role":"user","content":"hello"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != http.StatusOK || !bytes.Contains(data, []byte(`"content":"ok"`)) {
			t.Fatalf("inference failed: status=%d data=%s err=%v", response.StatusCode, data, err)
		}
	}
	wait := func(predicate func(usage.Status) bool) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for !predicate(d.usageWorker.Snapshot()) {
			select {
			case <-ctx.Done():
				t.Fatalf("persistence did not reach expected state: %#v", d.usageWorker.Snapshot())
			case <-tick.C:
			}
		}
	}
	readStatus := func() []byte {
		t.Helper()
		if cli := os.Getenv("GOBROOM_TEST_CLI"); cli != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			data, err := exec.CommandContext(ctx, cli, "--ipc", config.IPCPath, "status").CombinedOutput()
			if err != nil {
				t.Fatalf("installed status: %v (%s)", err, data)
			}
			return data
		}
		return rpc("status", nil)
	}
	assertDegraded := func(data []byte, persisted uint64) {
		t.Helper()
		var status struct {
			Status string       `json:"status"`
			Usage  usage.Status `json:"usagePersistence"`
		}
		if err := json.Unmarshal(data, &status); err != nil {
			t.Fatal(err)
		}
		if status.Status != "degraded" || status.Usage.Failed != 1 || status.Usage.Persisted != persisted || bytes.Contains(data, []byte("private-usage-write-marker")) {
			t.Fatalf("unsafe/inaccurate status: %s", data)
		}
	}
	call()
	wait(func(s usage.Status) bool { return s.Failed == 1 })
	assertDegraded(readStatus(), 0)
	response, err := client.Get("http://" + d.Addresses().HTTPControl + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("HTTP status view: %d %v", response.StatusCode, err)
	}
	assertDegraded(data, 0)
	if _, err := d.store.DB.Exec(`DROP TRIGGER reject_usage`); err != nil {
		t.Fatal(err)
	}
	call()
	wait(func(s usage.Status) bool { return s.Persisted == 1 })
	// Completion of the next ordered write also proves the previous failure's
	// synchronous diagnostic callback has returned; no logging timing assumption.
	logs := rpc("logs.list", map[string]any{"limit": 100})
	if bytes.Contains(logs, []byte("private-usage-write-marker")) || !bytes.Contains(logs, []byte("usage persistence incomplete")) {
		t.Fatalf("unsafe/missing operational diagnostic: %s", logs)
	}
	assertDegraded(readStatus(), 1) // A later success does not erase an earlier lost write.
	if calls.Load() != 2 {
		t.Fatalf("persistence failure triggered provider retry: calls=%d", calls.Load())
	}
	rows, err := d.store.UsageEvents(10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("persisted usage: rows=%d err=%v", len(rows), err)
	}
	if err := d.Stop(context.Background()); !errors.Is(err, ErrUsagePersistenceIncomplete) || strings.Contains(err.Error(), "private-usage-write-marker") {
		t.Fatalf("unsafe/inaccurate stop result: %v", err)
	}
	if err := d.Stop(context.Background()); !errors.Is(err, ErrUsagePersistenceIncomplete) {
		t.Fatalf("stop lost retained persistence failure: %v", err)
	}
}
