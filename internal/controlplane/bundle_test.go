package controlplane

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/store"
)

func bundleTestCatalog(t *testing.T, extraRefs ...extensions.Ref) *kernel.StrategyCatalog {
	t.Helper()
	base := extensions.NewCatalog()
	if err := kernel.RegisterBuiltinStrategyExtensions(base); err != nil {
		t.Fatal(err)
	}
	for _, ref := range extraRefs {
		descriptor := extensions.Descriptor{Ref: ref, ImplementationVersion: "fixture-1", DisplayName: ref.ID, Description: "Bundle dependency fixture."}
		if ref.Kind == kernel.RequestTransformKind || ref.Kind == kernel.ResponseTransformKind {
			descriptor.ResourceBounds = extensions.ResourceBounds{MaxBufferedBytes: 1024, DeadlineMillis: 1000}
			descriptor.FailureModes = []string{"safe_fail_open"}
		}
		if err := base.Register(descriptor, func(json.RawMessage) (any, error) { return struct{}{}, nil }); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := base.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	strategies, err := kernel.NewStrategyCatalog(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return strategies
}

func bundleWithDependencyLock(t *testing.T, bundle ConfigBundle, catalog *kernel.StrategyCatalog) ConfigBundle {
	t.Helper()
	bundle.Version = 5
	lock, err := catalog.Extensions().LockDependencies(bundleDependencyRoots(bundle))
	if err != nil {
		t.Fatal(err)
	}
	bundle.Dependencies = lock
	return bundle
}

func TestTransformBindingsRoundTripThroughTypedConfigBundle(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/transform-bundle.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	binding := kernel.TransformBinding{
		ID: "daemon.compress", TransformRef: extensions.Ref{Kind: kernel.RequestTransformKind, ID: "tokens.compress.v1", ContractVersion: 1},
		Enabled: true, Scope: kernel.TransformScope{Kind: kernel.TransformScopeDaemon}, Order: 10, FailureMode: kernel.TransformSafeFailOpen,
		Options: json.RawMessage(`{"ratio":0.5}`),
	}
	catalog := bundleTestCatalog(t, binding.TransformRef)
	if err := s.UpsertTransformBinding(binding); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(s)
	if err != nil {
		t.Fatal(err)
	}
	if got := manager.Snapshot().TransformBindings; len(got) != 1 || !reflect.DeepEqual(got[0], binding) {
		t.Fatalf("snapshot transform bindings=%#v want=%#v", got, binding)
	}
	bundle, err := ExportBundle(s, catalog.Extensions())
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.TransformBindings) != 1 || !reflect.DeepEqual(bundle.TransformBindings[0], binding) {
		t.Fatalf("transform binding round trip=%#v want=%#v", bundle.TransformBindings, binding)
	}
	if err := ValidateBundle(bundle); err != nil {
		t.Fatal(err)
	}
	bundle.TransformBindings[0].Enabled = false
	if err := ApplyBundle(s, bundle, catalog); err != nil {
		t.Fatal(err)
	}
	after, err := s.TransformBindings()
	if err != nil || len(after) != 1 || after[0].Enabled {
		t.Fatalf("applied transform binding=%#v err=%v", after, err)
	}
	if err := manager.Reload(); err != nil {
		t.Fatal(err)
	}
	if loaded := manager.Snapshot().TransformBindings; len(loaded) != 1 || loaded[0].Enabled {
		t.Fatalf("reloaded transform binding=%#v", loaded)
	}
}

func TestModelLossPoliciesRoundTripThroughConfigBundle(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/loss-policy-bundle.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	physical := store.PhysicalModel{Name: "physical", Policy: store.StrategySpec{Ref: kernel.StrategyRef("ordered-fallback", 1)}, LossPolicy: kernel.LossPolicy{Allow: []string{"reasoning.effort.clamped"}}, Enabled: true}
	if err := s.UpsertPhysicalModel(physical); err != nil {
		t.Fatal(err)
	}
	combo := store.ComboModel{Name: "role", Members: []store.ModelReference{{Kind: store.PhysicalReference, ID: "physical"}}, Strategy: store.StrategySpec{Ref: kernel.StrategyRef("ordered-fallback", 1)}, LossPolicy: kernel.LossPolicy{Deny: []string{"tools.arguments.dropped"}}, Discoverable: true, Enabled: true}
	if err := s.UpsertComboModel(combo); err != nil {
		t.Fatal(err)
	}
	ceiling := kernel.LossPolicyCeiling{AllowOnly: true, Allow: []string{"reasoning.effort.clamped"}, Deny: []string{"tools.arguments.dropped"}}
	if err := s.SetCompatibilityLossCeiling(ceiling); err != nil {
		t.Fatal(err)
	}
	catalog := bundleTestCatalog(t)
	bundle, err := ExportBundle(s, catalog.Extensions())
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Version != 5 || bundle.LossCeiling == nil || !reflect.DeepEqual(*bundle.LossCeiling, ceiling) {
		t.Fatalf("versioned global loss ceiling was not exported: %#v", bundle)
	}
	encoded, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	var portable ConfigBundle
	if err := json.Unmarshal(encoded, &portable); err != nil {
		t.Fatal(err)
	}
	if err := ValidateBundleWithStrategies(portable, catalog); err != nil {
		t.Fatal(err)
	}
	if err := ApplyBundle(s, portable, catalog); err != nil {
		t.Fatal(err)
	}
	gotPhysical, err := s.PhysicalModel("physical")
	if err != nil || !reflect.DeepEqual(gotPhysical.LossPolicy, physical.LossPolicy) {
		t.Fatalf("physical loss policy round trip=%#v err=%v", gotPhysical.LossPolicy, err)
	}
	gotCombo, err := s.ComboModel("role")
	if err != nil || !reflect.DeepEqual(gotCombo.LossPolicy, combo.LossPolicy) {
		t.Fatalf("combo loss policy round trip=%#v err=%v", gotCombo.LossPolicy, err)
	}
	gotCeiling, configured, err := s.CompatibilityLossCeiling()
	if err != nil || !configured || !reflect.DeepEqual(gotCeiling, ceiling) {
		t.Fatalf("server loss ceiling round trip=%#v configured=%v err=%v", gotCeiling, configured, err)
	}
}

func TestDiffBundleDetectsServerLossCeilingChanges(t *testing.T) {
	catalog := bundleTestCatalog(t)
	current := bundleWithDependencyLock(t, ConfigBundle{}, catalog)
	desired := bundleWithDependencyLock(t, ConfigBundle{LossCeiling: &kernel.LossPolicyCeiling{AllowOnly: true, Allow: []string{"reasoning.clamped"}}}, catalog)
	diff, err := DiffBundle(current, desired, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if !diff.LossCeilingChanged {
		t.Fatalf("server loss ceiling change missing from bundle diff: %#v", diff)
	}
}

func TestConfigBundleRejectsPreviousContractVersion(t *testing.T) {
	catalog := bundleTestCatalog(t)
	bundle := bundleWithDependencyLock(t, ConfigBundle{}, catalog)
	bundle.Version = 4
	if err := ValidateBundleWithStrategies(bundle, catalog); err == nil {
		t.Fatal("previous config bundle contract version was accepted")
	}
}

func TestBundleExportsUnusedUnresolvedProviderWithoutInventingBehavior(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/unresolved-provider.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB.Exec(`INSERT INTO provider_nodes(id,name,base_url,protocol,definition_id,prefix,models_path,auth_mode) VALUES('commandcode','CommandCode','https://api.commandcode.ai','openai_chat','commandcode-pending','commandcode','/models','api_key')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO connections(id,provider_node_id,name,credential_type,secret_ref,priority,enabled) VALUES('commandcode-account','commandcode','account','api_key','credential',1,0)`); err != nil {
		t.Fatal(err)
	}
	catalog := bundleTestCatalog(t)
	bundle, err := ExportBundle(s, catalog.Extensions())
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Providers) != 1 || bundle.Providers[0].DefinitionID != "commandcode-pending" || len(bundle.UnresolvedProviderDefinitions) != 1 || bundle.UnresolvedProviderDefinitions[0].ProviderNodeID != "commandcode" {
		t.Fatalf("unused unresolved provider was dropped or hidden: %#v", bundle)
	}
	if err := ValidateBundleWithStrategies(bundle, catalog); err != nil {
		t.Fatalf("inert provider setup bundle should remain portable: %v", err)
	}
	diff, err := DiffBundle(bundleWithDependencyLock(t, ConfigBundle{}, catalog), bundle, catalog)
	if err != nil || !diff.UnresolvedProvidersChanged {
		t.Fatalf("unresolved provider status missing from bundle diff: %#v err=%v", diff, err)
	}
	if err := s.UpsertCatalogModel(store.UpsertCatalogModelInput{ID: "commandcode-route", ProviderNodeID: "commandcode", Kind: "custom", ExternalID: "model-a", DisplayName: "Model A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`UPDATE model_catalog SET enabled=0 WHERE id='commandcode-route'`); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertPhysicalModel(store.PhysicalModel{Name: "commandcode-model", Sources: []store.RouteReference{{RouteID: "commandcode-route"}}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := ExportBundle(s, catalog.Extensions()); err == nil {
		t.Fatal("provider definition used by a Physical source was exported as unresolved and inert")
	}
}

func TestBundleEmbedsOnlySecretFreeProviderDefinitionsAndPersistsThem(t *testing.T) {
	registry, err := provider.NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	definition := provider.ProviderDefinition{ContractVersion: 1, ID: "bundle-portable", Version: "1", DisplayName: "Portable provider",
		Auth: provider.PrimitiveRef{Kind: provider.PrimitiveAuth, ID: "static-secret", ContractVersion: 1},
		Operations: map[provider.Operation]provider.OperationBinding{provider.OperationChat: {
			Protocol: kernel.ProtocolOpenAIChat, TaskRef: provider.OperationRef("chat.generate", 1), ProviderFormat: "openai",
			Endpoint:        provider.PrimitiveRef{Kind: provider.PrimitiveEndpoint, ID: "http-json", ContractVersion: 1},
			Transport:       provider.PrimitiveRef{Kind: provider.PrimitiveTransport, ID: "http", ContractVersion: 1},
			RequestCodec:    provider.PrimitiveRef{Kind: provider.PrimitiveRequestCodec, ID: "openai-chat-json", ContractVersion: 1},
			ResponseDecoder: provider.PrimitiveRef{Kind: provider.PrimitiveResponseDecoder, ID: "openai-sse", ContractVersion: 1},
		}},
	}
	if err := registry.Primitives.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	external := definition
	external.ID = "bundle-external"
	external.DisplayName = "External provider"
	external.Auth = provider.PrimitiveRef{Kind: provider.PrimitiveAuth, ID: "oauth2", ContractVersion: 1}
	external.AuthOptions = provider.AuthOptions{OAuth: &provider.OAuthFlowOptions{ClientID: "client", AuthURL: "https://identity.test/auth?client_secret=bundle-secret-marker", TokenURL: "https://identity.test/token"}}
	if err := registry.Primitives.RegisterDefinition(external); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.BuildBindings(); err != nil {
		t.Fatal(err)
	}
	extensionsSnapshot, err := registry.FreezeCatalog()
	if err != nil {
		t.Fatal(err)
	}
	strategies, err := kernel.NewStrategyCatalog(extensionsSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.Open(t.TempDir() + "/provider-definitions.db")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err := source.DB.Exec(`INSERT INTO provider_nodes(id,name,base_url,protocol,definition_id,prefix,models_path,auth_mode) VALUES
('portable-node','Portable','https://portable.test/v1','openai_chat','bundle-portable','portable','/models','api_key'),
('external-node','External','https://external.test/v1','openai_chat','bundle-external','external','/models','api_key')`); err != nil {
		t.Fatal(err)
	}
	bundle, err := ExportBundle(source, extensionsSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.ProviderDefinitions) != 1 || bundle.ProviderDefinitions[0].ID != definition.ID || len(bundle.ExternalProviderDefinitions) != 1 || bundle.ExternalProviderDefinitions[0].DefinitionRef.ID != external.ID {
		t.Fatalf("provider portability split=%#v", bundle)
	}
	encoded, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "bundle-secret-marker") {
		t.Fatalf("provider secret escaped through bundle: %s", encoded)
	}
	if err := ValidateBundleWithStrategies(bundle, strategies); err != nil {
		t.Fatal(err)
	}
	target, err := store.Open(t.TempDir() + "/provider-definitions-target.db")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	if err := ApplyBundle(target, bundle, strategies); err != nil {
		t.Fatal(err)
	}
	stored, err := target.ProviderDefinitions()
	if err != nil || len(stored) != 1 || !provider.SameProviderDefinition(stored[0], definition) {
		t.Fatalf("portable provider definitions=%#v err=%v", stored, err)
	}
	restoredRuntime, err := provider.NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range stored {
		if err := restoredRuntime.RegisterDefinitionIfAbsent(item); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := restoredRuntime.BuildBindings(); err != nil {
		t.Fatalf("persisted provider definition did not bind after restart: %v", err)
	}
}

func TestBundleRejectsProviderDefinitionPayloadDifferentFromPinnedDescriptor(t *testing.T) {
	registry, err := provider.NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	definition := provider.ProviderDefinition{
		ContractVersion: 1, ID: "pinned-chat", Version: "1", DisplayName: "Pinned chat",
		Auth: provider.PrimitiveRef{Kind: provider.PrimitiveAuth, ID: "static-secret", ContractVersion: 1},
		Operations: map[provider.Operation]provider.OperationBinding{provider.OperationChat: {
			Protocol: kernel.ProtocolOpenAIChat, TaskRef: provider.OperationRef("chat.generate", 1), ProviderFormat: "openai",
			Endpoint:        provider.PrimitiveRef{Kind: provider.PrimitiveEndpoint, ID: "http-json", ContractVersion: 1},
			Transport:       provider.PrimitiveRef{Kind: provider.PrimitiveTransport, ID: "http", ContractVersion: 1},
			RequestCodec:    provider.PrimitiveRef{Kind: provider.PrimitiveRequestCodec, ID: "openai-chat-json", ContractVersion: 1},
			ResponseDecoder: provider.PrimitiveRef{Kind: provider.PrimitiveResponseDecoder, ID: "openai-sse", ContractVersion: 1},
		}},
	}
	if err := registry.Primitives.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.BuildBindings(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := registry.FreezeCatalog()
	if err != nil {
		t.Fatal(err)
	}
	strategies, err := kernel.NewStrategyCatalog(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	modified := definition
	modified.DisplayName = "Unpinned display name"
	bundle := bundleWithDependencyLock(t, ConfigBundle{
		Providers:           []store.ProviderNode{{ID: "pinned-node", Name: "Pinned", DefinitionID: definition.ID}},
		ProviderDefinitions: []provider.ProviderDefinition{modified},
	}, strategies)
	if err := ValidateBundleWithStrategies(bundle, strategies); err == nil {
		t.Fatal("provider definition payload changed without changing its pinned catalog descriptor")
	}
}

func TestValidateBundleRejectsUnknownTypedReferences(t *testing.T) {
	catalog := bundleTestCatalog(t)
	bundle := bundleWithDependencyLock(t, ConfigBundle{Providers: []store.ProviderNode{{ID: "p"}}, Connections: []store.ConnectionRecord{{ID: "c", ProviderNodeID: "p"}}, Models: []store.Model{{ID: "r"}}, Physical: []store.PhysicalModel{{Name: "qwen", Policy: store.StrategySpec{Ref: kernel.StrategyRef("ordered-fallback", 1)}, Sources: []store.RouteReference{{RouteID: "missing"}}}}}, catalog)
	if err := ValidateBundle(bundle); err == nil {
		t.Fatal("expected unknown route rejection")
	}
}

func TestValidateBundleAcceptsSecretFreeTypedGraph(t *testing.T) {
	catalog := bundleTestCatalog(t)
	bundle := bundleWithDependencyLock(t, ConfigBundle{Providers: []store.ProviderNode{{ID: "p"}}, Connections: []store.ConnectionRecord{{ID: "c", ProviderNodeID: "p"}}, Models: []store.Model{{ID: "r"}}, Physical: []store.PhysicalModel{{Name: "qwen", Policy: store.StrategySpec{Ref: kernel.StrategyRef("ordered-fallback", 1)}, Sources: []store.RouteReference{{RouteID: "r"}}}}}, catalog)
	if err := ValidateBundle(bundle); err != nil {
		t.Fatal(err)
	}
}

func TestValidateBundleResolvesExactStrategyRefsAndOptions(t *testing.T) {
	catalog := bundleTestCatalog(t)
	base := bundleWithDependencyLock(t, ConfigBundle{Combos: []store.ComboModel{{
		Name: "junior", Strategy: store.StrategySpec{Ref: kernel.StrategyRef("round-robin-fallback", 1), Config: map[string]any{"stickyLimit": 2}},
	}}}, catalog)
	if err := ValidateBundleWithStrategies(base, catalog); err != nil {
		t.Fatalf("valid exact strategy bundle rejected: %v", err)
	}
	badOptions := base
	badOptions.Combos = append([]store.ComboModel(nil), base.Combos...)
	badOptions.Combos[0].Strategy.Config = map[string]any{"stickyLimit": 0}
	if err := ValidateBundleWithStrategies(badOptions, catalog); err == nil {
		t.Fatal("bundle with schema-invalid strategy options was accepted")
	}
	wrongVersion := base
	wrongVersion.Combos = append([]store.ComboModel(nil), base.Combos...)
	wrongVersion.Combos[0].Strategy.Ref.ContractVersion = 2
	if err := ValidateBundleWithStrategies(wrongVersion, catalog); err == nil {
		t.Fatal("bundle silently resolved an unregistered strategy version")
	}
}

func TestDiffBundleReportsTypedChanges(t *testing.T) {
	catalog := bundleTestCatalog(t)
	current := bundleWithDependencyLock(t, ConfigBundle{Providers: []store.ProviderNode{{ID: "old"}}}, catalog)
	desired := bundleWithDependencyLock(t, ConfigBundle{Providers: []store.ProviderNode{{ID: "new"}}}, catalog)
	diff, err := DiffBundle(current, desired)
	if err != nil {
		t.Fatal(err)
	}
	if diff.ProvidersAdded != 1 || diff.ProvidersRemoved != 1 || len(diff.Changes) != 2 {
		t.Fatalf("diff=%#v", diff)
	}
	encoded, err := json.Marshal(BundleDiff{ProvidersAdded: 1, ProvidersRemoved: 2, ConnectionsAdded: 3, ConnectionsRemoved: 4, ModelsAdded: 5, ModelsRemoved: 6, PhysicalAdded: 7, PhysicalRemoved: 8, CombosAdded: 9, CombosRemoved: 10, TransformBindingsAdded: 11, TransformBindingsRemoved: 12, TransformBindingsChanged: 13, LossCeilingChanged: true, UnresolvedProvidersChanged: true})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 17 || fields["providersRemoved"] != float64(2) || fields["connectionsAdded"] != float64(3) || fields["physicalRemoved"] != float64(8) || fields["combosRemoved"] != float64(10) || fields["transformBindingsChanged"] != float64(13) || fields["lossCeilingChanged"] != true || fields["externalProviderDefinitionsChanged"] != false || fields["unresolvedProvidersChanged"] != true {
		t.Fatalf("bundle diff JSON lost field tags: %s", encoded)
	}
}

func TestDiffBundleDetectsTransformBindingUpdates(t *testing.T) {
	ref := extensions.Ref{Kind: kernel.RequestTransformKind, ID: "tokens.compress.v1", ContractVersion: 1}
	catalog := bundleTestCatalog(t, ref)
	current := bundleWithDependencyLock(t, ConfigBundle{TransformBindings: []kernel.TransformBinding{{ID: "model.junior.compress", TransformRef: ref, Enabled: true, Scope: kernel.TransformScope{Kind: kernel.TransformScopeModel, ID: "junior"}, Order: 10}}}, catalog)
	desired := bundleWithDependencyLock(t, ConfigBundle{TransformBindings: []kernel.TransformBinding{{ID: "model.junior.compress", TransformRef: ref, Enabled: false, Scope: kernel.TransformScope{Kind: kernel.TransformScopeModel, ID: "junior"}, Order: 10}}}, catalog)
	diff, err := DiffBundle(current, desired, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if diff.TransformBindingsChanged != 1 || len(diff.Changes) != 1 || diff.Changes[0] != "~ transform binding model.junior.compress" {
		t.Fatalf("transform binding update was omitted from diff: %#v", diff)
	}
}

func TestApplyBundlePreservesConnectionSecretAndCommitsAtomically(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/bundle.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB.Exec(`INSERT INTO provider_nodes(id,name,base_url,protocol,prefix) VALUES('p','P','https://p.test','openai_chat','p'); INSERT INTO connections(id,provider_node_id,name,credential_type,secret_ref,priority) VALUES('c','p','C','api_key','secret-value',1);`); err != nil {
		t.Fatal(err)
	}
	bundle, err := ExportBundle(s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyBundle(s, bundle); err != nil {
		t.Fatal(err)
	}
	credential, ok := s.ConnectionCredentialByID("c")
	if !ok || credential.Secret != "secret-value" {
		t.Fatalf("credential=%#v ok=%v", credential, ok)
	}
}
