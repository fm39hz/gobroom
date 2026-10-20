package store

import (
	"context"
	"testing"
)

func catalogImportFixture(t *testing.T) (*Store, ConnectionRecord, CatalogImportInput) {
	t.Helper()
	s, connection := newCredentialRevisionFixture(t)
	t.Cleanup(func() { _ = s.Close() })
	binding, err := s.ConnectionCredentialBinding(context.Background(), connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	node, err := s.ProviderNode(connection.ProviderNodeID)
	if err != nil {
		t.Fatal(err)
	}
	input := CatalogImportInput{Expected: binding, Node: node, Source: "models_endpoint", Models: []UpsertCatalogModelInput{
		{ID: "route-a", ProviderNodeID: node.ID, Kind: "discovered", ExternalID: "model-a", DisplayName: "Model A"},
	}}
	return s, connection, input
}

func TestImportConnectionCatalogIncompleteAndStaticSemantics(t *testing.T) {
	s, c, input := catalogImportFixture(t)
	input.Complete = true
	if err := s.RecordConnectionModelSnapshot(c.ID, c.ProviderNodeID, []string{"old-model"}, true, "seed"); err != nil {
		t.Fatal(err)
	}
	input.ExternalIDs = []string{"model-a"}
	if err := s.ImportConnectionCatalog(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	items, err := s.ConnectionModelEntitlements(c.ProviderNodeID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.ExternalModelID == "old-model" && item.Status != EntitlementNotListed {
			t.Fatalf("complete snapshot did not revoke old entitlement: %+v", item)
		}
	}
	input.Complete = false
	input.ExternalIDs = nil
	input.Models = []UpsertCatalogModelInput{{ID: "route-b", ProviderNodeID: input.Node.ID, Kind: "discovered", ExternalID: "model-b", DisplayName: "Model B"}}
	if err := s.ImportConnectionCatalog(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	items, err = s.ConnectionModelEntitlements(c.ProviderNodeID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.ExternalModelID == "model-a" && item.Status != EntitlementAvailable {
			t.Fatalf("incomplete snapshot revoked entitlement: %+v", item)
		}
	}
	input.Static = true
	input.Models = append(input.Models, UpsertCatalogModelInput{ID: "route-c", ProviderNodeID: input.Node.ID, Kind: "discovered", ExternalID: "model-c", DisplayName: "Model C"})
	if err := s.ImportConnectionCatalog(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	assigned, err := s.StaticCatalogAssignedModels(c.ID, c.ProviderNodeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(assigned) != 2 {
		t.Fatalf("static assignments = %v, want both selected routes", assigned)
	}
	items, err = s.ConnectionModelEntitlements(c.ProviderNodeID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.ExternalModelID == "model-a" && item.Status != EntitlementAvailable {
			t.Fatalf("static import changed entitlement: %+v", item)
		}
	}
}

func TestImportConnectionCatalogRejectsStaleBindingAndNodeWithoutWrites(t *testing.T) {
	mutations := []struct {
		name  string
		apply func(*testing.T, *Store, ConnectionRecord, ProviderNode)
	}{
		{"same secret revision and principal", func(t *testing.T, s *Store, c ConnectionRecord, n ProviderNode) {
			if _, err := s.UpdateConnection(UpdateConnectionInput{ID: c.ID, Secret: "secret-a"}); err != nil {
				t.Fatal(err)
			}
		}},
		{"principal", func(t *testing.T, s *Store, c ConnectionRecord, n ProviderNode) {
			if _, err := s.DB.Exec(`UPDATE connection_principals SET revision='changed' WHERE connection_id=?`, c.ID); err != nil {
				t.Fatal(err)
			}
		}},
		{"owner", func(t *testing.T, s *Store, c ConnectionRecord, n ProviderNode) {
			if _, err := s.DB.Exec(`UPDATE provider_nodes SET definition_id='changed-definition' WHERE id=?`, n.ID); err != nil {
				t.Fatal(err)
			}
		}},
		{"disabled connection", func(t *testing.T, s *Store, c ConnectionRecord, n ProviderNode) {
			if _, err := s.DB.Exec(`UPDATE connections SET enabled=0 WHERE id=?`, c.ID); err != nil {
				t.Fatal(err)
			}
		}},
		{"node endpoint", func(t *testing.T, s *Store, c ConnectionRecord, n ProviderNode) {
			if _, err := s.DB.Exec(`UPDATE provider_nodes SET base_url='https://changed.test' WHERE id=?`, n.ID); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			s, c, input := catalogImportFixture(t)
			tc.apply(t, s, c, input.Node)
			if _, err := s.DB.Exec(`INSERT INTO model_availability(connection_id,model_ref,status) VALUES(?,?,'invalid')`, c.ID, "model-a"); err != nil {
				t.Fatal(err)
			}
			if err := s.ImportConnectionCatalog(context.Background(), input); err == nil {
				t.Fatal("stale import succeeded")
			}
			var count int
			if err := s.DB.QueryRow(`SELECT count(*) FROM model_catalog WHERE id='route-a'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("stale import wrote %d models", count)
			}
			var health string
			if err := s.DB.QueryRow(`SELECT status FROM model_availability WHERE connection_id=? AND model_ref='model-a'`, c.ID).Scan(&health); err != nil {
				t.Fatal(err)
			}
			if health != "invalid" {
				t.Fatalf("stale import changed health to %q", health)
			}
			_ = s.Close()
		})
	}
}

func TestImportConnectionCatalogRollsBackOnMidImportFailure(t *testing.T) {
	s, _, input := catalogImportFixture(t)
	input.Models = append(input.Models, UpsertCatalogModelInput{ID: "route-b", ProviderNodeID: input.Node.ID, Kind: "discovered", ExternalID: "model-b", DisplayName: "Model B", Raw: map[string]any{"unserializable": make(chan int)}})
	if err := s.ImportConnectionCatalog(context.Background(), input); err == nil {
		t.Fatal("duplicate model import succeeded")
	}
	var count int
	if err := s.DB.QueryRow(`SELECT count(*) FROM model_catalog`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("partial import left %d models", count)
	}
}

func TestImportConnectionCatalogHonorsCanceledContext(t *testing.T) {
	s, _, input := catalogImportFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.ImportConnectionCatalog(ctx, input); err == nil {
		t.Fatal("canceled import succeeded")
	}
	var count int
	if err := s.DB.QueryRow(`SELECT count(*) FROM model_catalog`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("canceled import wrote %d models", count)
	}
}

type cancelCatalogMarshal struct{ cancel context.CancelFunc }

func (v cancelCatalogMarshal) MarshalJSON() ([]byte, error) {
	v.cancel()
	return []byte(`{}`), nil
}

func TestImportConnectionCatalogCancellationDuringWritesRollsBack(t *testing.T) {
	s, _, input := catalogImportFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input.Models = append(input.Models, UpsertCatalogModelInput{ID: "second", ProviderNodeID: input.Node.ID, Kind: "discovered", ExternalID: "second", DisplayName: "Second", Raw: map[string]any{"cancel": cancelCatalogMarshal{cancel}}})
	if err := s.ImportConnectionCatalog(ctx, input); err == nil {
		t.Fatal("cancellation during import committed data")
	}
	var count int
	if err := s.DB.QueryRow(`SELECT count(*) FROM model_catalog`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("canceled import retained %d model writes", count)
	}
}
