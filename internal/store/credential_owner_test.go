package store

import (
	"context"
	"encoding/json"
	"testing"
)

func TestCredentialOwnerProjectionAndCAS(t *testing.T) {
	s, connection := newCredentialRevisionFixture(t)
	defer s.Close()
	node, err := s.ProviderNode(connection.ProviderNodeID)
	if err != nil {
		t.Fatal(err)
	}
	want := CredentialOwner{ProviderNodeID: node.ID, Definition: node.DefinitionRef()}

	for name, read := range map[string]func() (Credential, bool){
		"selected": func() (Credential, bool) { return s.ConnectionCredential(node.ID) },
		"by ID":    func() (Credential, bool) { return s.ConnectionCredentialByID(connection.ID) },
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := read()
			if !ok || got.Owner != want {
				t.Fatalf("credential owner = %#v, found=%v; want %#v", got.Owner, ok, want)
			}
			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatal(err)
			}
			if _, exists := fields["Owner"]; exists {
				t.Fatalf("credential JSON exposed owner: %s", encoded)
			}
			if _, exists := fields["owner"]; exists {
				t.Fatalf("credential JSON exposed owner: %s", encoded)
			}
		})
	}

	fresh, _ := s.ConnectionCredentialByID(connection.ID)
	for name, forge := range map[string]func(*Credential){
		"provider node":      func(c *Credential) { c.Owner.ProviderNodeID = "another-node" },
		"definition ref":     func(c *Credential) { c.Owner.Definition.ID += "-forged" },
		"definition version": func(c *Credential) { c.Owner.Definition.ContractVersion++ },
	} {
		t.Run("forged "+name, func(t *testing.T) {
			expected := fresh
			forge(&expected)
			if _, err := s.CompareAndSwapConnectionCredential(context.Background(), expected, "secret-b"); err == nil {
				t.Fatal("compare-and-swap accepted forged owner")
			}
		})
	}
	if _, err := s.CompareAndSwapConnectionCredential(context.Background(), fresh, "secret-b"); err != nil {
		t.Fatalf("compare-and-swap with current owner failed: %v", err)
	}
}

func TestProviderDefinitionChangesRevokeCredentialRevision(t *testing.T) {
	methods := []string{"raw SQL", "store update"}
	transitions := []struct {
		name string
		set  func(*testing.T, *Store, ProviderNode, string)
	}{
		{"definition A to B to A", func(t *testing.T, s *Store, n ProviderNode, method string) {
			t.Helper()
			for _, next := range []string{"definition-b", n.DefinitionID} {
				setProviderDefinition(t, s, n, method, next, n.DefinitionContractVersion)
			}
		}},
		{"contract v1 to v2 to v1", func(t *testing.T, s *Store, n ProviderNode, method string) {
			t.Helper()
			for _, next := range []uint64{n.DefinitionContractVersion + 1, n.DefinitionContractVersion} {
				setProviderDefinition(t, s, n, method, n.DefinitionID, next)
			}
		}},
	}
	for _, method := range methods {
		for _, transition := range transitions {
			t.Run(method+"/"+transition.name, func(t *testing.T) {
				s, connection := newCredentialRevisionFixture(t)
				defer s.Close()
				node, err := s.ProviderNode(connection.ProviderNodeID)
				if err != nil {
					t.Fatal(err)
				}
				other, err := s.CreateProviderNode(CreateProviderNodeInput{Name: "Other", Prefix: "other", BaseURL: "https://other.test", Protocol: "openai_chat"})
				if err != nil {
					t.Fatal(err)
				}
				otherConnection, err := s.CreateConnection(CreateConnectionInput{ProviderNodeID: other.ID, Name: "other-account", CredentialType: "api_key", Secret: "other-secret"})
				if err != nil {
					t.Fatal(err)
				}
				stale, _ := s.ConnectionCredentialByID(connection.ID)
				unrelated, _ := s.ConnectionCredentialByID(otherConnection.ID)
				if method == "raw SQL" {
					transition.set(t, s, node, "sql")
				} else {
					transition.set(t, s, node, "store")
				}
				current, ok := s.ConnectionCredentialByID(connection.ID)
				if !ok || current.Revision == stale.Revision || current.Secret != stale.Secret {
					t.Fatalf("definition change did not rotate revision: before=%#v after=%#v found=%v", stale, current, ok)
				}
				if _, err := s.CompareAndSwapConnectionCredential(context.Background(), stale, stale.Secret); err == nil {
					t.Fatal("old credential revision remained valid after definition change")
				}
				if got, ok := s.ConnectionCredentialByID(otherConnection.ID); !ok || got.Revision != unrelated.Revision {
					t.Fatalf("unrelated node credential changed: before=%#v after=%#v found=%v", unrelated, got, ok)
				}
			})
		}
	}
}

func TestProviderRenameAndNoopDefinitionDoNotRotateCredentialRevision(t *testing.T) {
	for _, method := range []string{"raw SQL", "store update"} {
		t.Run(method, func(t *testing.T) {
			s, connection := newCredentialRevisionFixture(t)
			defer s.Close()
			node, err := s.ProviderNode(connection.ProviderNodeID)
			if err != nil {
				t.Fatal(err)
			}
			before, _ := s.ConnectionCredentialByID(connection.ID)
			if method == "raw SQL" {
				if _, err := s.DB.Exec(`UPDATE provider_nodes SET name=name||'-renamed',definition_id=definition_id,definition_contract_version=definition_contract_version WHERE id=?`, node.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := s.UpdateProviderNode(UpdateProviderNodeInput{ID: node.ID, Name: node.Name + "-renamed", DefinitionID: node.DefinitionID, DefinitionContractVersion: node.DefinitionContractVersion}); err != nil {
					t.Fatal(err)
				}
			}
			after, ok := s.ConnectionCredentialByID(connection.ID)
			if !ok || after.Revision != before.Revision {
				t.Fatalf("rename/noop definition rotated revision: before=%#v after=%#v found=%v", before, after, ok)
			}
		})
	}
}

func TestProviderDefinitionRollbackRestoresCredentialRevisionAndOwner(t *testing.T) {
	s, connection := newCredentialRevisionFixture(t)
	defer s.Close()
	before, _ := s.ConnectionCredentialByID(connection.ID)
	tx, err := s.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE provider_nodes SET definition_id=?,definition_contract_version=? WHERE id=?`, "temporary-definition", 9, connection.ProviderNodeID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	after, ok := s.ConnectionCredentialByID(connection.ID)
	if !ok || after.Revision != before.Revision || after.Owner != before.Owner {
		t.Fatalf("rollback did not restore credential revision and owner: before=%#v after=%#v found=%v", before, after, ok)
	}
}

func setProviderDefinition(t *testing.T, s *Store, node ProviderNode, method string, id string, contractVersion uint64) {
	t.Helper()
	if method == "sql" {
		if _, err := s.DB.Exec(`UPDATE provider_nodes SET definition_id=?,definition_contract_version=? WHERE id=?`, id, contractVersion, node.ID); err != nil {
			t.Fatal(err)
		}
		return
	}
	if _, err := s.UpdateProviderNode(UpdateProviderNodeInput{ID: node.ID, DefinitionID: id, DefinitionContractVersion: contractVersion}); err != nil {
		t.Fatal(err)
	}
}
