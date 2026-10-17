package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	runtimehealth "github.com/fm39hz/gobroom/internal/runtime"
	"github.com/fm39hz/gobroom/internal/store"
)

func TestAuthHealthPersistenceCannotOverwriteOperationalCooldown(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/auth-lanes.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	node, err := s.CreateProviderNode(store.CreateProviderNodeInput{Name: "Provider", Prefix: "lanes", Protocol: "openai_chat", BaseURL: "https://provider.test"})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := s.CreateConnection(store.CreateConnectionInput{ProviderNodeID: node.ID, Name: "Account", CredentialType: "api_key", Secret: "old"})
	if err != nil {
		t.Fatal(err)
	}
	credential, _ := s.ConnectionCredentialByID(conn.ID)
	route := kernel.Route{ID: "route", NodeID: node.ID, CredentialID: conn.ID, ExternalModel: "model", DefinitionRef: node.DefinitionRef(), HealthGeneration: kernel.RouteHealthGeneration{Provider: "provider", Connection: credential.PrincipalGeneration, Credential: credential.Revision, Model: "model"}}
	deadline := time.Now().Add(time.Hour)
	operational := runtimehealth.RouteHealth{Lane: runtimehealth.HealthOperational, Failures: 1, CooldownUntil: deadline, LastScope: kernel.ScopeConnection, LastCause: kernel.CauseRateLimited, Generation: route.HealthGeneration.For(kernel.ScopeConnection)}
	if err := persistHealthState(s, route, operational); err != nil {
		t.Fatal(err)
	}
	auth := runtimehealth.RouteHealth{Lane: runtimehealth.HealthAuthentication, Failures: 1, Invalid: true, LastScope: kernel.ScopeConnection, LastCause: kernel.CauseAuth, Generation: route.HealthGeneration.ForAuthentication(kernel.ScopeConnection), LastAttempt: time.Now()}
	if err := persistHealthState(s, route, auth); err != nil {
		t.Fatal(err)
	}
	prior, ok, err := s.ConnectionRuntime(conn.ID)
	if err != nil || !ok || prior.Status != "cooldown" || parseTime(prior.CooldownUntil).IsZero() {
		t.Fatalf("auth overwrote operational state: %+v %v", prior, err)
	}
	rows, err := s.AuthRuntimes()
	if err != nil || len(rows) != 1 {
		t.Fatalf("separate auth rows=%+v %v", rows, err)
	}
	key, err := s.CompareAndSwapConnectionCredential(context.Background(), credential, "renewed", store.CredentialRenewal)
	if err != nil {
		t.Fatal(err)
	}
	if reset, err := s.ResetCredentialRoutingState(context.Background(), key); err != nil || !reset {
		t.Fatalf("renewal activation reset=%v %v", reset, err)
	}
	credential, _ = s.ConnectionCredentialByID(conn.ID)
	route.HealthGeneration.Credential = credential.Revision
	d := &Daemon{store: s, policy: runtimehealth.NewPolicyGate()}
	d.policy.SetGenerations(map[string]kernel.Route{route.ID: route})
	if err := d.restoreHealthForRoutes(map[string]kernel.Route{route.ID: route}); err != nil {
		t.Fatal(err)
	}
	if d.policy.Health.Usable(route, time.Now()) {
		t.Fatal("renewal lost operational cooldown after reload")
	}
	if rows, err := s.AuthRuntimes(); err != nil || len(rows) != 0 {
		t.Fatalf("renewal retained obsolete auth failure: %+v %v", rows, err)
	}
}

func TestRenewalCommitKeepsPrivatePrincipalOutOfActivationView(t *testing.T) {
	d, id := cancellationAuthDaemon(t, callbackOAuthFixture{})
	credential, _ := d.store.ConnectionCredentialByID(id)
	key, err := d.store.CompareAndSwapConnectionCredential(context.Background(), credential, "new", store.CredentialRenewal)
	if err != nil {
		t.Fatal(err)
	}
	view := d.credentialActivationView(context.Background(), key)
	if view.Transition != store.CredentialRenewal || !view.CredentialCommitted {
		t.Fatalf("transition receipt=%+v", view)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), key.PrincipalGeneration) || strings.Contains(string(encoded), key.Revision) {
		t.Fatal("activation view exposed private epoch")
	}
	if _, err := d.store.DB.Exec(`DELETE FROM credential_activations WHERE connection_id=?`, id); err != nil {
		t.Fatal(err)
	}
	view = d.credentialActivationView(context.Background(), key)
	if view.Transition != store.CredentialRenewal || view.Status != store.ActivationSuperseded {
		t.Fatalf("missing activation lost committed transition: %+v", view)
	}
}
