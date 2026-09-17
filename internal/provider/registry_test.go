package provider

import "testing"

func TestCustomPrefixCannotShadowBuiltIn(t *testing.T) {
	r := NewPrefixRegistry()
	if err := r.AddCustom("openai", "node-custom"); err == nil {
		t.Fatal("expected built-in collision")
	}
}

func TestCustomPrefixesAreUnique(t *testing.T) {
	r := NewPrefixRegistry()
	if err := r.AddCustom("g4f", "node-a"); err != nil {
		t.Fatal(err)
	}
	if err := r.AddCustom("g4f", "node-b"); err == nil {
		t.Fatal("expected custom collision")
	}
	entry, ok := r.Resolve("g4f")
	if !ok || entry.Canonical != "node-a" {
		t.Fatalf("unexpected entry: %#v", entry)
	}
}
