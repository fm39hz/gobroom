package provider

import (
	"testing"

	"github.com/fm39hz/gobroom/internal/kernel"
)

func TestAuthModeRecoveryPolicyIsExplicitAndPropagated(t *testing.T) {
	runtime, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}

	definition, ok := runtime.Primitives.Definition(ProviderDefinitionRef("openai-compatible-chat", 1))
	if !ok {
		t.Fatal("built-in provider definition missing")
	}
	if got := definition.AuthModes[0].Recovery; got == kernel.CredentialRecoveryRefresh {
		t.Fatalf("static-secret definition opted into recovery: %q", got)
	}
	definition.ID = "opaque-ticket-fixture"
	definition.AuthModes = append([]AuthModeDefinition(nil), definition.AuthModes...)
	definition.AuthModes[0].CredentialType = "opaque_ticket"
	definition.DefaultCredentialType = "opaque_ticket"
	definition.AuthModes[0].Recovery = kernel.CredentialRecoveryRefresh

	manifest, err := EncodeProviderDefinitionJSON(definition)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeProviderDefinitionJSON(manifest)
	if err != nil {
		t.Fatalf("decode recovery-configured manifest: %v", err)
	}
	if decoded.AuthModes[0].Recovery != kernel.CredentialRecoveryRefresh {
		t.Fatalf("manifest recovery policy=%q", decoded.AuthModes[0].Recovery)
	}
	if err := runtime.Primitives.RegisterDefinition(decoded); err != nil {
		t.Fatalf("register recovery-configured manifest: %v", err)
	}

	binding, err := NewRuntimeBindingBuilder(runtime).Build(ProviderDefinitionRef(decoded.ID, decoded.ContractVersion), OperationChat)
	if err != nil {
		t.Fatalf("build auth mode: %v", err)
	}
	if got := binding.AuthModes["opaque_ticket"].Recovery; got != kernel.CredentialRecoveryRefresh {
		t.Fatalf("bound recovery policy=%q", got)
	}

	metadata, err := runtime.DefinitionCatalog()
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, providerMetadata := range metadata {
		if providerMetadata.ID != decoded.ID {
			continue
		}
		for _, authMode := range providerMetadata.AuthModes {
			if authMode.CredentialType == "opaque_ticket" {
				found = true
				if authMode.Recovery != kernel.CredentialRecoveryRefresh {
					t.Fatalf("metadata recovery policy=%q", authMode.Recovery)
				}
			}
		}
	}
	if !found {
		t.Fatal("definition metadata omitted opaque_ticket auth mode")
	}

	defaultBinding, err := NewRuntimeBindingBuilder(runtime).Build(ProviderDefinitionRef("openai-compatible-chat", 1), OperationChat)
	if err != nil {
		t.Fatal(err)
	}
	if got := defaultBinding.AuthModes["api_key"].Recovery; got != kernel.CredentialRecoveryNone {
		t.Fatalf("omitted definition policy did not normalize to none: %q", got)
	}
	for _, providerMetadata := range metadata {
		if providerMetadata.ID != "openai-compatible-chat" {
			continue
		}
		if len(providerMetadata.AuthModes) == 0 || providerMetadata.AuthModes[0].Recovery != kernel.CredentialRecoveryNone {
			t.Fatalf("omitted metadata policy did not normalize to none: %#v", providerMetadata.AuthModes)
		}
		return
	}
	t.Fatal("built-in provider metadata missing")
}
