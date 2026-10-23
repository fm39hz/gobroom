package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/fm39hz/gobroom/internal/adapter/usagewire"
	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
)

func TestBuiltinReportedChargeIsRegisteredPrivateDataWithoutFactory(t *testing.T) {
	runtime, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := runtime.FreezeCatalog()
	if err != nil {
		t.Fatal(err)
	}
	value, err := usagewire.ChargeValue(json.RawMessage(`0.001234567890123456789`), nil, "body", "/usage/cost")
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.ValidateUsageValue(value); err != nil {
		t.Fatal(err)
	}
	meta, ok := catalog.UsageDescriptor(value.TypeRef)
	if !ok || meta.Classification != extensions.UsagePrivate {
		t.Fatalf("reported charge has no private frozen metadata: %#v", meta)
	}
	if _, _, err := catalog.Bind(value.TypeRef, nil); err == nil {
		t.Fatal("data-only accounting acquired executable behavior")
	}
}

func TestHeaderUsageDistinguishesChargeFromEstimateAndDiagnosesInvalidCounters(t *testing.T) {
	source := HTTPHeaderUsageSource{}
	got, err := source.EnrichUsage(context.Background(), kernel.Route{}, kernel.UsageEvidence{Headers: http.Header{
		"X-Usage-Input-Tokens": {"invalid-private-value"}, "X-Usage-Output-Tokens": {"0"},
		"X-Usage-Cost-Usd": {"9007199254740993.0123456789"}, "X-Estimated-Cost-Usd": {"0.125"},
	}}, kernel.UsageEvent{InputTokens: new(int64(7))})
	if err != nil || got.InputTokens == nil || *got.InputTokens != 7 || got.OutputTokens == nil || *got.OutputTokens != 0 || len(got.AccountingIssues) == 0 || len(got.Extensions) != 1 || got.EstimatedCost == nil || *got.EstimatedCost != 0.125 {
		t.Fatalf("header accounting semantics lost: %#v %v", got, err)
	}
	var charge usagewire.ReportedCharge
	if err := json.Unmarshal(got.Extensions[0].Value, &charge); err != nil {
		t.Fatal(err)
	}
	if charge.Amount != "9007199254740993.0123456789" || charge.Currency == nil || *charge.Currency != "USD" || charge.Provenance.Source != "header" {
		t.Fatalf("header charge precision/provenance lost: %#v", charge)
	}
	got, err = source.EnrichUsage(context.Background(), kernel.Route{UsageOptions: kernel.UsageSourceOptions{ReportedChargeHeader: "X-Account-Charge", ReportedCurrencyHeader: "X-Account-Currency"}}, kernel.UsageEvidence{Headers: http.Header{"X-Account-Charge": {"0"}}}, kernel.UsageEvent{})
	if err != nil || len(got.Extensions) != 1 || got.EstimatedCost != nil {
		t.Fatalf("custom reported header lost: %#v %v", got, err)
	}
	charge = usagewire.ReportedCharge{}
	if err := json.Unmarshal(got.Extensions[0].Value, &charge); err != nil {
		t.Fatal(err)
	}
	if charge.Currency != nil || charge.Amount != "0" {
		t.Fatalf("inferred currency for custom header: %#v", charge)
	}
}
