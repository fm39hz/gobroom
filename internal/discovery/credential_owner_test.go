package discovery

import (
	"testing"

	"github.com/fm39hz/gobroom/internal/store"
)

func TestDiscoveryCredentialBindingRequiresExactOwnerAndMode(t *testing.T) {
	node := store.ProviderNode{ID: "node", DefinitionID: "provider", DefinitionContractVersion: 1}
	selected := store.ConnectionRecord{ID: "account", CredentialType: "api_key"}
	binding := store.CredentialBinding{ID: selected.ID, Type: selected.CredentialType, Revision: "credential-r1", PrincipalGeneration: "principal-r1", Owner: store.CredentialOwner{ProviderNodeID: node.ID, Definition: node.DefinitionRef()}}
	if err := validateCredentialBinding(node, selected, binding); err != nil {
		t.Fatal(err)
	}
	for name, alter := range map[string]func(*store.CredentialBinding){
		"account":              func(c *store.CredentialBinding) { c.ID = "another" },
		"mode":                 func(c *store.CredentialBinding) { c.Type = "another" },
		"node":                 func(c *store.CredentialBinding) { c.Owner.ProviderNodeID = "another" },
		"definition":           func(c *store.CredentialBinding) { c.Owner.Definition.ID = "another" },
		"version":              func(c *store.CredentialBinding) { c.Owner.Definition.ContractVersion++ },
		"revision":             func(c *store.CredentialBinding) { c.Revision = "" },
		"principal generation": func(c *store.CredentialBinding) { c.PrincipalGeneration = "" },
	} {
		t.Run(name, func(t *testing.T) {
			stale := binding
			alter(&stale)
			if validateCredentialBinding(node, selected, stale) == nil {
				t.Fatal("stale connection binding was accepted")
			}
		})
	}
}
