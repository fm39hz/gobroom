package sessionstore

import (
	"context"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/store"
)

func TestCachePersistsSessionStateOffTheReadPath(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/sessions.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.DB.Exec(`INSERT INTO provider_nodes(id,name,base_url,protocol,prefix) VALUES('node','Provider','https://provider.test','openai_responses','provider'); INSERT INTO connections(id,provider_node_id,name,credential_type) VALUES('account','node','Account','api_key')`); err != nil {
		t.Fatal(err)
	}
	cache, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	cache.ttl = 2 * time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		cache.Run(ctx)
	}()
	route := kernel.Route{DefinitionID: "openai-compatible-responses", ExternalModel: "gpt-model", CredentialID: "account"}
	providerExpiry := time.Now().Add(time.Hour).UTC()
	state := kernel.SessionState{ResponseID: "resp_1", ProviderData: []byte(`{"opaque":"v"}`), ExpiresAt: providerExpiry}
	if err := cache.Save(context.Background(), route, "client-session", state); err != nil {
		t.Fatal(err)
	}
	loaded, found, err := cache.Load(context.Background(), route, "client-session")
	if err != nil || !found || loaded.ResponseID != state.ResponseID || string(loaded.ProviderData) != string(state.ProviderData) || !loaded.ExpiresAt.Equal(providerExpiry) {
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
	cancel()
	<-done
	rows, err := db.ProviderSessions()
	persistedExpiries := map[string]time.Time{}
	for _, row := range rows {
		persistedExpiries[row.State.ResponseID] = row.State.ExpiresAt
	}
	if err != nil || len(rows) != 2 || !persistedExpiries["resp_1"].Equal(providerExpiry) || !persistedExpiries["resp_2"].Equal(bounded.ExpiresAt) {
		t.Fatalf("persisted=%#v err=%v", rows, err)
	}
	reloaded, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	stateAfterRestart, found, err := reloaded.Load(context.Background(), route, "client-session")
	if err != nil || !found || stateAfterRestart.ResponseID != "resp_1" {
		t.Fatalf("reloaded=%#v found=%v err=%v", stateAfterRestart, found, err)
	}
}
