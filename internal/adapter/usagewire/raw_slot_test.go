package usagewire

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestRawSlotAtBoundOwnsExactJSON(t *testing.T) {
	const original = `{"input_tokens":9007199254740993, "cost":9007199254740993.0123456789}`
	raw := []byte(original)
	slot := RawSlot{MaxBytes: int64(len(raw))}
	if err := json.Unmarshal(raw, &slot); err != nil {
		t.Fatal(err)
	}
	if !slot.Present || slot.Rejected || slot.MaxBytes != int64(len(raw)) {
		t.Fatalf("exact-bound JSON was not accepted with its initialized limit: %+v", slot)
	}
	if !bytes.Equal(slot.Value, []byte(original)) {
		t.Fatalf("slot changed original whitespace or numeric lexemes: %s", slot.Value)
	}
	raw[0] = '['
	if !bytes.Equal(slot.Value, []byte(original)) {
		t.Fatal("accepted slot aliases the caller's input buffer")
	}
}

func TestRawSlotAboveBoundRejectsWithoutRetainingValue(t *testing.T) {
	const original = `{"input_tokens":9007199254740993}`
	raw := []byte(original)
	slot := RawSlot{MaxBytes: int64(len(raw) - 1)}
	if err := json.Unmarshal(raw, &slot); err != nil {
		t.Fatalf("oversized optional capture must report rejection through the slot: %v", err)
	}
	if !slot.Present || !slot.Rejected || slot.Value != nil {
		t.Fatalf("over-bound JSON was retained or rejection was not reported: %+v", slot)
	}
	if !bytes.Equal(raw, []byte(original)) {
		t.Fatal("rejecting capture modified the caller's JSON")
	}
}
