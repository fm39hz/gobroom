package controlplane

import (
	"testing"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/store"
)

func TestLoaderBuildsTypedPhysicalAndComboGraph(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/typed.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB.Exec(`
INSERT INTO provider_nodes(id,name,base_url,protocol,prefix) VALUES('node-a','A','http://a','openai_chat','a');
INSERT INTO model_catalog(id,provider_node_id,kind,external_id,display_name) VALUES('route:a','node-a','discovered','model-a','Model A');
INSERT INTO connections(id,provider_node_id,name,credential_type,secret_ref,priority) VALUES('account-a','node-a','A1','api_key','secret-a',1);
`); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertPhysicalModel(store.PhysicalModel{Name: "model-a", Sources: []store.RouteReference{{RouteID: "route:a"}}, Policy: store.StrategySpec{ID: "ordered-fallback"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertComboModel(store.ComboModel{Name: "junior", Members: []store.ModelReference{{Kind: store.PhysicalReference, ID: "model-a"}}, Strategy: store.StrategySpec{ID: "rotating-fallback"}, Discoverable: true, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := (Loader{Store: s}).LoadSnapshot(8)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snapshot.PublicModels["junior"]; !ok {
		t.Fatal("discoverable combo was not projected as a public model")
	}
	physical := snapshot.Nodes["model-a"]
	if physical.Kind != kernel.ModelPhysical || len(physical.Members) != 1 || physical.Members[0].Kind != kernel.MemberRouteGroup {
		t.Fatalf("physical node=%#v", physical)
	}
	combo := snapshot.Nodes["junior"]
	if combo.Strategy != kernel.StrategyRotatingFallback || len(combo.Members) != 1 || combo.Members[0].ID != "model-a" {
		t.Fatalf("combo node=%#v", combo)
	}
}

func mustSnapshotStore(t *testing.T, snapshot kernel.Snapshot) *kernel.SnapshotStore {
	t.Helper()
	store, err := kernel.NewSnapshotStore(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return store
}
