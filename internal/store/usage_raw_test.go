package store

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/usage"
)

func TestUsageRawCaptureIsOptInBoundedAndPrivate(t *testing.T) {
	const precise = `{"input_tokens":9007199254740993,"private_note":"private-marker"}`
	at := time.Date(2026, time.October, 22, 9, 30, 0, 0, time.UTC)

	t.Run("disabled capture preserves common record only", func(t *testing.T) {
		s := openUsageExtensionStore(t)
		event := rawUsageEvent(at, precise)
		if err := s.SaveUsageEvent(context.Background(), event); err != nil {
			t.Fatal(err)
		}
		if got := rawUsageRowCount(t, s); got != 0 {
			t.Fatalf("disabled capture persisted %d raw rows", got)
		}
		ordinary, err := s.UsageEvents(10)
		if err != nil || len(ordinary) != 1 || ordinary[0].InputTokens == nil || *ordinary[0].InputTokens != 7 || len(ordinary[0].AccountingIssues) == 0 {
			t.Fatalf("common event=(%d, %v), want one", len(ordinary), err)
		}
		encoded, err := json.Marshal(ordinary)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(encoded, []byte("private-marker")) || bytes.Contains(encoded, []byte("rawUsage")) {
			t.Fatalf("ordinary usage list exposed raw fragment: %s", encoded)
		}
	})

	t.Run("enabled capture is bounded, exact, and explicitly selected", func(t *testing.T) {
		s := openUsageExtensionStore(t)
		if err := s.SetUsageCapturePolicy(kernel.UsageCapturePolicy{Enabled: true, MaxBytes: 256}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetUsageCatalog(storeDisclosureUsageCatalog(t)); err != nil {
			t.Fatal(err)
		}
		event := rawUsageEvent(at, precise)
		event.Extensions = []extensions.UsageValue{
			{TypeRef: usageHistoryRef, Value: json.RawMessage(`"private-extension-marker"`)},
			{TypeRef: usageHistoryRef2, Value: json.RawMessage(`"public-extension-marker"`)},
		}
		if err := s.SaveUsageEvent(context.Background(), event); err != nil {
			t.Fatal(err)
		}
		if got := rawUsageRowCount(t, s); got != 1 {
			t.Fatalf("enabled capture persisted %d raw rows, want 1", got)
		}
		ordinary, err := s.UsageEvents(10)
		if err != nil || len(ordinary) != 1 {
			t.Fatalf("common events=(%d, %v)", len(ordinary), err)
		}
		encoded, err := json.Marshal(ordinary)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(encoded, []byte("private-marker")) || bytes.Contains(encoded, []byte("extension-marker")) || bytes.Contains(encoded, []byte("rawUsage")) {
			t.Fatalf("ordinary usage list exposed detail: %s", encoded)
		}
		common, err := s.UsageDetail(context.Background(), ordinary[0].ID, usage.DetailOptions{})
		if err != nil || len(common.Extensions) != 0 || common.RawUsage != nil {
			t.Fatalf("unrequested detail classes were loaded: detail=%#v err=%v", common, err)
		}
		public, err := s.UsageDetail(context.Background(), ordinary[0].ID, usage.DetailOptions{IncludeExtensions: true})
		if err != nil || len(public.Extensions) != 1 || string(public.Extensions[0].Value) != `"public-extension-marker"` || public.RawUsage != nil {
			t.Fatalf("public-only detail=%#v err=%v", public, err)
		}
		private, err := s.UsageDetail(context.Background(), ordinary[0].ID, usage.DetailOptions{IncludePrivate: true})
		if err != nil || len(private.Extensions) != 2 || private.RawUsage != nil {
			t.Fatalf("private-inclusive detail=%#v err=%v", private, err)
		}
		raw, err := s.UsageDetail(context.Background(), ordinary[0].ID, usage.DetailOptions{IncludeRaw: true})
		if err != nil || len(raw.Extensions) != 0 || raw.RawUsage == nil || string(raw.RawUsage.Value) != precise {
			t.Fatalf("raw-only detail=%#v err=%v", raw, err)
		}
		if !bytes.Contains(raw.RawUsage.Value, []byte("9007199254740993")) {
			t.Fatalf("raw number lost precision: %s", raw.RawUsage.Value)
		}
		all, err := s.UsageDetail(context.Background(), ordinary[0].ID, usage.DetailOptions{IncludePrivate: true, IncludeRaw: true})
		if err != nil || len(all.Extensions) != 2 || all.RawUsage == nil {
			t.Fatalf("combined explicit detail=%#v err=%v", all, err)
		}
	})

	t.Run("oversized and unsafe fragments are omitted whole", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			value json.RawMessage
		}{
			{name: "oversized", value: json.RawMessage(`{"value":"` + strings.Repeat("x", 300) + `"}`)},
			{name: "malformed", value: json.RawMessage(`{"value":`)},
		} {
			t.Run(tc.name, func(t *testing.T) {
				s := openUsageExtensionStore(t)
				if err := s.SetUsageCapturePolicy(kernel.UsageCapturePolicy{Enabled: true, MaxBytes: 128}); err != nil {
					t.Fatal(err)
				}
				event := kernel.UsageEvent{At: at, InputTokens: new(int64(7)), RawUsage: &kernel.RawUsageFragment{
					ProviderDefinition: extensions.Ref{Kind: "provider-definition", ID: "vendor.example.provider", ContractVersion: 1},
					API:                "chat_completions", Value: tc.value,
				}}
				if err := s.SaveUsageEvent(context.Background(), event); err != nil {
					t.Fatalf("unsafe optional fragment should not reject common usage: %v", err)
				}
				if rows, err := s.UsageEvents(10); err != nil || len(rows) != 1 || len(rows[0].AccountingIssues) == 0 {
					t.Fatalf("common event or sanitized issue missing: rows=%#v err=%v", rows, err)
				}
				if got := rawUsageRowCount(t, s); got != 0 {
					t.Fatalf("unsafe fragment was partially persisted (%d rows)", got)
				}
			})
		}
	})
}

func TestSetUsageCapturePolicyRequiresPositiveBound(t *testing.T) {
	s := openUsageExtensionStore(t)
	if err := s.SetUsageCapturePolicy(kernel.UsageCapturePolicy{Enabled: false}); err == nil {
		t.Fatal("capture policy with a zero byte limit was accepted")
	}
}

func TestPruneUsageAtomicallyRemovesRawExtensionsAndMetadataAndHonorsCancel(t *testing.T) {
	s := openUsageExtensionStore(t)
	if err := s.SetUsageCapturePolicy(kernel.UsageCapturePolicy{Enabled: true, MaxBytes: 256}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUsageCatalog(storeUsageCatalog(t, `{"type":"number"}`, "Retained metadata")); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, time.October, 20, 9, 30, 0, 0, time.UTC)
	event := rawUsageEvent(at, `{"input_tokens":4}`)
	event.Extensions = []extensions.UsageValue{{TypeRef: usageHistoryRef, Value: json.RawMessage(`4`)}}
	if err := s.SaveUsageEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.PruneUsage(ctx, at.Add(24*time.Hour)); err == nil {
		t.Fatal("canceled prune succeeded")
	}
	if got := rawUsageRowCount(t, s); got != 1 {
		t.Fatalf("canceled prune changed raw rows: %d", got)
	}
	if count, err := s.PruneUsage(context.Background(), at.Add(24*time.Hour)); err != nil || count != 1 {
		t.Fatalf("pruned (%d, %v), want one event", count, err)
	}
	for _, table := range []string{"usage_raw_fragments", "usage_extension_values", "usage_extension_descriptors", "usage_extension_schemas"} {
		var count int
		if err := s.DB.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("after prune %s rows=%d err=%v", table, count, err)
		}
	}
}

func rawUsageEvent(at time.Time, value string) kernel.UsageEvent {
	return kernel.UsageEvent{At: at, InputTokens: new(int64(7)), RawUsage: &kernel.RawUsageFragment{
		ProviderDefinition: extensions.Ref{Kind: "provider-definition", ID: "vendor.example.provider", ContractVersion: 1},
		API:                "chat_completions", Value: json.RawMessage(value),
	}}
}

func storeDisclosureUsageCatalog(t *testing.T) *extensions.Snapshot {
	t.Helper()
	catalog := extensions.NewCatalog()
	document, err := extensions.BindSchemaDocument(usageHistorySchemaRef, json.RawMessage(`{"type":"string"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.RegisterSchema(usageHistorySchemaRef, document); err != nil {
		t.Fatal(err)
	}
	for _, descriptor := range []extensions.Descriptor{
		{Ref: usageHistoryRef, ImplementationVersion: "1", DisplayName: "Private measurement", InputSchemaRef: &usageHistorySchemaRef,
			Description: "A private historical measurement",
			UsagePolicy: &extensions.UsagePolicy{Unit: "items", Disclosure: extensions.UsagePrivate, MaxValueBytes: 1024}},
		{Ref: usageHistoryRef2, ImplementationVersion: "1", DisplayName: "Public measurement", InputSchemaRef: &usageHistorySchemaRef,
			Description: "A management-visible historical measurement",
			UsagePolicy: &extensions.UsagePolicy{Unit: "items", Disclosure: extensions.UsageManagementVisible, MaxValueBytes: 1024}},
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

func rawUsageRowCount(t *testing.T, s *Store) int {
	t.Helper()
	var count int
	if err := s.DB.QueryRow(`SELECT count(*) FROM usage_raw_fragments`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
