package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
)

func TestProviderSessionSupersededAuthorityIsTyped(t *testing.T) {
	for _, change := range []string{"principal", "endpoint", "disabled", "expired"} {
		t.Run(change, func(t *testing.T) {
			s, connection := newCredentialRevisionFixture(t)
			defer s.Close()
			route := providerSessionRoute(t, s, connection.ID)
			namespace, err := kernel.EncodeSessionStoreNamespace(route)
			if err != nil {
				t.Fatal(err)
			}
			item := ProviderSession{ConnectionID: connection.ID, Namespace: namespace, SessionKey: "session", State: kernel.SessionState{ResponseID: "old", ExpiresAt: time.Now().Add(time.Hour)}}
			switch change {
			case "principal":
				credential, _ := s.ConnectionCredentialByID(connection.ID)
				if _, err := s.CompareAndSwapConnectionCredential(context.Background(), credential, "replacement", PrincipalReplacement); err != nil {
					t.Fatal(err)
				}
			case "endpoint":
				if _, err := s.DB.Exec(`UPDATE provider_nodes SET base_url='https://new-realm.test' WHERE id=(SELECT provider_node_id FROM connections WHERE id=?)`, connection.ID); err != nil {
					t.Fatal(err)
				}
			case "disabled":
				if _, err := s.DB.Exec(`UPDATE connections SET enabled=0 WHERE id=?`, connection.ID); err != nil {
					t.Fatal(err)
				}
			case "expired":
				item.State.ExpiresAt = time.Now().Add(-time.Second)
			}
			if err := s.SaveProviderSession(item); !errors.Is(err, kernel.ErrSessionStateSuperseded) {
				t.Fatalf("expected typed safe discard, got %v", err)
			}
		})
	}
}

func TestProviderSessionMetadataReadFailureIsNotSuperseded(t *testing.T) {
	s, connection := newCredentialRevisionFixture(t)
	defer s.Close()
	route := providerSessionRoute(t, s, connection.ID)
	namespace, err := kernel.EncodeSessionStoreNamespace(route)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`ALTER TABLE connection_principals RENAME TO unavailable_principals`); err != nil {
		t.Fatal(err)
	}
	err = s.SaveProviderSession(ProviderSession{ConnectionID: connection.ID, Namespace: namespace, SessionKey: "session", State: kernel.SessionState{ResponseID: "response", ExpiresAt: time.Now().Add(time.Hour)}})
	if err == nil || errors.Is(err, kernel.ErrSessionStateSuperseded) {
		t.Fatalf("infrastructure failure hidden as ordinary discard: %v", err)
	}
}
