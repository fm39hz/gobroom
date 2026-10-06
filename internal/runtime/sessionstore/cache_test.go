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
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		cache.Run(ctx)
	}()
	route := kernel.Route{DefinitionID: "openai-compatible-responses", ExternalModel: "gpt-model", CredentialID: "account"}
	state := kernel.SessionState{ResponseID: "resp_1", ProviderData: []byte(`{"opaque":"v"}`)}
	if err := cache.Save(context.Background(), route, "client-session", state); err != nil {
		t.Fatal(err)
	}
	loaded, found, err := cache.Load(context.Background(), route, "client-session")
	if err != nil || !found || loaded.ResponseID != state.ResponseID || string(loaded.ProviderData) != string(state.ProviderData) {
		t.Fatalf("cached=%#v found=%v err=%v", loaded, found, err)
	}
	cancel()
	<-done
	rows, err := db.ProviderSessions()
	if err != nil || len(rows) != 1 || rows[0].State.ResponseID != "resp_1" || !rows[0].State.ExpiresAt.After(time.Now()) {
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
