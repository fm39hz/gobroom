package daemon

import (
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	runtimehealth "github.com/fm39hz/gobroom/internal/runtime"
	"github.com/fm39hz/gobroom/internal/store"
)

func TestHealthPersistenceKeepsScopeSeparated(t *testing.T) {
	state, err := store.Open(t.TempDir() + "/health.db")
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if _, err := state.DB.Exec(`INSERT INTO provider_nodes(id,name,base_url,protocol,prefix) VALUES('node','Provider','http://provider','openai_chat','p'); INSERT INTO connections(id,provider_node_id,name,credential_type) VALUES('conn','node','Account','api_key')`); err != nil {
		t.Fatal(err)
	}
	route := kernel.Route{ID: "route", NodeID: "node", CredentialID: "conn", ExternalModel: "model"}
	blocked := time.Now().Add(time.Minute)
	stateFor := func(scope kernel.OutcomeScope) runtimehealth.RouteHealth {
		return runtimehealth.RouteHealth{Failures: 1, CooldownUntil: blocked, LastCause: kernel.CauseRateLimited, LastScope: scope}
	}

	if err := persistHealthState(state, route, stateFor(kernel.ScopeRoute)); err != nil {
		t.Fatal(err)
	}
	connections, err := state.ConnectionRuntimes()
	if err != nil || len(connections) != 0 {
		t.Fatalf("route-scoped result leaked into connection runtime: %#v err=%v", connections, err)
	}
	models, err := state.ModelAvailabilities()
	if err != nil || len(models) != 1 || models[0].ModelRef != "model" || models[0].ErrorCode != string(kernel.ScopeRoute) {
		t.Fatalf("route-scoped persistence=%#v err=%v", models, err)
	}
	invalidModel := stateFor(kernel.ScopeRoute)
	invalidModel.Invalid = true
	if err := persistHealthState(state, route, invalidModel); err != nil {
		t.Fatal(err)
	}
	models, err = state.ModelAvailabilities()
	if err != nil || len(models) != 1 || models[0].Status != "invalid" {
		t.Fatalf("permanent model invalidation was not persisted: %#v err=%v", models, err)
	}

	connectionState := stateFor(kernel.ScopeConnection)
	connectionState.Invalid = true
	if err := persistHealthState(state, route, connectionState); err != nil {
		t.Fatal(err)
	}
	connections, err = state.ConnectionRuntimes()
	if err != nil || len(connections) != 1 || connections[0].ConnectionID != "conn" || connections[0].Status != "invalid" {
		t.Fatalf("connection-scoped persistence=%#v err=%v", connections, err)
	}
	models, err = state.ModelAvailabilities()
	if err != nil || len(models) != 1 {
		t.Fatalf("connection result rewrote model-scoped persistence: %#v err=%v", models, err)
	}

	if err := persistHealthState(state, route, stateFor(kernel.ScopeProvider)); err != nil {
		t.Fatal(err)
	}
	providers, err := state.ProviderRuntimes()
	if err != nil || len(providers) != 1 || providers[0].ProviderNodeID != "node" {
		t.Fatalf("provider-scoped persistence=%#v err=%v", providers, err)
	}
	connections, err = state.ConnectionRuntimes()
	if err != nil || len(connections) != 1 {
		t.Fatalf("provider result rewrote connection-scoped persistence: %#v err=%v", connections, err)
	}
}
