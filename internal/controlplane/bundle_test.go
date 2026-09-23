package controlplane

import (
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
