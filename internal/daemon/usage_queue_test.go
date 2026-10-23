package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/store"
	"github.com/fm39hz/gobroom/internal/usage"
)

func TestDaemonAccountingQueueFailureDoesNotReplayInference(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"queue","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":2,"total_tokens":2}}`)
	}))
	defer upstream.Close()
	root := t.TempDir()
	d := New(Config{DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "daemon.sock"), HTTPEnabled: true, HTTPAddr: "127.0.0.1:0"})
	if err := d.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := d.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	rpc := func(method string, params map[string]any) json.RawMessage {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		response, err := CallIPC(ctx, d.config.IPCPath, IPCRequest{Method: method, Params: params})
		if err != nil || !response.OK {
			t.Fatalf("%s: %v / %s", method, err, response.Error)
		}
		data, err := json.Marshal(response.Result)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	var node store.ProviderNode
	if err := json.Unmarshal(rpc("providers.create", map[string]any{"name": "Queue fixture", "prefix": "queue", "baseUrl": upstream.URL + "/v1", "protocol": "openai_chat", "definitionID": "openai-compatible-chat"}), &node); err != nil {
		t.Fatal(err)
	}
	var connection store.ConnectionRecord
	if err := json.Unmarshal(rpc("connections.create", map[string]any{"providerNodeID": node.ID, "name": "Fixture account", "credentialType": "api_key", "secret": "fixture-key"}), &connection); err != nil {
		t.Fatal(err)
	}
	rpc("custom_models.upsert", map[string]any{"id": "queue-route", "providerNodeID": node.ID, "kind": "custom", "externalID": "fixture", "displayName": "Fixture", "connectionIds": []string{connection.ID}})
	rpc("physical_models.upsert", map[string]any{"name": "queue-physical", "identity": map[string]any{"canonicalName": "fixture"}, "sources": []map[string]any{{"routeId": "queue-route", "fidelity": "exact"}}, "enabled": true})
	rpc("combo_models.upsert", map[string]any{"name": "queue-role", "members": []map[string]any{{"kind": "physical", "id": "queue-physical"}}, "discoverable": true, "enabled": true})

	// Hold the actual SQLite writer, not a replacement kernel or a raced worker
	// field. WAL readers and inference remain usable while accounting blocks.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lock, err := d.store.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback()
	if !d.kernel.EmitUsage(kernel.UsageEvent{At: time.Now(), LogicalModel: "queue-blocker"}) {
		t.Fatal("first event not admitted")
	}
	wait := func(predicate func(usage.Status) bool) {
		t.Helper()
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for !predicate(d.usageWorker.Snapshot()) {
			select {
			case <-ctx.Done():
				t.Fatalf("accounting state did not arrive: %+v", d.usageWorker.Snapshot())
			case <-tick.C:
			}
		}
	}
	wait(func(s usage.Status) bool { return s.Received == 1 })
	for range cap(d.kernel.Events) {
		if !d.kernel.EmitUsage(kernel.UsageEvent{At: time.Now(), LogicalModel: "queue-filler"}) {
			t.Fatal("queue rejected before its configured capacity")
		}
	}
	if s := d.usageWorker.Snapshot(); s.Persisted != 0 || s.Failed != 0 || s.Abandoned != 0 {
		t.Fatalf("writer was not blocked: %+v", s)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Post("http://"+d.Addresses().HTTP+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"queue-role","messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || !bytes.Contains(data, []byte(`"content":"ok"`)) {
		t.Fatalf("queue pressure changed inference: status=%d body=%s err=%v", response.StatusCode, data, err)
	}
	if calls.Load() != 1 || d.kernel.DroppedUsage() != 1 {
		t.Fatalf("queue loss hidden or inference replayed: calls=%d dropped=%d", calls.Load(), d.kernel.DroppedUsage())
	}
	var status struct {
		Status string       `json:"status"`
		Usage  usage.Status `json:"usagePersistence"`
	}
	if err := json.Unmarshal(rpc("status", nil), &status); err != nil {
		t.Fatal(err)
	}
	if status.Status != "degraded" || status.Usage.QueueDropped != 1 || status.Usage.Received != 1 || status.Usage.Persisted != 0 || status.Usage.Failed != 0 {
		t.Fatalf("queue rejection not accurately exposed: %+v", status)
	}
	if err := lock.Rollback(); err != nil {
		t.Fatal(err)
	}
	accepted := uint64(cap(d.kernel.Events) + 1)
	wait(func(s usage.Status) bool { return s.Persisted == accepted })
	if err := json.Unmarshal(rpc("status", nil), &status); err != nil {
		t.Fatal(err)
	}
	if status.Status != "degraded" || status.Usage.QueueDropped != 1 || status.Usage.Persisted != accepted || status.Usage.Received != accepted || status.Usage.Failed != 0 || status.Usage.Abandoned != 0 {
		t.Fatalf("successful writes erased the earlier queue loss: %+v", status)
	}
	if err := d.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || d.kernel.DroppedUsage() != 1 {
		t.Fatal("draining accounting replayed inference or erased loss evidence")
	}
}
