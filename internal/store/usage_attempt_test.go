package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
)

func TestUsageAttemptIDsRoundTrip(t *testing.T) {
	s, err := Open(t.TempDir() + "/usage-attempt-roundtrip.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	event := kernel.UsageEvent{
		At:           time.Date(2026, time.October, 22, 10, 0, 0, 0, time.UTC),
		InvocationID: "invocation-host-1",
		AttemptID:    "attempt-host-1",
		InputTokens:  new(int64(0)),
	}
	if err := s.SaveUsageEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}

	rows, err := s.UsageEvents(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d usage events, want 1", len(rows))
	}
	if rows[0].InvocationID != event.InvocationID || rows[0].AttemptID != event.AttemptID {
		t.Fatalf("attempt identity did not round-trip: invocation=%q attempt=%q", rows[0].InvocationID, rows[0].AttemptID)
	}
	encoded, err := json.Marshal(rows[0])
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	var invocationID, attemptID string
	if err := json.Unmarshal(wire["invocationId"], &invocationID); err != nil {
		t.Fatalf("decode invocationId: %v", err)
	}
	if err := json.Unmarshal(wire["attemptId"], &attemptID); err != nil {
		t.Fatalf("decode attemptId: %v", err)
	}
	if invocationID != event.InvocationID || attemptID != event.AttemptID {
		t.Fatalf("JSON IDs mismatch: invocation=%q attempt=%q", invocationID, attemptID)
	}
}

func TestDuplicateUsageAttemptRejectsEventAndDailyAtomically(t *testing.T) {
	s, err := Open(t.TempDir() + "/usage-attempt-duplicate.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	at := time.Date(2026, time.October, 22, 10, 0, 0, 0, time.UTC)
	first := kernel.UsageEvent{At: at, InvocationID: "invocation-1", AttemptID: "attempt-duplicate", InputTokens: new(int64(3))}
	if err := s.SaveUsageEvent(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	duplicate := kernel.UsageEvent{At: at.Add(time.Minute), InvocationID: "invocation-2", AttemptID: first.AttemptID, OutputTokens: new(int64(9))}
	if err := s.SaveUsageEvent(context.Background(), duplicate); err == nil {
		t.Fatal("duplicate nonempty AttemptID was accepted")
	}
	assertUsageRowsUnchanged(t, s, 1, 1, 1)
}

func TestUsageAttemptsAllowDistinctAttemptsWithinInvocation(t *testing.T) {
	s, err := Open(t.TempDir() + "/usage-attempt-invocation.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	at := time.Date(2026, time.October, 22, 10, 0, 0, 0, time.UTC)
	for _, attemptID := range []string{"attempt-1", "attempt-2"} {
		if err := s.SaveUsageEvent(context.Background(), kernel.UsageEvent{
			At: at, InvocationID: "invocation-shared", AttemptID: attemptID,
		}); err != nil {
			t.Fatalf("save %s: %v", attemptID, err)
		}
	}
	rows, err := s.UsageEvents(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].InvocationID != "invocation-shared" || rows[1].InvocationID != "invocation-shared" || rows[0].AttemptID == rows[1].AttemptID {
		t.Fatalf("distinct attempts in one invocation were not retained: %#v", rows)
	}
	assertUsageRowsUnchanged(t, s, 2, 1, 2)
}

func TestUsageAttemptFailedAndCanceledTransactionsLeaveRowsUnchanged(t *testing.T) {
	s, err := Open(t.TempDir() + "/usage-attempt-rollback.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	baseline := kernel.UsageEvent{
		At:           time.Date(2026, time.October, 22, 10, 0, 0, 0, time.UTC),
		InvocationID: "invocation-base", AttemptID: "attempt-base",
	}
	if err := s.SaveUsageEvent(context.Background(), baseline); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`CREATE TRIGGER reject_attempt_daily BEFORE INSERT ON usage_daily BEGIN SELECT RAISE(FAIL,'fixture rejection'); END`); err != nil {
		t.Fatal(err)
	}
	failed := kernel.UsageEvent{
		At: baseline.At.Add(24 * time.Hour), InvocationID: "invocation-failed", AttemptID: "attempt-failed",
	}
	if err := s.SaveUsageEvent(context.Background(), failed); err == nil {
		t.Fatal("malformed daily transaction was accepted")
	}
	assertUsageRowsUnchanged(t, s, 1, 1, 1)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	canceled := kernel.UsageEvent{
		At: baseline.At.Add(48 * time.Hour), InvocationID: "invocation-canceled", AttemptID: "attempt-canceled",
	}
	if err := s.SaveUsageEvent(ctx, canceled); err == nil {
		t.Fatal("canceled attempt transaction was accepted")
	}
	assertUsageRowsUnchanged(t, s, 1, 1, 1)
}

func assertUsageRowsUnchanged(t *testing.T, s *Store, wantEvents, wantDays, wantAttempts int) {
	t.Helper()
	var events, days, attempts int
	if err := s.DB.QueryRow(`SELECT count(*) FROM usage_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT count(*) FROM usage_daily`).Scan(&days); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT COALESCE(sum(attempts),0) FROM usage_daily`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if events != wantEvents || days != wantDays || attempts != wantAttempts {
		t.Fatalf("usage transaction state=(events %d, days %d, attempts %d), want (%d, %d, %d)", events, days, attempts, wantEvents, wantDays, wantAttempts)
	}
}
