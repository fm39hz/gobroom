package provider

import (
	"context"
	"encoding/json"
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
	return kernel.Credential{ConnectionID: input.ConnectionID, Type: input.Type, Secret: input.Secret, ClientID: a.issuer}, nil
}
func (a deviceOAuthFixture) Refresh(_ context.Context, credential kernel.Credential) (kernel.Credential, error) {
	return credential, nil
}

func TestStaticSecretAuthIsReusablePrimitive(t *testing.T) {
	registry := NewAuthRegistry()
	flow, ok := registry.Resolve("static-secret")
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
	definition := ProviderDefinition{ContractVersion: 1, ID: "media-oauth", Version: "1", DisplayName: "Media OAuth", Auth: PrimitiveRef{Kind: PrimitiveAuth, ID: deviceAuthID, ContractVersion: 1}, AuthOptions: AuthOptions{Options: json.RawMessage(`{"issuer":"login.example"}`)}, Operations: map[Operation]OperationBinding{Operation(operationID): {
		Protocol: kernel.Protocol("vendor.audio.v1"), TaskRef: OperationRef(operationID, 1),
		Endpoint: PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json", ContractVersion: 1}, Transport: PrimitiveRef{Kind: PrimitiveTransport, ID: "http", ContractVersion: 1},
		RequestCodec: PrimitiveRef{Kind: PrimitiveRequestCodec, ID: "openai-chat-json", ContractVersion: 1}, ResponseDecoder: PrimitiveRef{Kind: PrimitiveResponseDecoder, ID: "openai-sse", ContractVersion: 1},
	}}}
	if err := registry.Primitives.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	binding, err := NewRuntimeBindingBuilder(registry).Build(definition.ID, Operation(operationID))
	if err != nil {
		t.Fatal(err)
	}
	credential, err := binding.Auth.Resolve(context.Background(), AuthInput{ConnectionID: "conn", Type: "oauth-device", Secret: "access-token"})
	if err != nil || credential.Secret != "access-token" || credential.ClientID != "login.example" {
		t.Fatalf("provider auth extension credential=%#v err=%v", credential, err)
	}
}
