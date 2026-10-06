package controlplane

import (
	"encoding/json"
	"testing"

	"github.com/fm39hz/gobroom/internal/store"
)

func TestValidateBundleRejectsUnknownTypedReferences(t *testing.T) {
	bundle := ConfigBundle{Version: 1, Providers: []store.ProviderNode{{ID: "p"}}, Connections: []store.ConnectionRecord{{ID: "c", ProviderNodeID: "p"}}, Models: []store.Model{{ID: "r"}}, Physical: []store.PhysicalModel{{Name: "qwen", Sources: []store.RouteReference{{RouteID: "missing"}}}}}
	if err := ValidateBundle(bundle); err == nil {
		t.Fatal("expected unknown route rejection")
	}
}

func TestValidateBundleAcceptsSecretFreeTypedGraph(t *testing.T) {
	bundle := ConfigBundle{Version: 1, Providers: []store.ProviderNode{{ID: "p"}}, Connections: []store.ConnectionRecord{{ID: "c", ProviderNodeID: "p"}}, Models: []store.Model{{ID: "r"}}, Physical: []store.PhysicalModel{{Name: "qwen", Sources: []store.RouteReference{{RouteID: "r"}}}}}
	if err := ValidateBundle(bundle); err != nil {
		t.Fatal(err)
	}
}

func TestDiffBundleReportsTypedChanges(t *testing.T) {
	current := ConfigBundle{Version: 1, Providers: []store.ProviderNode{{ID: "old"}}}
	desired := ConfigBundle{Version: 1, Providers: []store.ProviderNode{{ID: "new"}}}
	diff, err := DiffBundle(current, desired)
	if err != nil {
		t.Fatal(err)
	}
	if diff.ProvidersAdded != 1 || diff.ProvidersRemoved != 1 || len(diff.Changes) != 2 {
		t.Fatalf("diff=%#v", diff)
	}
	encoded, err := json.Marshal(BundleDiff{ProvidersAdded: 1, ProvidersRemoved: 2, ConnectionsAdded: 3, ConnectionsRemoved: 4, ModelsAdded: 5, ModelsRemoved: 6, PhysicalAdded: 7, PhysicalRemoved: 8, CombosAdded: 9, CombosRemoved: 10})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]int
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 11 || fields["providersRemoved"] != 2 || fields["connectionsAdded"] != 3 || fields["physicalRemoved"] != 8 || fields["combosRemoved"] != 10 {
		t.Fatalf("bundle diff JSON lost field tags: %s", encoded)
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
	bundle, err := ExportBundle(s)
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
