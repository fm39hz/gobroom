package kernel

import (
	"testing"
	"time"
)

func testSnapshot() Snapshot {
	return Snapshot{
		PublicModels: map[string]PublicModel{"tech-lead": {Name: "tech-lead", TargetRef: "role:tech-lead"}},
		Combos: map[string]Combo{
			"role:tech-lead":  {Name: "role:tech-lead", Strategy: StrategyRoundRobinFallback, Members: []string{"family:deepseek", "family:glm"}},
			"family:deepseek": {Name: "family:deepseek", Strategy: StrategyFallback, Members: []string{"route:bai", "route:g4f"}},
			"family:glm":      {Name: "family:glm", Strategy: StrategyFallback, Members: []string{"route:glm"}},
		},
		Routes: map[string]Route{
			"route:bai": {ID: "route:bai", NodeID: "bai", ExternalModel: "deepseek-v4-flash", Protocol: ProtocolOpenAIChat, Enabled: true},
			"route:g4f": {ID: "route:g4f", NodeID: "g4f", ExternalModel: "deepseek-v4-flash", Protocol: ProtocolOpenAIChat, Enabled: true},
			"route:glm": {ID: "route:glm", NodeID: "ocg", ExternalModel: "glm-5.3", Protocol: ProtocolOpenAIChat, Enabled: true},
		},
		LogicalModels: map[string]string{},
	}
}

func TestPublicModelResolvesNestedCombos(t *testing.T) {
	s := testSnapshot()
	if err := ValidateSnapshot(s); err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolvePublic(s, "tech-lead")
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.Candidates) != 3 || resolved.Candidates[0].NodeID != "bai" || resolved.Candidates[2].NodeID != "ocg" {
		t.Fatalf("unexpected candidates: %#v", resolved.Candidates)
	}
}

func TestOnlyPublishedNamesResolve(t *testing.T) {
	k, err := New(testSnapshot(), nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	if _, err := k.Resolve("family:deepseek"); err == nil {
		t.Fatal("internal combo must not be public")
	}
	if _, err := k.Select("tech-lead", time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestDuplicateComboRouteIsRemoved(t *testing.T) {
	s := testSnapshot()
	s.Combos["role:tech-lead"] = Combo{Name: "role:tech-lead", Members: []string{"route:bai", "route:bai", "route:g4f"}}
	resolved, err := ResolvePublic(s, "tech-lead")
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.Candidates) != 2 {
		t.Fatalf("expected deduplication, got %d", len(resolved.Candidates))
	}
}
