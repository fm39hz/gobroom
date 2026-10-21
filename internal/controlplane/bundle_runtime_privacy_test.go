package controlplane

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/quota"
	"github.com/fm39hz/gobroom/internal/store"
)

func TestConfigBundleExcludesLiveRuntimeState(t *testing.T) {
	runtime, err := provider.NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := runtime.FreezeCatalog()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := kernel.NewStrategyCatalog(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	open := func(name string) *store.Store {
		t.Helper()
		s, err := store.Open(t.TempDir() + "/" + name + ".db")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	source := open("source")
	if _, err := source.DB.Exec(`INSERT INTO provider_nodes(id,name,base_url,protocol,definition_id,prefix) VALUES('privacy-node','Privacy Provider','https://provider.test/v1','openai_chat','openai-compatible-chat','pv');
INSERT INTO connections(id,provider_node_id,name,credential_type,secret_ref) VALUES('privacy-connection','privacy-node','Primary','api_key','secret-reference-marker');
INSERT INTO model_catalog(id,provider_node_id,kind,external_id,display_name,capabilities_json,limits_json) VALUES('privacy-model','privacy-node','custom','vendor/model','Privacy Model','{}','{}');
INSERT INTO model_connection_assignments(model_id,connection_id,source) VALUES('privacy-model','privacy-connection','user_assignment');`); err != nil {
		t.Fatal(err)
	}
	// These markers live only in runtime tables. If a future exporter starts
	// serializing runtime rows, the structural and marker checks below will fail.
	credential, ok := source.ConnectionCredentialByID("privacy-connection")
	if !ok {
		t.Fatal("source credential missing")
	}
	definitionRef := provider.ProviderDefinitionRef("openai-compatible-chat", 1)
	route := kernel.Route{CredentialID: "privacy-connection", DefinitionID: definitionRef.ID, DefinitionRef: definitionRef, SessionOperationRef: extensions.Ref{Kind: "operation", ID: string(normalize.OperationChatGenerate), ContractVersion: 1}, ExternalModel: "vendor/model", BaseURL: "https://provider.test/v1", HealthGeneration: kernel.RouteHealthGeneration{Connection: credential.OperationalGeneration()}}
	namespace, err := kernel.EncodeSessionStoreNamespace(route)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := kernel.SignatureContextDigest(normalize.Request{Messages: []normalize.Message{{Role: "user", Content: "private context"}}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := kernel.NewSignatureProof("runtime-signature-proof-marker", definitionRef, digest, normalize.ToolCall{ID: "call-private", Type: "function", Name: "lookup", Arguments: map[string]any{"id": 7}})
	if err != nil {
		t.Fatal(err)
	}
	if err := source.SaveProviderSession(store.ProviderSession{ConnectionID: "privacy-connection", Namespace: namespace, SessionKey: "runtime-session-key-marker", State: kernel.SessionState{ResponseID: "runtime-response-id-marker", ProviderData: []byte(`{"opaque":"runtime-opaque-marker"}`), Signatures: []kernel.SignatureProof{proof}, ExpiresAt: time.Now().Add(time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	if rows, err := source.ProviderSessions(); err != nil || len(rows) != 1 {
		t.Fatalf("source session is not valid live state: rows=%#v err=%v", rows, err)
	}
	if _, err := source.DB.Exec(`INSERT INTO connection_runtime(connection_id,status,last_error,error_code,generation) VALUES('privacy-connection','degraded','runtime-health-marker','runtime-error-code-marker','runtime-health-generation-marker');
INSERT INTO provider_runtime(provider_node_id,status,last_error,error_code,generation) VALUES('privacy-node','degraded','provider-health-marker','provider-error-marker','provider-generation-marker');
INSERT INTO model_availability(connection_id,model_ref,status,reason,last_error,error_code,generation) VALUES('privacy-connection','privacy-model','blocked','runtime-quota-marker','model-health-marker','model-error-marker','model-generation-marker');
INSERT INTO usage_events(timestamp,logical_model,provider_node_id,external_model,connection_id,status,input_tokens,output_tokens,estimated_cost,compatibility_fidelity) VALUES(CURRENT_TIMESTAMP,'runtime-logical-marker','privacy-node','runtime-external-marker','privacy-connection','runtime-usage-marker',12345,6789,987.65,'runtime-fidelity-marker');`); err != nil {
		t.Fatal(err)
	}
	remaining := 17.0
	if err := source.SaveQuotaSnapshot(quota.Snapshot{ProviderNodeID: "privacy-node", ConnectionID: "privacy-connection", ModelRef: "privacy-model", WindowName: "daily", Generation: "runtime-quota-generation-marker", Used: 83, Remaining: &remaining, Source: "runtime-quota-source-marker"}); err != nil {
		t.Fatal(err)
	}
	// Also seed auth runtime evidence (including signature/proof-like data) to
	// guard against accidentally broadening the bundle to runtime state.
	if _, err := source.DB.Exec(`INSERT INTO auth_runtime(scope,provider_node_id,connection_id,model_ref,route_id,operation_id,operation_version,generation,credential_revision,status,failures,successes,blocked_until,last_error,cause,evidence,confidence,last_attempt,last_success,observed_at_ns) VALUES('route','privacy-node','privacy-connection','privacy-model','runtime-route-marker','chat.generate',1,'runtime-auth-generation-marker','runtime-credential-revision-marker','blocked',1,0,'','runtime-auth-error-marker','runtime-auth-cause-marker','runtime-signature-proof-marker',1,'','',1)`); err != nil {
		t.Fatal(err)
	}

	bundle, err := ExportBundle(source, catalog.Extensions())
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Providers) != 1 || bundle.Providers[0].ID != "privacy-node" || len(bundle.Connections) != 1 || bundle.Connections[0].ID != "privacy-connection" || len(bundle.Models) != 1 || bundle.Models[0].ID != "privacy-model" {
		t.Fatalf("configuration entities were not preserved: providers=%#v connections=%#v models=%#v", bundle.Providers, bundle.Connections, bundle.Models)
	}
	encoded, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"providerSessions", "connectionRuntime", "providerRuntime", "modelAvailability", "operationRuntime", "authRuntime",
		"quotaSnapshots", "operationQuotaSnapshots", "usageEvents", "usageSummary", "providerExtensions", "connectionTransports",
		"responseId", "providerData", "signatureProof", "lastError", "last_error", "remaining", "used", "inputTokens", "outputTokens",
	} {
		if containsJSONKey(decoded, key) {
			t.Errorf("runtime JSON field %q appeared in exported bundle", key)
		}
	}
	for _, marker := range []string{
		"runtime-session-key-marker", "runtime-response-id-marker", "runtime-opaque-marker", "runtime-signature-proof-marker",
		"runtime-health-marker", "runtime-error-code-marker", "runtime-health-generation-marker", "provider-health-marker",
		"runtime-quota-marker", "runtime-quota-generation-marker", "runtime-quota-source-marker", "runtime-usage-marker",
		"runtime-logical-marker", "runtime-external-marker", "runtime-auth-generation-marker", "runtime-auth-error-marker",
		"runtime-auth-cause-marker", "runtime-credential-revision-marker", "secret-reference-marker",
	} {
		if strings.Contains(string(encoded), marker) {
			t.Errorf("runtime or secret source marker %q leaked into bundle JSON", marker)
		}
	}

	target := open("target")
	if err := ApplyBundle(target, bundle, catalog); err != nil {
		t.Fatalf("apply exported bundle to fresh target: %v", err)
	}
	assertZero := func(table string) {
		t.Helper()
		var count int
		if err := target.DB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Errorf("fresh bundle target contains %d rows in runtime table %s", count, table)
		}
	}
	for _, table := range []string{"provider_sessions", "connection_runtime", "provider_runtime", "model_availability", "operation_runtime", "auth_runtime", "quota_snapshots", "operation_quota_snapshots", "usage_events"} {
		assertZero(table)
	}
	importedProviders, err := target.ProviderNodes()
	if err != nil || len(importedProviders) != 1 || importedProviders[0].ID != "privacy-node" {
		t.Errorf("provider configuration did not survive import: %#v err=%v", importedProviders, err)
	}
	importedConnections, err := target.Connections("")
	if err != nil || len(importedConnections) != 1 || importedConnections[0].ID != "privacy-connection" {
		t.Errorf("connection configuration did not survive import: %#v err=%v", importedConnections, err)
	}
	importedModels, err := target.Models()
	if err != nil || len(importedModels) != 1 || importedModels[0].ID != "privacy-model" {
		t.Errorf("model configuration did not survive import: %#v err=%v", importedModels, err)
	}
}

func containsJSONKey(value any, key string) bool {
	switch item := value.(type) {
	case map[string]any:
		for name, child := range item {
			if name == key || containsJSONKey(child, key) {
				return true
			}
		}
	case []any:
		for _, child := range item {
			if containsJSONKey(child, key) {
				return true
			}
		}
	}
	return false
}
