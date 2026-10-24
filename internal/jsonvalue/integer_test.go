package jsonvalue

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExactIntegerFormsAndBounds(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		want  int
		valid bool
	}{
		{"2048", 2048, true}, {"2048.0", 2048, true}, {"2.048e3", 2048, true}, {"-0.0", 0, true},
		{"2048.0000000000001", 0, false}, {"9223372036854775808", 0, false},
		{"1e1000000000", 0, false}, {"1e-1000000000", 0, false}, {"0x1p1", 0, false},
		{"1." + strings.Repeat("0", MaxIntegerTokenBytes), 0, false},
	} {
		got, ok := Integer(json.Number(tc.raw))
		if ok != tc.valid || (ok && got != tc.want) {
			t.Errorf("Integer(%q)=%d,%v want=%d,%v", tc.raw, got, ok, tc.want, tc.valid)
		}
	}
	if _, ok := Integer("2048"); ok {
		t.Fatal("string treated as numeric token")
	}
}

func TestStrictBindingRetainsValidationWithSemanticIntegers(t *testing.T) {
	for _, tc := range []struct {
		body  string
		valid bool
	}{
		{`{"value":2048.0}`, true}, {`{"value":2.048e3}`, true},
		{`{"value":2048.0000000000001}`, false}, {`{"value":"2048"}`, false},
		{`{"value":2048,"extra":true}`, false}, {`{"value":2048,"value":2048}`, false},
		{`{"value":2048} {}`, false},
	} {
		var value struct {
			Value int `json:"value"`
		}
		err := UnmarshalStrict([]byte(tc.body), &value)
		if (err == nil) != tc.valid || (tc.valid && value.Value != 2048) {
			t.Errorf("binding %s: value=%d err=%v", tc.body, value.Value, err)
		}
	}
}

func TestObjectPreservesNumericTokensAndSingleObjectBoundary(t *testing.T) {
	object, err := Object([]byte(`{"number":9007199254740993}`))
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := object["number"].(json.Number); !ok || got.String() != "9007199254740993" {
		t.Fatalf("number token rounded: %#v", object)
	}
	for _, body := range []string{`null`, `[]`, `{} {}`, `{} trailing`} {
		if _, err := Object([]byte(body)); err == nil {
			t.Errorf("invalid object accepted: %s", body)
		}
	}
}
