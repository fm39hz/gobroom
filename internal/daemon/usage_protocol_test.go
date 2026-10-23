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
	"sync/atomic"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/adapter/usagewire"
	"github.com/fm39hz/gobroom/internal/store"
)

const acceptanceReportedCharge = "0.123456789012345678901"

func TestDaemonReportedChargeProtocolAcceptance(t *testing.T) {
	runReportedChargeProtocolAcceptance(t, false)
}

func TestInstalledCLIReportedChargeProtocolAcceptance(t *testing.T) {
	if os.Getenv("GOBROOM_TEST_CLI") == "" {
		t.Skip("GOBROOM_TEST_CLI is unset; daemon IPC acceptance runs independently")
	}
	runReportedChargeProtocolAcceptance(t, true)
}

func runReportedChargeProtocolAcceptance(t *testing.T, installedCLI bool) {
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
		var request struct {
			Stream        bool `json:"stream"`
			StreamOptions struct {
				IncludeUsage bool `json:"include_usage"`
			} `json:"stream_options"`
		}
		if json.Unmarshal(body, &request) != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		switch calls.Add(1) {
		case 1:
			if request.Stream {
				http.Error(w, "expected non-streaming request", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"charge-json","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10,"cost":`+acceptanceReportedCharge+`}}`)
		case 2:
			if !request.Stream || !request.StreamOptions.IncludeUsage {
				http.Error(w, "expected streaming request with usage", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"id\":\"charge-sse\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"id\":\"charge-sse\",\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":3,\"total_tokens\":10,\"cost\":0}}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		default:
			http.Error(w, "unexpected extra upstream call", http.StatusInternalServerError)
		}
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
		if !result.OK {
			t.Fatalf("IPC %s failed: %s", method, result.Error)
		}
		data, err := json.Marshal(result.Result)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	var node store.ProviderNode
	if err := json.Unmarshal(rpc("providers.create", map[string]any{"name": "Reported charge fixture", "prefix": "reported-charge", "baseUrl": upstream.URL + "/v1", "protocol": "openai_chat", "definitionID": "openai-compatible-chat"}), &node); err != nil {
		t.Fatal(err)
	}
	var connection store.ConnectionRecord
	if err := json.Unmarshal(rpc("connections.create", map[string]any{"providerNodeID": node.ID, "name": "Fixture account", "credentialType": "api_key", "secret": "reported-charge-fixture-secret"}), &connection); err != nil {
		t.Fatal(err)
	}
	rpc("custom_models.upsert", map[string]any{"id": "reported-charge-route", "providerNodeID": node.ID, "kind": "custom", "externalID": "fixture", "displayName": "Fixture", "connectionIds": []string{connection.ID}})
	rpc("physical_models.upsert", map[string]any{"name": "reported-charge-physical", "identity": map[string]any{"canonicalName": "fixture"}, "sources": []map[string]any{{"routeId": "reported-charge-route", "fidelity": "exact"}}, "enabled": true})
	rpc("combo_models.upsert", map[string]any{"name": "reported-charge-combo", "members": []map[string]any{{"kind": "physical", "id": "reported-charge-physical"}}, "discoverable": true, "enabled": true})

	client := &http.Client{Timeout: 5 * time.Second}
	for _, request := range []map[string]any{
		{"model": "reported-charge-combo", "messages": []map[string]string{{"role": "user", "content": "hello"}}},
		{"model": "reported-charge-combo", "messages": []map[string]string{{"role": "user", "content": "hello"}}, "stream": true, "stream_options": map[string]any{"include_usage": true}},
	} {
		payload, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
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
	if err := json.Unmarshal(rpc("usage.list", map[string]any{"limit": 10}), &rows); err != nil || len(rows) != 2 {
		t.Fatalf("usage rows=%d decode error=%v", len(rows), err)
	}
	amounts := make(map[string]int)
	for _, row := range rows {
		if row.InputTokens == nil || *row.InputTokens != 7 || row.OutputTokens == nil || *row.OutputTokens != 3 || row.EstimatedCost != nil {
			t.Fatalf("common usage row is incorrect or contains normalized reported cost: %#v", row)
		}
	}
	for _, row := range rows {
		detail, err := d.store.UsageDetail(context.Background(), row.ID, UsageDetailOptions{IncludeExtensions: true, IncludePrivate: true})
		if err != nil {
			t.Fatalf("usage detail for event %d: %v", row.ID, err)
		}
		if len(detail.Extensions) != 1 || detail.Extensions[0].Descriptor.Ref != usagewire.ReportedChargeRef() {
			t.Fatalf("event %d reported charge extension missing or unregistered: %#v", row.ID, detail.Extensions)
		}
		var charge usagewire.ReportedCharge
		if err := json.Unmarshal(detail.Extensions[0].Value, &charge); err != nil {
			t.Fatalf("decode reported charge for event %d: %v", row.ID, err)
		}
		var workload struct {
			Stream bool `json:"Stream"`
		}
		if err := json.Unmarshal([]byte(row.RequestClass), &workload); err != nil {
			t.Fatal(err)
		}
		wantAmount := acceptanceReportedCharge
		if workload.Stream {
			wantAmount = "0"
		}
		if charge.Amount != wantAmount || charge.Currency != nil || charge.Provenance.Source != "body" || charge.Provenance.Field != "/usage/cost" {
			t.Fatalf("event %d reported charge=%#v, want exact body amount and unknown currency", row.ID, charge)
		}
		amounts[charge.Amount]++
	}
	if amounts[acceptanceReportedCharge] != 1 || amounts["0"] != 1 {
		t.Fatalf("persisted reported charge amounts=%v, want one precise amount and one explicit zero", amounts)
	}

	var summaries []store.UsageSummary
	if err := json.Unmarshal(rpc("usage.summary", nil), &summaries); err != nil || len(summaries) != 1 {
		t.Fatalf("daily summaries=%d decode error=%v", len(summaries), err)
	}
	summary := summaries[0]
	if summary.Attempts != 2 || summary.Input.Sum != 14 || summary.Input.Reported != 2 || summary.Output.Sum != 6 || summary.Output.Reported != 2 || summary.EstimatedCost.Sum != 0 || summary.EstimatedCost.Reported != 0 {
		t.Fatalf("common usage summary is incorrect: %#v", summary)
	}

	outputs := [][]byte{rpc("status", nil), rpc("usage.list", map[string]any{"limit": 10}), rpc("usage.summary", nil)}
	for _, output := range outputs {
		if bytes.Contains(output, []byte("gobroom.usage.reported-charge")) || bytes.Contains(output, []byte(acceptanceReportedCharge)) || bytes.Contains(output, []byte("reported-charge-fixture-secret")) {
			t.Fatalf("ordinary IPC disclosed reported charge extension or credential: %s", output)
		}
	}
	if installedCLI {
		for _, command := range []string{"status", "usage", "usage-summary"} {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			output, err := exec.CommandContext(ctx, os.Getenv("GOBROOM_TEST_CLI"), "--ipc", config.IPCPath, command).CombinedOutput()
			cancel()
			if err != nil {
				t.Fatalf("installed CLI %s: %v (%s)", command, err, output)
			}
			if bytes.Contains(output, []byte("gobroom.usage.reported-charge")) || bytes.Contains(output, []byte(acceptanceReportedCharge)) || bytes.Contains(output, []byte("reported-charge-fixture-secret")) {
				t.Fatalf("installed CLI %s disclosed reported charge extension or credential: %s", command, output)
			}
			switch command {
			case "usage":
				var cliRows []store.UsageRecord
				if err := json.Unmarshal(output, &cliRows); err != nil || len(cliRows) != 2 {
					t.Fatalf("decode CLI usage (%d rows): %v", len(cliRows), err)
				}
				for _, row := range cliRows {
					if row.InputTokens == nil || *row.InputTokens != 7 || row.OutputTokens == nil || *row.OutputTokens != 3 || row.EstimatedCost != nil {
						t.Fatalf("CLI usage contains incorrect normalized values: %#v", row)
					}
				}
			case "usage-summary":
				var cliSummaries []store.UsageSummary
				if err := json.Unmarshal(output, &cliSummaries); err != nil || len(cliSummaries) != 1 || cliSummaries[0].Attempts != 2 || cliSummaries[0].Input.Sum != 14 || cliSummaries[0].Input.Reported != 2 || cliSummaries[0].Output.Sum != 6 || cliSummaries[0].Output.Reported != 2 || cliSummaries[0].EstimatedCost.Sum != 0 || cliSummaries[0].EstimatedCost.Reported != 0 {
					t.Fatalf("CLI usage summary incorrect: %s (decode error=%v)", output, err)
				}
			case "status":
				var status struct {
					Status string `json:"status"`
				}
				if err := json.Unmarshal(output, &status); err != nil || status.Status != "ok" {
					t.Fatalf("CLI status not healthy: %s (decode error=%v)", output, err)
				}
			}
		}
	}
}
