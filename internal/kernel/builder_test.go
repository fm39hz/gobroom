package kernel

import "testing"

func TestBuildSnapshotRejectsUnpublishedTargetCycles(t *testing.T) {
	_, err := BuildSnapshot(SnapshotInput{
		PublicModels: []PublicModel{{Name: "tech-lead", TargetRef: "a"}},
		Combos:       []Combo{{Name: "a", Members: []string{"b"}}, {Name: "b", Members: []string{"a"}}},
	}, 1)
	if err == nil {
		t.Fatal("expected cycle validation error")
	}
}

func TestBuildSnapshotResolvesTypedRouteGraph(t *testing.T) {
	snapshot, err := BuildSnapshot(SnapshotInput{
		PublicModels: []PublicModel{{Name: "tech-lead", TargetRef: "role:tech-lead"}},
		Combos:       []Combo{{Name: "role:tech-lead", Members: []string{"route:a", "route:b"}}},
		Routes:       []Route{{ID: "route:a", NodeID: "a", Enabled: true}, {ID: "route:b", NodeID: "b", Enabled: true}},
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Version != 2 {
		t.Fatalf("version=%d", snapshot.Version)
	}
	resolved, err := ResolvePublic(snapshot, "tech-lead")
	if err != nil || len(resolved.Candidates) != 2 {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
}
