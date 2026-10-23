package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
)

func TestUsageSummaryReportsPartialCoverage(t *testing.T) {
	s := openUsageExtensionStore(t)
	if err := s.SetUsageCatalog(storeUsageCatalog(t, `{"type":"number"}`, "Metric")); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, time.October, 22, 0, 0, 0, 0, time.UTC)
	knownZero := usageHistoryEvent(`9007199254740993.125`)
	knownZero.At = at
	unknown := kernel.UsageEvent{At: at.Add(time.Hour)}
	for _, event := range []kernel.UsageEvent{knownZero, unknown} {
		if err := s.SaveUsageEvent(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	days, err := s.UsageSummary(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 1 {
		t.Fatalf("summary days=%d, want 1", len(days))
	}
	got := days[0]
	if got.Attempts != 2 || got.Input.Reported != 1 || got.Input.Sum != 0 {
		t.Fatalf("unknown counted as reported zero or extension value changed common sum: %#v", got)
	}
	if got.IncompleteAttempts != 1 {
		t.Fatalf("incomplete attempts=%d, want 1", got.IncompleteAttempts)
	}
}

func TestUsageTransactionRollsBackDetailsAndSummary(t *testing.T) {
	s := openUsageExtensionStore(t)
	if err := s.SetUsageCatalog(storeUsageCatalog(t, `{"type":"number"}`, "Metric")); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, time.October, 22, 0, 0, 0, 0, time.UTC)
	baseline := usageHistoryEvent(`1`)
	baseline.At = at
	if err := s.SaveUsageEvent(context.Background(), baseline); err != nil {
		t.Fatal(err)
	}
	baselineRows, err := s.UsageEvents(10)
	if err != nil || len(baselineRows) != 1 {
		t.Fatalf("baseline history rows=%d err=%v", len(baselineRows), err)
	}
	baselineDetail, err := s.UsageDetail(context.Background(), baselineRows[0].ID)
	if err != nil || len(baselineDetail.Extensions) != 1 || string(baselineDetail.Extensions[0].Value) != `1` {
		t.Fatalf("baseline detail=%#v err=%v", baselineDetail, err)
	}

	if _, err := s.DB.Exec(`CREATE TRIGGER reject_usage_summary BEFORE INSERT ON usage_daily BEGIN SELECT RAISE(FAIL,'fixture rejection'); END`); err != nil {
		t.Fatal(err)
	}
	failed := usageHistoryEvent(`2`)
	failed.At = at.Add(24 * time.Hour)
	if err := s.SaveUsageEvent(context.Background(), failed); err == nil {
		t.Fatal("summary failure did not reject the usage transaction")
	}
	assertUsageHistoryAndSummaryUnchanged(t, s, 1, 1)
	if _, err := s.DB.Exec(`DROP TRIGGER reject_usage_summary`); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	canceled := usageHistoryEvent(`3`)
	canceled.At = at.Add(48 * time.Hour)
	if err := s.SaveUsageEvent(ctx, canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled usage write error=%v, want context.Canceled", err)
	}
	assertUsageHistoryAndSummaryUnchanged(t, s, 1, 1)
}

func assertUsageHistoryAndSummaryUnchanged(t *testing.T, s *Store, wantEvents, wantAttempts int) {
	t.Helper()
	events, err := s.UsageEvents(10)
	if err != nil || len(events) != wantEvents {
		t.Fatalf("history rows=%d err=%v, want %d", len(events), err, wantEvents)
	}
	for _, event := range events {
		detail, err := s.UsageDetail(context.Background(), event.ID)
		if err != nil || len(detail.Extensions) != 1 || string(detail.Extensions[0].Value) != `1` {
			t.Fatalf("event %d has missing or uncommitted detail: %#v err=%v", event.ID, detail, err)
		}
	}
	days, err := s.UsageSummary(10)
	if err != nil || len(days) != 1 || days[0].Attempts != int64(wantAttempts) || days[0].Input.Sum != 0 || days[0].Input.Reported != 1 {
		t.Fatalf("daily summary changed after rejected write: %#v err=%v", days, err)
	}
}
