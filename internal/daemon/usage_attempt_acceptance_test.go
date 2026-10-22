package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/store"
)

func TestDaemonAttemptUsageAcceptance(t *testing.T) {
	runAttemptUsageAcceptance(t, false)
}

// The installed wrapper is optional; its skip must not suppress daemon IPC
// coverage above.
func TestInstalledCLIAttemptUsage(t *testing.T) {
	if os.Getenv("GOBROOM_TEST_CLI") == "" {
		t.Skip("GOBROOM_TEST_CLI is unset; daemon IPC acceptance runs independently")
	}
	runAttemptUsageAcceptance(t, true)
}

func runAttemptUsageAcceptance(t *testing.T, installedCLI bool) {
	t.Helper()
	var rejectedCalls, acceptedCalls atomic.Int64
	rejected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		rejectedCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Usage-Input-Tokens", "12")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"retry with fallback"}}`)
	}))
	defer rejected.Close()
	accepted := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		acceptedCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"accepted-fixture","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
	}))
	defer accepted.Close()

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
	type sourceIdentity struct {
		providerID   string
		connectionID string
	}
	createSource := func(name, prefix, baseURL, routeID string) sourceIdentity {
		t.Helper()
		var node store.ProviderNode
		if err := json.Unmarshal(rpc("providers.create", map[string]any{"name": name, "prefix": prefix, "baseUrl": baseURL + "/v1", "protocol": "openai_chat", "definitionID": "openai-compatible-chat"}), &node); err != nil {
			t.Fatal(err)
		}
		var connection store.ConnectionRecord
		secret := prefix + "-fixture-secret"
		if err := json.Unmarshal(rpc("connections.create", map[string]any{"providerNodeID": node.ID, "name": name + " account", "credentialType": "api_key", "secret": secret}), &connection); err != nil {
			t.Fatal(err)
		}
		rpc("custom_models.upsert", map[string]any{"id": routeID, "providerNodeID": node.ID, "kind": "custom", "externalID": "fixture-model", "displayName": "Fixture model", "connectionIds": []string{connection.ID}})
		return sourceIdentity{providerID: node.ID, connectionID: connection.ID}
	}
	first := createSource("Rejected provider", "attempt-rejected", rejected.URL, "a-attempt-rejected-route")
	second := createSource("Accepted provider", "attempt-accepted", accepted.URL, "b-attempt-accepted-route")
	rpc("physical_models.upsert", map[string]any{"name": "attempt-fallback-physical", "identity": map[string]any{"canonicalName": "fixture-model"}, "sources": []map[string]any{{"routeId": "a-attempt-rejected-route", "fidelity": "exact"}, {"routeId": "b-attempt-accepted-route", "fidelity": "exact"}}, "enabled": true})
	rpc("combo_models.upsert", map[string]any{"name": "attempt-fallback-combo", "members": []map[string]any{{"kind": "physical", "id": "attempt-fallback-physical"}}, "discoverable": true, "enabled": true})

	payload, err := json.Marshal(map[string]any{"model": "attempt-fallback-combo", "messages": []map[string]string{{"role": "user", "content": "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Post("http://"+d.Addresses().HTTP+"/v1/chat/completions", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	responseBody, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK || !bytes.Contains(responseBody, []byte(`"content":"ok"`)) {
		t.Fatalf("inference response: status=%d body=%s err=%v", response.StatusCode, responseBody, readErr)
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
	if rejectedCalls.Load() != 1 || acceptedCalls.Load() != 1 {
		t.Fatalf("upstream calls: rejected=%d accepted=%d, want one each", rejectedCalls.Load(), acceptedCalls.Load())
	}

	var rows []store.UsageRecord
	if err := json.Unmarshal(rpc("usage.list", map[string]any{"limit": 10}), &rows); err != nil {
		t.Fatal(err)
	}
	assertAttemptUsageRows(t, rows, first, second)
	expectedIdentities := attemptUsageIdentities(rows)
	var summaries []store.UsageSummary
	if err := json.Unmarshal(rpc("usage.summary", nil), &summaries); err != nil {
		t.Fatal(err)
	}
	assertAttemptUsageSummary(t, summaries)

	if installedCLI {
		for _, command := range []string{"status", "usage", "usage-summary"} {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			output, err := exec.CommandContext(ctx, os.Getenv("GOBROOM_TEST_CLI"), "--ipc", config.IPCPath, command).CombinedOutput()
			cancel()
			if err != nil {
				t.Fatalf("installed CLI %s: %v (%s)", command, err, output)
			}
			if bytes.Contains(output, []byte("attempt-rejected-fixture-secret")) || bytes.Contains(output, []byte("attempt-accepted-fixture-secret")) {
				t.Fatalf("CLI %s leaked fixture credentials: %s", command, output)
			}
			switch command {
			case "status":
				var status struct {
					Status string `json:"status"`
				}
				if err := json.Unmarshal(output, &status); err != nil || status.Status != "ok" {
					t.Fatalf("CLI status not healthy: %s (decode error=%v)", output, err)
				}
			case "usage":
				var cliRows []store.UsageRecord
				if err := json.Unmarshal(output, &cliRows); err != nil {
					t.Fatalf("decode CLI usage: %v", err)
				}
				assertAttemptUsageRows(t, cliRows, first, second)
				if got := attemptUsageIdentities(cliRows); !maps.Equal(got, expectedIdentities) {
					t.Fatalf("CLI usage identities differ from daemon IPC: got=%v want=%v", got, expectedIdentities)
				}
			case "usage-summary":
				var cliSummaries []store.UsageSummary
				if err := json.Unmarshal(output, &cliSummaries); err != nil {
					t.Fatalf("decode CLI usage summary: %v", err)
				}
				assertAttemptUsageSummary(t, cliSummaries)
			}
		}
	}
	for _, output := range [][]byte{rpc("status", nil), rpc("usage.list", map[string]any{"limit": 10}), rpc("usage.summary", nil)} {
		if bytes.Contains(output, []byte("attempt-rejected-fixture-secret")) || bytes.Contains(output, []byte("attempt-accepted-fixture-secret")) {
			t.Fatalf("IPC output leaked fixture credentials: %s", output)
		}
	}
}

func assertAttemptUsageRows(t *testing.T, rows []store.UsageRecord, rejected, accepted struct {
	providerID   string
	connectionID string
}) {
	t.Helper()
	if len(rows) != 2 {
		t.Fatalf("usage rows=%d, want 2: %#v", len(rows), rows)
	}
	var rejection, success *store.UsageRecord
	for i := range rows {
		switch rows[i].Status {
		case "error":
			rejection = &rows[i]
		case "ok":
			success = &rows[i]
		default:
			t.Fatalf("unexpected attempt status: %#v", rows[i])
		}
	}
	if rejection == nil || success == nil {
		t.Fatalf("expected one failed and one successful usage row: %#v", rows)
	}
	if rejection.InvocationID == "" || rejection.InvocationID != success.InvocationID || rejection.AttemptID == "" || success.AttemptID == "" || rejection.AttemptID == success.AttemptID {
		t.Fatalf("attempt identities do not describe one invocation with two dispatches: %#v", rows)
	}
	if rejection.ProviderNodeID != rejected.providerID || rejection.ConnectionID != rejected.connectionID || success.ProviderNodeID != accepted.providerID || success.ConnectionID != accepted.connectionID {
		t.Fatalf("attempt provider/connection attribution is wrong: %#v", rows)
	}
	if rejection.InputTokens == nil || *rejection.InputTokens != 12 || rejection.OutputTokens != nil {
		t.Fatalf("rejected attempt usage must preserve input=12 and unknown output: %#v", rejection)
	}
	if success.InputTokens == nil || *success.InputTokens != 2 || success.OutputTokens == nil || *success.OutputTokens != 1 {
		t.Fatalf("accepted attempt usage is wrong: %#v", success)
	}
}

func assertAttemptUsageSummary(t *testing.T, summaries []store.UsageSummary) {
	t.Helper()
	if len(summaries) != 1 {
		t.Fatalf("daily summaries=%d, want 1: %#v", len(summaries), summaries)
	}
	summary := summaries[0]
	if summary.Attempts != 2 || summary.Input.Sum != 14 || summary.Input.Reported != 2 || summary.Output.Sum != 1 || summary.Output.Reported != 1 ||
		summary.CachedRead.Sum != 0 || summary.CachedRead.Reported != 0 || summary.CachedWrite.Sum != 0 || summary.CachedWrite.Reported != 0 ||
		summary.Reasoning.Sum != 0 || summary.Reasoning.Reported != 0 || summary.EstimatedCost.Sum != 0 || summary.EstimatedCost.Reported != 0 || summary.IncompleteAttempts != 0 {
		t.Fatalf("usage summary coverage is inaccurate: %#v", summary)
	}
}

func attemptUsageIdentities(rows []store.UsageRecord) map[string]string {
	identities := make(map[string]string, len(rows))
	for _, row := range rows {
		identities[row.AttemptID] = row.InvocationID
	}
	return identities
}
