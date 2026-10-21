package sessionstore

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/store"
)

type cacheFixtureRepository struct {
	mu      sync.Mutex
	rows    []store.ProviderSession
	saveErr error
	entered chan struct{}
	resume  chan struct{}
}

func (r *cacheFixtureRepository) ProviderSessions() ([]store.ProviderSession, error) {
	return r.rows, nil
}
func (r *cacheFixtureRepository) SaveProviderSession(row store.ProviderSession) error {
	if r.entered != nil {
		select {
		case r.entered <- struct{}{}:
		default:
		}
	}
	if r.resume != nil {
		<-r.resume
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.saveErr != nil {
		return r.saveErr
	}
	for i := range r.rows {
		if r.rows[i].ConnectionID == row.ConnectionID && r.rows[i].Namespace == row.Namespace && r.rows[i].SessionKey == row.SessionKey {
			r.rows[i] = row
			return nil
		}
	}
	r.rows = append(r.rows, row)
	return nil
}
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

func invalidationRoute() kernel.Route {
	route := kernel.Route{DefinitionID: "openai-compatible-responses", DefinitionRef: extensions.Ref{Kind: "provider-definition", ID: "openai-compatible-responses", ContractVersion: 1}, SessionOperationRef: extensions.Ref{Kind: "operation", ID: string(normalize.OperationChatGenerate), ContractVersion: 1}, ExternalModel: "model", CredentialID: "account", BaseURL: "https://provider.test"}
	route.HealthGeneration.Connection = "principal-a"
	return route
}

func TestCacheInvalidatePersistsTombstoneAcrossRestart(t *testing.T) {
	r := &cacheFixtureRepository{}
	c, err := New(r)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.Run(ctx) }()
	route := invalidationRoute()
	state := kernel.SessionState{ResponseID: "old", ProviderData: []byte(`{"a":1}`), ExpiresAt: time.Now().Add(time.Hour)}
	if err := c.Save(context.Background(), route, "s", state); err != nil {
		t.Fatal(err)
	}
	state, _, _ = c.Load(context.Background(), route, "s")
	ok, err := c.Invalidate(context.Background(), route, "s", state)
	if err != nil || !ok {
		t.Fatalf("invalidate=%v err=%v", ok, err)
	}
	cancel()
	<-done
	reloaded, err := New(r)
	if err != nil {
		t.Fatal(err)
	}
	loaded, found, err := reloaded.Load(context.Background(), route, "s")
	if err != nil || found || loaded.ResponseID != "" {
		t.Fatalf("loaded=%#v found=%v err=%v", loaded, found, err)
	}
}

func TestCacheInvalidateRejectsStaleExpectedState(t *testing.T) {
	r := &cacheFixtureRepository{}
	c, _ := New(r)
	route := invalidationRoute()
	newer := kernel.SessionState{ResponseID: "new", ExpiresAt: time.Now().Add(time.Hour)}
	if err := c.Save(context.Background(), route, "s", newer); err != nil {
		t.Fatal(err)
	}
	ok, err := c.Invalidate(context.Background(), route, "s", kernel.SessionState{ResponseID: "old"})
	loaded, found, loadErr := c.Load(context.Background(), route, "s")
	if err != nil || loadErr != nil || ok || !found || loaded.ResponseID != "new" {
		t.Fatalf("invalidate=%v err=%v loaded=%#v found=%v loadErr=%v", ok, err, loaded, found, loadErr)
	}
}

func TestCacheInvalidateProtectsIdenticalResave(t *testing.T) {
	c, err := New(&cacheFixtureRepository{})
	if err != nil {
		t.Fatal(err)
	}
	route := invalidationRoute()
	ctx := context.Background()
	if err := c.Save(ctx, route, "same", kernel.SessionState{ResponseID: "same", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	observed, _, err := c.Load(ctx, route, "same")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Save(ctx, route, "same", observed); err != nil {
		t.Fatal(err)
	}
	removed, err := c.Invalidate(ctx, route, "same", observed)
	latest, found, loadErr := c.Load(ctx, route, "same")
	if err != nil || loadErr != nil || removed || !found || latest.ResponseID != observed.ResponseID || latest.StoreVersion == observed.StoreVersion || latest.StoreVersion == 0 {
		t.Fatalf("identical resave lost: removed=%v latest=%#v found=%v err=%v loadErr=%v", removed, latest, found, err, loadErr)
	}
}

func TestCacheInvalidateFullQueueDoesNotMutateEntry(t *testing.T) {
	c, _ := New(&cacheFixtureRepository{})
	route := invalidationRoute()
	state := kernel.SessionState{ResponseID: "current", ExpiresAt: time.Now().Add(time.Hour)}
	if err := c.Save(context.Background(), route, "s", state); err != nil {
		t.Fatal(err)
	}
	state, _, _ = c.Load(context.Background(), route, "s")
	for len(c.writes) < cap(c.writes) {
		c.writes <- write{}
	}
	ok, err := c.Invalidate(context.Background(), route, "s", state)
	loaded, found, loadErr := c.Load(context.Background(), route, "s")
	if err == nil || ok || loadErr != nil || !found || loaded.ResponseID != state.ResponseID {
		t.Fatalf("invalidate=%v err=%v loaded=%#v found=%v loadErr=%v", ok, err, loaded, found, loadErr)
	}
}

func TestCacheInvalidateCancellationAndPersistenceFailure(t *testing.T) {
	r := &cacheFixtureRepository{entered: make(chan struct{}, 1), resume: make(chan struct{})}
	c, _ := New(r)
	route := invalidationRoute()
	state := kernel.SessionState{ResponseID: "current", ExpiresAt: time.Now().Add(time.Hour)}
	if err := c.Save(context.Background(), route, "s", state); err != nil {
		t.Fatal(err)
	}
	state, _, _ = c.Load(context.Background(), route, "s")
	ctx, stop := context.WithCancel(context.Background())
	runCtx, stopRun := context.WithCancel(context.Background())
	defer stopRun()
	go c.Run(runCtx)
	<-r.entered // Hold the worker on the preceding best-effort save.
	cancelled := make(chan error, 1)
	go func() { _, err := c.Invalidate(ctx, route, "s", state); cancelled <- err }()
	deadline := time.After(time.Second)
	for len(c.writes) == 0 {
		select {
		case <-deadline:
			t.Fatal("invalidation was not queued")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	stop()
	select {
	case err := <-cancelled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled await error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("invalidation did not stop waiting after context cancellation")
	}
	close(r.resume)
	stopRun()

	failure := errors.New("disk unavailable")
	r2 := &cacheFixtureRepository{saveErr: failure}
	c2, _ := New(r2)
	if err := c2.Save(context.Background(), route, "s", state); err != nil {
		t.Fatal(err)
	}
	state, _, _ = c2.Load(context.Background(), route, "s")
	runCtx2, stopRun2 := context.WithCancel(context.Background())
	defer stopRun2()
	go c2.Run(runCtx2)
	if ok, err := c2.Invalidate(context.Background(), route, "s", state); ok || !errors.Is(err, failure) {
		t.Fatalf("invalidate=%v err=%v", ok, err)
	}
	loaded, found, err := c2.Load(context.Background(), route, "s")
	if err != nil || !found || loaded.ResponseID != state.ResponseID {
		t.Fatalf("failed reset lost original state: %#v found=%v err=%v", loaded, found, err)
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
