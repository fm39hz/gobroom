package kernel

import (
	"math"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
)

func TestUsagePresenceMergeAndClone(t *testing.T) {
	zero := new(int64(0))
	seven := new(int64(7))
	nine := new(int64(9))
	current := UsageEvent{OutputTokens: nine}
	update := UsageEvent{InputTokens: zero}
	got := MergeProviderUsage(current, update)
	if got.InputTokens == nil || *got.InputTokens != 0 || got.OutputTokens == nil || *got.OutputTokens != 9 {
		t.Fatal("lost zero correction or prior output")
	}
	if current.InputTokens != nil || *current.OutputTokens != 9 {
		t.Fatal("merge mutated its input snapshot")
	}

	if (UsageEvent{}).HasReportedUsage() {
		t.Fatal("nil counters must remain unknown")
	}
	if !update.HasReportedUsage() {
		t.Fatal("reported zero must count as reported usage")
	}

	merged := MergeProviderUsage(UsageEvent{InputTokens: seven, OutputTokens: nine}, UsageEvent{InputTokens: zero})
	if merged.InputTokens == nil || *merged.InputTokens != 0 || merged.OutputTokens == nil || *merged.OutputTokens != 9 {
		t.Fatal("zero update did not replace prior input while preserving absent output")
	}

	extensionOnly := UsageEvent{Extensions: []extensions.UsageValue{{
		TypeRef: extensions.Ref{Kind: extensions.UsageKind, ID: "fixture", ContractVersion: 1},
		Value:   []byte(`{"count":0}`),
	}}}
	if !extensionOnly.HasReportedUsage() {
		t.Fatal("extension-only usage must count as reported usage")
	}

	cacheRead := new(int64(3))
	cacheWrite := new(int64(4))
	reasoning := new(int64(5))
	current = UsageEvent{
		InputTokens: new(int64(1)), OutputTokens: new(int64(2)),
		CachedReadTokens: cacheRead, CachedWriteTokens: cacheWrite, ReasoningTokens: reasoning,
		Extensions:          extensionOnly.Extensions,
		AccountingIssues:    []UsageIssue{{Code: "bad_value", TypeRef: extensionOnly.Extensions[0].TypeRef}},
		CompatibilityLosses: []LossRecord{{ID: "loss", Requested: map[string]any{"nested": []any{"original"}}, SemanticPaths: []string{"original"}}},
		CompatibilityPlan:   &CompatibilityPlanSummary{Mappings: []CompatibilityMappingSummary{{Paths: []string{"original"}}}, RequestTransformSteps: []RequestTransformPlanStep{{Effects: []TransformEffect{TransformInput}}}},
	}
	merged = MergeProviderUsage(current, UsageEvent{})
	merged.InputTokens = new(int64(70))
	merged.Extensions[0].Value[2] = 'X'
	merged.AccountingIssues[0].Code = "changed"
	merged.CompatibilityLosses[0].Requested.(map[string]any)["nested"].([]any)[0] = "changed"
	merged.CompatibilityPlan.Mappings[0].Paths[0] = "changed"
	merged.CompatibilityPlan.RequestTransformSteps[0].Effects[0] = TransformTools
	clone := current.Clone()
	clone.CachedReadTokens = new(int64(30))
	clone.Extensions[0].Value[2] = 'Y'
	clone.AccountingIssues[0].TypeRef.ID = "changed"
	clone.CompatibilityLosses[0].SemanticPaths[0] = "changed"
	clone.CompatibilityPlan.Mappings[0].Paths[0] = "changed"
	clone.CompatibilityPlan.RequestTransformSteps[0].Effects[0] = TransformTools
	if *current.InputTokens != 1 || *current.CachedReadTokens != 3 || *current.CachedWriteTokens != 4 || *current.ReasoningTokens != 5 ||
		string(current.Extensions[0].Value) != `{"count":0}` || current.AccountingIssues[0].Code != "bad_value" ||
		current.CompatibilityLosses[0].Requested.(map[string]any)["nested"].([]any)[0] != "original" ||
		current.CompatibilityLosses[0].SemanticPaths[0] != "original" || current.CompatibilityPlan.Mappings[0].Paths[0] != "original" ||
		current.CompatibilityPlan.RequestTransformSteps[0].Effects[0] != TransformInput {
		t.Fatal("merge or clone shared nested usage state with its source")
	}

	for name, event := range map[string]UsageEvent{
		"negative input":       {InputTokens: new(int64(-1))},
		"negative cache read":  {CachedReadTokens: new(int64(-1))},
		"negative cache write": {CachedWriteTokens: new(int64(-1))},
		"negative reasoning":   {ReasoningTokens: new(int64(-1))},
		"negative output":      {OutputTokens: new(int64(-1))},
		"nonfinite cost":       {EstimatedCost: new(float64(math.Inf(1)))},
		"nonfinite throughput": {OutputTokensPerSecond: new(float64(math.NaN()))},
	} {
		t.Run(name, func(t *testing.T) {
			if err := event.ValidateCommon(); err == nil {
				t.Fatal("invalid known usage value passed validation")
			}
		})
	}
	if err := (UsageEvent{InputTokens: zero, EstimatedCost: new(float64(0))}).ValidateCommon(); err != nil {
		t.Fatalf("reported zero should be valid: %v", err)
	}

	issues := make([]UsageIssue, 0, 80)
	for i := 0; i < 80; i++ {
		issues = append(issues,
			UsageIssue{Code: "repeated", TypeRef: extensionOnly.Extensions[0].TypeRef},
			UsageIssue{Code: "distinct_" + string(rune('A'+i)), TypeRef: extensions.Ref{Kind: extensions.UsageKind, ID: "fixture", ContractVersion: 1}},
		)
	}
	bounded := MergeProviderUsage(UsageEvent{}, UsageEvent{AccountingIssues: issues})
	if len(bounded.AccountingIssues) > 33 {
		t.Fatalf("accounting issue evidence exceeded ceiling: %d", len(bounded.AccountingIssues))
	}
	repeated := 0
	for _, issue := range bounded.AccountingIssues {
		if issue.Code == "repeated" {
			repeated++
		}
	}
	if repeated != 1 {
		t.Fatalf("duplicate issue code/type ref was not coalesced: %d entries", repeated)
	}
}

func TestMergeProviderUsagePreservesUpdateMetadata(t *testing.T) {
	started := time.Date(2026, time.October, 22, 10, 0, 0, 0, time.UTC)
	firstByte := started.Add(time.Second)
	current := UsageEvent{
		At: started, LogicalModel: "old-model", ProviderNodeID: "old-provider",
		ExternalModel: "old-external", ConnectionID: "old-connection", Status: "pending",
		Latency: time.Second, FirstByteAt: firstByte, TTFT: time.Second,
		RequestClass: "old-class", SessionID: "old-session",
	}
	completedAt := started.Add(2 * time.Second)
	update := UsageEvent{
		At: completedAt, LogicalModel: "model", ProviderNodeID: "provider",
		ExternalModel: "external", ConnectionID: "connection", Status: "ok",
		Latency: 2 * time.Second, FirstByteAt: firstByte.Add(time.Second), TTFT: 2 * time.Second,
		RequestClass: "class", SessionID: "session",
	}
	got := MergeProviderUsage(current, update)
	if got.At != completedAt || got.LogicalModel != "model" || got.ProviderNodeID != "provider" ||
		got.ExternalModel != "external" || got.ConnectionID != "connection" || got.Status != "ok" ||
		got.Latency != 2*time.Second || got.FirstByteAt != update.FirstByteAt || got.TTFT != 2*time.Second ||
		got.RequestClass != "class" || got.SessionID != "session" {
		t.Fatalf("update operational metadata was discarded: %#v", got)
	}
	if current.Status != "pending" || current.LogicalModel != "old-model" {
		t.Fatalf("metadata merge mutated current snapshot: %#v", current)
	}
}
