package externalprovider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	"github.com/fm39hz/gobroom/pkg/daemon"
	"github.com/fm39hz/gobroom/pkg/provider"
)

const (
	searchDefinitionID = "fixture-search-units"
	searchSourceID     = "example.search-usage-source"
	searchUsageID      = "example.search-units"
	searchSchemaID     = "example.search-units.value"
	searchTransformID  = "example.search-usage-transform"
	searchPrivateMark  = "private-search-marker-89af"
	searchUnitsLexeme  = "9007199254740993.125"
	searchSchemaJSON   = `{"type":"object","properties":{"units":{"type":"number","minimum":0},"marker":{"type":"string","maxLength":128}},"required":["units","marker"],"additionalProperties":false}`
)

var (
	searchUsageRef  = provider.Ref{Kind: "usage-value", ID: searchUsageID, ContractVersion: 1}
	searchSchemaRef = provider.Ref{Kind: provider.SchemaKind, ID: searchSchemaID, ContractVersion: 1}
)

type searchUsageRowDTO struct {
	ID           int64  `json:"ID"`
	InputTokens  *int64 `json:"InputTokens"`
	OutputTokens *int64 `json:"OutputTokens"`
}

type searchUsageSummaryDTO struct {
	Attempts int64 `json:"attempts"`
	Input    struct {
		Sum      int64 `json:"sum"`
		Reported int64 `json:"reported"`
	} `json:"input"`
	Output struct {
		Sum      int64 `json:"sum"`
		Reported int64 `json:"reported"`
	} `json:"output"`
	EstimatedCost struct {
		Sum      float64 `json:"sum"`
		Reported int64   `json:"reported"`
	} `json:"estimatedCost"`
}

func decodeSearchUsageRows(t *testing.T, data []byte) []searchUsageRowDTO {
	t.Helper()
	var rows []searchUsageRowDTO
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatalf("decode usage rows: %v (%s)", err, data)
	}
	return rows
}

func assertSearchUsageRows(t *testing.T, rows []searchUsageRowDTO) {
	t.Helper()
	if len(rows) != 2 || rows[0].ID <= 0 || rows[1].ID <= 0 || rows[0].ID == rows[1].ID ||
		rows[0].InputTokens != nil || rows[0].OutputTokens != nil ||
		rows[1].InputTokens == nil || *rows[1].InputTokens != 0 ||
		rows[1].OutputTokens == nil || *rows[1].OutputTokens != 3 {
		t.Fatalf("usage rows lost unknown/zero/original values: %#v", rows)
	}
}

func assertSearchUsageSummary(t *testing.T, data []byte) {
	t.Helper()
	var summaries []searchUsageSummaryDTO
	if err := json.Unmarshal(data, &summaries); err != nil || len(summaries) != 1 {
		t.Fatalf("decode usage summary: rows=%#v err=%v data=%s", summaries, err, data)
	}
	got := summaries[0]
	if got.Attempts != 2 || got.Input.Sum != 0 || got.Input.Reported != 1 ||
		got.Output.Sum != 3 || got.Output.Reported != 1 ||
		got.EstimatedCost.Sum != 0 || got.EstimatedCost.Reported != 0 {
		t.Fatalf("usage summary lost presence coverage or inferred an extension charge: %#v", got)
	}
}

// SearchUsageModule is intentionally the only production fixture surface used
// here. Setup, expectations and decoded DTOs stay in this test file.
func TestExternalUsageExtensionSurvivesRestartAndModuleRemoval(t *testing.T) {
	runSearchUsageAcceptance(t, false, true)
}

func TestInstalledCLIUsagePreservesPresenceAndPrivacy(t *testing.T) {
	cli := os.Getenv("GOBROOM_TEST_CLI")
	if cli == "" {
		t.Skip("GOBROOM_TEST_CLI is required for installed-client acceptance")
	}
	runSearchUsageAcceptance(t, true, false)
}

func runSearchUsageAcceptance(t *testing.T, installedCLI, removeModule bool) {
	t.Helper()
	var upstreamCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		call := upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Search-Units", searchUnitsLexeme)
		w.Header().Set("X-Search-Marker", searchPrivateMark)
		if call == 1 {
			_, _ = io.WriteString(w, `{"id":"search-one","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"one"},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":3,"total_tokens":3}}`)
			return
		}
		if call == 2 {
			_, _ = io.WriteString(w, `{"id":"search-two","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"two"},"finish_reason":"stop"}]}`)
			return
		}
		http.Error(w, "unexpected extra inference", http.StatusInternalServerError)
	}))
	defer upstream.Close()

	root := t.TempDir()
	manifestDir := filepath.Join(root, "providers")
	if err := os.Mkdir(manifestDir, 0o700); err != nil {
		t.Fatal(err)
	}
	runtime, err := provider.NewRuntimeRegistryWithModules(SearchUsageModule{})
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := runtime.Primitives.Definition(provider.ProviderDefinitionRef("openai-compatible-chat", 1))
	if !ok {
		t.Fatal("OpenAI Chat provider definition is unavailable")
	}
	definition.ID = searchDefinitionID
	for operation, binding := range definition.Operations {
		if operation == provider.OperationChat {
			binding.UsageSource = provider.PrimitiveRef{Kind: provider.PrimitiveUsageSource, ID: searchSourceID, ContractVersion: 1}
			definition.Operations[operation] = binding
		}
	}
	manifest, err := provider.EncodeManifest(definition)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "search.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}

	policy := daemon.DefaultUsagePolicy()
	policy.AllowManagementDetails = true
	policy.AllowPrivateDetails = true
	config := daemon.Config{
		DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "run", "daemon.sock"),
		HTTPEnabled: true, HTTPAddr: "127.0.0.1:0", ProviderManifestDir: manifestDir, UsagePolicy: &policy,
	}
	newService := func(withModule bool) *daemon.Daemon {
		t.Helper()
		var service *daemon.Daemon
		if withModule {
			service = daemon.NewWithProviderModules(config, SearchUsageModule{})
		} else {
			service = daemon.New(config)
		}
		if err := service.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		return service
	}
	service := newService(true)
	t.Cleanup(func() {
		if service != nil {
			if err := service.Stop(context.Background()); err != nil {
				t.Error(err)
			}
		}
	})
	stop := func(d *daemon.Daemon) {
		t.Helper()
		if err := d.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		if service == d {
			service = nil
		}
	}

	rpc := func(method string, params map[string]any) json.RawMessage {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		response, err := daemon.CallIPC(ctx, config.IPCPath, daemon.IPCRequest{Method: method, Params: params})
		if err != nil || !response.OK {
			t.Fatalf("IPC %s: response=%#v err=%v", method, response, err)
		}
		encoded, err := json.Marshal(response.Result)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	var node, connection struct{ ID string }
	if err := json.Unmarshal(rpc("providers.create", map[string]any{
		"name": "Search usage fixture", "prefix": "search-usage", "baseUrl": upstream.URL + "/v1",
		"protocol": "openai_chat", "definitionID": searchDefinitionID,
	}), &node); err != nil || node.ID == "" {
		t.Fatalf("provider node=%#v err=%v", node, err)
	}
	if err := json.Unmarshal(rpc("connections.create", map[string]any{
		"providerNodeID": node.ID, "name": "Fixture account", "credentialType": "api_key", "secret": "search-usage-test-secret",
	}), &connection); err != nil || connection.ID == "" {
		t.Fatalf("connection=%#v err=%v", connection, err)
	}
	rpc("custom_models.upsert", map[string]any{"id": "search-usage-route", "providerNodeID": node.ID, "kind": "custom", "externalID": "fixture-model", "displayName": "Fixture", "connectionIds": []string{connection.ID}})
	rpc("physical_models.upsert", map[string]any{"name": "search-usage-physical", "identity": map[string]any{"canonicalName": "fixture-model"}, "sources": []map[string]any{{"routeId": "search-usage-route", "fidelity": "exact"}}, "enabled": true})
	rpc("combo_models.upsert", map[string]any{"name": "search-usage-combo", "members": []map[string]any{{"kind": "physical", "id": "search-usage-physical"}}, "discoverable": true, "enabled": true})
	rpc("transform_bindings.upsert", map[string]any{
		"id": "search-usage-output-adjustment", "transformRef": provider.Ref{Kind: "response_transform", ID: searchTransformID, ContractVersion: 1},
		"enabled": true, "scope": map[string]string{"kind": "model", "id": "search-usage-combo"},
		"failureMode": "fail_closed", "options": json.RawMessage(`{"outputAdjustment":100}`),
	})

	client := &http.Client{Timeout: 5 * time.Second}
	call := func() (int, []byte) {
		t.Helper()
		requestBody := []byte(`{"model":"search-usage-combo","messages":[{"role":"user","content":"count"}]}`)
		response, err := client.Post("http://"+service.Addresses().HTTP+"/v1/chat/completions", "application/json", bytes.NewReader(requestBody))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, body
	}
	status, firstResponse := call()
	if status != http.StatusOK {
		t.Fatalf("first inference status=%d body=%s", status, firstResponse)
	}
	var projected struct {
		Usage struct {
			PromptTokens     *int64 `json:"prompt_tokens"`
			CompletionTokens *int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(firstResponse, &projected); err != nil || projected.Usage.PromptTokens == nil || *projected.Usage.PromptTokens != 0 || projected.Usage.CompletionTokens == nil || *projected.Usage.CompletionTokens != 103 {
		t.Fatalf("client projection lost transformed/zero usage: %#v body=%s err=%v", projected, firstResponse, err)
	}
	status, secondResponse := call()
	if status != http.StatusOK || bytes.Contains(secondResponse, []byte(`"usage"`)) {
		t.Fatalf("client projection invented absent usage: status=%d body=%s", status, secondResponse)
	}

	var rows []searchUsageRowDTO
	decodeRows := func() {
		t.Helper()
		rows = decodeSearchUsageRows(t, rpc("usage.list", map[string]any{"limit": 10}))
	}
	deadline := time.Now().Add(5 * time.Second)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		decodeRows()
		if len(rows) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("usage writes did not arrive: rows=%#v", rows)
		}
		<-ticker.C
	}
	if upstreamCalls.Load() != 2 {
		t.Fatalf("upstream calls=%d, want two", upstreamCalls.Load())
	}
	// List order is newest first. The second attempt reported only the private
	// extension, so common counters remain unknown; the first reported zero and
	// the original output count before client response transformation.
	assertSearchUsageRows(t, rows)
	summaryData := rpc("usage.summary", nil)
	assertSearchUsageSummary(t, summaryData)
	for _, ordinary := range [][]byte{
		rpc("status", nil),
		rpc("usage.list", map[string]any{"limit": 10}),
		summaryData,
		rpc("config.export", nil),
		rpc("logs.list", map[string]any{"limit": 100}),
		firstResponse,
		secondResponse,
	} {
		if bytes.Contains(ordinary, []byte(searchPrivateMark)) || bytes.Contains(ordinary, []byte(searchUnitsLexeme)) || bytes.Contains(ordinary, []byte("search-usage-test-secret")) {
			t.Fatalf("ordinary response disclosed private extension data: %s", ordinary)
		}
	}

	eventID := rows[1].ID
	var detail struct {
		Event struct {
			ID           int64  `json:"ID"`
			InputTokens  *int64 `json:"InputTokens"`
			OutputTokens *int64 `json:"OutputTokens"`
		} `json:"event"`
		Extensions []struct {
			Descriptor struct {
				Ref            provider.Ref `json:"ref"`
				SchemaRef      provider.Ref `json:"schemaRef"`
				SchemaDigest   string       `json:"schemaDigest"`
				Label          string       `json:"label"`
				Description    string       `json:"description"`
				Unit           string       `json:"unit"`
				Classification string       `json:"classification"`
			} `json:"descriptor"`
			Value json.RawMessage `json:"value"`
		} `json:"extensions"`
	}
	decodeDetail := func() {
		t.Helper()
		if err := json.Unmarshal(rpc("usage.detail", map[string]any{"id": eventID, "includeExtensions": true, "includePrivate": true}), &detail); err != nil {
			t.Fatal(err)
		}
	}
	if removeModule {
		// Remove every active object that depends on the module before stopping:
		// the transform binding and the model/provider graph that uses its
		// module-bound provider definition/source.
		rpc("transform_bindings.delete", map[string]any{"id": "search-usage-output-adjustment"})
		rpc("combo_models.delete", map[string]any{"name": "search-usage-combo"})
		rpc("physical_models.delete", map[string]any{"name": "search-usage-physical"})
		rpc("custom_models.delete", map[string]any{"id": "search-usage-route"})
		rpc("connections.delete", map[string]any{"id": connection.ID})
		rpc("providers.delete", map[string]any{"id": node.ID})
		stop(service)
		if err := os.Remove(filepath.Join(manifestDir, "search.json")); err != nil {
			t.Fatalf("remove module-bound provider manifest after uninstalling active configuration: %v", err)
		}
		service = newService(false)
	}
	decodeDetail()
	if detail.Event.ID != eventID || detail.Event.InputTokens == nil || *detail.Event.InputTokens != 0 || detail.Event.OutputTokens == nil || *detail.Event.OutputTokens != 3 || len(detail.Extensions) != 1 {
		t.Fatalf("historical detail differs from original accounting: %#v", detail)
	}
	got := detail.Extensions[0]
	var schemaDocument map[string]any
	if err := json.Unmarshal([]byte(searchSchemaJSON), &schemaDocument); err != nil {
		t.Fatal(err)
	}
	schemaURI, err := searchSchemaRef.URI()
	if err != nil {
		t.Fatal(err)
	}
	schemaDocument["$id"] = schemaURI
	schemaDocument["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	canonicalSchema, err := json.Marshal(schemaDocument)
	if err != nil {
		t.Fatal(err)
	}
	wantDigest := sha256.Sum256(canonicalSchema)
	if got.Descriptor.Ref != searchUsageRef || got.Descriptor.SchemaRef != searchSchemaRef || got.Descriptor.SchemaDigest != hex.EncodeToString(wantDigest[:]) ||
		got.Descriptor.Label != "Search units" || got.Descriptor.Description != "Provider-reported search units; no automatic aggregation or pricing." ||
		got.Descriptor.Unit != "search" || got.Descriptor.Classification != "private" {
		t.Fatalf("historical descriptor or exact typed value changed: %#v value=%s", got.Descriptor, got.Value)
	}
	var value struct {
		Units  json.Number `json:"units"`
		Marker string      `json:"marker"`
	}
	decoder := json.NewDecoder(bytes.NewReader(got.Value))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil || value.Units.String() != searchUnitsLexeme || value.Marker != searchPrivateMark {
		t.Fatalf("historical typed value lost exact number/private field: %#v err=%v", value, err)
	}
	if installedCLI {
		for _, command := range []string{"status", "usage", "usage-summary"} {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			output, err := exec.CommandContext(ctx, os.Getenv("GOBROOM_TEST_CLI"), "--ipc", config.IPCPath, command).CombinedOutput()
			cancel()
			if err != nil {
				t.Fatalf("installed CLI %s: %v (%s)", command, err, output)
			}
			if bytes.Contains(output, []byte(searchPrivateMark)) || bytes.Contains(output, []byte(searchUnitsLexeme)) || bytes.Contains(output, []byte("search-usage-test-secret")) {
				t.Fatalf("installed CLI %s disclosed private extension data: %s", command, output)
			}
			switch command {
			case "status":
				var cliStatus struct {
					Status string `json:"status"`
				}
				if err := json.Unmarshal(output, &cliStatus); err != nil || cliStatus.Status != "ok" {
					t.Fatalf("installed CLI status=%s err=%v", output, err)
				}
			case "usage":
				assertSearchUsageRows(t, decodeSearchUsageRows(t, output))
			case "usage-summary":
				assertSearchUsageSummary(t, output)
			}
		}
	}
	stop(service)
}
