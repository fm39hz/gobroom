package provider

import "testing"

func TestParseModelSplitsOnlyFirstSlash(t *testing.T) {
	ref, err := ParseModel("g4f/srv_xxx:provider/model/name")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Prefix != "g4f" || ref.Model != "srv_xxx:provider/model/name" || !ref.HasPrefix {
		t.Fatalf("unexpected ref: %#v", ref)
	}
}

func TestParseModelKeepsBareNamesOpaque(t *testing.T) {
	ref, err := ParseModel("claude-sonnet-4")
	if err != nil {
		t.Fatal(err)
	}
	if ref.HasPrefix || ref.Model != "claude-sonnet-4" {
		t.Fatalf("unexpected ref: %#v", ref)
	}
	if InferProvider(ref.Model) != "anthropic" {
		t.Fatalf("unexpected inferred provider: %q", InferProvider(ref.Model))
	}
}

func TestResolvePrefixRejectsUnknownAndPreservesCanonicalID(t *testing.T) {
	registry := NewPrefixRegistry()
	if err := registry.AddCustom("g4f", "node-g4f"); err != nil {
		t.Fatal(err)
	}
	ref, err := ResolvePrefix(ModelRef{Raw: "g4f/model", Prefix: "g4f", Model: "model", HasPrefix: true}, registry)
	if err != nil || ref.CanonicalID != "node-g4f" {
		t.Fatalf("resolved=%#v err=%v", ref, err)
	}
	if _, err := ResolvePrefix(ModelRef{Raw: "unknown/model", Prefix: "unknown", Model: "model", HasPrefix: true}, registry); err == nil {
		t.Fatal("expected unknown prefix error")
	}
}

func TestResolveAliasUsesOpaqueTargetParsing(t *testing.T) {
	ref, found, err := ResolveAlias("fast", map[string]string{"fast": "g4f/model/with/slashes"})
	if err != nil || !found || ref.Prefix != "g4f" || ref.Model != "model/with/slashes" {
		t.Fatalf("ref=%#v found=%v err=%v", ref, found, err)
	}
}
