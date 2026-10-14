package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/quota"
)

func TestSQLitePragmasApplyToEveryPooledConnection(t *testing.T) {
	s, err := Open(t.TempDir() + "/pooled.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.DB.SetMaxOpenConns(4)
	s.DB.SetMaxIdleConns(0)
	for i := 0; i < 4; i++ {
		connection, err := s.DB.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var journal string
		var synchronous, foreignKeys, busyTimeout int
		err = connection.QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&journal)
		if err == nil {
			err = connection.QueryRowContext(context.Background(), "PRAGMA synchronous").Scan(&synchronous)
		}
		if err == nil {
			err = connection.QueryRowContext(context.Background(), "PRAGMA foreign_keys").Scan(&foreignKeys)
		}
		if err == nil {
			err = connection.QueryRowContext(context.Background(), "PRAGMA busy_timeout").Scan(&busyTimeout)
		}
		closeErr := connection.Close()
		if err != nil {
			t.Fatal(err)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		if journal != "wal" || synchronous != 1 || foreignKeys != 1 || busyTimeout != 5000 {
			t.Fatalf("pooled connection %d pragmas: journal=%q synchronous=%d foreign_keys=%d busy_timeout=%d", i, journal, synchronous, foreignKeys, busyTimeout)
		}
	}
}

func TestTransformBindingFailureModeMigrationDefaultsClosed(t *testing.T) {
	path := t.TempDir() + "/transform-migration.db"
	current, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	binding := kernel.TransformBinding{
		ID: "old-transform", TransformRef: extensions.Ref{Kind: kernel.RequestTransformKind, ID: "fixture.old-transform", ContractVersion: 1},
		Enabled: true, Scope: kernel.TransformScope{Kind: kernel.TransformScopeDaemon},
	}
	if err := current.UpsertTransformBinding(binding); err != nil {
		t.Fatal(err)
	}
	if err := current.Close(); err != nil {
		t.Fatal(err)
	}
	legacyDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacyDB.Exec(`ALTER TABLE transform_bindings DROP COLUMN failure_mode`); err != nil {
		legacyDB.Close()
		t.Fatalf("prepare pre-mode schema fixture: %v", err)
	}
	if err := legacyDB.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	bindings, err := reopened.TransformBindings()
	if err != nil || len(bindings) != 1 || bindings[0].FailureMode != kernel.TransformFailClosed {
		t.Fatalf("migrated transform failure mode=%#v err=%v", bindings, err)
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
	primary, ok := s.ConnectionCredential("node-a")
	if !ok || primary.ID != item.ID || primary.Secret != "do-not-return" {
		t.Fatalf("provider credential=%#v ok=%v", primary, ok)
	}
	updated, err := s.UpdateConnection(UpdateConnectionInput{ID: item.ID, CredentialType: "oauth2"})
	if err != nil || updated.CredentialType != "oauth2" {
		t.Fatalf("change credential type=%#v err=%v", updated, err)
	}
	credential, ok = s.ConnectionCredentialByID(item.ID)
	if !ok || credential.Type != "oauth2" || credential.Secret != "" {
		t.Fatalf("switching auth mode retained the previous mode's secret: %#v ok=%v", credential, ok)
	}
	if err := s.DeleteConnection(item.ID); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialCompareAndSwapDoesNotOverwriteNewerCredential(t *testing.T) {
	s, err := Open(t.TempDir() + "/credential-generation.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	node, err := s.CreateProviderNode(CreateProviderNodeInput{Name: "Provider", Prefix: "p", BaseURL: "https://provider.test", Protocol: "openai_chat"})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := s.CreateConnection(CreateConnectionInput{ProviderNodeID: node.ID, Name: "account", CredentialType: "api_key", Secret: "generation-a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateConnection(UpdateConnectionInput{ID: connection.ID, Secret: "generation-b"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateConnectionSecretIfUnchanged(connection.ID, "generation-a", "stale-oauth-result"); err == nil {
		t.Fatal("stale authorization result overwrote a newer connection credential")
	}
	if err := s.UpdateConnectionSecretIfUnchanged(connection.ID, "generation-b", "generation-c"); err != nil {
		t.Fatal(err)
	}
	stored, ok := s.ConnectionCredentialByID(connection.ID)
	if !ok || stored.Secret != "generation-c" {
		t.Fatalf("credential after compare-and-swap=%#v ok=%v", stored, ok)
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

func TestCompatibilityLossCeilingValidationAndClear(t *testing.T) {
	s, err := Open(t.TempDir() + "/loss-ceiling.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	want := kernel.LossPolicyCeiling{AllowOnly: true, Allow: []string{"reasoning.clamped"}, Deny: []string{"tool.history.dropped"}}
	if err := s.SetCompatibilityLossCeiling(want); err != nil {
		t.Fatal(err)
	}
	got, configured, err := s.CompatibilityLossCeiling()
	if err != nil || !configured || !reflect.DeepEqual(got, want) {
		t.Fatalf("ceiling=%#v configured=%v err=%v", got, configured, err)
	}
	if err := s.SetCompatibilityLossCeiling(kernel.LossPolicyCeiling{Allow: []string{"reasoning.clamped"}}); err == nil {
		t.Fatal("allowlist without allowOnly was accepted")
	}
	if err := s.SetCompatibilityLossCeiling(kernel.LossPolicyCeiling{}); err != nil {
		t.Fatal(err)
	}
	got, configured, err = s.CompatibilityLossCeiling()
	if err != nil || configured || !reflect.DeepEqual(got, kernel.LossPolicyCeiling{}) {
		t.Fatalf("cleared ceiling=%#v configured=%v err=%v", got, configured, err)
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
	remaining := float64(0)
	observedAt := time.Now().UTC()
	if err := s.SaveQuotaSnapshot(quota.Snapshot{ProviderNodeID: "node-a", ConnectionID: "conn-a", ModelRef: "model-a", WindowName: "daily", Generation: "generation-a", Authority: quota.AuthorityResponseHeader, ObservedAt: observedAt, Remaining: &remaining, Source: "response_header", RoutingEffect: quota.RoutingEffectObserve}); err != nil {
		t.Fatal(err)
	}
	if snapshots, err := s.QuotaSnapshots(); err != nil || len(snapshots) != 1 || snapshots[0].Generation != "generation-a" || snapshots[0].Authority != quota.AuthorityResponseHeader || snapshots[0].RoutingEffect != quota.RoutingEffectObserve || !snapshots[0].ObservedAt.Equal(observedAt) {
		t.Fatalf("quota generation did not round-trip: %#v err=%v", snapshots, err)
	}
	olderRemaining := float64(20)
	if err := s.SaveQuotaSnapshot(quota.Snapshot{ProviderNodeID: "node-a", ConnectionID: "conn-a", ModelRef: "model-a", WindowName: "daily", Generation: "generation-a", Authority: quota.AuthorityQuotaAPI, ObservedAt: time.Now().Add(-time.Minute), Remaining: &olderRemaining, Source: "http-json-quota"}); err != nil {
		t.Fatal(err)
	}
	if snapshots, err := s.QuotaSnapshots(); err != nil || len(snapshots) != 1 || snapshots[0].Remaining == nil || *snapshots[0].Remaining != 0 || snapshots[0].Authority != quota.AuthorityResponseHeader || snapshots[0].RoutingEffect != quota.RoutingEffectObserve {
		t.Fatalf("older quota API write overwrote newer stored observation: %#v err=%v", snapshots, err)
	}
	equalTimeAPI := quota.Snapshot{ProviderNodeID: "node-a", ConnectionID: "conn-a", ModelRef: "model-a", WindowName: "daily", Generation: "generation-a", Authority: quota.AuthorityQuotaAPI, ObservedAt: observedAt, Remaining: &olderRemaining, Source: "http-json-quota"}
	if err := s.SaveQuotaSnapshot(equalTimeAPI); err != nil {
		t.Fatal(err)
	}
	if snapshots, err := s.QuotaSnapshots(); err != nil || len(snapshots) != 1 || snapshots[0].Remaining == nil || *snapshots[0].Remaining != 0 || snapshots[0].Authority != quota.AuthorityResponseHeader || snapshots[0].RoutingEffect != quota.RoutingEffectObserve {
		t.Fatalf("equal-time lower-authority write overwrote header evidence: %#v err=%v", snapshots, err)
	}
	if err := s.SaveProviderRuntime(ProviderRuntime{ProviderNodeID: "node-a", Status: "cooldown", ConsecutiveFailures: 1, ErrorCode: "capacity"}); err != nil {
		t.Fatal(err)
	}
	providerRuntime, err := s.ProviderRuntimes()
	if err != nil || len(providerRuntime) != 1 || providerRuntime[0].ProviderNodeID != "node-a" || providerRuntime[0].ErrorCode != "capacity" {
		t.Fatalf("provider runtime=%#v err=%v", providerRuntime, err)
	}
	if err := s.SaveModelAvailability(ModelAvailability{ConnectionID: "conn-a", ModelRef: "model-a", Status: "invalid", Reason: "model_not_found", ErrorCode: "route"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetConnectionRoutingState("conn-a"); err != nil {
		t.Fatal(err)
	}
	if snapshots, err := s.QuotaSnapshots(); err != nil || len(snapshots) != 0 {
		t.Fatalf("connection credential recovery retained stale quota snapshots: %#v err=%v", snapshots, err)
	}
	if runtimes, err := s.ConnectionRuntimes(); err != nil || len(runtimes) != 1 || runtimes[0].Status != "available" {
		t.Fatalf("connection health reset=%#v err=%v", runtimes, err)
	}
	availability, err := s.ModelAvailabilities()
	if err != nil || len(availability) != 1 {
		t.Fatalf("connection model health reset=%#v err=%v", availability, err)
	}
	for _, model := range availability {
		if model.Status != "available" {
			t.Fatalf("connection model health reset did not clear invalid state: %#v", availability)
		}
	}
	if err := s.SaveProviderRuntime(ProviderRuntime{ProviderNodeID: "node-a", Status: "invalid", ConsecutiveFailures: 2, ErrorCode: "auth"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveQuotaSnapshot(quota.Snapshot{ProviderNodeID: "node-a", ConnectionID: "conn-a", ModelRef: "model-a", WindowName: "daily", Remaining: &remaining}); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetProviderRoutingState("node-a"); err != nil {
		t.Fatal(err)
	}
	if snapshots, err := s.QuotaSnapshots(); err != nil || len(snapshots) != 0 {
		t.Fatalf("provider configuration recovery retained stale quota snapshots: %#v err=%v", snapshots, err)
	}
	providerRuntime, err = s.ProviderRuntimes()
	if err != nil || len(providerRuntime) != 1 || providerRuntime[0].Status != "available" {
		t.Fatalf("provider health reset=%#v err=%v", providerRuntime, err)
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
	loss := kernel.LossRecord{ID: "reasoning.clamped", Requested: "xhigh", Effective: "high", SemanticPaths: []string{"request.reasoning.effort"}, PolicySources: []string{"combo.deep"}}
	plan := &kernel.CompatibilityPlanSummary{
		Supported: true, Fidelity: kernel.FidelityLossy,
		Mappings:          []kernel.CompatibilityMappingSummary{{Facet: "reasoning.effort", Paths: []string{"request.reasoning.effort"}, Disposition: kernel.FacetDegraded, Reason: "clamped"}},
		TransformFailures: []kernel.TransformFailure{{BindingID: "best-effort", TransformRef: extensions.Ref{Kind: kernel.RequestTransformKind, ID: "fixture.best-effort", ContractVersion: 1}, Scope: kernel.TransformScope{Kind: kernel.TransformScopeModel, ID: "junior"}, Mode: kernel.TransformSafeFailOpen, Reason: kernel.TransformFailureApply}},
	}
	if err := s.SaveUsageEvent(kernel.UsageEvent{At: time.Now(), ConnectionID: "conn-a", InputTokens: 2, OutputTokens: 3, EstimatedCost: 0.5, CompatibilityFidelity: kernel.FidelityLossy, CompatibilityLosses: []kernel.LossRecord{loss}, CompatibilityPlan: plan}); err != nil {
		t.Fatal(err)
	}
	usage, err := s.UsageEvents(1)
	if err != nil || len(usage) != 1 || usage[0].CompatibilityFidelity != kernel.FidelityLossy || len(usage[0].CompatibilityLosses) != 1 || !reflect.DeepEqual(usage[0].CompatibilityLosses[0], loss) || !reflect.DeepEqual(usage[0].CompatibilityPlan, plan) {
		t.Fatalf("compatibility outcome was not persisted with usage: %#v err=%v", usage, err)
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
