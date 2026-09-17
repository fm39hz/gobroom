package controlplane

import (
	"testing"

	"github.com/gorouter/gorouter/internal/store"
)

func TestManagerReloadsPublishedNestedCombo(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB.Exec(`
INSERT INTO provider_nodes(id,name,base_url,protocol,prefix) VALUES('node-a','A','http://a','openai_chat','a');
INSERT INTO model_catalog(id,provider_node_id,kind,external_id,display_name) VALUES('route:a','node-a','custom','a-model','A');
INSERT INTO combos(name,strategy) VALUES('role','fallback');
INSERT INTO combo_members(combo_name,position,target_ref) VALUES('role',0,'route:a');
INSERT INTO published_models(name,target_ref) VALUES('public-role','role');
`); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(s)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := m.Resolve("public-role")
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.Candidates) != 1 || resolved.Candidates[0].ID != "route:a" {
		t.Fatalf("resolved=%#v", resolved)
	}
}
