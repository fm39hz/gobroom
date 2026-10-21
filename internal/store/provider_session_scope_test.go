package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

func providerSessionRoute(t *testing.T, s *Store, connectionID string) kernel.Route {
	t.Helper()
	credential, ok := s.ConnectionCredentialByID(connectionID)
	if !ok {
		t.Fatal("credential unavailable")
	}
	return kernel.Route{
		CredentialID: connectionID, ExternalModel: "model-a", BaseURL: "https://provider.test",
		DefinitionRef:       extensions.Ref{Kind: "provider-definition", ID: credential.Owner.Definition.ID, ContractVersion: credential.Owner.Definition.ContractVersion},
		SessionOperationRef: extensions.Ref{Kind: "operation", ID: string(normalize.OperationChatGenerate), ContractVersion: 1},
		HealthGeneration:    kernel.RouteHealthGeneration{Connection: credential.OperationalGeneration()},
	}
}

func TestProviderSessionScopeAndAuthority(t *testing.T) {
	s, connection := newCredentialRevisionFixture(t)
	defer s.Close()
	route := providerSessionRoute(t, s, connection.ID)
	namespace, err := kernel.EncodeSessionStoreNamespace(route)
	if err != nil {
		t.Fatal(err)
	}
	state := kernel.SessionState{ResponseID: "response-a", ProviderData: json.RawMessage(`{"opaque":true}`), ExpiresAt: time.Now().Add(time.Hour).UTC()}
	item := ProviderSession{ConnectionID: connection.ID, Namespace: namespace, SessionKey: "client-session", State: state}
	if err := s.SaveProviderSession(item); err != nil {
		t.Fatalf("initial save: %v", err)
	}

	// Renewal changes the raw credential revision but keeps the principal scope.
	credential, _ := s.ConnectionCredentialByID(connection.ID)
	if _, err := s.CompareAndSwapConnectionCredential(context.Background(), credential, "renewed-secret", CredentialRenewal); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProviderSession(item); err != nil {
		t.Fatalf("renewal lost continuity: %v", err)
	}

	// A queued write from the prior principal cannot recreate its row.
	credential, _ = s.ConnectionCredentialByID(connection.ID)
	if _, err := s.CompareAndSwapConnectionCredential(context.Background(), credential, "replacement-secret", PrincipalReplacement); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProviderSession(item); err == nil {
		t.Fatal("stale queued write was accepted")
	}
	rows, err := s.ProviderSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("stale principal session survived: %#v", rows)
	}
}

func TestProviderSessionsDropUnsupportedAndEndpointStaleRows(t *testing.T) {
	s, connection := newCredentialRevisionFixture(t)
	defer s.Close()
	route := providerSessionRoute(t, s, connection.ID)
	namespace, err := kernel.EncodeSessionStoreNamespace(route)
	if err != nil {
		t.Fatal(err)
	}
	state := kernel.SessionState{ResponseID: "response-a", ExpiresAt: time.Now().Add(time.Hour).UTC()}
	if err := s.SaveProviderSession(ProviderSession{ConnectionID: connection.ID, Namespace: namespace, SessionKey: "valid", State: state}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProviderSession(ProviderSession{ConnectionID: connection.ID, Namespace: "gemini", SessionKey: "old", State: state}); err == nil {
		t.Fatal("unsupported namespace was accepted")
	}
	if err := s.SaveProviderSession(ProviderSession{ConnectionID: connection.ID, Namespace: namespace, SessionKey: "expired", State: kernel.SessionState{ResponseID: "expired", ExpiresAt: time.Now().Add(-time.Second)}}); err == nil {
		t.Fatal("expired state was accepted")
	}
	if _, err := s.DB.Exec(`UPDATE provider_nodes SET base_url='https://provider.changed' WHERE id=(SELECT provider_node_id FROM connections WHERE id=?)`, connection.ID); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ProviderSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("endpoint stale row survived: %#v", rows)
	}
	var count int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM provider_sessions WHERE connection_id=?`, connection.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("stale rows were not purged: %d", count)
	}
}

func TestProviderSessionsPurgeRawLegacyNamespace(t *testing.T) {
	s, connection := newCredentialRevisionFixture(t)
	defer s.Close()
	_, err := s.DB.Exec(`INSERT INTO provider_sessions(connection_id,namespace,session_key,state_json,expires_at) VALUES(?,?,?,?,?)`, connection.ID, `{"definitionId":"legacy","modelId":"model-a"}`, "legacy", `{"responseId":"old"}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.ProviderSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("legacy row was retained: %#v", rows)
	}
	var count int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM provider_sessions WHERE connection_id=?`, connection.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("legacy row was not purged: %d", count)
	}
}
