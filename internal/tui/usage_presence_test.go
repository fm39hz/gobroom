package tui

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestUsageEntryShowsIncompleteAccounting(t *testing.T) {
	entries, err := makeEntries("usage.list", json.RawMessage(`[{"ID":1,"LogicalModel":"role","Status":"ok","accountingIssues":[{"code":"invalid_input_tokens"}]}]`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !strings.Contains(entries[0].detail, "Accounting incomplete") || !strings.Contains(entries[0].detail, "invalid_input_tokens") {
		t.Fatalf("accounting fault was not visible: %#v", entries)
	}
}

func TestUsageRecordPresenceAndUnknownFormatting(t *testing.T) {
	raw := json.RawMessage(`[{"id":1,"logicalModel":"m","latencyMS":0,"inputTokens":0,"outputTokens":4,"estimatedCost":0,"ttftMS":0}]`)
	var values []usageRecord
	if err := json.Unmarshal(raw, &values); err != nil {
		t.Fatal(err)
	}
	value := values[0]
	if value.LatencyMS == nil || *value.LatencyMS != 0 || value.InputTokens == nil || *value.InputTokens != 0 || value.EstimatedCost == nil || *value.EstimatedCost != 0 {
		t.Fatalf("explicit zero lost presence: %#v", value)
	}
	if value.OutputTokens == nil || *value.OutputTokens != 4 || value.TTFTMS == nil || *value.TTFTMS != 0 {
		t.Fatalf("reported values lost presence: %#v", value)
	}
	if value.OutputTokensPerSecond != nil {
		t.Fatalf("absent throughput became present: %#v", value.OutputTokensPerSecond)
	}
	entries, err := makeEntries("usage.list", raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d usage rows", len(entries))
	}
	for _, expected := range []string{"Latency    0 ms", "TTFT       0 ms", "Throughput unknown", "Tokens     0 in / 4 out", "Cost       0.000000"} {
		if !strings.Contains(entries[0].detail, expected) {
			t.Errorf("usage detail missing %q:\n%s", expected, entries[0].detail)
		}
	}
	if strings.Contains(entries[0].detail, "0xc") {
		t.Fatalf("usage detail contains a pointer address: %s", entries[0].detail)
	}
}

func TestUsageRecordAbsentMeasurementsRemainUnknown(t *testing.T) {
	entries, err := makeEntries("usage.list", json.RawMessage(`[{"id":2,"logicalModel":"m"}]`), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"Latency    unknown", "TTFT       unknown", "Throughput unknown", "Tokens     unknown in / unknown out", "Cost       unknown"} {
		if !strings.Contains(entries[0].detail, expected) {
			t.Errorf("usage detail missing %q:\n%s", expected, entries[0].detail)
		}
	}
}
