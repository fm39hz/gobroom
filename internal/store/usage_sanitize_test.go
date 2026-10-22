package store

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
)

func TestUsageSanitizeCommonInvalidFields(t *testing.T) {
	cases := []struct {
		name       string
		invalidate func(*kernel.UsageEvent)
		unknown    func(kernel.UsageEvent) bool
	}{
		{"negative_input", func(e *kernel.UsageEvent) { e.InputTokens = new(int64(-1)) }, func(e kernel.UsageEvent) bool { return e.InputTokens == nil }},
		{"negative_output", func(e *kernel.UsageEvent) { e.OutputTokens = new(int64(-1)) }, func(e kernel.UsageEvent) bool { return e.OutputTokens == nil }},
		{"negative_cached_read", func(e *kernel.UsageEvent) { e.CachedReadTokens = new(int64(-1)) }, func(e kernel.UsageEvent) bool { return e.CachedReadTokens == nil }},
		{"negative_cached_write", func(e *kernel.UsageEvent) { e.CachedWriteTokens = new(int64(-1)) }, func(e kernel.UsageEvent) bool { return e.CachedWriteTokens == nil }},
		{"negative_reasoning", func(e *kernel.UsageEvent) { e.ReasoningTokens = new(int64(-1)) }, func(e kernel.UsageEvent) bool { return e.ReasoningTokens == nil }},
		{"negative_cost", func(e *kernel.UsageEvent) { e.EstimatedCost = new(float64(-1)) }, func(e kernel.UsageEvent) bool { return e.EstimatedCost == nil }},
		{"nan_cost", func(e *kernel.UsageEvent) { e.EstimatedCost = new(math.NaN()) }, func(e kernel.UsageEvent) bool { return e.EstimatedCost == nil }},
		{"positive_infinite_cost", func(e *kernel.UsageEvent) { e.EstimatedCost = new(math.Inf(1)) }, func(e kernel.UsageEvent) bool { return e.EstimatedCost == nil }},
		{"negative_infinite_cost", func(e *kernel.UsageEvent) { e.EstimatedCost = new(math.Inf(-1)) }, func(e kernel.UsageEvent) bool { return e.EstimatedCost == nil }},
		{"negative_throughput", func(e *kernel.UsageEvent) { e.OutputTokensPerSecond = new(float64(-1)) }, func(e kernel.UsageEvent) bool { return e.OutputTokensPerSecond == nil }},
		{"nan_throughput", func(e *kernel.UsageEvent) { e.OutputTokensPerSecond = new(math.NaN()) }, func(e kernel.UsageEvent) bool { return e.OutputTokensPerSecond == nil }},
		{"positive_infinite_throughput", func(e *kernel.UsageEvent) { e.OutputTokensPerSecond = new(math.Inf(1)) }, func(e kernel.UsageEvent) bool { return e.OutputTokensPerSecond == nil }},
		{"negative_infinite_throughput", func(e *kernel.UsageEvent) { e.OutputTokensPerSecond = new(math.Inf(-1)) }, func(e kernel.UsageEvent) bool { return e.OutputTokensPerSecond == nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event := kernel.UsageEvent{
				InputTokens: new(int64(7)), OutputTokens: new(int64(9)),
				CachedReadTokens: new(int64(0)), CachedWriteTokens: new(int64(0)), ReasoningTokens: new(int64(0)),
				EstimatedCost: new(float64(0)), OutputTokensPerSecond: new(float64(0)),
			}
			tc.invalidate(&event)
			clean := event.SanitizeCommon()
			if !tc.unknown(clean) || clean.ValidateCommon() != nil {
				t.Fatal("invalid metric remained reported after sanitization")
			}
			if event.ValidateCommon() == nil {
				t.Fatal("sanitization mutated the caller's invalid metric")
			}
			if len(clean.AccountingIssues) != 1 || clean.AccountingIssues[0].Code != "invalid_usage_counter" {
				t.Fatalf("sanitization must retain one constant issue: %#v", clean.AccountingIssues)
			}
			if tc.name == "negative_output" {
				if clean.InputTokens == nil || *clean.InputTokens != 7 {
					t.Fatal("valid input sibling was discarded")
				}
			} else if clean.OutputTokens == nil || *clean.OutputTokens != 9 {
				t.Fatal("valid output sibling was discarded")
			}
		})
	}
}

func TestUsageSanitizeCommonOwnsSnapshotWithoutMutatingCaller(t *testing.T) {
	backing := []kernel.UsageIssue{{Code: "prior_issue"}, {Code: "caller_sentinel"}}
	event := kernel.UsageEvent{
		InputTokens: new(int64(-7)), OutputTokens: new(int64(9)), CachedReadTokens: new(int64(0)),
		EstimatedCost: new(math.NaN()), OutputTokensPerSecond: new(math.Inf(1)),
		AccountingIssues: backing[:1],
	}
	clean := event.SanitizeCommon()
	if clean.InputTokens != nil || clean.EstimatedCost != nil || clean.OutputTokensPerSecond != nil || clean.OutputTokens == nil || *clean.OutputTokens != 9 || clean.CachedReadTokens == nil || *clean.CachedReadTokens != 0 {
		t.Fatal("sanitization lost valid siblings or retained invalid metrics")
	}
	if len(clean.AccountingIssues) != 2 || clean.AccountingIssues[0].Code != "prior_issue" || clean.AccountingIssues[1].Code != "invalid_usage_counter" {
		t.Fatalf("prior and sanitized accounting evidence was not coalesced: %#v", clean.AccountingIssues)
	}
	if *event.InputTokens != -7 || !math.IsNaN(*event.EstimatedCost) || !math.IsInf(*event.OutputTokensPerSecond, 1) || len(event.AccountingIssues) != 1 || backing[1].Code != "caller_sentinel" {
		t.Fatal("sanitization mutated caller metrics or issue backing storage")
	}
	*clean.OutputTokens = 100
	*clean.CachedReadTokens = 100
	clean.AccountingIssues[0].Code = "changed"
	if *event.OutputTokens != 9 || *event.CachedReadTokens != 0 || event.AccountingIssues[0].Code != "prior_issue" {
		t.Fatal("sanitized snapshot aliases caller storage")
	}
	if clean.SanitizeCommon().ValidateCommon() != nil {
		t.Fatal("sanitizing an already sanitized event made it invalid")
	}
}

func TestSaveUsageEventSanitizesInvalidCommonSnapshot(t *testing.T) {
	s, err := Open(t.TempDir() + "/invalid-usage.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	event := kernel.UsageEvent{
		At:          time.Date(2026, time.October, 22, 0, 0, 0, 0, time.UTC),
		InputTokens: new(int64(-7)), OutputTokens: new(int64(9)),
		EstimatedCost: new(math.Inf(1)), OutputTokensPerSecond: new(math.NaN()),
	}
	if err := s.SaveUsageEvent(context.Background(), event); err != nil {
		t.Fatalf("optional invalid accounting must not reject the event: %v", err)
	}
	rows, err := s.UsageEvents(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("usage rows=%d, want 1", len(rows))
	}
	row := rows[0]
	if row.InputTokens != nil || row.EstimatedCost != nil || row.OutputTokensPerSecond != nil || row.OutputTokens == nil || *row.OutputTokens != 9 {
		t.Fatal("persistence reported invalid metrics or discarded valid siblings")
	}
	if len(row.AccountingIssues) != 1 || row.AccountingIssues[0].Code != "invalid_usage_counter" {
		t.Fatalf("missing constant accounting diagnostic: %#v", row.AccountingIssues)
	}
	days, err := s.UsageSummary(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 1 || days[0].Attempts != 1 || days[0].IncompleteAttempts != 1 || days[0].Input.Reported != 0 || days[0].Input.Sum != 0 || days[0].EstimatedCost.Reported != 0 || days[0].Output.Sum != 9 || days[0].Output.Reported != 1 {
		t.Fatalf("invalid snapshot polluted daily coverage: %#v", days)
	}
	if *event.InputTokens != -7 || !math.IsInf(*event.EstimatedCost, 1) || !math.IsNaN(*event.OutputTokensPerSecond) || len(event.AccountingIssues) != 0 {
		t.Fatal("persistence mutated the caller's usage snapshot")
	}
}

func TestUsageIncompleteAttemptsAreIndependentCases(t *testing.T) {
	cases := []struct {
		name       string
		event      kernel.UsageEvent
		incomplete int64
		reported   int64
		sum        int64
	}{
		{name: "wholly_missing_without_issue", event: kernel.UsageEvent{}, incomplete: 1},
		{name: "reported_with_issue", event: kernel.UsageEvent{InputTokens: new(int64(7)), AccountingIssues: []kernel.UsageIssue{{Code: "invalid_usage_counter"}}}, incomplete: 1, reported: 1, sum: 7},
		{name: "reported_clean", event: kernel.UsageEvent{InputTokens: new(int64(7))}, incomplete: 0, reported: 1, sum: 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Open(t.TempDir() + "/incomplete.db")
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			event := tc.event
			event.At = time.Date(2026, time.October, 22, 0, 0, 0, 0, time.UTC)
			if err := s.SaveUsageEvent(context.Background(), event); err != nil {
				t.Fatal(err)
			}
			days, err := s.UsageSummary(1)
			if err != nil {
				t.Fatal(err)
			}
			if len(days) != 1 || days[0].Attempts != 1 || days[0].IncompleteAttempts != tc.incomplete || days[0].Input.Reported != tc.reported || days[0].Input.Sum != tc.sum {
				t.Fatalf("unexpected incomplete flag or metric coverage: %#v", days)
			}
		})
	}
}
