package provider

import (
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
)

func TestAuthorizationAwaitContractSeparatesWaitAndInput(t *testing.T) {
	now := time.Now()
	state, _ := NewAuthorizationState("private")
	valid := AuthorizationTransition{State: state, Await: true, ExpiresAt: now.Add(20 * time.Minute)}
	if err := valid.Validate(now); err != nil {
		t.Fatal(err)
	}
	input := AuthorizationAction{ID: "input", Kind: AuthorizationInput, Label: "Input", InputSchemaRef: &extensions.Ref{Kind: extensions.SchemaKind, ID: "test.input", ContractVersion: 1}}
	credential, _ := NewCredentialState("credential")
	for _, invalid := range []AuthorizationTransition{
		{State: state, Await: true, PollAfter: time.Second, ExpiresAt: valid.ExpiresAt},
		{State: state, Await: true, Actions: []AuthorizationAction{input}, ExpiresAt: valid.ExpiresAt},
		{Await: true, Credential: &credential},
		{Await: true, Failure: AuthorizationDenied},
		{State: state, Await: true, ExpiresAt: now.Add(MaxAuthorizationDuration + time.Second)},
	} {
		if err := invalid.Validate(now); err == nil {
			t.Fatal("accepted conflicting or unbounded authorization wait")
		}
	}
}

func TestOAuthFactoryPreservesCodeCapabilityAlongsideDeviceActions(t *testing.T) {
	flow, err := OAuthAuthFactory(AuthOptions{OAuth: &OAuthFlowOptions{ClientID: "client", AuthURL: "https://identity.test/auth", TokenURL: "https://identity.test/token", DeviceAuthURL: "https://identity.test/device", RedirectURL: "http://127.0.0.1:12345/callback"}})
	if err != nil {
		t.Fatal(err)
	}
	if !AuthorizationCodeEnabled(flow) {
		t.Fatal("device composition hid the existing code capability")
	}
	if _, ok := flow.(AuthorizationFlow); !ok {
		t.Fatal("device factory lacks generic actions")
	}
}

func TestAuthorizationAwaitRejectsOldModuleHost(t *testing.T) {
	_, err := NewRuntimeRegistryWithModules(RuntimeModuleFunc{Name: "fixture.old-device", Version: "1", HostContractVersion: 3, Init: func(*RuntimeRegistry) error { return nil }})
	if err == nil {
		t.Fatal("removed device lifecycle host contract was accepted")
	}
}
