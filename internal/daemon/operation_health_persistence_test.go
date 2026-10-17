package daemon

import (
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
	runtimehealth "github.com/fm39hz/gobroom/internal/runtime"
	"github.com/fm39hz/gobroom/internal/store"
)

func TestOperationHealthPersistencePreservesScopeAndExactRef(t *testing.T) {
	state, err := store.Open(t.TempDir() + "/operation-health.db")
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if _, err := state.DB.Exec(`INSERT INTO provider_nodes(id,name,base_url,protocol,prefix) VALUES('node','Provider','http://provider','openai_chat','p'); INSERT INTO connections(id,provider_node_id,name,credential_type) VALUES('conn','node','Account','api_key')`); err != nil {
		t.Fatal(err)
	}
	op := extensions.Ref{Kind: "operation", ID: "messages", ContractVersion: 3}
	generation := "provider\x00connection\x00model"
	route := kernel.Route{
		ID: "route", NodeID: "node", CredentialID: "conn", ExternalModel: "model",
		HealthGeneration:  kernel.RouteHealthGeneration{Provider: "provider", Connection: "connection", Model: "model"},
		OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{normalize.Operation("messages"): {ContractVersion: 3}},
	}
	observed := time.Date(2026, 6, 7, 8, 9, 10, 0, time.UTC)
	health := runtimehealth.RouteHealth{OperationRef: op, Failures: 2, Successes: 1, Invalid: true, Generation: generation, CooldownUntil: observed.Add(time.Minute), LastError: "operation failed", LastCause: kernel.CauseRateLimited, LastScope: kernel.ScopeOperation, LastEvidence: kernel.EvidenceResponseHeader, Confidence: .95, LastAttempt: observed, LastSuccess: observed.Add(-time.Minute)}
	if err := persistHealthState(state, route, health); err != nil {
		t.Fatal(err)
	}
	rows, err := state.OperationRuntimes()
	if err != nil || len(rows) != 1 || rows[0].OperationRef != op || rows[0].Generation != generation || rows[0].Status != "invalid" || rows[0].Failures != 2 || rows[0].Successes != 1 || rows[0].Cause != string(kernel.CauseRateLimited) || rows[0].Evidence != string(kernel.EvidenceResponseHeader) || !rows[0].ObservedAt.Equal(observed) {
		t.Fatalf("operation health was not durably preserved: %#v err=%v", rows, err)
	}
	connections, err := state.ConnectionRuntimes()
	if err != nil || len(connections) != 0 {
		t.Fatalf("operation health leaked into broad connection state: %#v err=%v", connections, err)
	}
	models, err := state.ModelAvailabilities()
	if err != nil || len(models) != 0 {
		t.Fatalf("operation health leaked into broad model state: %#v err=%v", models, err)
	}

	otherVersion := route
	otherVersion.OperationBindings = map[normalize.Operation]kernel.RouteOperationBinding{normalize.Operation("messages"): {ContractVersion: 4}}
	daemon := &Daemon{store: state, policy: runtimehealth.NewPolicyGate()}
	if err := daemon.restoreHealthForRoutes(map[string]kernel.Route{"wrong-version": otherVersion}); err != nil {
		t.Fatal(err)
	}
	wrong := otherVersion
	wrong.OperationBindings = route.OperationBindings
	wrong.HealthGeneration.Model = "changed"
	if err := daemon.restoreHealthForRoutes(map[string]kernel.Route{"wrong-generation": wrong}); err != nil {
		t.Fatal(err)
	}
	view := daemon.policy.Health.Snapshot()[route.ID]
	if view.LastScope == kernel.ScopeOperation || view.OperationRef == op || len(view.Operations) != 0 {
		t.Fatalf("mismatched operation version or generation was restored: %#v", view)
	}

	if err := daemon.restoreHealthForRoutes(map[string]kernel.Route{"route": route}); err != nil {
		t.Fatal(err)
	}
	view = daemon.policy.Health.Snapshot()[route.ID]
	if len(view.Operations) != 1 || view.Operations[0].OperationRef != op || view.Operations[0].LastScope != kernel.ScopeOperation || view.Operations[0].Generation != generation || view.Operations[0].Failures != 2 || view.Operations[0].LastError != "operation failed" || view.Operations[0].LastCause != kernel.CauseRateLimited || view.Operations[0].LastEvidence != kernel.EvidenceResponseHeader || view.Operations[0].Confidence != .95 {
		t.Fatalf("exact operation health was not restored independently: %#v", view)
	}
}
