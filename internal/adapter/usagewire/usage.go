// Package usagewire binds optional accounting fields without weakening the
// enclosing response contract. It does not calculate prices or settled bills.
package usagewire

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"unicode"

	"github.com/fm39hz/gobroom/internal/extensions"
)

type Counter struct {
	Value   *int64
	Invalid bool
	Present bool
}

func (counter *Counter) UnmarshalJSON(data []byte) error {
	*counter = Counter{Present: true}
	if !json.Valid(data) {
		return fmt.Errorf("malformed usage counter JSON")
	}
	var value *int64
	if err := json.Unmarshal(data, &value); err != nil || value != nil && *value < 0 {
		counter.Invalid = true
		return nil
	}
	counter.Value = value
	return nil
}

type ChargeProvenance struct {
	Source string `json:"source"`
	Field  string `json:"field"`
}

type ReportedCharge struct {
	Amount     string           `json:"amount"`
	Currency   *string          `json:"currency,omitempty"`
	Provenance ChargeProvenance `json:"provenance"`
}

const currencyPattern = `^[A-Za-z][A-Za-z0-9_-]{0,15}$`

var currencyCode = regexp.MustCompile(currencyPattern)

func ReportedChargeRef() extensions.Ref {
	return extensions.Ref{Kind: extensions.UsageKind, ID: "gobroom.usage.reported-charge", ContractVersion: 1}
}

func ChargeValue(raw json.RawMessage, currency *string, source, field string) (extensions.UsageValue, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > 128 || !json.Valid(raw) {
		return extensions.UsageValue{}, fmt.Errorf("invalid reported charge")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var token any
	if err := decoder.Decode(&token); err != nil {
		return extensions.UsageValue{}, fmt.Errorf("invalid reported charge")
	}
	amount, ok := token.(json.Number)
	if !ok || source != "body" && source != "header" || len(field) == 0 || len(field) > 256 || currency != nil && !currencyCode.MatchString(*currency) {
		return extensions.UsageValue{}, fmt.Errorf("invalid reported charge")
	}
	for _, r := range field {
		if unicode.IsControl(r) {
			return extensions.UsageValue{}, fmt.Errorf("invalid reported charge provenance")
		}
	}
	value, err := json.Marshal(ReportedCharge{Amount: amount.String(), Currency: currency, Provenance: ChargeProvenance{Source: source, Field: field}})
	if err != nil {
		return extensions.UsageValue{}, fmt.Errorf("encode reported charge")
	}
	return extensions.UsageValue{TypeRef: ReportedChargeRef(), Value: value}, nil
}

func RegisterReportedCharge(catalog *extensions.Catalog) error {
	if catalog == nil {
		return fmt.Errorf("usage catalog is required")
	}
	schemaRef := extensions.Ref{Kind: extensions.SchemaKind, ID: "gobroom.usage.reported-charge.value", ContractVersion: 1}
	shape := map[string]any{
		"type": "object", "required": []string{"amount", "provenance"}, "additionalProperties": false,
		"properties": map[string]any{
			"amount":   map[string]any{"type": "string", "maxLength": 128, "pattern": `^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`},
			"currency": map[string]any{"type": "string", "pattern": currencyPattern},
			"provenance": map[string]any{"type": "object", "required": []string{"source", "field"}, "additionalProperties": false, "properties": map[string]any{
				"source": map[string]any{"enum": []string{"body", "header"}},
				"field":  map[string]any{"type": "string", "minLength": 1, "maxLength": 256, "pattern": "^[^\x00-\x1f\x7f]+$"},
			}},
		},
	}
	raw, err := json.Marshal(shape)
	if err != nil {
		return err
	}
	document, err := extensions.BindSchemaDocument(schemaRef, raw)
	if err != nil {
		return err
	}
	if err := catalog.RegisterSchema(schemaRef, document); err != nil {
		return err
	}
	return catalog.RegisterDescriptor(extensions.Descriptor{Ref: ReportedChargeRef(), ImplementationVersion: "1", DisplayName: "Reported charge", Description: "Provider-reported amount and explicit provenance; currency is unknown unless reported. Not a settled bill or pricing rule.", InputSchemaRef: &schemaRef, UsagePolicy: &extensions.UsagePolicy{Disclosure: extensions.UsagePrivate, MaxValueBytes: 2048}})
}
