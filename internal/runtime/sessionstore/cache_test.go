package sessionstore

import (
	"context"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/store"
)

type cacheFixtureRepository struct{ rows []store.ProviderSession }

func (r *cacheFixtureRepository) ProviderSessions() ([]store.ProviderSession, error) {
	return r.rows, nil
}
func (*cacheFixtureRepository) SaveProviderSession(store.ProviderSession) error       { return nil }
func (*cacheFixtureRepository) PruneExpiredProviderSessions(time.Time) (int64, error) { return 0, nil }

func TestCacheDropsUnsupportedNamespaceFromRepository(t *testing.T) {
	repository := &cacheFixtureRepository{rows: []store.ProviderSession{{ConnectionID: "account", Namespace: `{"definitionId":"legacy"}`, SessionKey: "session", State: kernel.SessionState{ResponseID: "legacy"}}}}
	cache, err := New(repository)
	if err != nil {
		t.Fatal(err)
	}
	if len(cache.entries) != 0 {
		t.Fatalf("unsupported namespace was preloaded: %#v", cache.entries)
	}
}

func TestCachePersistsSessionStateOffTheReadPath(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/sessions.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.DB.Exec(`INSERT INTO provider_nodes(id,name,base_url,protocol,prefix,definition_id,definition_contract_version) VALUES('node','Provider','https://provider.test','openai_responses','provider','openai-compatible-responses',1); INSERT INTO connections(id,provider_node_id,name,credential_type) VALUES('account','node','Account','api_key')`); err != nil {
		t.Fatal(err)
	}
	legacyExpiry := time.Now().Add(time.Hour).UTC()
	if _, err := db.DB.Exec(`INSERT INTO provider_sessions(connection_id,namespace,session_key,state_json,expires_at) VALUES(?,?,?,?,?)`, "account", `{"definitionId":"openai-compatible-responses","modelId":"gpt-model"}`, "legacy-session", `{"responseId":"legacy-response"}`, legacyExpiry.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	cache, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(cache.entries) != 0 {
		t.Fatalf("old unversioned session namespace was loaded: %#v", cache.entries)
	}
	cache.ttl = 2 * time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		cache.Run(ctx)
	}()
	credential, ok := db.ConnectionCredentialByID("account")
	if !ok {
		t.Fatal("connection credential unavailable")
	}
	route := kernel.Route{DefinitionID: "openai-compatible-responses", DefinitionRef: extensions.Ref{Kind: "provider-definition", ID: "openai-compatible-responses", ContractVersion: 1}, SessionOperationRef: extensions.Ref{Kind: "operation", ID: string(normalize.OperationChatGenerate), ContractVersion: 1}, ExternalModel: "gpt-model", CredentialID: "account", BaseURL: "https://provider.test", HealthGeneration: kernel.RouteHealthGeneration{Connection: credential.OperationalGeneration()}}
	providerExpiry := time.Now().Add(time.Hour).UTC()
	proofRequest := normalize.Request{Prompt: normalize.PromptPlan{Layers: []normalize.PromptLayer{{Origin: normalize.PromptHarness, Text: "system"}}}, Messages: []normalize.Message{{Role: "user", Content: "turn"}}, Tools: []normalize.Tool{{Type: "function", Name: "lookup"}}}
	proofContext, err := kernel.SignatureContextDigest(proofRequest, len(proofRequest.Messages))
	if err != nil {
		t.Fatal(err)
	}
	proof, err := kernel.NewSignatureProof("provider-opaque-signature", route.DefinitionRef, proofContext, normalize.ToolCall{ID: "call-1", Type: "function", Name: "lookup", Arguments: map[string]any{"id": 7}})
	if err != nil {
		t.Fatal(err)
	}
	state := kernel.SessionState{ResponseID: "resp_1", ProviderData: []byte(`{"opaque":"v"}`), Signatures: []kernel.SignatureProof{proof}, ExpiresAt: providerExpiry}
	if err := cache.Save(context.Background(), route, "client-session", state); err != nil {
		t.Fatal(err)
	}
	state.Signatures[0].Digest = "mutated-after-save"
	loaded, found, err := cache.Load(context.Background(), route, "client-session")
	if err != nil || !found || loaded.ResponseID != state.ResponseID || string(loaded.ProviderData) != string(state.ProviderData) || !loaded.ExpiresAt.Equal(providerExpiry) || len(loaded.Signatures) != 1 || loaded.Signatures[0] != proof {
		t.Fatalf("cached=%#v found=%v err=%v", loaded, found, err)
	}
	longProviderExpiry := time.Now().Add(4 * time.Hour).UTC()
	if err := cache.Save(context.Background(), route, "client-session-long", kernel.SessionState{ResponseID: "resp_2", ExpiresAt: longProviderExpiry}); err != nil {
		t.Fatal(err)
	}
	bounded, found, err := cache.Load(context.Background(), route, "client-session-long")
	if err != nil || !found || !bounded.ExpiresAt.Before(longProviderExpiry) || time.Until(bounded.ExpiresAt) > 2*time.Hour {
		t.Fatalf("provider session expiry was not bounded by the cache TTL: state=%#v found=%v err=%v", bounded, found, err)
	}
	otherOperation := route
	otherOperation.SessionOperationRef = extensions.Ref{Kind: "operation", ID: "responses.generate", ContractVersion: 1}
	if err := cache.Save(context.Background(), otherOperation, "client-session", kernel.SessionState{ResponseID: "resp_other_operation"}); err != nil {
		t.Fatal(err)
	}
	stillChat, found, err := cache.Load(context.Background(), route, "client-session")
	if err != nil || !found || stillChat.ResponseID != state.ResponseID {
		t.Fatalf("durable session state crossed operation scope: state=%#v found=%v err=%v", stillChat, found, err)
	}
	cancel()
	<-done
	rows, err := db.ProviderSessions()
	persistedExpiries := map[string]time.Time{}
	for _, row := range rows {
		persistedExpiries[row.State.ResponseID] = row.State.ExpiresAt
	}
	if err != nil || len(rows) != 3 || !persistedExpiries["resp_1"].Equal(providerExpiry) || !persistedExpiries["resp_2"].Equal(bounded.ExpiresAt) {
		t.Fatalf("persisted=%#v err=%v", rows, err)
	}
	reloaded, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.entries) != 3 {
		t.Fatalf("reloaded unsupported namespaces into the cache: %#v", reloaded.entries)
	}
	stateAfterRestart, found, err := reloaded.Load(context.Background(), route, "client-session")
	if err != nil || !found || stateAfterRestart.ResponseID != "resp_1" || len(stateAfterRestart.Signatures) != 1 || stateAfterRestart.Signatures[0] != proof {
		t.Fatalf("reloaded=%#v found=%v err=%v", stateAfterRestart, found, err)
	}
	otherOperationAfterRestart, found, err := reloaded.Load(context.Background(), otherOperation, "client-session")
	if err != nil || !found || otherOperationAfterRestart.ResponseID != "resp_other_operation" {
		t.Fatalf("operation-scoped durable state did not survive restart: state=%#v found=%v err=%v", otherOperationAfterRestart, found, err)
	}
}
