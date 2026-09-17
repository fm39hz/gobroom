package store

import "testing"

func TestPublicModelsAreSeparateFromCatalog(t *testing.T) {
	s, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.UpsertPublicModel(PublicModel{Name: "tech-lead", TargetRef: "combo:tech-lead"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertPublicModel(PublicModel{Name: "senior", TargetRef: "combo:senior"}); err != nil {
		t.Fatal(err)
	}
	items, err := s.PublicModels()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Name != "senior" || items[1].TargetRef != "combo:tech-lead" {
		t.Fatalf("unexpected public models: %#v", items)
	}
	if err := s.DeletePublicModel("senior"); err != nil {
		t.Fatal(err)
	}
	items, err = s.PublicModels()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Name != "tech-lead" {
		t.Fatalf("unexpected models after delete: %#v", items)
	}
}
