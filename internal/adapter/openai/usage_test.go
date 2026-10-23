package openai

import (
	"encoding/json"
	"testing"

	"github.com/fm39hz/gobroom/internal/adapter/usagewire"
)

func TestDecodeOpenAIUsagePreservesReportedChargeAndCanonicalSubsets(t *testing.T) {
	event, err := decodeOpenAIUsage(json.RawMessage(`{"prompt_tokens":1000,"completion_tokens":30,"prompt_tokens_details":{"cached_tokens":900,"cache_write_tokens":50},"completion_tokens_details":{"reasoning_tokens":20},"cost":9007199254740993.0123456789}`))
	if err != nil {
		t.Fatal(err)
	}
	if event.InputTokens == nil || *event.InputTokens != 1000 || event.OutputTokens == nil || *event.OutputTokens != 30 || event.CachedReadTokens == nil || *event.CachedReadTokens != 900 || event.CachedWriteTokens == nil || *event.CachedWriteTokens != 50 || event.ReasoningTokens == nil || *event.ReasoningTokens != 20 {
		t.Fatalf("canonical subsets changed totals: %#v", event)
	}
	if event.EstimatedCost != nil || len(event.Extensions) != 1 || event.Extensions[0].TypeRef != usagewire.ReportedChargeRef() {
		t.Fatalf("reported amount conflated with estimate or lost: %#v", event)
	}
	var charge usagewire.ReportedCharge
	if err := json.Unmarshal(event.Extensions[0].Value, &charge); err != nil {
		t.Fatal(err)
	}
	if charge.Amount != "9007199254740993.0123456789" || charge.Currency != nil || charge.Provenance.Field != "/usage/cost" {
		t.Fatalf("charge precision/unit changed: %#v", charge)
	}
}

func TestDecodeOpenAIUsageMalformedChargeDoesNotDiscardCounters(t *testing.T) {
	for _, cost := range []string{`null`, `"private"`, `{"unknown":3}`} {
		event, err := decodeOpenAIUsage(json.RawMessage(`{"input_tokens":0,"output_tokens":4,"cost":` + cost + `}`))
		if err != nil || event.InputTokens == nil || *event.InputTokens != 0 || event.OutputTokens == nil || *event.OutputTokens != 4 || len(event.Extensions) != 0 || event.EstimatedCost != nil {
			t.Fatalf("optional cost poisoned response accounting: %#v %v", event, err)
		}
		if cost != `null` && len(event.AccountingIssues) == 0 {
			t.Fatal("invalid charge silently discarded")
		}
	}
	event, err := decodeOpenAIUsage(json.RawMessage(`{"cost":0,"currency":"EUR"}`))
	if err != nil || len(event.Extensions) != 1 {
		t.Fatalf("explicitzerocharge lost: %#v %v", event, err)
	}
	var charge usagewire.ReportedCharge
	if err := json.Unmarshal(event.Extensions[0].Value, &charge); err != nil {
		t.Fatal(err)
	}
	if charge.Amount != "0" || charge.Currency == nil || *charge.Currency != "EUR" {
		t.Fatalf("explicitcurrency lost: %#v", charge)
	}
}
