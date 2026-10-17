package discovery

import (
	"testing"

	"github.com/fm39hz/gobroom/internal/store"
)

func TestDiscoveryCredentialBindingRequiresExactOwnerAndMode(t *testing.T) {
	node := store.ProviderNode{ID: "node", DefinitionID: "provider", DefinitionContractVersion: 1}
	selected := store.ConnectionRecord{ID: "account", CredentialType: "api_key"}
	credential := store.Credential{ID: selected.ID, Type: selected.CredentialType, Owner: store.CredentialOwner{ProviderNodeID: node.ID, Definition: node.DefinitionRef()}}
	if err := validateCredentialBinding(node, selected, credential); err != nil {
		t.Fatal(err)
	}
	for name, alter := range map[string]func(*store.Credential){
		"account":    func(c *store.Credential) { c.ID = "another" },
		"mode":       func(c *store.Credential) { c.Type = "another" },
		"node":       func(c *store.Credential) { c.Owner.ProviderNodeID = "another" },
		"definition": func(c *store.Credential) { c.Owner.Definition.ID = "another" },
		"version":    func(c *store.Credential) { c.Owner.Definition.ContractVersion++ },
	} {
		t.Run(name, func(t *testing.T) {
			stale := credential
			alter(&stale)
			if validateCredentialBinding(node, selected, stale) == nil {
				t.Fatal("stale connection binding was accepted")
			}
		})
	}
}
