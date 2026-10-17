package store

import (
	"context"
	"errors"
	"testing"
)

func TestCredentialRevisionRejectsStaleCompareAndSwap(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *Store, ConnectionRecord)
	}{
		{
			name: "secret A to B to A",
			mutate: func(t *testing.T, s *Store, c ConnectionRecord) {
				t.Helper()
				for _, secret := range []string{"secret-b", "secret-a"} {
					if _, err := s.UpdateConnection(UpdateConnectionInput{ID: c.ID, Secret: secret}); err != nil {
						t.Fatal(err)
					}
				}
			},
		},
		{
			name: "same-secret resave",
			mutate: func(t *testing.T, s *Store, c ConnectionRecord) {
				t.Helper()
				if _, err := s.UpdateConnection(UpdateConnectionInput{ID: c.ID, Secret: "secret-a"}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "disable then enable",
			mutate: func(t *testing.T, s *Store, c ConnectionRecord) {
				t.Helper()
				disabled := false
				if _, err := s.UpdateConnection(UpdateConnectionInput{ID: c.ID, Enabled: &disabled}); err != nil {
					t.Fatal(err)
				}
				enabled := true
				if _, err := s.UpdateConnection(UpdateConnectionInput{ID: c.ID, Enabled: &enabled}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "credential type change",
			mutate: func(t *testing.T, s *Store, c ConnectionRecord) {
				t.Helper()
				if _, err := s.UpdateConnection(UpdateConnectionInput{ID: c.ID, CredentialType: "oauth2"}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "delete and recreate same ID",
			mutate: func(t *testing.T, s *Store, c ConnectionRecord) {
				t.Helper()
				if err := s.DeleteConnection(c.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := s.DB.Exec(`INSERT INTO connections(id,provider_node_id,name,credential_type,secret_ref,enabled) VALUES(?,?,?,?,?,1)`, c.ID, c.ProviderNodeID, c.Name, c.CredentialType, "secret-a"); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, connection := newCredentialRevisionFixture(t)
			defer s.Close()
			stale, ok := s.ConnectionCredentialByID(connection.ID)
			if !ok {
				t.Fatal("initial credential not found")
			}
			tt.mutate(t, s, connection)
			if _, err := s.CompareAndSwapConnectionCredential(context.Background(), stale, "stale-write", PrincipalReplacement); err == nil {
				t.Fatal("stale credential revision was accepted")
			}
		})
	}
}

func TestCredentialCompareAndSwapRotatesRevision(t *testing.T) {
	s, connection := newCredentialRevisionFixture(t)
	defer s.Close()
	old, ok := s.ConnectionCredentialByID(connection.ID)
	if !ok {
		t.Fatal("credential not found")
	}
	commit, err := s.CompareAndSwapConnectionCredential(context.Background(), old, "secret-b", PrincipalReplacement)
	if err != nil {
		t.Fatalf("fresh compare-and-swap failed: %v", err)
	}
	current, ok := s.ConnectionCredentialByID(connection.ID)
	if !ok || current.Secret != "secret-b" || current.Revision == old.Revision {
		t.Fatalf("credential after compare-and-swap = %#v, found=%v", current, ok)
	}
	if commit.Revision != current.Revision || commit.ConnectionID != old.ID || commit.CredentialType != old.Type || commit.Owner != old.Owner {
		t.Fatalf("commit = %#v; credential = %#v", commit, current)
	}
	if _, err := s.CompareAndSwapConnectionCredential(context.Background(), old, "secret-c", PrincipalReplacement); err == nil {
		t.Fatal("old revision was reusable after a successful compare-and-swap")
	}
}

func TestCredentialRevisionPersistsAcrossReopen(t *testing.T) {
	path := t.TempDir() + "/credential-revision.db"
	s, connection := openCredentialRevisionFixture(t, path)
	before, ok := s.ConnectionCredentialByID(connection.ID)
	if !ok {
		t.Fatal("credential not found before reopen")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	after, ok := reopened.ConnectionCredentialByID(connection.ID)
	if !ok || after.Revision != before.Revision {
		t.Fatalf("revision after reopen = %q, before = %q, found=%v", after.Revision, before.Revision, ok)
	}
}

func TestCanceledCredentialCompareAndSwapDoesNotWrite(t *testing.T) {
	s, connection := newCredentialRevisionFixture(t)
	defer s.Close()
	expected, ok := s.ConnectionCredentialByID(connection.ID)
	if !ok {
		t.Fatal("credential not found")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.CompareAndSwapConnectionCredential(ctx, expected, "should-not-write", PrincipalReplacement); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled compare-and-swap error = %v, want context.Canceled", err)
	}
	current, ok := s.ConnectionCredentialByID(connection.ID)
	if !ok || current.Secret != expected.Secret || current.Revision != expected.Revision {
		t.Fatalf("credential changed after canceled compare-and-swap: %#v, found=%v", current, ok)
	}
}

func newCredentialRevisionFixture(t *testing.T) (*Store, ConnectionRecord) {
	t.Helper()
	return openCredentialRevisionFixture(t, t.TempDir()+"/credential-revision.db")
}

func openCredentialRevisionFixture(t *testing.T, path string) (*Store, ConnectionRecord) {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	node, err := s.CreateProviderNode(CreateProviderNodeInput{Name: "Provider", Prefix: "p", BaseURL: "https://provider.test", Protocol: "openai_chat"})
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	connection, err := s.CreateConnection(CreateConnectionInput{ProviderNodeID: node.ID, Name: "account", CredentialType: "api_key", Secret: "secret-a"})
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	return s, connection
}
