package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/store"
)

func TestDaemonUsageHistoryWithoutModule(t *testing.T) {
	runUsageHistoryWithoutModule(t, false)
}

func TestInstalledCLIUsageHistoryWithoutModule(t *testing.T) {
	if os.Getenv("GOBROOM_TEST_CLI") == "" {
		t.Skip("GOBROOM_TEST_CLI is unset; daemon IPC acceptance runs independently")
	}
	runUsageHistoryWithoutModule(t, true)
}

func runUsageHistoryWithoutModule(t *testing.T, installedCLI bool) {
	t.Helper()
	const (
		usageID  = "vendor.example.private-meter"
		schemaID = "vendor.example.private-meter.value"
		marker   = "gobroom-private-history-marker-7f39"
		value    = `{"marker":"gobroom-private-history-marker-7f39","precise":9007199254740993.1250}`
	)
	usageRef := extensions.Ref{Kind: extensions.UsageKind, ID: usageID, ContractVersion: 1}
	schemaRef := extensions.Ref{Kind: extensions.SchemaKind, ID: schemaID, ContractVersion: 1}
	schemaDocument := json.RawMessage(`{"type":"object","properties":{"marker":{"type":"string"},"precise":{"type":"number"}},"required":["marker","precise"],"additionalProperties":false}`)
	descriptor := extensions.Descriptor{
		Ref: usageRef, ImplementationVersion: "17", DisplayName: "Private precise meter",
		Description:    "A private provider measurement retained for historical interpretation.",
		InputSchemaRef: &schemaRef,
		UsagePolicy:    &extensions.UsagePolicy{Unit: "vendor-units", Disclosure: extensions.UsagePrivate, MaxValueBytes: 1024},
	}
	module := provider.RuntimeModuleFunc{
		Name: "fixture.private-usage-history", Version: "1", HostContractVersion: provider.RuntimeModuleHostContractVersion,
		Init: func(runtime *provider.RuntimeRegistry) error {
			if err := runtime.RegisterSchemaDocument(schemaRef, schemaDocument); err != nil {
				return err
			}
			return runtime.RegisterExtension(descriptor, nil)
		},
	}

	catalog := extensions.NewCatalog()
	schema, err := extensions.BindSchemaDocument(schemaRef, schemaDocument)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.RegisterSchema(schemaRef, schema); err != nil {
		t.Fatal(err)
	}
	if err := catalog.RegisterDescriptor(descriptor); err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	wantDescriptor, ok := snapshot.UsageDescriptor(usageRef)
	if !ok {
		t.Fatal("fixture usage descriptor missing")
	}

	root := t.TempDir()
	config := Config{DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "run", "daemon.sock")}
	first := NewWithProviderModules(config, module)
	if err := first.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	zero := int64(0)
	event := kernel.UsageEvent{
		At:     time.Date(2026, time.October, 22, 9, 30, 0, 0, time.UTC),
		Status: "ok", InputTokens: &zero,
		Extensions: []extensions.UsageValue{{TypeRef: usageRef, Value: json.RawMessage(value)}},
	}
	if err := first.store.SaveUsageEvent(context.Background(), event); err != nil {
		_ = first.Stop(context.Background())
		t.Fatalf("save repository-boundary usage event: %v", err)
	}
	rows, err := first.store.UsageEvents(10)
	if err != nil || len(rows) != 1 {
		_ = first.Stop(context.Background())
		t.Fatalf("initial usage rows=(%d, %v), want one event", len(rows), err)
	}
	eventID := rows[0].ID
	if err := first.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The historical read must work after restarting with no module that knows
	// the descriptor or schema.
	restarted := New(config)
	if err := restarted.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Stop(context.Background()) })

	rpc := func(method string, params map[string]any) json.RawMessage {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		response, err := CallIPC(ctx, config.IPCPath, IPCRequest{Method: method, Params: params})
		if err != nil {
			t.Fatalf("IPC %s: %v", method, err)
		}
		if !response.OK {
			t.Fatalf("IPC %s failed: %s", method, response.Error)
		}
		encoded, err := json.Marshal(response.Result)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}

	statusData := rpc("status", nil)
	var status struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(statusData, &status); err != nil || status.Status != "ok" {
		t.Fatalf("restarted daemon status=%s (decode error=%v)", statusData, err)
	}
	usageData := rpc("usage.list", map[string]any{"limit": 10})
	var ordinary []store.UsageRecord
	if err := json.Unmarshal(usageData, &ordinary); err != nil || len(ordinary) != 1 {
		t.Fatalf("restarted ordinary usage=(%d rows, decode error=%v): %s", len(ordinary), err, usageData)
	}
	if ordinary[0].ID != eventID || ordinary[0].InputTokens == nil || *ordinary[0].InputTokens != 0 {
		t.Fatalf("ordinary usage lost event identity or known-zero input: %#v", ordinary[0])
	}
	summaryData := rpc("usage.summary", nil)
	var summaries []store.UsageSummary
	if err := json.Unmarshal(summaryData, &summaries); err != nil || len(summaries) != 1 {
		t.Fatalf("restarted usage summary=(%d rows, decode error=%v): %s", len(summaries), err, summaryData)
	}
	if summaries[0].Attempts != 1 || summaries[0].Input.Sum != 0 || summaries[0].Input.Reported != 1 {
		t.Fatalf("restarted common usage summary is wrong: %#v", summaries[0])
	}
	for _, output := range [][]byte{statusData, usageData, summaryData} {
		if bytes.Contains(output, []byte(marker)) {
			t.Fatalf("ordinary daemon response exposed a private usage value: %s", output)
		}
	}

	detail, err := restarted.store.UsageDetail(context.Background(), eventID, UsageDetailOptions{IncludeExtensions: true, IncludePrivate: true})
	if err != nil {
		t.Fatalf("repository historical detail without live catalog: %v", err)
	}
	if detail.Event.ID != eventID || len(detail.Extensions) != 1 {
		t.Fatalf("historical detail identity/value count=(%d, %d), want (%d, 1)", detail.Event.ID, len(detail.Extensions), eventID)
	}
	if got := detail.Extensions[0]; got.Descriptor != wantDescriptor || !bytes.Equal(got.Value, []byte(value)) {
		t.Fatalf("historical detail changed descriptor or exact JSON: descriptor=%#v value=%s", got.Descriptor, got.Value)
	}

	if installedCLI {
		for _, command := range []string{"status", "usage", "usage-summary"} {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			output, err := exec.CommandContext(ctx, os.Getenv("GOBROOM_TEST_CLI"), "--ipc", config.IPCPath, command).CombinedOutput()
			cancel()
			if err != nil {
				t.Fatalf("installed CLI %s: %v (%s)", command, err, output)
			}
			if bytes.Contains(output, []byte(marker)) {
				t.Fatalf("installed CLI %s exposed a private usage value: %s", command, output)
			}
			switch command {
			case "status":
				var cliStatus struct {
					Status string `json:"status"`
				}
				if err := json.Unmarshal(output, &cliStatus); err != nil || cliStatus.Status != "ok" {
					t.Fatalf("installed CLI status=%s (decode error=%v)", output, err)
				}
			case "usage":
				var cliRows []store.UsageRecord
				if err := json.Unmarshal(output, &cliRows); err != nil || len(cliRows) != 1 || cliRows[0].ID != eventID || cliRows[0].InputTokens == nil || *cliRows[0].InputTokens != 0 {
					t.Fatalf("installed CLI usage lost historical common data: rows=%#v decode error=%v", cliRows, err)
				}
			case "usage-summary":
				var cliSummaries []store.UsageSummary
				if err := json.Unmarshal(output, &cliSummaries); err != nil || len(cliSummaries) != 1 || cliSummaries[0].Attempts != 1 || cliSummaries[0].Input.Sum != 0 || cliSummaries[0].Input.Reported != 1 {
					t.Fatalf("installed CLI summary lost common coverage: summaries=%#v decode error=%v", cliSummaries, err)
				}
			}
		}
	}
}
