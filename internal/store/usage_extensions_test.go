package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
)

var usageHistoryRef = extensions.Ref{Kind: extensions.UsageKind, ID: "vendor.example.precise", ContractVersion: 1}
var usageHistoryRef2 = extensions.Ref{Kind: extensions.UsageKind, ID: "vendor.example.other", ContractVersion: 1}
var usageHistorySchemaRef = extensions.Ref{Kind: extensions.SchemaKind, ID: "vendor.example.precise.value", ContractVersion: 1}

func storeUsageCatalog(t *testing.T, schema, label string) *extensions.Snapshot {
	t.Helper()
	catalog := extensions.NewCatalog()
	document, err := extensions.BindSchemaDocument(usageHistorySchemaRef, json.RawMessage(schema))
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.RegisterSchema(usageHistorySchemaRef, document); err != nil {
		t.Fatal(err)
	}
	for _, descriptor := range []extensions.Descriptor{
		{Ref: usageHistoryRef, ImplementationVersion: "7", DisplayName: label,
			Description: "Exact historical measurement", InputSchemaRef: &usageHistorySchemaRef,
			UsagePolicy: &extensions.UsagePolicy{Unit: "items", Disclosure: extensions.UsagePrivate, MaxValueBytes: 1024}},
		{Ref: usageHistoryRef2, ImplementationVersion: "2", DisplayName: "Second measurement",
			Description: "A separate bounded value", InputSchemaRef: &usageHistorySchemaRef,
			UsagePolicy: &extensions.UsagePolicy{Unit: "items", Disclosure: extensions.UsagePrivate, MaxValueBytes: 1024}},
	} {
		if err := catalog.RegisterDescriptor(descriptor); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := catalog.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func usageHistoryEvent(value string) kernel.UsageEvent {
	return kernel.UsageEvent{
		At:          time.Date(2026, time.October, 22, 9, 30, 0, 0, time.UTC),
		InputTokens: new(int64(0)),
		Extensions:  []extensions.UsageValue{{TypeRef: usageHistoryRef, Value: json.RawMessage(value)}},
	}
}

func TestUsageHistoryRetainsExactDescriptorWithoutModule(t *testing.T) {
	path := t.TempDir() + "/usage-history.db"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog := storeUsageCatalog(t, `{"type":"object","properties":{"value":{"type":"number"},"note":{"type":"string"}},"required":["value"],"additionalProperties":false}`, "Precise counter")
	wantDescriptor, ok := catalog.UsageDescriptor(usageHistoryRef)
	if !ok {
		t.Fatal("fixture descriptor missing")
	}
	if err := s.SetUsageCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	const precise = `{"value":9007199254740993.1250,"note":"kept"}`
	if err := s.SaveUsageEvent(context.Background(), usageHistoryEvent(precise)); err != nil {
		t.Fatal(err)
	}
	ordinary, err := s.UsageEvents(10)
	if err != nil || len(ordinary) != 1 {
		t.Fatalf("ordinary history=(%d, %v), want one event", len(ordinary), err)
	}
	encoded, err := json.Marshal(ordinary)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), precise) || strings.Contains(string(encoded), "Precise counter") || strings.Contains(string(encoded), "extensions") {
		t.Fatalf("ordinary usage list exposed private extension history: %s", encoded)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen without rebinding the catalog: history must use persisted data only.
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	detail, err := s.UsageDetail(context.Background(), ordinary[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Event.ID != ordinary[0].ID || len(detail.Extensions) != 1 {
		t.Fatalf("historical detail identity/values = (%d, %d), want (%d, 1)", detail.Event.ID, len(detail.Extensions), ordinary[0].ID)
	}
	got := detail.Extensions[0]
	if got.Descriptor != wantDescriptor {
		t.Fatalf("persisted descriptor changed after restart: %#v", got.Descriptor)
	}
	if string(got.Value) != precise {
		t.Fatalf("raw historical value = %s, want byte exact %s", got.Value, precise)
	}
}

func TestUsageExtensionDescriptorAndSchemaConflictsFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name, changedSchema, changedLabel string
	}{
		{name: "schema digest", changedSchema: `{"type":"string"}`, changedLabel: "Precise counter"},
		{name: "descriptor metadata", changedSchema: `{"type":"number"}`, changedLabel: "Renamed counter"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := t.TempDir() + "/conflict.db"
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.SetUsageCatalog(storeUsageCatalog(t, `{"type":"number"}`, "Precise counter")); err != nil {
				t.Fatal(err)
			}
			if err := s.SaveUsageEvent(context.Background(), usageHistoryEvent(`3`)); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err := s.SetUsageCatalog(storeUsageCatalog(t, tc.changedSchema, tc.changedLabel)); err != nil {
				t.Fatal(err)
			}
			secondValue := `4`
			if tc.name == "schema digest" {
				secondValue = `"still valid under the conflicting schema"`
			}
			if err := s.SaveUsageEvent(context.Background(), usageHistoryEvent(secondValue)); err == nil {
				t.Fatal("conflicting exact descriptor/schema metadata was accepted")
			}
			rows, err := s.UsageEvents(10)
			if err != nil || len(rows) != 1 {
				t.Fatalf("conflicting write changed ordinary history: rows=%d err=%v", len(rows), err)
			}
			days, err := s.UsageSummary(10)
			if err != nil || len(days) != 1 || days[0].Attempts != 1 {
				t.Fatalf("conflicting write changed summary: days=%#v err=%v", days, err)
			}
		})
	}
}

func TestUsageExtensionInvalidReferencesBytesAndCountsWriteNothing(t *testing.T) {
	t.Run("unregistered exact reference", func(t *testing.T) {
		s := openUsageExtensionStore(t)
		if err := s.SetUsageCatalog(storeUsageCatalog(t, `{"type":"number"}`, "Precise counter")); err != nil {
			t.Fatal(err)
		}
		event := usageHistoryEvent(`3`)
		event.Extensions[0].TypeRef.ContractVersion = 2
		assertInvalidUsageExtensionWritesNothing(t, s, event)
	})
	t.Run("host byte limit", func(t *testing.T) {
		s := openUsageExtensionStore(t)
		event := usageHistoryEvent(`"` + strings.Repeat("x", 220) + `"`)
		if err := s.SetUsageCatalog(storeUsageCatalog(t, `{"type":"string"}`, "Small value")); err != nil {
			t.Fatal(err)
		}
		if err := s.SetUsageLimits(kernel.UsageLimits{MaxValues: 32, MaxExtensionBytes: 256}); err != nil {
			t.Fatal(err)
		}
		assertInvalidUsageExtensionWritesNothing(t, s, event)
	})
	t.Run("host count limit", func(t *testing.T) {
		s := openUsageExtensionStore(t)
		if err := s.SetUsageCatalog(storeUsageCatalog(t, `{"type":"number"}`, "Precise counter")); err != nil {
			t.Fatal(err)
		}
		if err := s.SetUsageLimits(kernel.UsageLimits{MaxValues: 1, MaxExtensionBytes: 1024}); err != nil {
			t.Fatal(err)
		}
		event := usageHistoryEvent(`3`)
		event.Extensions = append(event.Extensions, extensions.UsageValue{TypeRef: usageHistoryRef2, Value: json.RawMessage(`4`)})
		assertInvalidUsageExtensionWritesNothing(t, s, event)
	})
}

func TestPrunedUsageReleasesDescriptorMetadataForLaterWrite(t *testing.T) {
	path := t.TempDir() + "/usage-prune.db"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetUsageCatalog(storeUsageCatalog(t, `{"type":"number"}`, "Before prune")); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveUsageEvent(context.Background(), usageHistoryEvent(`3`)); err != nil {
		t.Fatal(err)
	}
	if n, err := s.PruneUsage(time.Date(2026, time.October, 23, 0, 0, 0, 0, time.UTC)); err != nil || n != 1 {
		t.Fatalf("PruneUsage=(%d, %v), want one removed event", n, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// A different immutable snapshot for the same identity is valid once the
	// old history is gone; stale unreferenced metadata must have been removed.
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SetUsageCatalog(storeUsageCatalog(t, `{"type":"string"}`, "After prune")); err != nil {
		t.Fatal(err)
	}
	event := usageHistoryEvent(`"new"`)
	event.At = time.Date(2026, time.October, 24, 9, 30, 0, 0, time.UTC)
	if err := s.SaveUsageEvent(context.Background(), event); err != nil {
		t.Fatalf("write after pruning orphan metadata: %v", err)
	}
	rows, err := s.UsageEvents(10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("history after recreation: rows=%d err=%v", len(rows), err)
	}
	detail, err := s.UsageDetail(context.Background(), rows[0].ID)
	if err != nil || len(detail.Extensions) != 1 || detail.Extensions[0].Descriptor.Label != "After prune" || string(detail.Extensions[0].Value) != `"new"` {
		t.Fatalf("recreated descriptor/value mismatch: detail=%#v err=%v", detail, err)
	}
}

func openUsageExtensionStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "/usage-extensions.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func assertInvalidUsageExtensionWritesNothing(t *testing.T, s *Store, event kernel.UsageEvent) {
	t.Helper()
	if err := s.SaveUsageEvent(context.Background(), event); err == nil {
		t.Fatal("invalid extension input was persisted")
	}
	rows, err := s.UsageEvents(10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("invalid extension wrote common event: rows=%d err=%v", len(rows), err)
	}
	days, err := s.UsageSummary(10)
	if err != nil || len(days) != 0 {
		t.Fatalf("invalid extension changed summary: days=%#v err=%v", days, err)
	}
}
