package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/operations"
	"github.com/fm39hz/gobroom/internal/quota"
)

func testPrimitiveRegistry() *PrimitiveRegistry {
	r := NewPrimitiveRegistry()
	if err := operations.RegisterChatGenerate(r.catalog, r.Operations); err != nil {
		panic(err)
	}
	for _, item := range []struct {
		kind PrimitiveKind
		id   string
	}{
		{PrimitiveEndpoint, "http-json"},
		{PrimitiveTransport, "http"},
		{PrimitiveAuth, "static-secret"},
		{PrimitiveRequestCodec, "openai-chat-json"},
		{PrimitiveResponseDecoder, "openai-sse"},
		{PrimitiveModelSource, "openai-models"},
		{PrimitiveErrorClassifier, "http-json"},
	} {
		if err := r.RegisterPrimitive(item.kind, item.id); err != nil {
			panic(err)
		}
	}
	return r
}

type schemaBoundRequestTransform struct{ optionsSchemaRef extensions.Ref }

func (schemaBoundRequestTransform) Definition() kernel.TransformDefinition {
	optionsRef := extensions.Ref{Kind: extensions.SchemaKind, ID: "fixture.transform-options", ContractVersion: 1}
	return kernel.TransformDefinition{
		Ref:                   extensions.Ref{Kind: kernel.RequestTransformKind, ID: "fixture.add-suffix", ContractVersion: 1},
		ImplementationVersion: "1.0.0", Label: "Add suffix", Description: "Add a configured suffix to user text.",
		OptionsSchemaRef: &optionsRef, Stage: kernel.TransformBeforeRequirements,
		Effects: []kernel.TransformEffect{kernel.TransformInput},
	}
}

func (schemaBoundRequestTransform) Apply(context.Context, *kernel.NormalizedRequest, json.RawMessage) error {
	return nil
}

func TestPrimitiveRegistryValidatesComposedProvider(t *testing.T) {
	r := testPrimitiveRegistry()
	err := r.RegisterDefinition(ProviderDefinition{ContractVersion: 1, ID: "openai-compatible",
		Version:     "1",
		DisplayName: "OpenAI-compatible",
		Auth:        PrimitiveRef{Kind: PrimitiveAuth, ID: "static-secret", ContractVersion: 1},
		Operations: map[Operation]OperationBinding{
			OperationChat: {
				TaskRef:         OperationRef(normalize.OperationChatGenerate, 1),
				Endpoint:        PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json", ContractVersion: 1},
				Transport:       PrimitiveRef{Kind: PrimitiveTransport, ID: "http", ContractVersion: 1},
				RequestCodec:    PrimitiveRef{Kind: PrimitiveRequestCodec, ID: "openai-chat-json", ContractVersion: 1},
				ResponseDecoder: PrimitiveRef{Kind: PrimitiveResponseDecoder, ID: "openai-sse", ContractVersion: 1},
				ModelSource:     PrimitiveRef{Kind: PrimitiveModelSource, ID: "openai-models", ContractVersion: 1},
				ErrorClassifier: PrimitiveRef{Kind: PrimitiveErrorClassifier, ID: "http-json", ContractVersion: 1},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Definition("openai-compatible"); !ok {
		t.Fatal("definition was not registered")
	}
}

func TestPrimitiveRegistryRejectsUnknownPrimitiveAndDuplicateDefinition(t *testing.T) {
	r := NewPrimitiveRegistry()
	if err := r.RegisterDefinition(ProviderDefinition{ContractVersion: 1, ID: "bad", Version: "1", DisplayName: "Bad", Operations: map[Operation]OperationBinding{OperationChat: {Endpoint: PrimitiveRef{Kind: PrimitiveEndpoint, ID: "missing", ContractVersion: 1}, RequestCodec: PrimitiveRef{Kind: PrimitiveRequestCodec, ID: "missing", ContractVersion: 1}, ResponseDecoder: PrimitiveRef{Kind: PrimitiveResponseDecoder, ID: "missing", ContractVersion: 1}}}}); err == nil {
		t.Fatal("expected unknown primitive error")
	}
	r = testPrimitiveRegistry()
	definition := ProviderDefinition{ContractVersion: 1, ID: "same", Version: "1", DisplayName: "Same", Auth: PrimitiveRef{Kind: PrimitiveAuth, ID: "static-secret", ContractVersion: 1}, Operations: map[Operation]OperationBinding{OperationChat: {TaskRef: OperationRef(normalize.OperationChatGenerate, 1), Endpoint: PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json", ContractVersion: 1}, Transport: PrimitiveRef{Kind: PrimitiveTransport, ID: "http", ContractVersion: 1}, RequestCodec: PrimitiveRef{Kind: PrimitiveRequestCodec, ID: "openai-chat-json", ContractVersion: 1}, ResponseDecoder: PrimitiveRef{Kind: PrimitiveResponseDecoder, ID: "openai-sse", ContractVersion: 1}}}}
	if err := r.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterDefinition(definition); err == nil {
		t.Fatal("expected duplicate definition error")
	}
}

func TestRuntimeRegistryRejectsNilCodecImplementation(t *testing.T) {
	runtime, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.RegisterRequestCodec(nil); err == nil {
		t.Fatal("expected nil codec error")
	}
	if err := runtime.RegisterRequestCodec(runtime.requestCodecs[extensions.Ref{Kind: string(PrimitiveRequestCodec), ID: "openai-chat-json", ContractVersion: 1}]); err == nil {
		t.Fatal("duplicate primitive implementation must not silently replace the registered codec")
	}
}

func TestProviderDefinitionJSONBindingRoundTripsTypedContract(t *testing.T) {
	r := testPrimitiveRegistry()
	definition := ProviderDefinition{ContractVersion: 1, ID: "json-provider", Version: "1", DisplayName: "JSON Provider", Auth: PrimitiveRef{Kind: PrimitiveAuth, ID: "static-secret", ContractVersion: 1}, Operations: map[Operation]OperationBinding{OperationChat: {TaskRef: OperationRef(normalize.OperationChatGenerate, 1), Endpoint: PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json", ContractVersion: 1}, Transport: PrimitiveRef{Kind: PrimitiveTransport, ID: "http", ContractVersion: 1}, RequestCodec: PrimitiveRef{Kind: PrimitiveRequestCodec, ID: "openai-chat-json", ContractVersion: 1}, ResponseDecoder: PrimitiveRef{Kind: PrimitiveResponseDecoder, ID: "openai-sse", ContractVersion: 1}}}}
	data, err := EncodeProviderDefinitionJSON(definition)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeProviderDefinitionJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterDefinition(decoded); err != nil {
		t.Fatal(err)
	}
}

func TestProviderDefinitionPortabilityRequiresSecretsAndOpaqueOptionsToStayExternal(t *testing.T) {
	base := ProviderDefinition{ContractVersion: 1, ID: "portable", Version: "1", DisplayName: "Portable",
		Auth: PrimitiveRef{Kind: PrimitiveAuth, ID: "static-secret", ContractVersion: 1},
		Operations: map[Operation]OperationBinding{OperationChat: {
			Endpoint:        PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json", ContractVersion: 1},
			Transport:       PrimitiveRef{Kind: PrimitiveTransport, ID: "http", ContractVersion: 1},
			RequestCodec:    PrimitiveRef{Kind: PrimitiveRequestCodec, ID: "openai-chat-json", ContractVersion: 1},
			ResponseDecoder: PrimitiveRef{Kind: PrimitiveResponseDecoder, ID: "openai-sse", ContractVersion: 1},
		}},
	}
	if portable, reason := ProviderDefinitionPortable(base); !portable || reason != "" {
		t.Fatalf("plain typed provider definition should be portable: portable=%v reason=%q", portable, reason)
	}
	cases := []struct {
		name   string
		mutate func(*ProviderDefinition)
	}{
		{"auth options", func(definition *ProviderDefinition) {
			definition.AuthOptions.Options = json.RawMessage(`{"client_secret":"secret"}`)
		}},
		{"defaults", func(definition *ProviderDefinition) { definition.Defaults = map[string]any{"apiKey": "secret"} }},
		{"request codec options", func(definition *ProviderDefinition) {
			binding := definition.Operations[OperationChat]
			binding.RequestCodecOptions = json.RawMessage(`{"apiKey":"secret"}`)
			definition.Operations[OperationChat] = binding
		}},
		{"endpoint query", func(definition *ProviderDefinition) {
			binding := definition.Operations[OperationChat]
			binding.EndpointOptions.Query = map[string]string{"key": "secret"}
			definition.Operations[OperationChat] = binding
		}},
		{"oauth URL query", func(definition *ProviderDefinition) {
			definition.AuthOptions.OAuth = &OAuthFlowOptions{ClientID: "client", AuthURL: "https://identity.test/auth?client_secret=secret", TokenURL: "https://identity.test/token"}
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			definition := base
			definition.Operations = map[Operation]OperationBinding{OperationChat: base.Operations[OperationChat]}
			test.mutate(&definition)
			if portable, reason := ProviderDefinitionPortable(definition); portable || reason == "" {
				t.Fatalf("opaque/sensitive definition was embedded: portable=%v reason=%q", portable, reason)
			}
		})
	}
}

func TestRuntimeRegistryLoadsExternalManifest(t *testing.T) {
	runtime, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"contractVersion":1,"id":"external-chat","version":"1","displayName":"External Chat","auth":{"kind":"auth","id":"static-secret","contractVersion":1},"operations":{"chat":{"task":{"kind":"operation","id":"chat.generate","contractVersion":1},"endpoint":{"kind":"endpoint","id":"http-json","contractVersion":1},"transport":{"kind":"transport","id":"http","contractVersion":1},"requestCodec":{"kind":"request_codec","id":"openai-chat-json","contractVersion":1},"responseDecoder":{"kind":"response_decoder","id":"openai-sse","contractVersion":1},"errorClassifier":{"kind":"error_classifier","id":"http-json","contractVersion":1}}}}`)
	if err := runtime.LoadDefinitionJSON(data); err != nil {
		t.Fatal(err)
	}
	if _, ok := runtime.Primitives.Definition("external-chat"); !ok {
		t.Fatal("external definition was not loaded")
	}
}

func TestManifestBindsTypedHTTPJSONErrorPathsToSharedClassifier(t *testing.T) {
	registry, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	definition := ProviderDefinition{ContractVersion: 1, ID: "custom-errors", Version: "1", DisplayName: "Custom error envelope",
		Auth: PrimitiveRef{Kind: PrimitiveAuth, ID: "static-secret", ContractVersion: 1},
		Operations: map[Operation]OperationBinding{OperationChat: {
			TaskRef:         OperationRef(normalize.OperationChatGenerate, 1),
			Endpoint:        PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json", ContractVersion: 1},
			Transport:       PrimitiveRef{Kind: PrimitiveTransport, ID: "http", ContractVersion: 1},
			RequestCodec:    PrimitiveRef{Kind: PrimitiveRequestCodec, ID: "openai-chat-json", ContractVersion: 1},
			ResponseDecoder: PrimitiveRef{Kind: PrimitiveResponseDecoder, ID: "openai-sse", ContractVersion: 1},
			ErrorClassifier: PrimitiveRef{Kind: PrimitiveErrorClassifier, ID: "http-json", ContractVersion: 1},
			ErrorClassifierOptions: ErrorClassifierOptions{HTTPJSON: &HTTPJSONErrorClassifierOptions{
				CodePath: "/failure/reason", MessagePath: "/failure/explanation", ResetAtPath: "/failure/reopens_at",
				WindowNamePath: "/failure/window", WindowLimitPath: "/failure/budget", WindowUsedPath: "/failure/spent", WindowRemainingPath: "/failure/left",
				QuotaWindowKind: "tokens", QuotaCodes: []string{"BALANCE_EMPTY"}, QuotaMessageTokens: []string{"credits exhausted"},
			}},
		}},
	}
	encoded, err := EncodeProviderDefinitionJSON(definition)
	if err != nil {
		t.Fatal(err)
	}
	definition, err = DecodeProviderDefinitionJSON(encoded)
	if err != nil {
		t.Fatalf("decode typed classifier manifest options: %v", err)
	}
	manifest, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.LoadDefinitionJSON(manifest); err != nil {
		t.Fatal(err)
	}
	bindings, err := registry.BuildBindings()
	if err != nil {
		t.Fatal(err)
	}
	binding := bindings[RuntimeBindingKey(definition.ID, OperationChat)]
	if binding.ErrorClassifierRef != RuntimeErrorClassifierRef(definition.ID, OperationChat, PrimitiveRef{Kind: PrimitiveErrorClassifier, ID: "http-json", ContractVersion: 1}) {
		t.Fatalf("configured classifier ref=%q", binding.ErrorClassifierRef.Key())
	}
	classifiers := registry.ErrorClassifiersForBindings(bindings)
	classifier, ok := classifiers[binding.ErrorClassifierRef].(kernel.OutcomeClassifier)
	if !ok {
		t.Fatal("configured rich classifier was not installed under its bound identity")
	}
	reset := time.Now().Add(7 * time.Minute).UTC().Truncate(time.Second)
	body := []byte(`{"failure":{"reason":"BALANCE_EMPTY","explanation":"monthly credits exhausted","reopens_at":"` + reset.Format(time.RFC3339) + `","window":"weekly","budget":1000,"spent":1000,"left":0}}`)
	outcome := classifier.ClassifyOutcome(http.StatusTooManyRequests, nil, body)
	if outcome.Cause != kernel.CauseQuotaExhausted || outcome.RetryAt.IsZero() || len(outcome.Limits) != 1 {
		t.Fatalf("configured error outcome=%#v", outcome)
	}
	window := outcome.Limits[0]
	if window.Name != "weekly" || window.Kind != "tokens" || window.Limit == nil || *window.Limit != 1000 || window.Used == nil || *window.Used != 1000 || window.Remaining == nil || *window.Remaining != 0 || window.ResetAt == nil || !window.ResetAt.Equal(reset) {
		t.Fatalf("configured named limit window=%#v", window)
	}
}

func TestManifestRejectsInvalidErrorEvidencePointer(t *testing.T) {
	registry, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	definition := ProviderDefinition{ContractVersion: 1, ID: "bad-error-path", Version: "1", DisplayName: "Bad error path",
		Auth: PrimitiveRef{Kind: PrimitiveAuth, ID: "static-secret", ContractVersion: 1},
		Operations: map[Operation]OperationBinding{OperationChat: {
			TaskRef:                OperationRef(normalize.OperationChatGenerate, 1),
			Endpoint:               PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json", ContractVersion: 1},
			Transport:              PrimitiveRef{Kind: PrimitiveTransport, ID: "http", ContractVersion: 1},
			RequestCodec:           PrimitiveRef{Kind: PrimitiveRequestCodec, ID: "openai-chat-json", ContractVersion: 1},
			ResponseDecoder:        PrimitiveRef{Kind: PrimitiveResponseDecoder, ID: "openai-sse", ContractVersion: 1},
			ErrorClassifier:        PrimitiveRef{Kind: PrimitiveErrorClassifier, ID: "http-json", ContractVersion: 1},
			ErrorClassifierOptions: ErrorClassifierOptions{HTTPJSON: &HTTPJSONErrorClassifierOptions{ResetAtPath: "error.reset"}},
		}},
	}
	if err := registry.Primitives.RegisterDefinition(definition); err == nil {
		t.Fatal("invalid non-absolute JSON path should reject manifest registration")
	}
}

func TestProviderManifestRejectsUnknownAndRemovedFields(t *testing.T) {
	registry, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"id":"old-shape","version":"1","displayName":"Old shape","operations":{"chat":{"endpoint":{"kind":"endpoint","id":"http-json"},"transport":{"kind":"transport","id":"http"},"requestCodec":{"kind":"request_codec","id":"openai-chat-json"},"responseDecoder":{"kind":"response_decoder","id":"openai-sse"},"runtimeAdapterId":"openai-chat"}}}`)
	if err := registry.LoadDefinitionJSON(data); err == nil {
		t.Fatal("unknown/removed runtimeAdapterId field must not be silently ignored")
	}
}

func TestPrimitiveReferencesRequireTheExactContractVersion(t *testing.T) {
	registry := NewPrimitiveRegistry()
	for _, primitive := range []struct {
		kind PrimitiveKind
		id   string
	}{
		{PrimitiveAuth, "static-secret"},
		{PrimitiveEndpoint, "http-json"},
		{PrimitiveTransport, "http"},
	} {
		if err := registry.RegisterPrimitiveVersion(primitive.kind, primitive.id, 1); err != nil {
			t.Fatal(err)
		}
	}
	definition := ProviderDefinition{ContractVersion: 1, ID: "versioned-provider", Version: "1", DisplayName: "Versioned provider",
		Auth: PrimitiveRef{Kind: PrimitiveAuth, ID: "static-secret", ContractVersion: 1},
		Operations: map[Operation]OperationBinding{OperationChat: {
			Endpoint:  PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json"},
			Transport: PrimitiveRef{Kind: PrimitiveTransport, ID: "http", ContractVersion: 1},
		}},
	}
	definition.ContractVersion = 0
	if err := registry.RegisterDefinition(definition); err == nil || !strings.Contains(err.Error(), "contractVersion 1") {
		t.Fatalf("unsupported or missing provider contract version was accepted: %v", err)
	}
	definition.ContractVersion = 1
	if err := registry.RegisterDefinition(definition); err == nil || !strings.Contains(err.Error(), "contractVersion") {
		t.Fatalf("reference without contractVersion was accepted: %v", err)
	}
	binding := definition.Operations[OperationChat]
	binding.Endpoint.ContractVersion = 2
	definition.Operations[OperationChat] = binding
	if err := registry.RegisterDefinition(definition); err == nil || !strings.Contains(err.Error(), "contract 2") {
		t.Fatalf("unsupported primitive contract version was accepted: %v", err)
	}
	binding.Endpoint.ContractVersion = 1
	definition.Operations[OperationChat] = binding
	if err := registry.RegisterDefinition(definition); err != nil {
		t.Fatalf("exact registered primitive contract was rejected: %v", err)
	}
}

func TestBuiltinManifestsBuildExecutableBindings(t *testing.T) {
	registry, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.LoadDefinitionDir(filepath.Join("..", "..", "manifests", "builtin")); err != nil {
		t.Fatal(err)
	}
	bindings, err := registry.BuildBindings()
	if err != nil {
		t.Fatal(err)
	}
	for _, definitionID := range []string{"openai", "openrouter", "deepseek", "anthropic"} {
		definition, ok := registry.Primitives.Definition(definitionID)
		if !ok {
			t.Fatalf("manifest %q was not loaded", definitionID)
		}
		for operation := range definition.Operations {
			if _, ok := bindings[RuntimeBindingKey(definitionID, operation)]; !ok {
				t.Fatalf("binding missing for %s/%s", definitionID, operation)
			}
		}
	}
}

func TestDecodeQuotaJSONNormalizesGenericShape(t *testing.T) {
	values, err := DecodeQuotaJSON([]byte(`{"usage":{"consumed":3,"total":10,"left":7},"window":"daily"}`), kernel.Route{NodeID: "node", CredentialID: "conn", ExternalModel: "model"}, "")
	if err != nil || len(values) != 1 {
		t.Fatalf("values=%#v err=%v", values, err)
	}
	if values[0].Used != 3 || values[0].Limit == nil || *values[0].Limit != 10 || values[0].Remaining == nil || *values[0].Remaining != 7 {
		t.Fatalf("snapshot=%#v", values[0])
	}
}

func TestRuntimeBindingBuilderBuildsComposedOperation(t *testing.T) {
	registry, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	binding, err := NewRuntimeBindingBuilder(registry).Build("openai-compatible-chat", OperationChat)
	if err != nil {
		t.Fatal(err)
	}
	if binding.AdapterID != "openai-compatible-chat:chat" || binding.Adapter == nil || binding.Auth == nil || binding.AuthFlowID != AuthBindingKey("openai-compatible-chat", PrimitiveRef{Kind: PrimitiveAuth, ID: "static-secret", ContractVersion: 1}) || binding.EndpointID != "http-json" || binding.TransportID != "http" || binding.RequestCodecID != "openai-chat-json" || binding.ResponseDecoderID != "openai-sse" {
		t.Fatalf("binding=%#v", binding)
	}
	if binding.ErrorClassifier == nil || binding.ErrorClassifierID != "http-json" {
		t.Fatalf("error classifier was not resolved: %#v", binding)
	}
	modelBinding, err := NewRuntimeBindingBuilder(registry).Build("openai-compatible-chat", OperationModels)
	if err != nil || modelBinding.ModelSource == nil {
		t.Fatalf("model source was not resolved: %#v err=%v", modelBinding, err)
	}
	responseBinding, err := NewRuntimeBindingBuilder(registry).Build("openai-compatible-responses", OperationResponses)
	if err != nil || responseBinding.SessionStoreID != "session" || responseBinding.SessionStore == nil {
		t.Fatalf("response session store was not resolved: %#v err=%v", responseBinding, err)
	}
	all, err := registry.BuildBindings()
	if err != nil || len(all) < 3 {
		t.Fatalf("bindings=%d err=%v", len(all), err)
	}
}

func TestRegisteredHTTPJSONClassifierPreservesRichOutcomeEvidence(t *testing.T) {
	registry, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	classifier, ok := registry.ErrorClassifiers()[extensions.Ref{Kind: string(PrimitiveErrorClassifier), ID: "http-json", ContractVersion: 1}].(kernel.OutcomeClassifier)
	if !ok {
		t.Fatal("registered rich classifier was downgraded to ClassifyError-only adapter")
	}
	reset := time.Now().Add(5 * time.Minute).UTC().Truncate(time.Second)
	headers := http.Header{
		"Retry-After":           []string{reset.Format(http.TimeFormat)},
		"X-Ratelimit-Remaining": []string{"0"},
		"X-Ratelimit-Reset":     []string{strconv.FormatInt(reset.Unix(), 10)},
	}
	outcome := classifier.ClassifyOutcome(http.StatusTooManyRequests, headers, []byte(`{"error":{"message":"rate limit exceeded"}}`))
	if outcome.Cause != kernel.CauseRateLimited || outcome.RetryAt.IsZero() || outcome.Limits == nil {
		t.Fatalf("rich outcome was not preserved: %#v", outcome)
	}
	if delta := outcome.RetryAt.Sub(reset); delta < -time.Second || delta > time.Second {
		t.Fatalf("Retry-After HTTP-date was not parsed accurately: reset=%s outcome=%s", reset, outcome.RetryAt)
	}
	if len(outcome.Limits) != 1 || outcome.Limits[0].Remaining == nil || *outcome.Limits[0].Remaining != 0 || outcome.Limits[0].ResetAt == nil {
		t.Fatalf("rate-limit window was lost: %#v", outcome.Limits)
	}
}

func TestDefinitionBuildsConfiguredOAuthAuthFlow(t *testing.T) {
	registry, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	definition := ProviderDefinition{ContractVersion: 1, ID: "oauth-provider", Version: "1", DisplayName: "OAuth provider",
		Auth:        PrimitiveRef{Kind: PrimitiveAuth, ID: "oauth2", ContractVersion: 1},
		AuthOptions: AuthOptions{OAuth: &OAuthFlowOptions{ClientID: "public-client", AuthURL: "https://oauth.test/authorize", TokenURL: "https://oauth.test/token", DeviceAuthURL: "https://oauth.test/device", Scopes: []string{"models.read"}}},
		Operations: map[Operation]OperationBinding{OperationChat: {
			TaskRef:  OperationRef(normalize.OperationChatGenerate, 1),
			Endpoint: PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json", ContractVersion: 1}, Transport: PrimitiveRef{Kind: PrimitiveTransport, ID: "http", ContractVersion: 1},
			RequestCodec: PrimitiveRef{Kind: PrimitiveRequestCodec, ID: "openai-chat-json", ContractVersion: 1}, ResponseDecoder: PrimitiveRef{Kind: PrimitiveResponseDecoder, ID: "openai-sse", ContractVersion: 1},
		}},
	}
	if err := registry.Primitives.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	binding, err := NewRuntimeBindingBuilder(registry).Build(definition.ID, OperationChat)
	if err != nil {
		t.Fatal(err)
	}
	if binding.Auth == nil || binding.Auth.ID() != "oauth2" || binding.AuthFlowID != AuthBindingKey("oauth-provider", PrimitiveRef{Kind: PrimitiveAuth, ID: "oauth2", ContractVersion: 1}) {
		t.Fatalf("auth binding=%#v", binding)
	}
	if oauth, ok := binding.Auth.(OAuthAuth); !ok || oauth.Config.DeviceAuthURL != "https://oauth.test/device" {
		t.Fatalf("device authorization endpoint did not bind from provider definition: %#v", binding.Auth)
	}
	state := []byte(`{"access_token":"access","refresh_token":"refresh","client_secret":"per-connection-secret"}`)
	credential, err := binding.Auth.Resolve(context.Background(), AuthInput{ConnectionID: "conn", Type: "oauth2", Secret: string(state)})
	if err != nil {
		t.Fatal(err)
	}
	if credential.Secret != "access" || credential.RefreshToken != "refresh" || credential.ClientID != "public-client" || credential.ClientSecret != "per-connection-secret" {
		t.Fatalf("resolved credential=%#v", credential)
	}
}

func TestDefinitionCatalogExposesGenericSetupMetadataWithoutSecrets(t *testing.T) {
	registry, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	definition := ProviderDefinition{ContractVersion: 1, ID: "catalog-oauth", Version: "2", DisplayName: "Catalog OAuth", Aliases: []string{"co"},
		Auth:         PrimitiveRef{Kind: PrimitiveAuth, ID: "oauth2", ContractVersion: 1},
		AuthOptions:  AuthOptions{OAuth: &OAuthFlowOptions{ClientID: "not-returned", AuthURL: "https://auth.test", TokenURL: "https://token.test"}, Options: json.RawMessage(`{"client_secret":"not-returned"}`)},
		Defaults:     map[string]any{"apiKey": "not-returned", "baseUrl": "https://upstream.test"},
		Capabilities: CapabilitySet{Chat: true, Streaming: true},
		Operations: map[Operation]OperationBinding{
			OperationChat: {Protocol: kernel.Protocol("vendor.chat.v1"), TaskRef: OperationRef(normalize.OperationChatGenerate, 1), ProviderFormat: normalize.Format("vendor-wire"), Endpoint: PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json", ContractVersion: 1}, Transport: PrimitiveRef{Kind: PrimitiveTransport, ID: "http", ContractVersion: 1}, RequestCodec: PrimitiveRef{Kind: PrimitiveRequestCodec, ID: "openai-chat-json", ContractVersion: 1}, ResponseDecoder: PrimitiveRef{Kind: PrimitiveResponseDecoder, ID: "openai-sse", ContractVersion: 1}},
		},
	}
	if err := registry.Primitives.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	catalog, err := registry.DefinitionCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for index := 1; index < len(catalog); index++ {
		if catalog[index-1].ID > catalog[index].ID {
			t.Fatalf("provider definition catalog is not deterministic: %q precedes %q", catalog[index-1].ID, catalog[index].ID)
		}
	}
	var got *DefinitionMetadata
	for index := range catalog {
		if catalog[index].ID == definition.ID {
			got = &catalog[index]
			break
		}
	}
	if got == nil || got.Auth.ID != "oauth2" || len(got.Auth.SetupSchema.Fields) == 0 || len(got.Operations) != 1 {
		t.Fatalf("generic provider setup metadata missing: %#v", got)
	}
	if got.Operations[0].Protocol != "vendor.chat.v1" || got.Operations[0].Primitives["responseDecoder"].ID != "openai-sse" || got.Operations[0].Primitives["responseDecoder"].ContractVersion != 1 {
		t.Fatalf("operation binding metadata=%#v", got.Operations[0])
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"not-returned", "https://auth.test", "https://token.test", "upstream.test"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("provider catalog leaked definition configuration %q: %s", secret, encoded)
		}
	}
}

func TestTransformOptionsUseVersionedSharedExtensionSchema(t *testing.T) {
	registry, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	optionsShape := json.RawMessage(`{"type":"object","properties":{"suffix":{"type":"string","minLength":1}},"required":["suffix"],"additionalProperties":false}`)
	if err := registry.RegisterRequestTransform(schemaBoundRequestTransform{}, optionsShape); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterResponseTransform(uppercaseResponseTransform{}, nil); err != nil {
		t.Fatal(err)
	}
	view, err := registry.ExtensionCatalog()
	if err != nil {
		t.Fatal(err)
	}
	transformRef := extensions.Ref{Kind: kernel.RequestTransformKind, ID: "fixture.add-suffix", ContractVersion: 1}
	optionsRef := extensions.Ref{Kind: extensions.SchemaKind, ID: "fixture.transform-options", ContractVersion: 1}
	foundDescriptor, foundSchema := false, false
	for _, descriptor := range view.Descriptors {
		if descriptor.Ref == transformRef && descriptor.OptionsSchemaRef != nil && *descriptor.OptionsSchemaRef == optionsRef {
			foundDescriptor = true
		}
	}
	for _, schema := range view.Schemas {
		foundSchema = foundSchema || schema.Ref == optionsRef
	}
	if !foundDescriptor || !foundSchema {
		t.Fatalf("transform extension missing descriptor/schema: descriptor=%v schema=%v", foundDescriptor, foundSchema)
	}
	binding := kernel.TransformBinding{
		ID: "model.junior.suffix", TransformRef: transformRef, Enabled: true,
		Scope:   kernel.TransformScope{Kind: kernel.TransformScopeModel, ID: "junior"},
		Options: json.RawMessage(`{"suffix":"!"}`),
	}
	if err := registry.ValidateTransformBindings([]kernel.TransformBinding{binding}); err != nil {
		t.Fatalf("valid binding options rejected: %v", err)
	}
	catalogSnapshot, err := registry.FreezeCatalog()
	if err != nil {
		t.Fatal(err)
	}
	requestTransforms, err := kernel.NewRequestTransformRegistryFromCatalog(catalogSnapshot)
	if err != nil {
		t.Fatalf("transform implementation did not bind from shared catalog: %v", err)
	}
	if err := requestTransforms.ValidateBindings([]kernel.TransformBinding{binding}); err != nil {
		t.Fatalf("catalog-bound transform registry rejected valid options: %v", err)
	}
	binding.Options = json.RawMessage(`{"suffix":7}`)
	if err := registry.ValidateTransformBindings([]kernel.TransformBinding{binding}); err == nil {
		t.Fatal("binding options violating the shared catalog schema were accepted")
	}
	if err := requestTransforms.ValidateBindings([]kernel.TransformBinding{binding}); err == nil {
		t.Fatal("catalog-bound kernel registry accepted invalid options")
	}
	responseTransforms, err := kernel.NewResponseTransformRegistryFromCatalog(catalogSnapshot)
	if err != nil {
		t.Fatalf("response transform implementation did not bind from shared catalog: %v", err)
	}
	responseBinding := kernel.TransformBinding{
		ID: "daemon.uppercase", TransformRef: uppercaseResponseTransform{}.Definition().Ref, Enabled: true,
		Scope: kernel.TransformScope{Kind: kernel.TransformScopeDaemon},
	}
	event, err := responseTransforms.ApplyScopes(context.Background(), kernel.ResponseEvent{Kind: kernel.EventTextDelta, Text: "catalog"}, []kernel.TransformBinding{responseBinding}, responseBinding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if event.Text != "CATALOG" {
		t.Fatalf("catalog-bound response transform output=%q", event.Text)
	}
	binding.TransformRef.ContractVersion = 2
	if err := registry.ValidateTransformBindings([]kernel.TransformBinding{binding}); err == nil {
		t.Fatal("transform binding silently resolved a different contract version")
	}
}

func TestProviderMustDeclareNoAuthExplicitly(t *testing.T) {
	registry, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	definition := ProviderDefinition{ContractVersion: 1, ID: "public-provider", Version: "1", DisplayName: "Public provider",
		Auth: PrimitiveRef{Kind: PrimitiveAuth, ID: "none", ContractVersion: 1},
		Operations: map[Operation]OperationBinding{OperationChat: {
			TaskRef:  OperationRef(normalize.OperationChatGenerate, 1),
			Endpoint: PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json", ContractVersion: 1}, Transport: PrimitiveRef{Kind: PrimitiveTransport, ID: "http", ContractVersion: 1},
			RequestCodec: PrimitiveRef{Kind: PrimitiveRequestCodec, ID: "openai-chat-json", ContractVersion: 1}, ResponseDecoder: PrimitiveRef{Kind: PrimitiveResponseDecoder, ID: "openai-sse", ContractVersion: 1},
		}},
	}
	if err := registry.Primitives.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	bindings, err := registry.BuildBindings()
	if err != nil {
		t.Fatal(err)
	}
	binding := bindings[RuntimeBindingKey(definition.ID, OperationChat)]
	if binding.AuthFlowID != AuthBindingKey("public-provider", PrimitiveRef{Kind: PrimitiveAuth, ID: "none", ContractVersion: 1}) || binding.Auth == nil {
		t.Fatalf("no-auth binding=%#v", binding)
	}
	credential, err := binding.Auth.Resolve(context.Background(), AuthInput{ConnectionID: "conn", Type: "none"})
	if err != nil || credential.Secret != "" {
		t.Fatalf("no-auth credential=%#v err=%v", credential, err)
	}
	if got := registry.DefaultCredentialTypes()[definition.ID]; got != "none" {
		t.Fatalf("no-auth credential type default=%q", got)
	}
}

func TestHTTPHeaderUsageSourceUsesTypedHeaderMapping(t *testing.T) {
	source := HTTPHeaderUsageSource{}
	route := kernel.Route{UsageOptions: kernel.UsageSourceOptions{InputTokensHeader: "X-Prompt", OutputTokensHeader: "X-Completion", EstimatedCostHeader: "X-Cost"}}
	headers := http.Header{"X-Prompt": []string{"12"}, "X-Completion": []string{"7"}, "X-Cost": []string{"0.004"}}
	event, err := source.EnrichUsage(context.Background(), route, headers, kernel.UsageEvent{InputTokens: 2, OutputTokens: 3, EstimatedCost: 1})
	if err != nil {
		t.Fatal(err)
	}
	if event.InputTokens != 12 || event.OutputTokens != 7 || event.EstimatedCost != 0.004 {
		t.Fatalf("header usage not applied: %#v", event)
	}
	event, err = source.EnrichUsage(context.Background(), route, http.Header{"X-Cost": []string{"Inf"}}, kernel.UsageEvent{EstimatedCost: 1})
	if err != nil || event.EstimatedCost != 1 {
		t.Fatalf("non-finite cost header should not replace the recorded value: %#v err=%v", event, err)
	}
}

type runtimeTestEndpoint struct{ options kernel.EndpointOptions }

func (*runtimeTestEndpoint) ID() string { return "test-endpoint" }
func (e *runtimeTestEndpoint) Resolve(baseURL, operationPath string, options kernel.EndpointOptions) (string, error) {
	e.options = options
	return baseURL + "/endpoint" + operationPath, nil
}

type runtimeTestTransport struct{ executed bool }

func (*runtimeTestTransport) ID() string { return "test-transport" }
func (e *runtimeTestTransport) Execute(context.Context, kernel.UpstreamRequest) (kernel.UpstreamResponse, error) {
	e.executed = true
	return kernel.UpstreamResponse{Status: http.StatusOK}, nil
}

type runtimeTestRequestCodec struct{}

func (runtimeTestRequestCodec) ID() string { return "test-request" }
func (runtimeTestRequestCodec) DescribeCompatibility(kernel.CompatibilityContext) []kernel.FacetMapping {
	return []kernel.FacetMapping{{Facet: kernel.FacetWireRequest, Paths: []string{"fixture.request"}, Disposition: kernel.FacetPreserved}}
}
func (runtimeTestRequestCodec) Prepare(context.Context, kernel.NormalizedRequest, kernel.Route, kernel.Credential) (kernel.UpstreamRequest, error) {
	return kernel.UpstreamRequest{Method: http.MethodPost, URL: "/request-codec"}, nil
}

type runtimeTestResponseDecoder struct{}

func (runtimeTestResponseDecoder) ID() string { return "test-response" }
func (runtimeTestResponseDecoder) PossibleEvents() []kernel.ResponseEventKind {
	return []kernel.ResponseEventKind{kernel.EventTextDelta, kernel.EventContentBlockEnd, kernel.EventResponseComplete}
}

type runtimeTestRenderer struct{}

func (runtimeTestRenderer) ID() normalize.Format { return normalize.Format("vendor-test") }
func (runtimeTestRenderer) SupportsResponse(options kernel.ResponseRenderContext) kernel.CompatibilityPlan {
	if options.ClientFormat != normalize.Format("vendor-test") {
		return kernel.CompatibilityPlan{Fidelity: kernel.FidelityUnsupported}
	}
	reports := [][]kernel.FacetMapping{{{
		Facet: kernel.FacetWireResponse, Paths: []string{"fixture.response"}, Disposition: kernel.FacetTranslated,
	}}}
	policy := kernel.CompatibilityPolicy{RequiredFacets: []string{kernel.FacetWireResponse}}
	for _, event := range options.RequiredEvents {
		facet := kernel.ResponseEventFacet(event)
		policy.RequiredFacets = append(policy.RequiredFacets, facet)
		disposition := kernel.FacetUnsupported
		for _, emitted := range options.ProviderEvents {
			if emitted == event && (event == kernel.EventTextDelta || event == kernel.EventResponseComplete) {
				disposition = kernel.FacetTranslated
			}
		}
		reports = append(reports, []kernel.FacetMapping{{Facet: facet, Paths: []string{"fixture.events"}, Disposition: disposition}})
	}
	return kernel.ComposeCompatibilityPlan(reports, policy)
}
func (runtimeTestRenderer) Begin(_ context.Context, options kernel.ResponseRenderContext, writer http.ResponseWriter) (kernel.ResponseRenderSession, error) {
	writer.Header().Set("Content-Type", "application/vnd.vendor.test+text")
	return runtimeTestRenderSession{writer: writer}, nil
}

type runtimeTestRenderSession struct{ writer http.ResponseWriter }

func (s runtimeTestRenderSession) Emit(_ context.Context, event kernel.ResponseEvent) error {
	if event.Kind == kernel.EventTextDelta {
		_, err := s.writer.Write([]byte(event.Text))
		return err
	}
	return nil
}
func (runtimeTestRenderSession) Finish(_ context.Context, err error) error { return err }

type uppercaseResponseTransform struct{}

func (uppercaseResponseTransform) Definition() kernel.ResponseTransformDefinition {
	return kernel.ResponseTransformDefinition{Ref: extensions.Ref{Kind: kernel.ResponseTransformKind, ID: "fixture.uppercase-response.v1", ContractVersion: 1}, ImplementationVersion: "1", Label: "Uppercase response text", Description: "Modify semantic text before rendering.", Effects: []kernel.ResponseTransformEffect{kernel.ResponseEffectText}}
}
func (uppercaseResponseTransform) ApplyResponse(_ context.Context, event kernel.ResponseEvent, _ json.RawMessage) (kernel.ResponseEvent, error) {
	if event.Kind == kernel.EventTextDelta {
		event.Text = strings.ToUpper(event.Text)
	}
	return event, nil
}
func (runtimeTestResponseDecoder) ClassifyError(int, []byte) kernel.ErrorClass {
	return kernel.ErrorCooldown
}
func (runtimeTestResponseDecoder) Decode(_ context.Context, _ kernel.UpstreamResponse, emit func(kernel.ResponseEvent) error, _ kernel.StreamHooks) error {
	if err := emit(kernel.ResponseEvent{Kind: kernel.EventTextDelta, Text: "decoded by provider"}); err != nil {
		return err
	}
	if err := emit(kernel.ResponseEvent{Kind: kernel.EventContentBlockEnd, StopReason: "stop"}); err != nil {
		return err
	}
	return emit(kernel.ResponseEvent{Kind: kernel.EventResponseComplete})
}
func TestRuntimeBindingExecutesSelectedEndpointAndCodecs(t *testing.T) {
	registry, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	endpoint := &runtimeTestEndpoint{}
	transport := &runtimeTestTransport{}
	endpointSchema, err := endpointOptionsSchema()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterPrimitiveOptionsSchema(PrimitiveEndpoint, endpoint.ID(), 1, endpointOptionsSchemaRef, endpointSchema); err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{
		registry.RegisterEndpoint(endpoint),
		registry.RegisterTransport(transport),
		registry.RegisterRequestCodec(runtimeTestRequestCodec{}),
		registry.RegisterResponseDecoder(runtimeTestResponseDecoder{}),
		registry.RegisterResponseRenderer(runtimeTestRenderer{}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	definition := ProviderDefinition{ContractVersion: 1, ID: "composed-test", Version: "1", DisplayName: "Composed test",
		Auth: PrimitiveRef{Kind: PrimitiveAuth, ID: "static-secret", ContractVersion: 1},
		Operations: map[Operation]OperationBinding{OperationChat: {
			TaskRef:         OperationRef(normalize.OperationChatGenerate, 1),
			ProviderFormat:  "test-native",
			Endpoint:        PrimitiveRef{Kind: PrimitiveEndpoint, ID: endpoint.ID(), ContractVersion: 1},
			EndpointOptions: kernel.EndpointOptions{Path: "/manifest-path", Query: map[string]string{"api-version": "v2"}},
			Transport:       PrimitiveRef{Kind: PrimitiveTransport, ID: transport.ID(), ContractVersion: 1},
			RequestCodec:    PrimitiveRef{Kind: PrimitiveRequestCodec, ID: "test-request", ContractVersion: 1},
			ResponseDecoder: PrimitiveRef{Kind: PrimitiveResponseDecoder, ID: "test-response", ContractVersion: 1},
		}},
	}
	if err := registry.Primitives.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	binding, err := NewRuntimeBindingBuilder(registry).Build(definition.ID, OperationChat)
	if err != nil {
		t.Fatal(err)
	}
	request, err := binding.Adapter.Prepare(context.Background(), kernel.NormalizedRequest{}, kernel.Route{BaseURL: "https://base.test/v1"}, kernel.Credential{})
	if err != nil || request.URL != "https://base.test/v1/endpoint/request-codec" {
		t.Fatalf("request=%#v err=%v", request, err)
	}
	if endpoint.options.Path != "/manifest-path" || endpoint.options.Query["api-version"] != "v2" {
		t.Fatalf("endpoint did not receive manifest options: %#v", endpoint.options)
	}
	if _, err := binding.Adapter.Execute(context.Background(), request); err != nil || !transport.executed {
		t.Fatalf("selected transport was not invoked: executed=%v err=%v", transport.executed, err)
	}
	if got := binding.Adapter.ClassifyError(http.StatusInternalServerError, nil); got != kernel.ErrorCooldown {
		t.Fatalf("selected response codec classification=%q", got)
	}
	writer := httptest.NewRecorder()
	if err := binding.Adapter.RenderResponse(context.Background(), kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"application/json"}}}, writer, normalize.FormatOpenAIChat, kernel.StreamHooks{}); err != nil {
		t.Fatal(err)
	}
	if got := writer.Header().Get("Content-Type"); got != "application/json" || !strings.Contains(writer.Body.String(), "decoded by provider") {
		t.Fatalf("semantic renderer content-type=%q body=%q", got, writer.Body.String())
	}
	customWriter := httptest.NewRecorder()
	responseTransforms := kernel.NewResponseTransformRegistry()
	if err := responseTransforms.Register(uppercaseResponseTransform{}); err != nil {
		t.Fatal(err)
	}
	scope := kernel.TransformScope{Kind: kernel.TransformScopeDaemon}
	bindings := []kernel.TransformBinding{{ID: "test.uppercase", TransformRef: extensions.Ref{Kind: kernel.ResponseTransformKind, ID: "fixture.uppercase-response.v1", ContractVersion: 1}, Enabled: true, Scope: scope}}
	transform := func(ctx context.Context, event kernel.ResponseEvent) (kernel.ResponseEvent, error) {
		return responseTransforms.ApplyScopes(ctx, event, bindings, scope)
	}
	if err := binding.Adapter.RenderResponse(context.Background(), kernel.UpstreamResponse{Status: http.StatusOK}, customWriter, normalize.Format("vendor-test"), kernel.StreamHooks{TransformResponse: transform}); err != nil {
		t.Fatal(err)
	}
	if customWriter.Header().Get("Content-Type") != "application/vnd.vendor.test+text" || customWriter.Body.String() != "DECODED BY PROVIDER" {
		t.Fatalf("registered client renderer output=%q headers=%v", customWriter.Body.String(), customWriter.Header())
	}
}

func TestRuntimeBindingAcceptsNewNamespacedOperationWithoutOperationSwitch(t *testing.T) {
	registry, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	endpoint := &runtimeTestEndpoint{}
	transport := &runtimeTestTransport{}
	for _, registerErr := range []error{
		registry.RegisterEndpoint(endpoint), registry.RegisterTransport(transport),
		registry.RegisterRequestCodec(runtimeTestRequestCodec{}), registry.RegisterResponseDecoder(runtimeTestResponseDecoder{}),
	} {
		if registerErr != nil {
			t.Fatal(registerErr)
		}
	}
	const operation Operation = "audio.transcribe.v1"
	if err := registry.Primitives.Operations.RegisterRawPayload(
		extensions.Ref{Kind: "operation", ID: string(operation), ContractVersion: 1},
		"Audio transcription", "Transcribe an audio payload",
		json.RawMessage(`{"type":"object","properties":{"audio":{"type":"string","minLength":1}},"required":["audio"],"additionalProperties":false}`),
		operations.ReplayNever,
	); err != nil {
		t.Fatal(err)
	}
	definition := ProviderDefinition{ContractVersion: 1, ID: "media-extension", Version: "1", DisplayName: "Media extension", Auth: PrimitiveRef{Kind: PrimitiveAuth, ID: "static-secret", ContractVersion: 1}, Operations: map[Operation]OperationBinding{
		operation: {Protocol: kernel.Protocol("vendor.audio.v1"), TaskRef: OperationRef(normalize.Operation(operation), 1), Endpoint: PrimitiveRef{Kind: PrimitiveEndpoint, ID: endpoint.ID(), ContractVersion: 1}, Transport: PrimitiveRef{Kind: PrimitiveTransport, ID: transport.ID(), ContractVersion: 1}, RequestCodec: PrimitiveRef{Kind: PrimitiveRequestCodec, ID: "test-request", ContractVersion: 1}, ResponseDecoder: PrimitiveRef{Kind: PrimitiveResponseDecoder, ID: "test-response", ContractVersion: 1}},
	}}
	manifest, err := EncodeProviderDefinitionJSON(definition)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.LoadDefinitionJSON(manifest); err != nil {
		t.Fatal(err)
	}
	binding, err := NewRuntimeBindingBuilder(registry).Build(definition.ID, operation)
	if err != nil {
		t.Fatal(err)
	}
	if binding.Adapter == nil || binding.AdapterID != "media-extension:audio.transcribe.v1" || binding.TaskRef.ID != string(operation) || binding.TaskRef.ContractVersion != 1 || binding.Protocol != "vendor.audio.v1" {
		t.Fatalf("operation binding=%#v", binding)
	}
}

func TestGenericProviderManifestBindsProtocolsAndSemanticTasks(t *testing.T) {
	registry, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.LoadDefinitionFile("../../manifests/builtin/generic.json"); err != nil {
		t.Fatal(err)
	}
	bindings, err := registry.BuildBindings()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		definition string
		operation  Operation
		protocol   kernel.Protocol
	}{
		{"openai", OperationChat, kernel.ProtocolOpenAIChat},
		{"anthropic", OperationMessages, kernel.ProtocolAnthropic},
	} {
		binding := bindings[RuntimeBindingKey(test.definition, test.operation)]
		if binding.Protocol != test.protocol || binding.TaskRef.ID != string(normalize.OperationChatGenerate) || binding.TaskRef.ContractVersion != 1 || binding.Adapter == nil {
			t.Errorf("%s/%s binding=%#v", test.definition, test.operation, binding)
		}
	}
	modelBinding := bindings[RuntimeBindingKey("openai", OperationModels)]
	if modelBinding.Auth == nil || modelBinding.ModelSource == nil || modelBinding.Endpoint == nil || modelBinding.Transport == nil {
		t.Fatalf("manifest-bound API-key /models operation was incomplete: %#v", modelBinding)
	}
}

type fixtureFeatureEvaluator struct{ accept bool }

func (f fixtureFeatureEvaluator) Evaluate(kernel.Capability, json.RawMessage) (bool, string) {
	if f.accept {
		return true, ""
	}
	return false, "fixture rejects feature"
}

type versionedPrimitiveEndpoint struct{ version uint64 }

func (e *versionedPrimitiveEndpoint) ID() string { return "versioned-endpoint" }
func (e *versionedPrimitiveEndpoint) Resolve(_, _ string, _ kernel.EndpointOptions) (string, error) {
	return fmt.Sprintf("https://provider.test/v%d", e.version), nil
}

type versionedPrimitiveTransport struct{ version uint64 }

func (t *versionedPrimitiveTransport) ID() string { return "versioned-transport" }
func (t *versionedPrimitiveTransport) Execute(context.Context, kernel.UpstreamRequest) (kernel.UpstreamResponse, error) {
	return kernel.UpstreamResponse{Status: int(t.version)}, nil
}

type versionedPrimitiveRequestCodec struct{ version uint64 }

func (c versionedPrimitiveRequestCodec) ID() string { return "versioned-request" }
func (versionedPrimitiveRequestCodec) DescribeCompatibility(kernel.CompatibilityContext) []kernel.FacetMapping {
	return []kernel.FacetMapping{{Facet: kernel.FacetWireRequest, Paths: []string{"request"}, Disposition: kernel.FacetPreserved}}
}
func (c versionedPrimitiveRequestCodec) Prepare(context.Context, kernel.NormalizedRequest, kernel.Route, kernel.Credential) (kernel.UpstreamRequest, error) {
	return kernel.UpstreamRequest{Method: http.MethodPost, URL: fmt.Sprintf("/v%d", c.version)}, nil
}

type versionedPrimitiveResponseDecoder struct{ version uint64 }

func (d versionedPrimitiveResponseDecoder) ID() string { return "versioned-response" }
func (versionedPrimitiveResponseDecoder) PossibleEvents() []kernel.ResponseEventKind {
	return []kernel.ResponseEventKind{kernel.EventResponseComplete}
}
func (versionedPrimitiveResponseDecoder) ClassifyError(int, []byte) kernel.ErrorClass {
	return kernel.ErrorRetryable
}
func (versionedPrimitiveResponseDecoder) Decode(_ context.Context, _ kernel.UpstreamResponse, emit func(kernel.ResponseEvent) error, _ kernel.StreamHooks) error {
	return emit(kernel.ResponseEvent{Kind: kernel.EventResponseComplete})
}

type versionedPrimitiveQuotaSource struct{ version uint64 }

func (versionedPrimitiveQuotaSource) ID() string { return "versioned-quota" }
func (versionedPrimitiveQuotaSource) Fetch(context.Context, QuotaRequest) ([]quota.Snapshot, error) {
	return nil, nil
}

func TestVersionedPrimitiveRefsSelectExactAdapterComponents(t *testing.T) {
	registry, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []uint64{1, 2} {
		if err := registry.RegisterEndpointVersion(&versionedPrimitiveEndpoint{version}, version); err != nil {
			t.Fatal(err)
		}
		if err := registry.RegisterTransportVersion(&versionedPrimitiveTransport{version}, version); err != nil {
			t.Fatal(err)
		}
		if err := registry.RegisterRequestCodecVersion(versionedPrimitiveRequestCodec{version}, version); err != nil {
			t.Fatal(err)
		}
		if err := registry.RegisterResponseDecoderVersion(versionedPrimitiveResponseDecoder{version}, version); err != nil {
			t.Fatal(err)
		}
		if err := registry.RegisterQuotaSourceVersion(versionedPrimitiveQuotaSource{version}, version); err != nil {
			t.Fatal(err)
		}
	}
	const definitionID = "versioned-primitive-provider"
	ref := func(kind PrimitiveKind, id string) PrimitiveRef {
		return PrimitiveRef{Kind: kind, ID: id, ContractVersion: 2}
	}
	definition := ProviderDefinition{
		ContractVersion: 1, ID: definitionID, Version: "1", DisplayName: "Versioned primitive fixture",
		Auth: PrimitiveRef{Kind: PrimitiveAuth, ID: "none", ContractVersion: 1},
		Operations: map[Operation]OperationBinding{OperationChat: {
			Protocol: kernel.ProtocolOpenAIChat, TaskRef: OperationRef(normalize.OperationChatGenerate, 1), ProviderFormat: normalize.FormatOpenAIChat,
			Endpoint: ref(PrimitiveEndpoint, "versioned-endpoint"), Transport: ref(PrimitiveTransport, "versioned-transport"),
			RequestCodec: ref(PrimitiveRequestCodec, "versioned-request"), ResponseDecoder: ref(PrimitiveResponseDecoder, "versioned-response"),
		}},
	}
	if err := registry.Primitives.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	binding, err := NewRuntimeBindingBuilder(registry).Build(definitionID, OperationChat)
	if err != nil {
		t.Fatal(err)
	}
	adapter, ok := binding.Adapter.(kernel.ComposedAdapter)
	if !ok {
		t.Fatalf("runtime adapter=%T", binding.Adapter)
	}
	if adapter.Endpoint.(*versionedPrimitiveEndpoint).version != 2 || adapter.Transport.(*versionedPrimitiveTransport).version != 2 || adapter.Request.(versionedPrimitiveRequestCodec).version != 2 || adapter.Response.(versionedPrimitiveResponseDecoder).version != 2 {
		t.Fatalf("adapter did not bind exact v2 components: %#v", adapter)
	}
	endpoints := registry.Endpoints()
	quotaSources := registry.QuotaSources()
	endpointV1 := extensions.Ref{Kind: string(PrimitiveEndpoint), ID: "versioned-endpoint", ContractVersion: 1}
	endpointV2 := extensions.Ref{Kind: string(PrimitiveEndpoint), ID: "versioned-endpoint", ContractVersion: 2}
	quotaV1 := extensions.Ref{Kind: string(PrimitiveQuotaSource), ID: "versioned-quota", ContractVersion: 1}
	quotaV2 := extensions.Ref{Kind: string(PrimitiveQuotaSource), ID: "versioned-quota", ContractVersion: 2}
	if endpoints[endpointV1].(*versionedPrimitiveEndpoint).version != 1 || endpoints[endpointV2].(*versionedPrimitiveEndpoint).version != 2 {
		t.Fatalf("endpoint runtime cache did not retain exact refs: %#v", endpoints)
	}
	if quotaSources[quotaV1].(versionedPrimitiveQuotaSource).version != 1 || quotaSources[quotaV2].(versionedPrimitiveQuotaSource).version != 2 {
		t.Fatalf("quota runtime cache did not retain exact refs: %#v", quotaSources)
	}
}

type catalogStrategyFixture struct{}

func (catalogStrategyFixture) Plan(_ string, members []kernel.MemberRef, _ *kernel.StrategyState) []kernel.MemberRef {
	return append([]kernel.MemberRef(nil), members...)
}
func (catalogStrategyFixture) OnFailure(kernel.StrategyFailure, *kernel.StrategyState) kernel.FailureAction {
	return kernel.FailureContinue
}

func TestPrimitiveFactoriesWithSameIDBindOnlyTheirExactContractVersion(t *testing.T) {
	registry, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []uint64{1, 2} {
		version := version
		if err := registry.RegisterPrimitiveImplementation(PrimitiveExtension, "vendor.codec", version, func(json.RawMessage) (any, error) {
			return version, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := registry.FreezeCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []uint64{1, 2} {
		ref := extensions.Ref{Kind: string(PrimitiveExtension), ID: "vendor.codec", ContractVersion: version}
		_, implementation, err := snapshot.Bind(ref, json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("bind %s: %v", ref.Key(), err)
		}
		if implementation != version {
			t.Fatalf("%s resolved implementation %v", ref.Key(), implementation)
		}
	}
	if _, _, err := snapshot.Bind(extensions.Ref{Kind: string(PrimitiveExtension), ID: "vendor.codec", ContractVersion: 3}, json.RawMessage(`{}`)); err == nil {
		t.Fatal("missing primitive contract version resolved to another implementation")
	}
}

func TestRuntimeRegistryStoresStrategyImplementationsByExactVersion(t *testing.T) {
	registry, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []uint64{1, 2} {
		ref := extensions.Ref{Kind: kernel.StrategyExtensionKind, ID: "vendor.policy", ContractVersion: version}
		if err := registry.RegisterStrategyDefinition(kernel.StrategyDefinition{
			Ref: ref, ID: ref.ID, Label: "Vendor policy", Description: "Fixture versioned strategy.",
			Runtime: kernel.Strategy(fmt.Sprintf("vendor-policy-v%d", version)), Primitive: catalogStrategyFixture{},
		}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := registry.FreezeCatalog()
	if err != nil {
		t.Fatal(err)
	}
	definitions, err := kernel.StrategyDefinitionsFromCatalog(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	found := map[uint64]bool{}
	for _, definition := range definitions {
		if definition.Ref.ID == "vendor.policy" {
			found[definition.Ref.ContractVersion] = true
		}
	}
	if !found[1] || !found[2] {
		t.Fatalf("same strategy ID did not retain both exact versions: %v", found)
	}
	builtinRef := extensions.Ref{Kind: kernel.StrategyExtensionKind, ID: "round-robin-fallback", ContractVersion: 1}
	if err := snapshot.ValidateOptions(builtinRef, json.RawMessage(`{"stickyLimit":4}`)); err != nil {
		t.Fatalf("valid strategy options rejected by shared schema: %v", err)
	}
	if err := snapshot.ValidateOptions(builtinRef, json.RawMessage(`{"stickyLimit":0}`)); err == nil {
		t.Fatal("strategy options below schema minimum were accepted")
	}
}

func TestRuntimeRegistryContributesVersionedFeatureEvaluatorsToSharedCatalog(t *testing.T) {
	registry, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	featureID := "vendor.example.structured-output"
	shape := json.RawMessage(`{"type":"object","properties":{"format":{"type":"string","minLength":1}},"required":["format"],"additionalProperties":false}`)
	featureV1 := extensions.Ref{Kind: kernel.FeatureEvaluatorKind, ID: featureID, ContractVersion: 1}
	featureV2 := extensions.Ref{Kind: kernel.FeatureEvaluatorKind, ID: featureID, ContractVersion: 2}
	for _, item := range []struct {
		ref       extensions.Ref
		evaluator kernel.FeatureEvaluator
	}{
		{featureV1, fixtureFeatureEvaluator{accept: true}},
		{featureV2, fixtureFeatureEvaluator{accept: false}},
	} {
		schemaRef := extensions.Ref{Kind: extensions.SchemaKind, ID: "feature.constraints." + featureID, ContractVersion: item.ref.ContractVersion}
		if err := registry.RegisterFeatureEvaluator(item.ref, "1", "Structured output", "Checks the requested structured output format.", schemaRef, shape, item.evaluator); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := registry.FreezeCatalog()
	if err != nil {
		t.Fatal(err)
	}
	features, err := kernel.NewFeatureRegistryFromCatalog(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	route := kernel.Route{Profile: kernel.CapabilityProfile{featureID: {State: kernel.SupportNative}}}
	constraints := json.RawMessage(`{"format":"json"}`)
	if ok, reason := features.Evaluate(route, []normalize.FeatureRequirement{{Ref: featureV1, Constraints: constraints}}); !ok {
		t.Fatalf("catalog-bound v1 evaluator rejected feature: %s", reason)
	}
	if ok, reason := features.Evaluate(route, []normalize.FeatureRequirement{{Ref: featureV2, Constraints: constraints}}); ok || reason == "" {
		t.Fatalf("exact v2 evaluator ref was not used: %v %q", ok, reason)
	}
	if ok, reason := features.Evaluate(route, []normalize.FeatureRequirement{{Ref: featureV1, Constraints: json.RawMessage(`{"unknown":true}`)}}); ok || reason == "" {
		t.Fatalf("feature input schema was not enforced: %v %q", ok, reason)
	}
}
