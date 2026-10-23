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
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/store"
)

const acceptanceRawUsageNumber = "9007199254740993"

func TestDaemonRawUsageCaptureAndDetailPolicy(t *testing.T) {
	runRawUsageCaptureAndDetailPolicy(t, false)
}

func TestInstalledCLIRawUsageCaptureAndDetailPolicy(t *testing.T) {
	if os.Getenv("GOBROOM_TEST_CLI") == "" {
		t.Skip("GOBROOM_TEST_CLI is unset; daemon IPC acceptance runs independently")
	}
	runRawUsageCaptureAndDetailPolicy(t, true)
}

func runRawUsageCaptureAndDetailPolicy(t *testing.T, installedCLI bool) {
	t.Helper()
	policies := []struct {
		name       string
		captureRaw bool
		grantRaw   bool
	}{
		{name: "raw off details denied"},
		{name: "raw on details denied", captureRaw: true},
		{name: "raw on detail grant", captureRaw: true, grantRaw: true},
	}
	for _, policyCase := range policies {
		t.Run(policyCase.name, func(t *testing.T) {
			var calls atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/chat/completions" {
					http.NotFound(w, r)
					return
				}
				if calls.Add(1) != 1 {
					http.Error(w, "unexpected replay", http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"raw-usage-fixture","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":`+acceptanceRawUsageNumber+`,"completion_tokens":7,"total_tokens":9007199254741000}}`)
			}))
			defer upstream.Close()

			root := t.TempDir()
			usagePolicy := DefaultUsagePolicy()
			usagePolicy.CaptureRaw = policyCase.captureRaw
			usagePolicy.AllowManagementDetails = policyCase.grantRaw
			usagePolicy.AllowRawDetails = policyCase.grantRaw
			config := Config{DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "run", "daemon.sock"), HTTPEnabled: true, HTTPAddr: "127.0.0.1:0", UsagePolicy: &usagePolicy}
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
				encoded, err := json.Marshal(result.Result)
				if err != nil {
					t.Fatal(err)
				}
				return encoded
			}
			var node store.ProviderNode
			if err := json.Unmarshal(rpc("providers.create", map[string]any{"name": "Raw usage fixture", "prefix": "raw-usage", "baseUrl": upstream.URL + "/v1", "protocol": "openai_chat", "definitionID": "openai-compatible-chat"}), &node); err != nil {
				t.Fatal(err)
			}
			var connection store.ConnectionRecord
			if err := json.Unmarshal(rpc("connections.create", map[string]any{"providerNodeID": node.ID, "name": "Fixture connection", "credentialType": "api_key", "secret": "temporary-raw-usage-secret"}), &connection); err != nil {
				t.Fatal(err)
			}
			rpc("custom_models.upsert", map[string]any{"id": "raw-usage-route", "providerNodeID": node.ID, "kind": "custom", "externalID": "fixture", "displayName": "Fixture", "connectionIds": []string{connection.ID}})
			rpc("physical_models.upsert", map[string]any{"name": "raw-usage-physical", "identity": map[string]any{"canonicalName": "fixture"}, "sources": []map[string]any{{"routeId": "raw-usage-route", "fidelity": "exact"}}, "enabled": true})
			rpc("combo_models.upsert", map[string]any{"name": "raw-usage-combo", "members": []map[string]any{{"kind": "physical", "id": "raw-usage-physical"}}, "discoverable": true, "enabled": true})

			requestBody := []byte(`{"model":"raw-usage-combo","messages":[{"role":"user","content":"hello"}]}`)
			response, err := (&http.Client{Timeout: 5 * time.Second}).Post("http://"+d.Addresses().HTTP+"/v1/chat/completions", "application/json", bytes.NewReader(requestBody))
			if err != nil {
				t.Fatal(err)
			}
			responseBody, readErr := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if readErr != nil || response.StatusCode != http.StatusOK || !bytes.Contains(responseBody, []byte(`"content":"ok"`)) {
				t.Fatalf("inference response: status=%d body=%s err=%v", response.StatusCode, responseBody, readErr)
			}
			waitCtx, cancelWait := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancelWait()
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			for d.usageWorker.Snapshot().Persisted < 1 {
				select {
				case <-waitCtx.Done():
					t.Fatalf("usage write was not committed: %#v", d.usageWorker.Snapshot())
				case <-ticker.C:
				}
			}
			if got := calls.Load(); got != 1 {
				t.Fatalf("upstream calls=%d, want exactly one", got)
			}

			var rows []store.UsageRecord
			if err := json.Unmarshal(rpc("usage.list", map[string]any{"limit": 10}), &rows); err != nil || len(rows) != 1 {
				t.Fatalf("usage rows=%d decode error=%v", len(rows), err)
			}
			row := rows[0]
			if row.InputTokens == nil || *row.InputTokens != 9007199254740993 || row.OutputTokens == nil || *row.OutputTokens != 7 {
				t.Fatalf("usage counters lost integer precision: %#v", row)
			}
			detail, err := d.store.UsageDetail(context.Background(), row.ID, UsageDetailOptions{IncludeRaw: true})
			if policyCase.captureRaw {
				if err != nil || detail.RawUsage == nil {
					t.Fatalf("captured raw usage missing: detail=%#v err=%v", detail, err)
				}
				var original struct {
					PromptTokens json.Number `json:"prompt_tokens"`
					Completion   json.Number `json:"completion_tokens"`
				}
				decoder := json.NewDecoder(bytes.NewReader(detail.RawUsage.Value))
				decoder.UseNumber()
				if err := decoder.Decode(&original); err != nil || original.PromptTokens.String() != acceptanceRawUsageNumber || original.Completion.String() != "7" {
					t.Fatalf("raw usage changed number precision: %s (decode error=%v)", detail.RawUsage.Value, err)
				}
			} else if err != nil || detail.RawUsage != nil {
				t.Fatalf("raw-off policy retained raw usage: detail=%#v err=%v", detail, err)
			}

			for _, method := range []string{"status", "usage.list", "usage.summary"} {
				var params map[string]any
				if method == "usage.list" {
					params = map[string]any{"limit": 10}
				}
				out := rpc(method, params)
				if bytes.Contains(out, []byte("rawUsage")) || bytes.Contains(out, []byte("temporary-raw-usage-secret")) {
					t.Fatalf("ordinary IPC %s disclosed raw data: %s", method, out)
				}
			}

			params := map[string]any{"id": row.ID, "includeRaw": true}
			denied := !policyCase.grantRaw
			rawDetail, err := CallIPC(context.Background(), config.IPCPath, IPCRequest{Method: "usage.detail", Params: params})
			if (err != nil) != denied || rawDetail.OK == denied {
				t.Fatalf("IPC raw detail authorization: response=%#v err=%v; denied=%v", rawDetail, err, denied)
			}
			if !denied {
				encoded, _ := json.Marshal(rawDetail.Result)
				if !bytes.Contains(encoded, []byte(`"rawUsage"`)) || !bytes.Contains(encoded, []byte(acceptanceRawUsageNumber)) {
					t.Fatalf("granted IPC detail omitted raw usage: %s", encoded)
				}
			}

			if installedCLI {
				for _, command := range [][]string{{"status"}, {"usage"}, {"usage-summary"}} {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					output, err := exec.CommandContext(ctx, os.Getenv("GOBROOM_TEST_CLI"), append([]string{"--ipc", config.IPCPath}, command...)...).CombinedOutput()
					cancel()
					if err != nil || bytes.Contains(output, []byte("rawUsage")) {
						t.Fatalf("installed CLI %v: err=%v output=%s", command, err, output)
					}
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				output, err := exec.CommandContext(ctx, os.Getenv("GOBROOM_TEST_CLI"), "--ipc", config.IPCPath, "usage", "detail", strconv.FormatInt(row.ID, 10), "--include-raw").CombinedOutput()
				cancel()
				if denied {
					if err == nil {
						t.Fatalf("installed CLI raw detail unexpectedly succeeded: %s", output)
					}
				} else if err != nil || !bytes.Contains(output, []byte(`"rawUsage"`)) || !bytes.Contains(output, []byte(acceptanceRawUsageNumber)) {
					t.Fatalf("installed CLI granted raw detail: err=%v output=%s", err, output)
				}
			}
		})
	}
}
