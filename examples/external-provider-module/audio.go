package externalprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/fm39hz/gobroom/pkg/provider"
)

// AudioModule is intentionally a separate Go module. It contributes only the
// audio wire codec and semantic task; GoBroom owns shared HTTP transport,
// credentials, manifests, model graph and daemon lifecycle.
type AudioModule struct{}

func (AudioModule) ModuleDescriptor() provider.ModuleRef {
	return provider.ModuleRef{ID: "example.audio", Version: "1.0.0", HostContractVersion: provider.RuntimeModuleHostContractVersion}
}

func (AudioModule) Register(runtime *provider.RuntimeRegistry) error {
	if err := runtime.RegisterAuthFactory("example-audio-auth", func(provider.AuthOptions) (provider.AuthFlow, error) {
		return exampleAudioAuth{}, nil
	}); err != nil {
		return err
	}
	task := provider.Ref{Kind: "operation", ID: "audio.transcribe", ContractVersion: 1}
	if err := runtime.Primitives.Operations.RegisterRawPayload(task, "Audio transcription", "Transcribe an audio resource.", json.RawMessage(`{"type":"object","properties":{"audio_url":{"type":"string","minLength":1},"language":{"type":"string"}},"required":["audio_url"],"additionalProperties":false}`), provider.ReplayNever); err != nil {
		return err
	}
	if err := registerAudioArtifactInspection(runtime); err != nil {
		return err
	}
	if err := runtime.RegisterRequestCodec(audioRequestCodec{}); err != nil {
		return err
	}
	if err := runtime.RegisterResponseDecoder(audioResponseDecoder{}); err != nil {
		return err
	}
	if err := runtime.RegisterClientIngress(provider.JSONOperationIngress{
		CodecID: "example-audio-json", IngressContractVersion: 1,
		Operation: "audio.transcribe", OperationContractVersion: 1,
		Endpoints: []provider.ClientIngressRoute{{Method: http.MethodPost, Path: "/v1/audio/transcriptions"}},
	}); err != nil {
		return err
	}
	if err := runtime.RegisterClientIngress(provider.MultipartOperationIngress{
		CodecID: "example-audio-inspect-multipart", IngressContractVersion: 1,
		Operation: audioInspectOperation, OperationContractVersion: 1,
		Endpoints: []provider.ClientIngressRoute{{Method: http.MethodPost, Path: "/v1/audio/inspect"}},
		Bindings: []provider.MultipartArtifactBinding{{
			Field: "audio", Role: "audio", TypeRef: provider.Ref{Kind: provider.ArtifactKind, ID: "example.audio.body", ContractVersion: 1},
			MaxBytes: 16 * 1024 * 1024,
		}},
	}); err != nil {
		return err
	}
	return runtime.RegisterQuotaSource(audioQuotaSource{})
}

// exampleAudioAuth demonstrates that an out-of-tree Auth primitive can
// derive request-codec context from its own connection credential state.
type exampleAudioAuth struct{}

func (exampleAudioAuth) ID() string { return "example-audio-auth" }
func (exampleAudioAuth) SetupSchema() provider.SetupSchema {
	return provider.SetupSchema{Fields: []provider.SetupField{{Name: "secret", Type: "string", Required: true, Secret: true}}}
}
func (exampleAudioAuth) Resolve(_ context.Context, input provider.AuthInput) (provider.Credential, error) {
	if input.Secret == "" {
		return provider.Credential{}, fmt.Errorf("account secret is required")
	}
	return provider.Credential{ConnectionID: input.ConnectionID, Type: input.Type, Secret: input.Secret, AuthContext: provider.AuthContext{"userId": "user:" + input.Secret}}, nil
}
func (exampleAudioAuth) Refresh(_ context.Context, credential provider.Credential) (provider.Credential, error) {
	return credential, nil
}

// audioQuotaSource demonstrates that the public QuotaSource contract can be
// implemented outside the GoBroom module without importing internal/quota.
type audioQuotaSource struct{}

func (audioQuotaSource) ID() string { return "example-audio-quota" }
func (audioQuotaSource) Fetch(_ context.Context, request provider.QuotaRequest) ([]provider.QuotaSnapshot, error) {
	remaining, limit := float64(42), float64(100)
	return []provider.QuotaSnapshot{{
		ProviderNodeID: request.Route.NodeID, ConnectionID: request.Route.CredentialID,
		ModelRef: request.Route.ExternalModel, WindowName: "example-daily",
		Limit: &limit, Remaining: &remaining, Source: "quota_api",
	}}, nil
}

const audioInspectOperation = "audio.inspect"

func registerAudioArtifactInspection(runtime *provider.RuntimeRegistry) error {
	artifactRef := provider.Ref{Kind: provider.ArtifactKind, ID: "example.audio.body", ContractVersion: 1}
	artifactPolicy := provider.ArtifactPolicy{
		OwnerDomains: []string{"client"}, ReplayScopes: []provider.ArtifactReplayScope{provider.ArtifactReplayRequest},
		SensitivityLimit: provider.ArtifactPrivate, AllowedMediaTypes: []string{"audio/wav"},
		RecipientContracts: []provider.Ref{
			{Kind: provider.ArtifactInterpreterKind, ID: "example.audio.header", ContractVersion: 1},
			{Kind: "provider-definition", ID: "example-audio", ContractVersion: 1},
		},
	}
	if err := runtime.RegisterExtension(provider.ExtensionDescriptor{
		Ref: artifactRef, ImplementationVersion: "1", DisplayName: "Audio body", Description: "Bounded audio input for the external module example.",
		ArtifactPolicy: &artifactPolicy, ResourceBounds: provider.ResourceBounds{MaxInputBytes: 16 * 1024 * 1024},
	}, func(json.RawMessage) (any, error) { return struct{}{}, nil }); err != nil {
		return err
	}
	factsSchemaRef := provider.Ref{Kind: provider.SchemaKind, ID: "example.audio.facts", ContractVersion: 1}
	factsSchema := json.RawMessage(`{"type":"object","properties":{"codec":{"const":"wav"}},"required":["codec"],"additionalProperties":false}`)
	if err := runtime.RegisterArtifactInterpreter(
		provider.Ref{Kind: provider.ArtifactInterpreterKind, ID: "example.audio.header", ContractVersion: 1},
		"1", "WAV header interpreter", "Read a small audio header without buffering the artifact.",
		factsSchemaRef, factsSchema,
		nil, nil, provider.ResourceBounds{MaxInputBytes: 64, MaxOutputBytes: 128, MaxBufferedBytes: 128, DeadlineMillis: 250},
		func(json.RawMessage) (any, error) { return wavHeaderInterpreter{}, nil },
	); err != nil {
		return err
	}
	featureRef := provider.Ref{Kind: "feature-evaluator", ID: "example.audio.codec", ContractVersion: 1}
	if err := runtime.RegisterFeatureEvaluator(featureRef, "1", "Audio codec", "Require a codec proven by bounded artifact inspection.", factsSchemaRef, factsSchema, audioCodecEvaluator{}); err != nil {
		return err
	}
	inputSchemaRef := provider.Ref{Kind: provider.SchemaKind, ID: "example.audio.inspect.input", ContractVersion: 1}
	if err := runtime.RegisterSchemaDocument(inputSchemaRef, json.RawMessage(`{"type":"object"}`)); err != nil {
		return err
	}
	return runtime.Primitives.Operations.Register(provider.OperationDefinition{
		Ref:         provider.Ref{Kind: "operation", ID: audioInspectOperation, ContractVersion: 1},
		DisplayName: "Inspect audio", Description: "Derive decoder requirements from bounded audio bytes.",
		InputSchemaRef:       inputSchemaRef,
		ArtifactInputs:       []provider.ArtifactInput{{Role: "audio", TypeRef: artifactRef, MinCount: 1, MaxCount: 1}},
		ArtifactInterpreters: []provider.ArtifactInterpreterBinding{{Role: "audio", InterpreterRef: provider.Ref{Kind: provider.ArtifactInterpreterKind, ID: "example.audio.header", ContractVersion: 1}}},
		ResourceBounds:       provider.ResourceBounds{MaxInputBytes: 16 * 1024 * 1024, MaxOutputBytes: 64 * 1024, MaxBufferedBytes: 64 * 1024, DeadlineMillis: 2000},
		ReplaySafety:         provider.ReplayNever,
		InputPayload:         func(provider.OperationRequest) (json.RawMessage, error) { return json.RawMessage(`{}`), nil },
		CompileRequirements:  func(provider.OperationRequest) ([]provider.FeatureRequirement, error) { return nil, nil },
		CompileArtifactRequirements: func(_ provider.OperationRequest, facts []provider.ArtifactFacts) ([]provider.FeatureRequirement, error) {
			if len(facts) != 1 {
				return nil, fmt.Errorf("expected one interpreted audio artifact")
			}
			return []provider.FeatureRequirement{{Ref: featureRef, Constraints: append(json.RawMessage(nil), facts[0].Value...)}}, nil
		},
	})
}

type audioCodecEvaluator struct{}

func (audioCodecEvaluator) Evaluate(capability provider.Capability, constraints json.RawMessage) (bool, string) {
	if capability.State != provider.SupportNative {
		return false, "route has no native audio-codec evidence"
	}
	var supported, requested struct {
		Codec string `json:"codec"`
	}
	if json.Unmarshal(capability.Constraints, &supported) != nil || json.Unmarshal(constraints, &requested) != nil || requested.Codec == "" {
		return false, "audio codec constraint is invalid or absent"
	}
	if supported.Codec != requested.Codec {
		return false, "route does not support the requested audio codec"
	}
	return true, "route supports the requested audio codec"
}

type wavHeaderInterpreter struct{}

func (wavHeaderInterpreter) InspectArtifact(ctx context.Context, _ provider.ArtifactRef, body io.Reader) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	header := make([]byte, 4)
	if _, err := io.ReadFull(body, header); err != nil {
		return nil, err
	}
	if string(header) != "RIFF" {
		return nil, fmt.Errorf("audio body does not have a RIFF header")
	}
	return json.RawMessage(`{"codec":"wav"}`), nil
}

type audioRequestCodec struct{}

func (audioRequestCodec) ID() string { return "example-audio-json" }
func (audioRequestCodec) DescribeCompatibility(input provider.CompatibilityContext) []provider.FacetMapping {
	return []provider.FacetMapping{{Facet: provider.FacetWireRequest, Paths: []string{"operationPayload"}, Disposition: provider.FacetTranslated}}
}
func (audioRequestCodec) Prepare(_ context.Context, request provider.NormalizedRequest, route provider.Route, credential provider.Credential) (provider.UpstreamRequest, error) {
	if len(request.OperationPayload) == 0 || !json.Valid(request.OperationPayload) {
		return provider.UpstreamRequest{}, fmt.Errorf("audio payload must be valid JSON")
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	if userID := credential.AuthContext["userId"]; userID != "" {
		headers.Set("X-Example-User", userID)
	}
	if err := provider.ApplyCredentialPresentation(headers, credential, route.CredentialPresentation); err != nil {
		return provider.UpstreamRequest{}, err
	}
	return provider.UpstreamRequest{Method: http.MethodPost, URL: "/audio/transcriptions", Headers: headers, Body: bytes.NewReader(request.OperationPayload)}, nil
}

type audioResponseDecoder struct{}

func (audioResponseDecoder) ID() string { return "example-audio-json" }
func (audioResponseDecoder) PossibleEvents() []provider.ResponseEventKind {
	return []provider.ResponseEventKind{provider.EventTextDelta, provider.EventResponseComplete}
}
func (audioResponseDecoder) ClassifyError(status int, _ []byte) provider.ErrorClass {
	if status >= http.StatusBadRequest {
		return "terminal"
	}
	return "retryable"
}
func (audioResponseDecoder) Decode(_ context.Context, response provider.UpstreamResponse, emit func(provider.ResponseEvent) error, _ provider.StreamHooks) error {
	if response.Body == nil {
		return fmt.Errorf("audio response body is required")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	var result struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.Text == "" {
		return fmt.Errorf("audio response must contain non-empty text")
	}
	if err := emit(provider.ResponseEvent{Kind: provider.EventTextDelta, Text: result.Text}); err != nil {
		return err
	}
	return emit(provider.ResponseEvent{Kind: provider.EventResponseComplete})
}
