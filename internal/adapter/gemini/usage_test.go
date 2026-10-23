package gemini

import (
	"encoding/json"
	"testing"

	"github.com/fm39hz/gobroom/internal/kernel"
)

func TestDecodeGeminiUsageReturnsCanonicalSnapshot(t *testing.T) {
	usage, err := decodeGeminiUsage(json.RawMessage(`{"promptTokenCount":1000,"candidatesTokenCount":6,"totalTokenCount":1020,"cachedContentTokenCount":900,"thoughtsTokenCount":14}`))
	if err != nil {
		t.Fatal(err)
	}
	if usage.InputTokens == nil || *usage.InputTokens != 1000 || usage.OutputTokens == nil || *usage.OutputTokens != 20 || usage.CachedReadTokens == nil || *usage.CachedReadTokens != 900 || usage.ReasoningTokens == nil || *usage.ReasoningTokens != 14 {
		t.Fatalf("Gemini helper changed source-verified native normalization: %#v", usage)
	}
	if usage.UpdateKind != kernel.UsageFullSnapshot {
		t.Fatalf("usage helper must return a full snapshot: %#v", usage)
	}
}

func TestDecodeGeminiUsagePreservesUnknownAndExplicitZero(t *testing.T) {
	usage, err := decodeGeminiUsage(json.RawMessage(`{"promptTokenCount":0,"thoughtsTokenCount":0}`))
	if err != nil {
		t.Fatal(err)
	}
	if usage.InputTokens == nil || *usage.InputTokens != 0 || usage.OutputTokens != nil || usage.ReasoningTokens == nil || *usage.ReasoningTokens != 0 {
		t.Fatalf("missing output must remain unknown while explicit zero remains known: %#v", usage)
	}
}

func TestDecodeGeminiUsageMalformedCounterDoesNotPoisonSiblings(t *testing.T) {
	usage, err := decodeGeminiUsage(json.RawMessage(`{"promptTokenCount":1.5,"candidatesTokenCount":7,"thoughtsTokenCount":0}`))
	if err != nil {
		t.Fatal(err)
	}
	if usage.InputTokens != nil || usage.OutputTokens == nil || *usage.OutputTokens != 7 || usage.ReasoningTokens == nil || *usage.ReasoningTokens != 0 {
		t.Fatalf("malformed input must remain unknown while valid native components survive: %#v", usage)
	}
	if len(usage.AccountingIssues) == 0 || usage.AccountingIssues[0].Code != "invalid_usage_counter" {
		t.Fatalf("invalid optional counter needs a sanitized issue: %#v", usage.AccountingIssues)
	}
}
