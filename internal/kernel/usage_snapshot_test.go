package kernel

import (
	"testing"

	"github.com/fm39hz/gobroom/internal/extensions"
)

func TestUsageSnapshotPartialRetainsAbsentValuesAndFullClearsUnknown(t *testing.T) {
	current := UsageEvent{
		InputTokens: new(int64(100)), OutputTokens: new(int64(6)),
		OutputTokensPerSecond: new(float64(2)),
	}

	partial := MergeProviderUsage(current, UsageEvent{OutputTokens: new(int64(4))})
	if partial.InputTokens == nil || *partial.InputTokens != 100 || partial.OutputTokens == nil || *partial.OutputTokens != 4 {
		t.Fatalf("partial update did not retain absent input and replace output: %#v", partial)
	}

	fullUnknown := MergeProviderUsage(partial, UsageEvent{UpdateKind: UsageFullSnapshot})
	if fullUnknown.InputTokens != nil || fullUnknown.OutputTokens != nil || fullUnknown.OutputTokensPerSecond != nil {
		t.Fatalf("full snapshot with unknown quantities did not clear prior values: %#v", fullUnknown)
	}
}

func TestUsageSnapshotFullReplacementClearsExtensionsAndKeepsIssues(t *testing.T) {
	oldRef := extensions.Ref{Kind: extensions.UsageKind, ID: "old", ContractVersion: 1}
	newRef := extensions.Ref{Kind: extensions.UsageKind, ID: "new", ContractVersion: 1}
	current := UsageEvent{
		InputTokens:      new(int64(3)),
		Extensions:       []extensions.UsageValue{{TypeRef: oldRef, Value: []byte(`{"count":7}`)}},
		AccountingIssues: []UsageIssue{{Code: "earlier_issue", TypeRef: oldRef}},
	}

	got := MergeProviderUsage(current, UsageEvent{
		UpdateKind:       UsageFullSnapshot,
		Extensions:       []extensions.UsageValue{{TypeRef: newRef, Value: []byte(`{"count":9}`)}},
		AccountingIssues: []UsageIssue{{Code: "later_issue", TypeRef: newRef}},
	})
	if got.InputTokens != nil {
		t.Fatalf("full snapshot did not replace absent input with unknown: %#v", got.InputTokens)
	}
	if len(got.Extensions) != 1 || got.Extensions[0].TypeRef != newRef {
		t.Fatalf("full snapshot did not replace the extension collection: %#v", got.Extensions)
	}
	if len(got.AccountingIssues) != 2 || got.AccountingIssues[0].Code != "earlier_issue" || got.AccountingIssues[1].Code != "later_issue" {
		t.Fatalf("diagnostics were not retained and coalesced across updates: %#v", got.AccountingIssues)
	}
}

func TestUsageSnapshotClonePreservesKindAndInvalidKindCannotEraseValues(t *testing.T) {
	snapshot := UsageEvent{UpdateKind: UsageFullSnapshot, InputTokens: new(int64(12))}
	clone := snapshot.Clone()
	if clone.UpdateKind != UsageFullSnapshot || clone.InputTokens == nil || *clone.InputTokens != 12 {
		t.Fatalf("clone did not preserve snapshot kind and quantity: %#v", clone)
	}

	invalid := UsageEvent{UpdateKind: UsageUpdateKind("future-kind"), OutputTokens: new(int64(5))}
	if err := invalid.ValidateCommon(); err == nil {
		t.Fatal("unsupported update kind passed common validation")
	}
	sanitized := invalid.SanitizeCommon()
	if sanitized.UpdateKind != UsagePartialSnapshot || sanitized.ValidateCommon() != nil {
		t.Fatalf("invalid update kind was not sanitized to a valid partial observation: %#v", sanitized)
	}
	if sanitized.OutputTokens == nil || *sanitized.OutputTokens != 5 {
		t.Fatalf("invalid update kind erased a valid quantity: %#v", sanitized)
	}
	foundIssue := false
	for _, issue := range sanitized.AccountingIssues {
		if issue.Code == invalidCommonUsageCode {
			foundIssue = true
			break
		}
	}
	if !foundIssue {
		t.Fatalf("invalid update kind did not add a constant diagnostic: %#v", sanitized.AccountingIssues)
	}
	merged := MergeProviderUsage(UsageEvent{InputTokens: new(int64(8))}, invalid)
	if merged.InputTokens == nil || *merged.InputTokens != 8 {
		t.Fatalf("invalid update kind erased an absent quantity instead of using partial semantics: %#v", merged)
	}
}
