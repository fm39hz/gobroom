package provider

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestCredentialStateBoundsAndRedacts(t *testing.T) {
	if _, err := NewCredentialState(""); err == nil {
		t.Fatal("empty state must be rejected")
	}
	if _, err := NewCredentialState(string(make([]byte, 64*1024+1))); err == nil {
		t.Fatal("state larger than 64 KiB must be rejected")
	}

	const raw = ` { "vendor_token" : "keep exact bytes" } `
	state, err := NewCredentialState(raw)
	if err != nil {
		t.Fatal(err)
	}
	if state.Encoded() != raw {
		t.Fatalf("encoded state changed: %q", state.Encoded())
	}
	if err := state.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		if got := fmt.Sprintf(format, state); strings.Contains(got, "keep exact bytes") {
			t.Errorf("format %s exposed state: %q", format, got)
		}
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "keep exact bytes") {
		t.Fatalf("JSON exposed state: %s", encoded)
	}
}
