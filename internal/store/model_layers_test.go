package store

import (
	"errors"
	"reflect"
	"testing"

	"github.com/fm39hz/gobroom/internal/kernel"
)

func TestTypedModelLayersReuseCatalog(t *testing.T) {
	s, err := Open(t.TempDir() + "/models.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, err := s.DB.Exec(`INSERT INTO provider_nodes(id,name,base_url,protocol,prefix) VALUES
('node-a','A','https://a.test','openai_chat','orca'),
('node-b','B','https://b.test','openai_chat','g4f')`); err != nil {
		t.Fatal(err)
	}
	for _, route := range []UpsertCatalogModelInput{
		{ID: "route-a", ProviderNodeID: "node-a", Kind: "discovered", ExternalID: "vendor/qwen:free", DisplayName: "Qwen", Profile: map[string]kernel.Capability{"input.image": {State: kernel.SupportNative}}},
		{ID: "route-b", ProviderNodeID: "node-b", Kind: "custom", ExternalID: "Qwen/Qwen", DisplayName: "Qwen", Capabilities: map[string]bool{"vision": true}},
	} {
		if err := s.UpsertCatalogModel(route); err != nil {
			t.Fatal(err)
		}
	}
	discovered, err := s.DiscoveredRoutes("")
	if err != nil || len(discovered) != 2 || discovered[0].ID != "route-b" || discovered[1].ProviderPrefix != "orca" {
		t.Fatalf("discovered=%#v err=%v", discovered, err)
	}
	if discovered[1].Profile["input.image"].State != kernel.SupportNative {
		t.Fatalf("typed capability profile was not persisted: %#v", discovered[1].Profile)
	}

	physical := PhysicalModel{
		Name:         "qwen-3.7-max",
		Sources:      []RouteReference{{RouteID: "route-b"}, {RouteID: "route-a"}},
		Policy:       StrategySpec{ID: "rotating-fallback", Config: map[string]any{"stickyLimit": 2}},
		Capabilities: map[string]bool{"reasoning": true}, Discoverable: false, Enabled: true,
	}
	if err := s.UpsertPhysicalModel(physical); err != nil {
		t.Fatal(err)
	}
	gotPhysical, err := s.PhysicalModel(physical.Name)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotPhysical.Sources, physical.Sources) || gotPhysical.Policy.ID != "rotating-fallback" || gotPhysical.Discoverable {
		t.Fatalf("physical=%#v", gotPhysical)
	}

	combo := ComboModel{
		Name:         "junior",
		Members:      []ModelReference{{Kind: PhysicalReference, ID: "qwen-3.7-max"}},
		Strategy:     StrategySpec{ID: "weighted-fallback", Config: map[string]any{"weights": []int{3}}},
		Discoverable: true, Enabled: true,
	}
	if err := s.UpsertComboModel(combo); err != nil {
		t.Fatal(err)
	}
	gotCombo, err := s.ComboModel(combo.Name)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotCombo.Members, combo.Members) || gotCombo.Strategy.ID != combo.Strategy.ID || !gotCombo.Discoverable {
		t.Fatalf("combo=%#v", gotCombo)
	}

	// Re-running the schema migration must leave typed records intact.
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
}

func TestModelLayerUpsertsAreAtomicAndValidateReferences(t *testing.T) {
	s, err := Open(t.TempDir() + "/models.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB.Exec(`INSERT INTO provider_nodes(id,name,base_url,protocol) VALUES('node-a','A','https://a.test','openai_chat')`); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertCatalogModel(UpsertCatalogModelInput{ID: "route-a", ProviderNodeID: "node-a", Kind: "discovered", ExternalID: "a", DisplayName: "A"}); err != nil {
		t.Fatal(err)
	}
	base := PhysicalModel{Name: "model-a", Sources: []RouteReference{{RouteID: "route-a"}}, Enabled: true}
	if err := s.UpsertPhysicalModel(base); err != nil {
		t.Fatal(err)
	}
	badUpdate := base
	badUpdate.Sources = []RouteReference{{RouteID: "missing"}}
	if err := s.UpsertPhysicalModel(badUpdate); err == nil {
		t.Fatal("expected unknown route error")
	}
	stored, err := s.PhysicalModel("model-a")
	if err != nil || !reflect.DeepEqual(stored.Sources, base.Sources) {
		t.Fatalf("failed update changed stored model: model=%#v err=%v", stored, err)
	}

	if err := s.UpsertComboModel(ComboModel{Name: "child", Members: []ModelReference{{Kind: PhysicalReference, ID: "model-a"}}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertComboModel(ComboModel{Name: "parent", Members: []ModelReference{{Kind: ComboReference, ID: "child"}}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertComboModel(ComboModel{Name: "child", Members: []ModelReference{{Kind: ComboReference, ID: "parent"}}, Enabled: true}); err == nil {
		t.Fatal("expected cycle validation error")
	}
	child, err := s.ComboModel("child")
	if err != nil || !reflect.DeepEqual(child.Members, []ModelReference{{Kind: PhysicalReference, ID: "model-a"}}) {
		t.Fatalf("cycle update was not rolled back: combo=%#v err=%v", child, err)
	}

	if err := s.DeletePhysicalModel("model-a"); !errors.Is(err, ErrModelReferenced) {
		t.Fatalf("delete referenced physical model error=%v", err)
	}
	if err := s.DeleteComboModel("child"); !errors.Is(err, ErrModelReferenced) {
		t.Fatalf("delete referenced combo error=%v", err)
	}
}

func TestMigrationAddsModelLayerTablesWithoutReplacingCatalog(t *testing.T) {
	path := t.TempDir() + "/typed.db"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO provider_nodes(id,name,base_url,protocol) VALUES('node','Provider','https://provider.test','openai_chat')`); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertCatalogModel(UpsertCatalogModelInput{ID: "catalog-id", ProviderNodeID: "node", Kind: "discovered", ExternalID: "upstream-id", DisplayName: "Model"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Reopen exercises the additive migration against a persisted database.
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	items, err := s.DiscoveredRoutes("node")
	if err != nil || len(items) != 1 || items[0].ExternalID != "upstream-id" {
		t.Fatalf("catalog route=%#v err=%v", items, err)
	}
	var tables int
	if err := s.DB.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('physical_models','physical_model_sources','combo_models','combo_model_members')`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 4 {
		t.Fatalf("typed model tables=%d, want 4", tables)
	}
}
