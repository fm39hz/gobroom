package resolve

import "testing"

func TestNestedComboFlattensInOrderAndDeduplicates(t *testing.T) {
	r := &Resolver{
		Models: map[string]Candidate{
			"bai/deepseek": {NodeID: "bai", ExternalModel: "deepseek"},
			"g4f/deepseek": {NodeID: "g4f", ExternalModel: "deepseek"},
		},
		Combos: map[string]Combo{
			"deepseek-v4-flash": {Name: "deepseek-v4-flash", Members: []Member{{Ref: "bai/deepseek"}, {Ref: "g4f/deepseek"}}},
			"tech-lead":         {Name: "tech-lead", Members: []Member{{Ref: "deepseek-v4-flash"}, {Ref: "bai/deepseek"}}},
		},
	}
	items, err := r.Resolve("tech-lead")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].NodeID != "bai" || items[1].NodeID != "g4f" {
		t.Fatalf("unexpected candidates: %#v", items)
	}
}

func TestComboCycleIsRejected(t *testing.T) {
	r := &Resolver{Combos: map[string]Combo{
		"a": {Name: "a", Members: []Member{{Ref: "b"}}},
		"b": {Name: "b", Members: []Member{{Ref: "a"}}},
	}}
	if _, err := r.Resolve("a"); err == nil {
		t.Fatal("expected cycle error")
	}
}
