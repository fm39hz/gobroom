package controlplane

import (
	"testing"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/store"
)

func TestLoaderBuildsSnapshotFromSQLite(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.DB.Exec(`
INSERT INTO provider_nodes(id,name,base_url,protocol) VALUES('node-a','A','http://a','openai_chat');
INSERT INTO model_catalog(id,provider_node_id,kind,external_id,display_name) VALUES('route:a','node-a','discovered','model-a','Model A');
INSERT INTO connections(id,provider_node_id,name,credential_type,secret_ref,priority) VALUES('account-a','node-a','A1','api_key','secret-a',1);
INSERT INTO connections(id,provider_node_id,name,credential_type,secret_ref,priority) VALUES('account-b','node-a','A2','api_key','secret-b',2);
INSERT INTO combos(name,strategy) VALUES('role','fallback');
INSERT INTO combo_members(combo_name,position,target_ref) VALUES('role',0,'route:a');
INSERT INTO published_models(name,target_ref) VALUES('public-role','role');
`)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := (Loader{Store: s}).LoadSnapshot(7)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Version != 7 {
		t.Fatalf("version=%d", snapshot.Version)
	}
	if _, err := snapshot.PublicModels["public-role"]; !err {
		t.Fatal("public model missing")
	}
	k := kernel.Kernel{Snapshots: mustSnapshotStore(t, snapshot)}
	resolved, err := k.Resolve("public-role")
	if err != nil || len(resolved.Candidates) != 2 {
		t.Fatalf("expected two account candidates, got %#v err=%v", resolved, err)
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
