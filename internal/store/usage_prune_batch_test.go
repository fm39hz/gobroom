package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/usage"
)

func TestPruneUsageBatchDeletesOldestIDsWithinLimit(t *testing.T) {
	s, before, ids := usagePruneBatchFixture(t)
	options := usage.DetailOptions{IncludePrivate: true, IncludeRaw: true}

	if n, err := s.PruneUsageBatch(context.Background(), before, 2); err != nil || n != 2 {
		t.Fatalf("first batch=(%d, %v), want (2, nil)", n, err)
	}
	assertUsagePruneBatchState(t, s, 2, 2, 2)
	for _, id := range ids[:2] {
		if _, err := s.UsageDetail(context.Background(), id, options); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("deleted event %d detail error=%v, want sql.ErrNoRows", id, err)
		}
	}
	for _, id := range ids[2:] {
		detail, err := s.UsageDetail(context.Background(), id, options)
		if err != nil || detail.RawUsage == nil || len(detail.Extensions) != 1 {
			t.Fatalf("surviving event %d lost detail: detail=%#v err=%v", id, detail, err)
		}
	}
	// A partial batch must retain the original whole-day aggregate while any
	// event from that day remains; it must not expose a partially recomputed day.
	days, err := s.UsageSummary(10)
	if err != nil || len(days) != 2 {
		t.Fatalf("summary after partial batch=%#v err=%v", days, err)
	}
	for _, day := range days {
		if day.DateKey == "2026-10-20" && (day.Attempts != 3 || day.Input.Sum != 21 || day.Input.Reported != 3) {
			t.Fatalf("partial batch changed whole-day aggregate: %#v", day)
		}
	}

	if n, err := s.PruneUsageBatch(context.Background(), before, 2); err != nil || n != 1 {
		t.Fatalf("second batch=(%d, %v), want (1, nil)", n, err)
	}
	assertUsagePruneBatchState(t, s, 1, 1, 1)
	if _, err := s.UsageDetail(context.Background(), ids[2], options); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("last expired event detail error=%v, want sql.ErrNoRows", err)
	}
	detail, err := s.UsageDetail(context.Background(), ids[3], options)
	if err != nil || detail.RawUsage == nil || len(detail.Extensions) != 1 || detail.Extensions[0].Descriptor.Ref != usageHistoryRef2 {
		t.Fatalf("fresh event metadata/detail was pruned: detail=%#v err=%v", detail, err)
	}
	days, err = s.UsageSummary(10)
	if err != nil || len(days) != 1 || days[0].DateKey != "2026-10-21" || days[0].Attempts != 1 {
		t.Fatalf("finished expired day was not removed: days=%#v err=%v", days, err)
	}
	if n, err := s.PruneUsageBatch(context.Background(), before, 2); err != nil || n != 0 {
		t.Fatalf("drained batch=(%d, %v), want (0, nil)", n, err)
	}
	assertUsagePruneBatchState(t, s, 1, 1, 1)
}

func TestPruneUsageBatchRejectsInvalidLimitAndCancellationWithoutChanges(t *testing.T) {
	s, before, _ := usagePruneBatchFixture(t)
	for _, limit := range []int{0, -1} {
		if n, err := s.PruneUsageBatch(context.Background(), before, limit); err == nil || n != 0 {
			t.Fatalf("invalid limit %d returned (%d, %v)", limit, n, err)
		}
		assertUsagePruneBatchState(t, s, 4, 2, 2)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if n, err := s.PruneUsageBatch(ctx, before, 2); !errors.Is(err, context.Canceled) || n != 0 {
		t.Fatalf("canceled batch=(%d, %v), want (0, context.Canceled)", n, err)
	}
	assertUsagePruneBatchState(t, s, 4, 2, 2)
}

func TestPruneUsageBatchRollsBackCascadeWhenMetadataCleanupFails(t *testing.T) {
	s, before, ids := usagePruneBatchFixture(t)
	if _, err := s.DB.Exec(`CREATE TRIGGER reject_pruned_metadata BEFORE DELETE ON usage_extension_descriptors BEGIN SELECT RAISE(ABORT,'fixture metadata rejection'); END`); err != nil {
		t.Fatal(err)
	}
	// Removing all three old events makes their descriptor unreferenced. A
	// failure deleting that descriptor must roll back the event/raw cascade too.
	if n, err := s.PruneUsageBatch(context.Background(), before, 3); err == nil || n != 0 {
		t.Fatalf("failed cleanup batch=(%d, %v), want (0, error)", n, err)
	}
	assertUsagePruneBatchState(t, s, 4, 2, 2)
	for _, id := range ids {
		detail, err := s.UsageDetail(context.Background(), id, usage.DetailOptions{IncludePrivate: true, IncludeRaw: true})
		if err != nil || detail.RawUsage == nil || len(detail.Extensions) != 1 {
			t.Fatalf("failed batch changed event %d detail: detail=%#v err=%v", id, detail, err)
		}
	}
}

func usagePruneBatchFixture(t *testing.T) (*Store, time.Time, []int64) {
	t.Helper()
	s := openUsageExtensionStore(t)
	if err := s.SetUsageCapturePolicy(kernel.UsageCapturePolicy{Enabled: true, MaxBytes: 256}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUsageCatalog(storeUsageCatalog(t, `{"type":"number"}`, "Batch retention measurement")); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, time.October, 20, 9, 0, 0, 0, time.UTC)
	before := time.Date(2026, time.October, 21, 0, 0, 0, 0, time.UTC)
	for index := 0; index < 4; index++ {
		event := rawUsageEvent(at.Add(time.Duration(index)*time.Hour), `{"input_tokens":7}`)
		ref := usageHistoryRef
		if index == 3 {
			event.At = before // The exact cutoff must remain outside the batch.
			ref = usageHistoryRef2
		}
		event.Extensions = []extensions.UsageValue{{TypeRef: ref, Value: []byte(`7`)}}
		if err := s.SaveUsageEvent(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.DB.Query(`SELECT id FROM usage_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 4 {
		t.Fatalf("fixture has %d events, want 4", len(ids))
	}
	return s, before, ids
}

func assertUsagePruneBatchState(t *testing.T, s *Store, events, descriptors, days int) {
	t.Helper()
	for table, want := range map[string]int{
		"usage_events":                       events,
		"usage_raw_fragments":                events,
		"usage_extension_values":             events,
		"usage_extension_descriptors":        descriptors,
		"usage_extension_descriptor_schemas": descriptors,
		"usage_extension_schemas":            1,
		"usage_daily":                        days,
	} {
		var got int
		if err := s.DB.QueryRow(`SELECT count(*) FROM ` + table).Scan(&got); err != nil || got != want {
			t.Fatalf("%s count=(%d, %v), want %d", table, got, err, want)
		}
	}
	for name, query := range map[string]string{
		"raw orphan":              `SELECT count(*) FROM usage_raw_fragments r LEFT JOIN usage_events e ON e.id=r.event_id WHERE e.id IS NULL`,
		"extension orphan":        `SELECT count(*) FROM usage_extension_values v LEFT JOIN usage_events e ON e.id=v.event_id WHERE e.id IS NULL`,
		"unreferenced descriptor": `SELECT count(*) FROM usage_extension_descriptors d WHERE NOT EXISTS (SELECT 1 FROM usage_extension_values v WHERE v.descriptor_kind=d.kind AND v.descriptor_id=d.id AND v.descriptor_version=d.contract_version)`,
		"unreferenced schema":     `SELECT count(*) FROM usage_extension_schemas s WHERE NOT EXISTS (SELECT 1 FROM usage_extension_descriptor_schemas ds WHERE ds.schema_kind=s.kind AND ds.schema_id=s.id AND ds.schema_version=s.contract_version)`,
	} {
		var count int
		if err := s.DB.QueryRow(query).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count=(%d, %v), want 0", name, count, err)
		}
	}
}
