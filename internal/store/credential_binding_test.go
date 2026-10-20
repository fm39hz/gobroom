package store

import (
	"context"
	"errors"
	"testing"
)

func TestConnectionCredentialBindingMatchesCredentialMetadata(t *testing.T) {
	s, connection := newCredentialRevisionFixture(t)
	defer s.Close()

	credential, ok := s.ConnectionCredentialByID(connection.ID)
	if !ok {
		t.Fatal("credential not found")
	}
	binding, err := s.ConnectionCredentialBinding(context.Background(), connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := CredentialBinding{ID: credential.ID, Type: credential.Type, Revision: credential.Revision, PrincipalGeneration: credential.PrincipalGeneration, Owner: credential.Owner}
	if binding != want {
		t.Fatalf("binding = %#v, want %#v", binding, want)
	}
	if binding.Owner.Definition.Kind != "provider-definition" {
		t.Fatalf("owner definition kind = %q", binding.Owner.Definition.Kind)
	}
}

func TestConnectionCredentialBindingTracksCredentialAndOwnerChanges(t *testing.T) {
	t.Run("same-secret update rotates revision only", func(t *testing.T) {
		s, connection := newCredentialRevisionFixture(t)
		defer s.Close()
		before, err := s.ConnectionCredentialBinding(context.Background(), connection.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.UpdateConnection(UpdateConnectionInput{ID: connection.ID, Secret: "secret-a"}); err != nil {
			t.Fatal(err)
		}
		after, err := s.ConnectionCredentialBinding(context.Background(), connection.ID)
		if err != nil {
			t.Fatal(err)
		}
		if after.Revision == before.Revision || after.PrincipalGeneration != before.PrincipalGeneration {
			t.Fatalf("before=%#v after=%#v", before, after)
		}
	})

	t.Run("renewal CAS rotates revision and preserves principal", func(t *testing.T) {
		s, connection := newCredentialRevisionFixture(t)
		defer s.Close()
		before, err := s.ConnectionCredentialBinding(context.Background(), connection.ID)
		if err != nil {
			t.Fatal(err)
		}
		credential, ok := s.ConnectionCredentialByID(connection.ID)
		if !ok {
			t.Fatal("credential not found")
		}
		if _, err := s.CompareAndSwapConnectionCredential(context.Background(), credential, "secret-b", CredentialRenewal); err != nil {
			t.Fatal(err)
		}
		after, err := s.ConnectionCredentialBinding(context.Background(), connection.ID)
		if err != nil {
			t.Fatal(err)
		}
		if after.Revision == before.Revision || after.PrincipalGeneration != before.PrincipalGeneration {
			t.Fatalf("before=%#v after=%#v", before, after)
		}
	})

	t.Run("principal replacement changes generation", func(t *testing.T) {
		s, connection := newCredentialRevisionFixture(t)
		defer s.Close()
		before, err := s.ConnectionCredentialBinding(context.Background(), connection.ID)
		if err != nil {
			t.Fatal(err)
		}
		credential, ok := s.ConnectionCredentialByID(connection.ID)
		if !ok {
			t.Fatal("credential not found")
		}
		if _, err := s.CompareAndSwapConnectionCredential(context.Background(), credential, "secret-b", PrincipalReplacement); err != nil {
			t.Fatal(err)
		}
		after, err := s.ConnectionCredentialBinding(context.Background(), connection.ID)
		if err != nil {
			t.Fatal(err)
		}
		if after.Revision == before.Revision || after.PrincipalGeneration == before.PrincipalGeneration {
			t.Fatalf("before=%#v after=%#v", before, after)
		}
	})
}

func TestConnectionCredentialBindingUnavailableAfterRevocation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *Store, ConnectionRecord)
	}{
		{name: "disable", mutate: func(t *testing.T, s *Store, c ConnectionRecord) {
			disabled := false
			if _, err := s.UpdateConnection(UpdateConnectionInput{ID: c.ID, Enabled: &disabled}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "delete", mutate: func(t *testing.T, s *Store, c ConnectionRecord) {
			if err := s.DeleteConnection(c.ID); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, connection := newCredentialRevisionFixture(t)
			defer s.Close()
			if _, err := s.ConnectionCredentialBinding(context.Background(), connection.ID); err != nil {
				t.Fatal(err)
			}
			tt.mutate(t, s, connection)
			if binding, err := s.ConnectionCredentialBinding(context.Background(), connection.ID); err == nil || binding != (CredentialBinding{}) {
				t.Fatalf("binding=%#v err=%v", binding, err)
			}
		})
	}
}

func TestConnectionCredentialBindingChangesWhenOwnerIsRebound(t *testing.T) {
	s, connection := newCredentialRevisionFixture(t)
	defer s.Close()
	before, err := s.ConnectionCredentialBinding(context.Background(), connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := s.CreateProviderNode(CreateProviderNodeInput{Name: "Replacement", Prefix: "r", BaseURL: "https://replacement.test", Protocol: "openai_chat"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`UPDATE connections SET provider_node_id=? WHERE id=?`, replacement.ID, connection.ID); err != nil {
		t.Fatal(err)
	}
	after, err := s.ConnectionCredentialBinding(context.Background(), connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before == after || after.Owner.ProviderNodeID != replacement.ID {
		t.Fatalf("before=%#v after=%#v", before, after)
	}
}

func TestConnectionCredentialBindingChangesWhenProviderDefinitionChanges(t *testing.T) {
	s, connection := newCredentialRevisionFixture(t)
	defer s.Close()
	before, err := s.ConnectionCredentialBinding(context.Background(), connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`UPDATE provider_nodes SET definition_id=?,definition_contract_version=definition_contract_version+1 WHERE id=?`, "replacement-definition", connection.ProviderNodeID); err != nil {
		t.Fatal(err)
	}
	after, err := s.ConnectionCredentialBinding(context.Background(), connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before == after || after.Owner.Definition.ID != "replacement-definition" {
		t.Fatalf("before=%#v after=%#v", before, after)
	}
}

func TestConnectionCredentialBindingHonorsCanceledContext(t *testing.T) {
	s, connection := newCredentialRevisionFixture(t)
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	binding, err := s.ConnectionCredentialBinding(ctx, connection.ID)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want context.Canceled", err)
	}
	if binding != (CredentialBinding{}) {
		t.Fatalf("canceled read returned data: %#v", binding)
	}
}

func TestConnectionCredentialBindingMissingAndDisabledAreErrorSafe(t *testing.T) {
	s, connection := newCredentialRevisionFixture(t)
	defer s.Close()
	if binding, err := s.ConnectionCredentialBinding(context.Background(), "missing"); err == nil || binding != (CredentialBinding{}) {
		t.Fatalf("missing binding=%#v err=%v", binding, err)
	}
	disabled := false
	if _, err := s.UpdateConnection(UpdateConnectionInput{ID: connection.ID, Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	if binding, err := s.ConnectionCredentialBinding(context.Background(), connection.ID); err == nil || binding != (CredentialBinding{}) {
		t.Fatalf("disabled binding=%#v err=%v", binding, err)
	}
}
