package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/operations"
)

type deviceOAuthFixture struct{ issuer string }

func (a deviceOAuthFixture) ID() string { return "vendor.device-oauth.v1" }
func (a deviceOAuthFixture) SetupSchema() SetupSchema {
	return SetupSchema{Fields: []SetupField{{Name: "access_token", Type: "string", Required: true, Secret: true}, {Name: "refresh_token", Type: "string", Secret: true}}}
}
func (a deviceOAuthFixture) Resolve(_ context.Context, input AuthInput) (kernel.Credential, error) {
	if input.Secret == "" {
		return kernel.Credential{}, context.Canceled
	}
	return kernel.Credential{ConnectionID: input.ConnectionID, Type: input.Type, Secret: input.Secret, AuthContext: kernel.AuthContext{"issuer": a.issuer}, RequestHeaders: http.Header{"X-Provider-Device": []string{a.issuer}}}, nil
}
func (a deviceOAuthFixture) Refresh(_ context.Context, input AuthInput, credential kernel.Credential) (CredentialState, error) {
	return NewCredentialState(input.Secret)
}

type customStateAuth struct{}

func (customStateAuth) ID() string               { return "custom-state" }
func (customStateAuth) SetupSchema() SetupSchema { return SetupSchema{} }
func (customStateAuth) Resolve(_ context.Context, input AuthInput) (kernel.Credential, error) {
	var state struct {
		Token string `json:"vendor_token"`
	}
	if err := json.Unmarshal([]byte(input.Secret), &state); err != nil {
		return kernel.Credential{}, err
	}
	if state.Token == "" {
		return kernel.Credential{}, context.Canceled
	}
	return kernel.Credential{ConnectionID: input.ConnectionID, Type: input.Type, Secret: state.Token}, nil
}
func (customStateAuth) Refresh(_ context.Context, input AuthInput, _ kernel.Credential) (CredentialState, error) {
	return NewCredentialState(input.Secret)
}

func TestCustomAuthRefreshStateRemainsOpaqueUntilResolve(t *testing.T) {
	flow := customStateAuth{}
	input := AuthInput{ConnectionID: "conn", Type: "custom", Secret: `{"vendor_token":"new-token"}`}
	state, err := flow.Refresh(context.Background(), input, kernel.Credential{ConnectionID: "conn", Type: "custom", Secret: "old-token"})
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Encoded(); got != input.Secret {
		t.Fatalf("state=%q", got)
	}
	credential, err := flow.Resolve(context.Background(), AuthInput{ConnectionID: input.ConnectionID, Type: input.Type, Secret: state.Encoded()})
	if err != nil {
		t.Fatal(err)
	}
	if credential.Secret != "new-token" || credential.ConnectionID != input.ConnectionID {
		t.Fatalf("resolved credential=%#v", credential)
	}
}

func TestStaticSecretAuthIsReusablePrimitive(t *testing.T) {
	registry := NewAuthRegistry()
	flow, ok := registry.Resolve(extensions.Ref{Kind: string(PrimitiveAuth), ID: "static-secret", ContractVersion: 1})
	if !ok {
		t.Fatal("static-secret flow is not registered")
	}
	credential, err := flow.Resolve(context.Background(), AuthInput{ConnectionID: "conn-a", Type: "api_key", Secret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if credential.ConnectionID != "conn-a" || credential.Secret != "secret" {
		t.Fatalf("credential=%#v", credential)
	}
	if _, err := flow.Resolve(context.Background(), AuthInput{}); err == nil {
		t.Fatal("expected missing secret error")
	}
}

func TestOAuthAuthFactoryBuildsGenericDeviceAuthorizationWithoutAuthURL(t *testing.T) {
	flow, err := OAuthAuthFactory(AuthOptions{OAuth: &OAuthFlowOptions{
		ClientID: "device-client", TokenURL: "https://identity.example/token", DeviceAuthURL: "https://identity.example/device",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := flow.(AuthorizationFlow); !ok {
		t.Fatalf("device OAuth flow does not implement generic authorization: %T", flow)
	}
}

func TestOAuthAuthFactoryKeepsTokenOnlyConfigurationNoninteractive(t *testing.T) {
	flow, err := OAuthAuthFactory(AuthOptions{OAuth: &OAuthFlowOptions{
		ClientID: "token-client", AuthURL: "https://identity.example/authorize", TokenURL: "https://identity.example/token",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := flow.(AuthorizationFlow); ok {
		t.Fatalf("OAuth configuration without a redirect unexpectedly enabled interactive setup: %T", flow)
	}
}

func TestAuthPrimitiveVersionsBindTheirOwnFactoriesAndSchemas(t *testing.T) {
	registry, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	schemaRef := extensions.Ref{Kind: extensions.SchemaKind, ID: "gobroom.auth.oauth2.options", ContractVersion: 2}
	schema, err := bindSchemaDocument(schemaRef, json.RawMessage(`{
"type":"object","properties":{"oauth":{"type":"object","properties":{"clientId":{"type":"string","minLength":1},"authUrl":{"type":"string","format":"uri"},"tokenUrl":{"type":"string","format":"uri"},"scopes":{"type":"array","items":{"type":"string"}},"redirectUrl":{"type":"string","format":"uri"}},"required":["clientId","authUrl","tokenUrl"],"additionalProperties":false},"options":false},"required":["oauth"],"additionalProperties":false
}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterAuthFactoryWithOptionsSchema("oauth2", 2, schemaRef, schema, OAuthAuthFactory); err != nil {
		t.Fatal(err)
	}
	snapshot, err := registry.FreezeCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		version uint64
		client  string
	}{
		{version: 1, client: "client-v1"},
		{version: 2, client: "client-v2"},
	} {
		ref := extensions.Ref{Kind: string(PrimitiveAuth), ID: "oauth2", ContractVersion: test.version}
		options, _ := json.Marshal(AuthOptions{OAuth: &OAuthFlowOptions{ClientID: test.client, AuthURL: "https://auth.example/authorize", TokenURL: "https://auth.example/token"}})
		_, implementation, err := snapshot.Bind(ref, options)
		if err != nil {
			t.Fatalf("bind %s: %v", ref.Key(), err)
		}
		flow, ok := implementation.(OAuthAuth)
		if !ok || flow.Config.ClientID != test.client {
			t.Fatalf("auth ref %s resolved to %#v", ref.Key(), implementation)
		}
	}
}

func TestProviderSpecificDeviceOAuthFlowUsesGenericAuthExtensionContract(t *testing.T) {
	registry, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	const operationID = "audio.transcribe.v1"
	if err := registry.Primitives.Operations.RegisterRawPayload(extensions.Ref{Kind: "operation", ID: operationID, ContractVersion: 1}, "Audio transcription", "Transcribe audio input", json.RawMessage(`{"type":"object","properties":{"audio":{"type":"string","minLength":1}},"required":["audio"],"additionalProperties":false}`), operations.ReplayNever); err != nil {
		t.Fatal(err)
	}
	const deviceAuthID = "vendor.device-oauth.v1"
	deviceSchemaRef := extensions.Ref{Kind: extensions.SchemaKind, ID: "vendor.example.device-oauth-options", ContractVersion: 1}
	deviceSchema, err := bindSchemaDocument(deviceSchemaRef, json.RawMessage(`{"type":"object","properties":{"options":{"type":"object","properties":{"issuer":{"type":"string","minLength":1}},"required":["issuer"],"additionalProperties":false}},"required":["options"],"additionalProperties":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterAuthFactoryWithOptionsSchema(deviceAuthID, 1, deviceSchemaRef, deviceSchema, func(options AuthOptions) (AuthFlow, error) {
		var config struct {
			Issuer string `json:"issuer"`
		}
		if err := json.Unmarshal(options.Options, &config); err != nil {
			return nil, err
		}
		if config.Issuer == "" {
			return nil, context.Canceled
		}
		return deviceOAuthFixture{issuer: config.Issuer}, nil
	}); err != nil {
		t.Fatal(err)
	}
	definition := ProviderDefinition{ContractVersion: 1, ID: "media-oauth", Version: "1", DisplayName: "Media OAuth", DefaultCredentialType: "oauth-device", AuthModes: []AuthModeDefinition{{CredentialType: "oauth-device", Auth: PrimitiveRef{Kind: PrimitiveAuth, ID: deviceAuthID, ContractVersion: 1}, AuthOptions: AuthOptions{Options: json.RawMessage(`{"issuer":"login.example"}`)}}}, Operations: map[Operation]OperationBinding{Operation(operationID): {
		Protocol: kernel.Protocol("vendor.audio.v1"), TaskRef: OperationRef(operationID, 1),
		Endpoint: PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json", ContractVersion: 1}, Transport: PrimitiveRef{Kind: PrimitiveTransport, ID: "http", ContractVersion: 1},
		RequestCodec: PrimitiveRef{Kind: PrimitiveRequestCodec, ID: "openai-chat-json", ContractVersion: 1}, ResponseDecoder: PrimitiveRef{Kind: PrimitiveResponseDecoder, ID: "openai-sse", ContractVersion: 1},
	}}}
	if err := registry.Primitives.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	binding, err := NewRuntimeBindingBuilder(registry).Build(ProviderDefinitionRef(definition.ID, definition.ContractVersion), Operation(operationID))
	if err != nil {
		t.Fatal(err)
	}
	credential, err := binding.AuthModes["oauth-device"].Flow.Resolve(context.Background(), AuthInput{ConnectionID: "conn", Type: "oauth-device", Secret: "access-token"})
	if err != nil || credential.Secret != "access-token" || credential.AuthContext["issuer"] != "login.example" || credential.RequestHeaders.Get("X-Provider-Device") != "login.example" {
		t.Fatalf("provider auth extension credential=%#v err=%v", credential, err)
	}
	requestHeaders := make(http.Header)
	if err := kernel.ApplyCredentialPresentation(requestHeaders, credential, kernel.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "}); err != nil {
		t.Fatal(err)
	}
	if requestHeaders.Get("Authorization") != "Bearer access-token" || requestHeaders.Get("X-Provider-Device") != "login.example" {
		t.Fatalf("auth-flow-derived dynamic headers were not applied: %#v", requestHeaders)
	}
}
