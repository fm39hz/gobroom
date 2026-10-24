// Package jsonvalue binds JSON scalar contracts without losing number tokens.
package jsonvalue

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"io"
	"math"
	"math/big"
)

// MaxIntegerTokenBytes bounds alternate decimal/exponent spellings before
// exact rational conversion. Ordinary integer tokens take the Int64 fast path.
const MaxIntegerTokenBytes = 4096

func Integer(value any) (int, bool) {
	switch number := value.(type) {
	case int:
		return number, true
	case float64:
		if number != float64(int(number)) {
			return 0, false
		}
		return int(number), true
	case json.Number:
		if len(number) > MaxIntegerTokenBytes || !json.Valid([]byte(number)) {
			return 0, false
		}
		if integer, err := number.Int64(); err == nil {
			return narrow(integer)
		}
		// Reject unbounded magnitude before allocating an exact representation.
		if approximate, err := number.Float64(); err != nil || math.IsNaN(approximate) || math.IsInf(approximate, 0) {
			return 0, false
		}
		exact, ok := new(big.Rat).SetString(number.String())
		if !ok || !exact.IsInt() || !exact.Num().IsInt64() {
			return 0, false
		}
		return narrow(exact.Num().Int64())
	default:
		return 0, false
	}
}

func narrow(value int64) (int, bool) {
	integer := int(value)
	return integer, int64(integer) == value
}

func Object(data []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("JSON must contain exactly one object")
	}
	if object == nil {
		return nil, errors.New("JSON must contain a nonnull object")
	}
	return object, nil
}

var exactIntegers = jsonv2.WithUnmarshalers(jsonv2.UnmarshalFunc(func(raw []byte, target *int) error {
	if jsontext.Value(raw).Kind() != '0' {
		return errors.New("JSON integer required")
	}
	value, ok := Integer(json.Number(raw))
	if !ok {
		return errors.New("JSON number is not an exact machine-size integer")
	}
	*target = value
	return nil
}))

// UnmarshalStrict uses the maintained JSON binding implementation, retaining
// strict members/duplicates/trailing checks and semantic integer spellings.
func UnmarshalStrict(data []byte, target any) error {
	return jsonv2.Unmarshal(data, target, jsonv2.RejectUnknownMembers(true), exactIntegers)
}
