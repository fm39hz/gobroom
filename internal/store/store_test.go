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

func TestConnectionsAreManagedWithoutExposingSecrets(t *testing.T) {
	s, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB.Exec(`INSERT INTO provider_nodes(id,name,base_url,protocol,prefix) VALUES('node-a','A','http://a','openai_chat','a')`); err != nil {
		t.Fatal(err)
	}
	item, err := s.CreateConnection(CreateConnectionInput{ProviderNodeID: "node-a", Name: "primary", CredentialType: "api_key", Secret: "do-not-return"})
	if err != nil {
		t.Fatal(err)
	}
	items, err := s.Connections("node-a")
	if err != nil || len(items) != 1 || items[0].ID != item.ID {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	credential, ok := s.ConnectionCredentialByID(item.ID)
	if !ok || credential.Secret != "do-not-return" {
		t.Fatalf("credential=%#v ok=%v", credential, ok)
	}
	if err := s.DeleteConnection(item.ID); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotVersionSettingSurvivesStoreReopen(t *testing.T) {
	path := t.TempDir() + "/test.db"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting("snapshot_version", "42"); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	value, ok, err := s.Setting("snapshot_version")
	if err != nil || !ok || value != "42" {
		t.Fatalf("value=%q ok=%v err=%v", value, ok, err)
	}
}
