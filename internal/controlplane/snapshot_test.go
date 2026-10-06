package controlplane

import (
	"testing"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/store"
)

func TestLoaderBuildsTypedPhysicalAndComboGraph(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/typed.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB.Exec(`
INSERT INTO provider_nodes(id,name,base_url,protocol,definition_id,prefix) VALUES('node-a','A','http://a','openai_chat','manifest-selected-provider','a');
INSERT INTO model_catalog(id,provider_node_id,kind,external_id,display_name) VALUES('route:a','node-a','discovered','model-a','Model A');
INSERT INTO connections(id,provider_node_id,name,credential_type,secret_ref,priority) VALUES('account-a','node-a','A1','api_key','secret-a',1);
`); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordConnectionModelSnapshot("account-a", "node-a", []string{"model-a"}, true, "models_endpoint"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertPhysicalModel(store.PhysicalModel{Name: "model-a", Sources: []store.RouteReference{{RouteID: "route:a"}}, Policy: store.StrategySpec{ID: "ordered-fallback"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertComboModel(store.ComboModel{Name: "junior", Members: []store.ModelReference{{Kind: store.PhysicalReference, ID: "model-a", Weight: 4}}, Strategy: store.StrategySpec{ID: "round-robin-fallback", Config: map[string]any{"stickyLimit": 2}}, Discoverable: true, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	registry, err := provider.NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Primitives.RegisterDefinition(provider.ProviderDefinition{
		ID: "manifest-selected-provider", Version: "1", DisplayName: "Manifest selected",
		Auth:    provider.PrimitiveRef{Kind: provider.PrimitiveAuth, ID: "static-secret"},
		Session: provider.PrimitiveRef{Kind: provider.PrimitiveSessionStore, ID: "session"},
		Operations: map[provider.Operation]provider.OperationBinding{
			provider.OperationChat: {
				Endpoint:        provider.PrimitiveRef{Kind: provider.PrimitiveEndpoint, ID: "http-json"},
				Transport:       provider.PrimitiveRef{Kind: provider.PrimitiveTransport, ID: "http"},
				RequestCodec:    provider.PrimitiveRef{Kind: provider.PrimitiveRequestCodec, ID: "anthropic-messages-json"},
				ResponseCodec:   provider.PrimitiveRef{Kind: provider.PrimitiveResponseCodec, ID: "anthropic-sse"},
				UsageSource:     provider.PrimitiveRef{Kind: provider.PrimitiveUsageSource, ID: "http-header-usage"},
				UsageOptions:    kernel.UsageSourceOptions{InputTokensHeader: "X-Input-Count"},
				ErrorClassifier: provider.PrimitiveRef{Kind: provider.PrimitiveErrorClassifier, ID: "http-json"},
				ErrorClassifierOptions: provider.ErrorClassifierOptions{HTTPJSON: &provider.HTTPJSONErrorClassifierOptions{
					CodePath: "/fault/code", ResetAtPath: "/fault/retry_at", QuotaCodes: []string{"weekly_limit"},
				}},
			},
			provider.OperationQuota: {
				Endpoint:        provider.PrimitiveRef{Kind: provider.PrimitiveEndpoint, ID: "http-json"},
				EndpointOptions: kernel.EndpointOptions{Path: "/account/quota"},
				Transport:       provider.PrimitiveRef{Kind: provider.PrimitiveTransport, ID: "http"},
				QuotaSource:     provider.PrimitiveRef{Kind: provider.PrimitiveQuotaSource, ID: "http-json-quota"},
				QuotaWindowName: "account",
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	bindings, err := registry.BuildBindings()
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := (Loader{Store: s, Bindings: bindings}).LoadSnapshot(8)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snapshot.PublicModels["junior"]; !ok {
		t.Fatal("discoverable combo was not projected as a public model")
	}
	physical := snapshot.Nodes["model-a"]
	if physical.Kind != kernel.ModelPhysical || len(physical.Members) != 1 || physical.Members[0].Kind != kernel.MemberRouteGroup {
		t.Fatalf("physical node=%#v", physical)
	}
	if route := snapshot.Routes["route:a@account-a"]; route.AdapterID != "manifest-selected-provider:chat" || route.AuthFlowID != "manifest-selected-provider:static-secret" || route.SessionStoreID != "session" || route.ErrorClassifierID != provider.RuntimeErrorClassifierKey("manifest-selected-provider", provider.OperationChat, "http-json") || route.UsageSourceID != "http-header-usage" || route.UsageOptions.InputTokensHeader != "X-Input-Count" || route.QuotaSourceID != "http-json-quota" || route.QuotaEndpointOptions.Path != "/account/quota" || route.QuotaWindowName != "account" {
		t.Fatalf("route did not use provider runtime binding: %#v", route)
	}
	combo := snapshot.Nodes["junior"]
	if combo.Strategy != kernel.StrategyRoundRobinFallback || combo.StickyLimit != 2 || len(combo.Members) != 1 || combo.Members[0].ID != "model-a" || combo.Members[0].Weight != 4 {
		t.Fatalf("combo node=%#v", combo)
	}
}

func TestLoaderKeepsUnentitledDiscoveredRouteGroupValidUntilRefresh(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/unentitled.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB.Exec(`
INSERT INTO provider_nodes(id,name,base_url,protocol,definition_id,prefix) VALUES('node-a','A','https://provider.test/v1','openai_chat','openai-compatible-chat','a');
INSERT INTO model_catalog(id,provider_node_id,kind,external_id,display_name) VALUES('route:a','node-a','discovered','model-a','Model A');
INSERT INTO connections(id,provider_node_id,name,credential_type,secret_ref) VALUES('account-a','node-a','A1','api_key','secret-a');`); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertPhysicalModel(store.PhysicalModel{Name: "physical-a", Sources: []store.RouteReference{{RouteID: "route:a", Fidelity: kernel.FidelityExact}}, Policy: store.StrategySpec{ID: "ordered-fallback"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertComboModel(store.ComboModel{Name: "role-a", Members: []store.ModelReference{{Kind: store.PhysicalReference, ID: "physical-a"}}, Strategy: store.StrategySpec{ID: "ordered-fallback"}, Discoverable: true, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	registry, err := provider.NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := registry.BuildBindings()
	if err != nil {
		t.Fatal(err)
	}
	loader := Loader{Store: s, Bindings: bindings}
	snapshot, err := loader.LoadSnapshot(1)
	if err != nil {
		t.Fatalf("unentitled catalog reference must not invalidate snapshot: %v", err)
	}
	if len(snapshot.Routes) != 0 {
		t.Fatalf("unknown entitlement created executable routes: %#v", snapshot.Routes)
	}
	group, exists := snapshot.RouteGroups["route:a"]
	if !exists || len(group) != 0 {
		t.Fatalf("unentitled route group=%#v exists=%v", group, exists)
	}
	if _, err := kernel.ResolvePublic(snapshot, "role-a"); err != nil {
		t.Fatalf("exposed logical model should remain resolvable before entitlement: %v", err)
	}

	if err := s.RecordConnectionModelSnapshot("account-a", "node-a", []string{"model-a"}, true, "models_endpoint"); err != nil {
		t.Fatal(err)
	}
	snapshot, err = loader.LoadSnapshot(2)
	if err != nil {
		t.Fatalf("refresh must publish after entitlement is recorded: %v", err)
	}
	if len(snapshot.Routes) != 1 {
		t.Fatalf("available connection route count=%d", len(snapshot.Routes))
	}
	group = snapshot.RouteGroups["route:a"]
	if len(group) != 1 || group[0] != "route:a@account-a" {
		t.Fatalf("available route group=%#v", group)
	}
}

func mustSnapshotStore(t *testing.T, snapshot kernel.Snapshot) *kernel.SnapshotStore {
	t.Helper()
	store, err := kernel.NewSnapshotStore(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return store
}
