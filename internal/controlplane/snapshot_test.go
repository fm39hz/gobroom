package controlplane

import (
	"testing"

	"github.com/gorouter/gorouter/internal/store"
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
}
