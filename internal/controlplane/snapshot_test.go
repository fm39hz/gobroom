package controlplane

import (
	"reflect"
	"testing"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/operations"
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
INSERT INTO provider_nodes(id,name,base_url,protocol,definition_id,prefix) VALUES('node-media','Media','http://media','vendor.audio.v1','manifest-selected-provider','media');
INSERT INTO model_catalog(id,provider_node_id,kind,external_id,display_name) VALUES('route:audio','node-media','custom','transcribe-v1','Transcribe V1');
`); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordConnectionModelSnapshot("account-a", "node-a", []string{"model-a"}, true, "models_endpoint"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertPhysicalModel(store.PhysicalModel{Name: "model-a", Sources: []store.RouteReference{{RouteID: "route:a"}}, Policy: store.StrategySpec{Ref: kernel.StrategyRef("ordered-fallback", 1)}, LossPolicy: kernel.LossPolicy{Allow: []string{"reasoning.clamped"}}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertComboModel(store.ComboModel{Name: "junior", Members: []store.ModelReference{{Kind: store.PhysicalReference, ID: "model-a", Weight: 4}}, Strategy: store.StrategySpec{Ref: kernel.StrategyRef("round-robin-fallback", 1), Config: map[string]any{"stickyLimit": 2}}, LossPolicy: kernel.LossPolicy{Deny: []string{"tools.history.loss"}}, Discoverable: true, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	registry, err := provider.NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Primitives.Operations.RegisterRawPayload(extensions.Ref{Kind: "operation", ID: "audio.transcribe.v1", ContractVersion: 1}, "Audio transcription", "Transcribe audio input", []byte(`{"type":"object","properties":{"audio":{"type":"string","minLength":1}},"required":["audio"],"additionalProperties":false}`), operations.ReplayNever); err != nil {
		t.Fatal(err)
	}
	if err := registry.Primitives.RegisterDefinition(provider.ProviderDefinition{ContractVersion: 1, ID: "manifest-selected-provider", Version: "1", DisplayName: "Manifest selected",
		Auth:    provider.PrimitiveRef{Kind: provider.PrimitiveAuth, ID: "static-secret", ContractVersion: 1},
		Session: provider.PrimitiveRef{Kind: provider.PrimitiveSessionStore, ID: "session", ContractVersion: 1},
		Operations: map[provider.Operation]provider.OperationBinding{
			provider.OperationChat: {
				Protocol:        kernel.ProtocolOpenAIChat,
				TaskRef:         provider.OperationRef("chat.generate", 1),
				Endpoint:        provider.PrimitiveRef{Kind: provider.PrimitiveEndpoint, ID: "http-json", ContractVersion: 1},
				Transport:       provider.PrimitiveRef{Kind: provider.PrimitiveTransport, ID: "http", ContractVersion: 1},
				RequestCodec:    provider.PrimitiveRef{Kind: provider.PrimitiveRequestCodec, ID: "anthropic-messages-json", ContractVersion: 1},
				ResponseDecoder: provider.PrimitiveRef{Kind: provider.PrimitiveResponseDecoder, ID: "anthropic-sse", ContractVersion: 1},
				UsageSource:     provider.PrimitiveRef{Kind: provider.PrimitiveUsageSource, ID: "http-header-usage", ContractVersion: 1},
				UsageOptions:    kernel.UsageSourceOptions{InputTokensHeader: "X-Input-Count"},
				ErrorClassifier: provider.PrimitiveRef{Kind: provider.PrimitiveErrorClassifier, ID: "http-json", ContractVersion: 1},
				ErrorClassifierOptions: provider.ErrorClassifierOptions{HTTPJSON: &provider.HTTPJSONErrorClassifierOptions{
					CodePath: "/fault/code", ResetAtPath: "/fault/retry_at", QuotaCodes: []string{"weekly_limit"},
				}},
			},
			provider.Operation("audio.transcribe.v1"): {
				Protocol:        kernel.Protocol("vendor.audio.v1"),
				TaskRef:         provider.OperationRef("audio.transcribe.v1", 1),
				Endpoint:        provider.PrimitiveRef{Kind: provider.PrimitiveEndpoint, ID: "http-json", ContractVersion: 1},
				Transport:       provider.PrimitiveRef{Kind: provider.PrimitiveTransport, ID: "http", ContractVersion: 1},
				RequestCodec:    provider.PrimitiveRef{Kind: provider.PrimitiveRequestCodec, ID: "gemini-json", ContractVersion: 1},
				ResponseDecoder: provider.PrimitiveRef{Kind: provider.PrimitiveResponseDecoder, ID: "gemini-json", ContractVersion: 1},
			},
			provider.OperationQuota: {
				Endpoint:        provider.PrimitiveRef{Kind: provider.PrimitiveEndpoint, ID: "http-json", ContractVersion: 1},
				EndpointOptions: kernel.EndpointOptions{Path: "/account/quota"},
				Transport:       provider.PrimitiveRef{Kind: provider.PrimitiveTransport, ID: "http", ContractVersion: 1},
				QuotaSource:     provider.PrimitiveRef{Kind: provider.PrimitiveQuotaSource, ID: "http-json-quota", ContractVersion: 1},
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
	if got := snapshot.Nodes["junior"].LossPolicy; !reflect.DeepEqual(got, (kernel.LossPolicy{Deny: []string{"tools.history.loss"}})) {
		t.Fatalf("combo loss policy did not reach immutable snapshot: %#v", got)
	}
	if _, ok := snapshot.PublicModels["junior"]; !ok {
		t.Fatal("discoverable combo was not projected as a public model")
	}
	physical := snapshot.Nodes["model-a"]
	if physical.Kind != kernel.ModelPhysical || len(physical.Members) != 1 || physical.Members[0].Kind != kernel.MemberRouteGroup {
		t.Fatalf("physical node=%#v", physical)
	}
	route := snapshot.Routes["route:a@account-a"]
	if route.OperationBindings["chat.generate"].ContractVersion != 1 || len(route.OperationBindings["chat.generate"].AdapterIDs) != 1 || route.OperationBindings["chat.generate"].AdapterIDs[0] != "manifest-selected-provider:chat" || route.AuthFlowID != provider.AuthBindingKey("manifest-selected-provider", provider.PrimitiveRef{Kind: provider.PrimitiveAuth, ID: "static-secret", ContractVersion: 1}) || route.SessionStoreRef != (extensions.Ref{Kind: string(provider.PrimitiveSessionStore), ID: "session", ContractVersion: 1}) || route.ErrorClassifierRef != provider.RuntimeErrorClassifierRef("manifest-selected-provider", provider.OperationChat, provider.PrimitiveRef{Kind: provider.PrimitiveErrorClassifier, ID: "http-json", ContractVersion: 1}) || route.UsageSourceRef != (extensions.Ref{Kind: string(provider.PrimitiveUsageSource), ID: "http-header-usage", ContractVersion: 1}) || route.UsageOptions.InputTokensHeader != "X-Input-Count" || route.QuotaSourceRef != (extensions.Ref{Kind: string(provider.PrimitiveQuotaSource), ID: "http-json-quota", ContractVersion: 1}) || route.QuotaEndpointRef != (extensions.Ref{Kind: string(provider.PrimitiveEndpoint), ID: "http-json", ContractVersion: 1}) || route.QuotaTransportRef != (extensions.Ref{Kind: string(provider.PrimitiveTransport), ID: "http", ContractVersion: 1}) || route.QuotaEndpointOptions.Path != "/account/quota" || route.QuotaWindowName != "account" {
		t.Fatalf("route did not use provider runtime binding: %#v", route)
	}
	mediaRoute := snapshot.Routes["route:audio"]
	mediaBinding := mediaRoute.OperationBindings["audio.transcribe.v1"]
	if mediaRoute.Protocol != "vendor.audio.v1" || len(mediaBinding.AdapterIDs) != 1 || mediaBinding.AdapterIDs[0] != "manifest-selected-provider:audio.transcribe.v1" {
		t.Fatalf("namespaced operation binding did not reach the route snapshot: %#v", mediaRoute)
	}
	combo := snapshot.Nodes["junior"]
	if combo.Strategy != kernel.StrategyRoundRobinFallback || combo.StickyLimit != 2 || len(combo.Members) != 1 || combo.Members[0].ID != "model-a" || combo.Members[0].Weight != 4 {
		t.Fatalf("combo node=%#v", combo)
	}
}

func TestControlPlaneRejectsUnregisteredStrategyVersionInsteadOfFallingBackByID(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/exact-strategy-ref.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.UpsertComboModel(store.ComboModel{
		Name: "junior", Strategy: store.StrategySpec{Ref: kernel.StrategyRef("ordered-fallback", 2)}, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(s); err == nil {
		t.Fatal("an unregistered strategy version fell back to another version with the same ID")
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
	if err := s.UpsertPhysicalModel(store.PhysicalModel{Name: "physical-a", Sources: []store.RouteReference{{RouteID: "route:a", Fidelity: kernel.FidelityExact}}, Policy: store.StrategySpec{Ref: kernel.StrategyRef("ordered-fallback", 1)}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertComboModel(store.ComboModel{Name: "role-a", Members: []store.ModelReference{{Kind: store.PhysicalReference, ID: "physical-a"}}, Strategy: store.StrategySpec{Ref: kernel.StrategyRef("ordered-fallback", 1)}, Discoverable: true, Enabled: true}); err != nil {
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
