package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
)

func TestSaveUsageEventCanceledBeforeAdmissionDoesNotWrite(t *testing.T) {
	s, err := Open(t.TempDir() + "/usage-context.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = s.SaveUsageEvent(ctx, kernel.UsageEvent{
		At:          time.Date(2026, time.October, 21, 12, 0, 0, 0, time.UTC),
		InputTokens: 11, OutputTokens: 4, EstimatedCost: 0.75,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("SaveUsageEvent with canceled context=%v", err)
	}

	var events, daily int
	if err := s.DB.QueryRow(`SELECT count(*) FROM usage_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT count(*) FROM usage_daily`).Scan(&daily); err != nil {
		t.Fatal(err)
	}
	if events != 0 || daily != 0 {
		t.Fatalf("canceled write persisted data: events=%d daily=%d", events, daily)
	}
}
