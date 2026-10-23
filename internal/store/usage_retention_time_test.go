package store

import (
	"context"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
)

func TestUsageRetentionPreservesExactAndNewerNanosecondTimestamps(t *testing.T) {
	for _, mode := range []string{"full", "batch"} {
		t.Run(mode, func(t *testing.T) {
			for _, fixture := range []struct {
				name string
				year int
			}{
				{name: "current-year", year: 2026},
				{name: "year-one", year: 1},
			} {
				t.Run(fixture.name, func(t *testing.T) {
					s := openUsageExtensionStore(t)
					cutoff := time.Date(fixture.year, time.October, 23, 10, 0, 0, 0, time.UTC)
					instants := map[string]time.Time{
						"before":            cutoff.Add(-time.Nanosecond),
						"exact":             cutoff,
						"after-nanosecond":  cutoff.Add(time.Nanosecond),
						"after-half-second": cutoff.Add(500 * time.Millisecond),
					}
					// Insert future events first so pruning cannot rely on insertion
					// order as a substitute for comparing persisted event times.
					for _, label := range []string{"after-half-second", "after-nanosecond", "exact", "before"} {
						if err := s.SaveUsageEvent(context.Background(), kernel.UsageEvent{
							At: instants[label], AttemptID: label,
						}); err != nil {
							t.Fatalf("save %s: %v", label, err)
						}
						var seconds int64
						var nanos int
						if err := s.DB.QueryRow(`SELECT timestamp_seconds,timestamp_nanos FROM usage_events WHERE attempt_id=?`, label).Scan(&seconds, &nanos); err != nil {
							t.Fatal(err)
						}
						if seconds != instants[label].Unix() || nanos != instants[label].Nanosecond() {
							t.Fatalf("persisted instant %s=(%d, %d), want (%d, %d)", label, seconds, nanos, instants[label].Unix(), instants[label].Nanosecond())
						}
					}
					prune := func() (int64, error) {
						if mode == "batch" {
							return s.PruneUsageBatch(context.Background(), cutoff, 10)
						}
						return s.PruneUsage(context.Background(), cutoff)
					}
					if n, err := prune(); err != nil || n != 1 {
						t.Fatalf("prune at exact second=(%d, %v), want only the event 1ns before cutoff deleted", n, err)
					}
					rows, err := s.UsageEvents(10)
					if err != nil || len(rows) != 3 {
						t.Fatalf("surviving events=(%d, %v), want exact cutoff and both newer events", len(rows), err)
					}
					seen := make(map[string]bool)
					for _, row := range rows {
						want, ok := instants[row.AttemptID]
						if !ok || row.AttemptID == "before" || seen[row.AttemptID] {
							t.Fatalf("unexpected surviving event %q", row.AttemptID)
						}
						seen[row.AttemptID] = true
						if row.Timestamp != want.Format(time.RFC3339Nano) {
							t.Fatalf("display timestamp changed: got %q want %q", row.Timestamp, want.Format(time.RFC3339Nano))
						}
					}
					for _, label := range []string{"exact", "after-nanosecond", "after-half-second"} {
						if !seen[label] {
							t.Fatalf("event %s was incorrectly pruned", label)
						}
					}
					if n, err := prune(); err != nil || n != 0 {
						t.Fatalf("repeat prune=(%d, %v), want (0, nil)", n, err)
					}
				})
			}
		})
	}
}
