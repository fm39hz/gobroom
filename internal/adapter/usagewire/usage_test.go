package usagewire

import (
	"encoding/json"
	"testing"

	"github.com/fm39hz/gobroom/internal/extensions"
)

func TestCounterBindsPresenceAndIsolatesOptionalFaults(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		known   bool
		value   int64
		invalid bool
	}{
		{`{}`, false, 0, false}, {`{"n":null}`, false, 0, false}, {`{"n":0}`, true, 0, false},
		{`{"n":9007199254740993}`, true, 9007199254740993, false},
		{`{"n":-1}`, false, 0, true}, {`{"n":1.5}`, false, 0, true}, {`{"n":"1"}`, false, 0, true}, {`{"n":9223372036854775808}`, false, 0, true},
	} {
		var wire struct {
			N       Counter `json:"n"`
			Sibling int     `json:"sibling"`
		}
		if err := json.Unmarshal([]byte(tc.raw), &wire); err != nil {
			t.Fatal(err)
		}
		if (wire.N.Value != nil) != tc.known || wire.N.Invalid != tc.invalid || tc.known && *wire.N.Value != tc.value {
			t.Fatalf("counter %s = %#v", tc.raw, wire.N)
		}
	}
	var wire struct {
		N       Counter `json:"n"`
		Sibling int     `json:"sibling"`
	}
	if err := json.Unmarshal([]byte(`{"n":-1,"sibling":7}`), &wire); err != nil || wire.Sibling != 7 || !wire.N.Invalid {
		t.Fatalf("invalid optional counter poisoned sibling: %#v %v", wire, err)
	}
}

func TestReportedChargePreservesPrecisionWithoutInferredCurrency(t *testing.T) {
	catalog := extensions.NewCatalog()
	if err := RegisterReportedCharge(catalog); err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	value, err := ChargeValue(json.RawMessage(`9007199254740993.012345678901`), nil, "body", "/usage/cost")
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.ValidateUsageValue(value); err != nil {
		t.Fatal(err)
	}
	var charge ReportedCharge
	if err := json.Unmarshal(value.Value, &charge); err != nil {
		t.Fatal(err)
	}
	if charge.Amount != "9007199254740993.012345678901" || charge.Currency != nil || charge.Provenance.Source != "body" || charge.Provenance.Field != "/usage/cost" {
		t.Fatalf("charge changed semantics: %#v", charge)
	}
	meta, ok := snapshot.UsageDescriptor(value.TypeRef)
	if !ok || meta.Classification != extensions.UsagePrivate || meta.Unit != "" {
		t.Fatalf("charge granted currency/disclosure: %#v", meta)
	}
	for _, raw := range []string{`null`, `"0.5"`, `NaN`, `{"amount":1}`} {
		if _, err := ChargeValue(json.RawMessage(raw), nil, "body", "/usage/cost"); err == nil {
			t.Fatalf("accepted invalid reported amount %s", raw)
		}
	}
	value, err = ChargeValue(json.RawMessage(`0`), new("EUR"), "header", "X-Reported-Charge")
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.ValidateUsageValue(value); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(value.Value, &charge); err != nil {
		t.Fatal(err)
	}
	if charge.Amount != "0" || charge.Currency == nil || *charge.Currency != "EUR" {
		t.Fatalf("lost explicit zero/currency: %#v", charge)
	}
}
