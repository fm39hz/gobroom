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
		RouteGroups:   map[string][]string{},
		WireRoutes:    map[string][]string{},
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

func TestRouteGroupExpandsConnectionCandidates(t *testing.T) {
	snapshot, err := BuildSnapshot(SnapshotInput{
		PublicModels: []PublicModel{{Name: "public", TargetRef: "combo"}},
		Combos:       []Combo{{Name: "combo", Members: []string{"catalog:model"}}},
		Routes: []Route{
			{ID: "catalog:model@a", NodeID: "node", CredentialID: "a", Enabled: true},
			{ID: "catalog:model@b", NodeID: "node", CredentialID: "b", Enabled: true},
		},
		RouteGroups: map[string][]string{"catalog:model": {"catalog:model@a", "catalog:model@b"}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolvePublic(snapshot, "public")
	if err != nil || len(resolved.Candidates) != 2 {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	if resolved.Candidates[0].CredentialID != "a" || resolved.Candidates[1].CredentialID != "b" {
		t.Fatalf("unexpected connection order: %#v", resolved.Candidates)
	}
}

func TestPublishedModelCanTargetOpaqueWireReference(t *testing.T) {
	snapshot, err := BuildSnapshot(SnapshotInput{
		PublicModels: []PublicModel{{Name: "public", TargetRef: "g4f/srv_xxx:provider/model"}},
		Routes:       []Route{{ID: "route", DisplayPrefix: "g4f", ExternalModel: "srv_xxx:provider/model", Enabled: true}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolvePublic(snapshot, "public")
	if err != nil || len(resolved.Candidates) != 1 || resolved.Candidates[0].ID != "route" {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
}

func TestSchedulerRoundRobinOrdersFallbackCandidates(t *testing.T) {
	s := NewScheduler(nil)
	model := ResolvedModel{PublicName: "public", Strategy: StrategyFallback, Candidates: []Route{
		{ID: "a", Enabled: true}, {ID: "b", Enabled: true}, {ID: "c", Enabled: true},
	}}
	first := s.Order(model, time.Unix(0, 0))
	second := s.Order(model, time.Unix(0, 0))
	if first[0].ID != "a" || second[0].ID != "a" {
		t.Fatalf("fallback order must remain stable: first=%v second=%v", CandidatesByID(first), CandidatesByID(second))
	}
	model.Strategy = StrategyRoundRobin
	first = s.Order(model, time.Unix(0, 0))
	second = s.Order(model, time.Unix(0, 0))
	if first[0].ID == second[0].ID {
		t.Fatalf("round robin did not advance: first=%v second=%v", CandidatesByID(first), CandidatesByID(second))
	}
}
