package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
)

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
	primary, ok := s.ConnectionCredential("node-a")
	if !ok || primary.ID != item.ID || primary.Secret != "do-not-return" {
		t.Fatalf("provider credential=%#v ok=%v", primary, ok)
	}
	if err := s.DeleteConnection(item.ID); err != nil {
		t.Fatal(err)
	}
}

func TestCreateProviderNodeDefaultsGeminiDefinition(t *testing.T) {
	s, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	item, err := s.CreateProviderNode(CreateProviderNodeInput{Name: "Gemini", Prefix: "gemini", BaseURL: "https://generativelanguage.googleapis.com/v1beta", Protocol: "gemini"})
	if err != nil {
		t.Fatal(err)
	}
	if item.DefinitionID != "gemini" {
		t.Fatalf("definition ID=%q", item.DefinitionID)
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

func TestNormalizedRuntimePrimitivesPersistSeparately(t *testing.T) {
	s, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB.Exec(`INSERT INTO provider_nodes(id,name,base_url,protocol,prefix) VALUES('node-a','A','http://a','openai_chat','a'); INSERT INTO connections(id,provider_node_id,name,credential_type) VALUES('conn-a','node-a','A','api_key')`); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveConnectionRuntime(ConnectionRuntime{ConnectionID: "conn-a", Status: "cooldown", BackoffLevel: 2, LastError: "429"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveModelAvailability(ModelAvailability{ConnectionID: "conn-a", ModelRef: "model-a", Status: "blocked", Reason: "quota"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProxyPool(ProxyPool{ID: "pool-a", Name: "test", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveConnectionTransport(ConnectionTransport{ConnectionID: "conn-a", ProxyPoolID: "pool-a", NoProxy: "localhost"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProviderExtension(ProviderExtension{ConnectionID: "conn-a", Namespace: "test", Data: map[string]any{"project": "p"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProviderSession(ProviderSession{ConnectionID: "conn-a", Namespace: "gemini", SessionKey: "session-a", State: kernel.SessionState{ResponseID: "resp-a", ProviderData: json.RawMessage(`{"signature":"opaque"}`), ExpiresAt: time.Now().Add(time.Hour).UTC()}}); err != nil {
		t.Fatal(err)
	}
	sessions, err := s.ProviderSessions()
	if err != nil || len(sessions) != 1 || sessions[0].State.ResponseID != "resp-a" || string(sessions[0].State.ProviderData) != `{"signature":"opaque"}` || sessions[0].State.ExpiresAt.IsZero() {
		t.Fatalf("provider sessions=%#v err=%v", sessions, err)
	}
	if err := s.SaveUsageEvent(kernel.UsageEvent{At: time.Now(), ConnectionID: "conn-a", InputTokens: 2, OutputTokens: 3, EstimatedCost: 0.5}); err != nil {
		t.Fatal(err)
	}
	var runtimeCount, modelCount, transportCount, extensionCount, sessionCount, dailyCount int
	if err := s.DB.QueryRow(`SELECT count(*) FROM connection_runtime`).Scan(&runtimeCount); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT count(*) FROM model_availability`).Scan(&modelCount); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT count(*) FROM connection_transports`).Scan(&transportCount); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT count(*) FROM provider_extensions`).Scan(&extensionCount); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT count(*) FROM provider_sessions`).Scan(&sessionCount); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT count(*) FROM usage_daily`).Scan(&dailyCount); err != nil {
		t.Fatal(err)
	}
	if runtimeCount != 1 || modelCount != 1 || transportCount != 1 || extensionCount != 1 || sessionCount != 1 || dailyCount != 1 {
		t.Fatalf("runtime=%d model=%d transport=%d extension=%d session=%d daily=%d", runtimeCount, modelCount, transportCount, extensionCount, sessionCount, dailyCount)
	}
}
