package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

func testPrimitiveRegistry() *PrimitiveRegistry {
	r := NewPrimitiveRegistry()
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

func TestPrimitiveRegistryValidatesComposedProvider(t *testing.T) {
	r := testPrimitiveRegistry()
	err := r.RegisterDefinition(ProviderDefinition{
		ID:          "openai-compatible",
		Version:     "1",
		DisplayName: "OpenAI-compatible",
		Auth:        PrimitiveRef{Kind: PrimitiveAuth, ID: "static-secret"},
		Operations: map[Operation]OperationBinding{
			OperationChat: {
				Endpoint:        PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json"},
				Transport:       PrimitiveRef{Kind: PrimitiveTransport, ID: "http"},
				RequestCodec:    PrimitiveRef{Kind: PrimitiveRequestCodec, ID: "openai-chat-json"},
				ResponseDecoder: PrimitiveRef{Kind: PrimitiveResponseDecoder, ID: "openai-sse"},
				ModelSource:     PrimitiveRef{Kind: PrimitiveModelSource, ID: "openai-models"},
				ErrorClassifier: PrimitiveRef{Kind: PrimitiveErrorClassifier, ID: "http-json"},
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
	if err := r.RegisterDefinition(ProviderDefinition{ID: "bad", Version: "1", DisplayName: "Bad", Operations: map[Operation]OperationBinding{OperationChat: {Endpoint: PrimitiveRef{Kind: PrimitiveEndpoint, ID: "missing"}, RequestCodec: PrimitiveRef{Kind: PrimitiveRequestCodec, ID: "missing"}, ResponseDecoder: PrimitiveRef{Kind: PrimitiveResponseDecoder, ID: "missing"}}}}); err == nil {
		t.Fatal("expected unknown primitive error")
	}
	r = testPrimitiveRegistry()
	definition := ProviderDefinition{ID: "same", Version: "1", DisplayName: "Same", Auth: PrimitiveRef{Kind: PrimitiveAuth, ID: "static-secret"}, Operations: map[Operation]OperationBinding{OperationChat: {Endpoint: PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json"}, Transport: PrimitiveRef{Kind: PrimitiveTransport, ID: "http"}, RequestCodec: PrimitiveRef{Kind: PrimitiveRequestCodec, ID: "openai-chat-json"}, ResponseDecoder: PrimitiveRef{Kind: PrimitiveResponseDecoder, ID: "openai-sse"}}}}
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
	if err := runtime.RegisterRequestCodec(runtime.requestCodecs["openai-chat-json"]); err == nil {
		t.Fatal("duplicate primitive implementation must not silently replace the registered codec")
	}
}

func TestProviderDefinitionJSONBindingRoundTripsTypedContract(t *testing.T) {
	r := testPrimitiveRegistry()
	definition := ProviderDefinition{ID: "json-provider", Version: "1", DisplayName: "JSON Provider", Auth: PrimitiveRef{Kind: PrimitiveAuth, ID: "static-secret"}, Operations: map[Operation]OperationBinding{OperationChat: {Endpoint: PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json"}, Transport: PrimitiveRef{Kind: PrimitiveTransport, ID: "http"}, RequestCodec: PrimitiveRef{Kind: PrimitiveRequestCodec, ID: "openai-chat-json"}, ResponseDecoder: PrimitiveRef{Kind: PrimitiveResponseDecoder, ID: "openai-sse"}}}}
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

func TestRuntimeRegistryLoadsExternalManifest(t *testing.T) {
	runtime, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"id":"external-chat","version":"1","displayName":"External Chat","auth":{"kind":"auth","id":"static-secret"},"operations":{"chat":{"endpoint":{"kind":"endpoint","id":"http-json"},"transport":{"kind":"transport","id":"http"},"requestCodec":{"kind":"request_codec","id":"openai-chat-json"},"responseDecoder":{"kind":"response_decoder","id":"openai-sse"},"errorClassifier":{"kind":"error_classifier","id":"http-json"}}}}`)
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
	definition := ProviderDefinition{
		ID: "custom-errors", Version: "1", DisplayName: "Custom error envelope",
		Auth: PrimitiveRef{Kind: PrimitiveAuth, ID: "static-secret"},
		Operations: map[Operation]OperationBinding{OperationChat: {
			Endpoint:        PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json"},
			Transport:       PrimitiveRef{Kind: PrimitiveTransport, ID: "http"},
			RequestCodec:    PrimitiveRef{Kind: PrimitiveRequestCodec, ID: "openai-chat-json"},
			ResponseDecoder: PrimitiveRef{Kind: PrimitiveResponseDecoder, ID: "openai-sse"},
			ErrorClassifier: PrimitiveRef{Kind: PrimitiveErrorClassifier, ID: "http-json"},
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
	if binding.ErrorClassifierID != RuntimeErrorClassifierKey(definition.ID, OperationChat, "http-json") {
		t.Fatalf("configured classifier key=%q", binding.ErrorClassifierID)
	}
	classifiers := registry.ErrorClassifiersForBindings(bindings)
	classifier, ok := classifiers[binding.ErrorClassifierID].(kernel.OutcomeClassifier)
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
	definition := ProviderDefinition{
		ID: "bad-error-path", Version: "1", DisplayName: "Bad error path",
		Auth: PrimitiveRef{Kind: PrimitiveAuth, ID: "static-secret"},
		Operations: map[Operation]OperationBinding{OperationChat: {
			Endpoint:               PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json"},
			Transport:              PrimitiveRef{Kind: PrimitiveTransport, ID: "http"},
			RequestCodec:           PrimitiveRef{Kind: PrimitiveRequestCodec, ID: "openai-chat-json"},
			ResponseDecoder:        PrimitiveRef{Kind: PrimitiveResponseDecoder, ID: "openai-sse"},
			ErrorClassifier:        PrimitiveRef{Kind: PrimitiveErrorClassifier, ID: "http-json"},
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
	if binding.AdapterID != "openai-compatible-chat:chat" || binding.Adapter == nil || binding.Auth == nil || binding.AuthFlowID != "openai-compatible-chat:static-secret" || binding.EndpointID != "http-json" || binding.TransportID != "http" || binding.RequestCodecID != "openai-chat-json" || binding.ResponseDecoderID != "openai-sse" {
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
	classifier, ok := registry.ErrorClassifiers()["http-json"].(kernel.OutcomeClassifier)
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
	definition := ProviderDefinition{
		ID: "oauth-provider", Version: "1", DisplayName: "OAuth provider",
		Auth:        PrimitiveRef{Kind: PrimitiveAuth, ID: "oauth2"},
		AuthOptions: AuthOptions{OAuth: &OAuthFlowOptions{ClientID: "public-client", AuthURL: "https://oauth.test/authorize", TokenURL: "https://oauth.test/token", Scopes: []string{"models.read"}}},
		Operations: map[Operation]OperationBinding{OperationChat: {
			Endpoint: PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json"}, Transport: PrimitiveRef{Kind: PrimitiveTransport, ID: "http"},
			RequestCodec: PrimitiveRef{Kind: PrimitiveRequestCodec, ID: "openai-chat-json"}, ResponseDecoder: PrimitiveRef{Kind: PrimitiveResponseDecoder, ID: "openai-sse"},
		}},
	}
	if err := registry.Primitives.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	binding, err := NewRuntimeBindingBuilder(registry).Build(definition.ID, OperationChat)
	if err != nil {
		t.Fatal(err)
	}
	if binding.Auth == nil || binding.Auth.ID() != "oauth2" || binding.AuthFlowID != "oauth-provider:oauth2" {
		t.Fatalf("auth binding=%#v", binding)
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
	definition := ProviderDefinition{
		ID: "catalog-oauth", Version: "2", DisplayName: "Catalog OAuth", Aliases: []string{"co"},
		Auth:         PrimitiveRef{Kind: PrimitiveAuth, ID: "oauth2"},
		AuthOptions:  AuthOptions{OAuth: &OAuthFlowOptions{ClientID: "not-returned", AuthURL: "https://auth.test", TokenURL: "https://token.test"}, Options: json.RawMessage(`{"client_secret":"not-returned"}`)},
		Defaults:     map[string]any{"apiKey": "not-returned", "baseUrl": "https://upstream.test"},
		Capabilities: CapabilitySet{Chat: true, Streaming: true},
		Operations: map[Operation]OperationBinding{
			OperationChat: {Protocol: kernel.Protocol("vendor.chat.v1"), Task: "chat.generate", ProviderFormat: normalize.Format("vendor-wire"), Endpoint: PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json"}, Transport: PrimitiveRef{Kind: PrimitiveTransport, ID: "http"}, RequestCodec: PrimitiveRef{Kind: PrimitiveRequestCodec, ID: "openai-chat-json"}, ResponseDecoder: PrimitiveRef{Kind: PrimitiveResponseDecoder, ID: "openai-sse"}},
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
	if got.Operations[0].Protocol != "vendor.chat.v1" || got.Operations[0].Primitives["responseDecoder"] != "openai-sse" {
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

func TestProviderMustDeclareNoAuthExplicitly(t *testing.T) {
	registry, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	definition := ProviderDefinition{
		ID: "public-provider", Version: "1", DisplayName: "Public provider",
		Auth: PrimitiveRef{Kind: PrimitiveAuth, ID: "none"},
		Operations: map[Operation]OperationBinding{OperationChat: {
			Endpoint: PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json"}, Transport: PrimitiveRef{Kind: PrimitiveTransport, ID: "http"},
			RequestCodec: PrimitiveRef{Kind: PrimitiveRequestCodec, ID: "openai-chat-json"}, ResponseDecoder: PrimitiveRef{Kind: PrimitiveResponseDecoder, ID: "openai-sse"},
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
	if binding.AuthFlowID != "public-provider:none" || binding.Auth == nil {
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
func (runtimeTestRequestCodec) Prepare(context.Context, kernel.NormalizedRequest, kernel.Route, kernel.Credential) (kernel.UpstreamRequest, error) {
	return kernel.UpstreamRequest{Method: http.MethodPost, URL: "/request-codec"}, nil
}

type runtimeTestResponseDecoder struct{}

func (runtimeTestResponseDecoder) ID() string { return "test-response" }

type runtimeTestRenderer struct{}

func (runtimeTestRenderer) ID() normalize.Format { return normalize.Format("vendor-test") }
func (runtimeTestRenderer) SupportsResponse(options kernel.ResponseRenderContext) kernel.CompatibilityDecision {
	if options.ClientFormat != normalize.Format("vendor-test") {
		return kernel.CompatibilityDecision{Fidelity: kernel.FidelityUnsupported}
	}
	return kernel.CompatibilityDecision{Supported: true, Fidelity: kernel.FidelityTranslated}
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
	return kernel.ResponseTransformDefinition{ID: "fixture.uppercase-response.v1", Label: "Uppercase response text", Description: "Modify semantic text before rendering.", Effects: []kernel.ResponseTransformEffect{kernel.ResponseEffectText}}
}
func (uppercaseResponseTransform) ApplyResponse(_ context.Context, event kernel.ResponseEvent) (kernel.ResponseEvent, error) {
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
	definition := ProviderDefinition{
		ID: "composed-test", Version: "1", DisplayName: "Composed test",
		Auth: PrimitiveRef{Kind: PrimitiveAuth, ID: "static-secret"},
		Operations: map[Operation]OperationBinding{OperationChat: {
			ProviderFormat:  "test-native",
			Endpoint:        PrimitiveRef{Kind: PrimitiveEndpoint, ID: endpoint.ID()},
			EndpointOptions: kernel.EndpointOptions{Path: "/manifest-path", Query: map[string]string{"api-version": "v2"}},
			Transport:       PrimitiveRef{Kind: PrimitiveTransport, ID: transport.ID()},
			RequestCodec:    PrimitiveRef{Kind: PrimitiveRequestCodec, ID: "test-request"},
			ResponseDecoder: PrimitiveRef{Kind: PrimitiveResponseDecoder, ID: "test-response"},
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
	if err := binding.Adapter.RenderResponse(context.Background(), kernel.UpstreamResponse{Status: http.StatusOK}, customWriter, normalize.Format("vendor-test"), kernel.StreamHooks{TransformResponse: responseTransforms.Apply}); err != nil {
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
	definition := ProviderDefinition{ID: "media-extension", Version: "1", DisplayName: "Media extension", Auth: PrimitiveRef{Kind: PrimitiveAuth, ID: "static-secret"}, Operations: map[Operation]OperationBinding{
		operation: {Protocol: kernel.Protocol("vendor.audio.v1"), Task: normalize.Operation(operation), Endpoint: PrimitiveRef{Kind: PrimitiveEndpoint, ID: endpoint.ID()}, Transport: PrimitiveRef{Kind: PrimitiveTransport, ID: transport.ID()}, RequestCodec: PrimitiveRef{Kind: PrimitiveRequestCodec, ID: "test-request"}, ResponseDecoder: PrimitiveRef{Kind: PrimitiveResponseDecoder, ID: "test-response"}},
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
	if binding.Adapter == nil || binding.AdapterID != "media-extension:audio.transcribe.v1" || binding.Task != normalize.Operation(operation) || binding.Protocol != "vendor.audio.v1" {
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
		if binding.Protocol != test.protocol || binding.Task != normalize.OperationChatGenerate || binding.Adapter == nil {
			t.Errorf("%s/%s binding=%#v", test.definition, test.operation, binding)
		}
	}
	modelBinding := bindings[RuntimeBindingKey("openai", OperationModels)]
	if modelBinding.Auth == nil || modelBinding.ModelSource == nil || modelBinding.Endpoint == nil || modelBinding.Transport == nil {
		t.Fatalf("manifest-bound API-key /models operation was incomplete: %#v", modelBinding)
	}
}
