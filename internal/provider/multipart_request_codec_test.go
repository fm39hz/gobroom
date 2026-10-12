package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/artifacts"
	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/operations"
)

type multipartRoundTripper func(*http.Request) (*http.Response, error)

func (roundTrip multipartRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

type audioTranscriptProjector struct{ text strings.Builder }

func (p *audioTranscriptProjector) ConsumeEvent(raw json.RawMessage) error {
	var event struct {
		Content struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		return err
	}
	_, _ = p.text.WriteString(event.Content.Text)
	return nil
}
func (p *audioTranscriptProjector) Finalize() (json.RawMessage, error) {
	return json.Marshal(map[string]string{"transcript": p.text.String()})
}

func TestMultipartRequestCodecStreamsConfiguredArtifactFields(t *testing.T) {
	recipient := extensions.Ref{Kind: "provider-definition", ID: "fixture.asr", ContractVersion: 1}
	artifactType := extensions.Ref{Kind: extensions.ArtifactKind, ID: "fixture.audio", ContractVersion: 1}
	catalog := extensions.NewCatalog()
	if err := catalog.Register(extensions.Descriptor{Ref: recipient, ImplementationVersion: "1", DisplayName: "ASR provider", Description: "Fixture."}, func(json.RawMessage) (any, error) { return struct{}{}, nil }); err != nil {
		t.Fatal(err)
	}
	policy := extensions.ArtifactPolicy{
		OwnerDomains: []string{"client"}, ReplayScopes: []extensions.ArtifactReplayScope{extensions.ArtifactReplayRequest},
		SensitivityLimit: extensions.ArtifactPrivate, AllowedMediaTypes: []string{"audio/wav"}, RecipientContracts: []extensions.Ref{recipient},
	}
	if err := catalog.Register(extensions.Descriptor{
		Ref: artifactType, ImplementationVersion: "1", DisplayName: "Audio input", Description: "Fixture audio body.",
		ArtifactPolicy: &policy, ResourceBounds: extensions.ResourceBounds{MaxInputBytes: 128},
	}, func(json.RawMessage) (any, error) { return struct{}{}, nil }); err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	bodyStore, err := artifacts.NewStore(t.TempDir(), artifacts.Limits{MaxBytes: 256, MaxBodyBytes: 128})
	if err != nil {
		t.Fatal(err)
	}
	defer bodyStore.Close()
	owner := extensions.ArtifactOwner{Domain: "client", ClientContract: "multipart-asr"}
	bodyRef, err := bodyStore.Put(context.Background(), owner, "audio/wav", strings.NewReader("waveform"), 128, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	artifact := extensions.ArtifactRef{
		TypeRef: artifactType, Role: "source-audio", Owner: owner, ReplayScope: extensions.ArtifactReplayRequest,
		Sensitivity: extensions.ArtifactPrivate, MediaType: bodyRef.MediaType, SizeBytes: bodyRef.SizeBytes,
		SHA256: bodyRef.SHA256, ExpiresAt: bodyRef.ExpiresAt, Body: &bodyRef,
	}
	codec, err := NewMultipartRequestCodec(MultipartRequestCodecOptions{
		ModelField: "model", PayloadFields: []MultipartPayloadField{{Property: "language", Field: "language", Required: true}}, StreamField: "stream",
		Artifacts: []MultipartArtifactField{{Role: "source-audio", Field: "file", FileName: "input.wav"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := artifacts.WithAccess(context.Background(), artifacts.Access{
		Store: bodyStore, Catalog: snapshot,
		Receiver:  extensions.ArtifactOwner{Domain: "provider", ProviderDefinitionID: recipient.ID, ConnectionID: "asr-1"},
		Recipient: recipient,
	})
	upstream, err := codec.Prepare(ctx, normalize.Request{
		Model: "asr-small", OperationPayload: json.RawMessage(`{"language":"vi"}`), Stream: true,
		Artifacts: []extensions.ArtifactRef{artifact},
	}, kernel.Route{}, kernel.Credential{})
	if err != nil {
		t.Fatal(err)
	}
	closer, ok := upstream.Body.(io.Closer)
	if !ok {
		t.Fatal("multipart request body is not closeable")
	}
	defer closer.Close()
	mediaType, params, err := mime.ParseMediaType(upstream.Headers.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		t.Fatalf("multipart content-type=%q err=%v", upstream.Headers.Get("Content-Type"), err)
	}
	reader := multipart.NewReader(upstream.Body, params["boundary"])
	fields := map[string]string{}
	var artifactBody string
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(part)
		if err != nil {
			t.Fatal(err)
		}
		switch part.FormName() {
		case "model", "language", "stream":
			fields[part.FormName()] = string(content)
		case "file":
			artifactBody = string(content)
			if part.FileName() != "input.wav" || part.Header.Get("Content-Type") != "audio/wav" {
				t.Fatalf("artifact part metadata filename=%q content-type=%q", part.FileName(), part.Header.Get("Content-Type"))
			}
		default:
			t.Fatalf("unexpected multipart form field %q", part.FormName())
		}
		_ = part.Close()
	}
	if fields["model"] != "asr-small" || fields["language"] != "vi" || fields["stream"] != "true" || artifactBody != "waveform" {
		t.Fatalf("multipart fields=%#v artifact=%q", fields, artifactBody)
	}
}

func TestRuntimeBindingBindsManifestConfiguredMultipartCodecOptions(t *testing.T) {
	registry, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	providerRef := ProviderDefinitionRef("multipart-manifest-provider", 1)
	operationRef := extensions.Ref{Kind: "operation", ID: "audio.transcribe", ContractVersion: 1}
	inputSchemaRef := extensions.Ref{Kind: extensions.SchemaKind, ID: "audio.transcribe.input", ContractVersion: 1}
	inputSchema, err := extensions.BindSchemaDocument(inputSchemaRef, json.RawMessage(`{"type":"object"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Primitives.catalog.RegisterSchema(inputSchemaRef, inputSchema); err != nil {
		t.Fatal(err)
	}
	artifactType := extensions.Ref{Kind: extensions.ArtifactKind, ID: "audio.input", ContractVersion: 1}
	policy := extensions.ArtifactPolicy{
		OwnerDomains: []string{"client"}, ReplayScopes: []extensions.ArtifactReplayScope{extensions.ArtifactReplayRequest},
		SensitivityLimit: extensions.ArtifactPrivate, AllowedMediaTypes: []string{"audio/wav"}, RecipientContracts: []extensions.Ref{providerRef},
	}
	if err := registry.Primitives.catalog.Register(extensions.Descriptor{
		Ref: artifactType, ImplementationVersion: "1", DisplayName: "Audio input", Description: "Transcription source.",
		ArtifactPolicy: &policy, ResourceBounds: extensions.ResourceBounds{MaxInputBytes: 32 << 20},
	}, func(json.RawMessage) (any, error) { return struct{}{}, nil }); err != nil {
		t.Fatal(err)
	}
	if err := registry.Primitives.Operations.Register(operations.Definition{
		Ref: operationRef, DisplayName: "Transcribe audio", Description: "Transcribe one audio artifact.",
		InputSchemaRef: inputSchemaRef, ArtifactInputs: []operations.ArtifactInput{{Role: "source-audio", TypeRef: artifactType, MinCount: 1, MaxCount: 1}},
		ResourceBounds: operations.DefaultResourceBounds(), ReplaySafety: operations.ReplayNever,
		InputPayload:        func(normalize.Request) (json.RawMessage, error) { return json.RawMessage(`{}`), nil },
		CompileRequirements: func(normalize.Request) ([]normalize.FeatureRequirement, error) { return nil, nil },
	}); err != nil {
		t.Fatal(err)
	}
	validBinding := OperationBinding{
		Protocol: kernel.Protocol("fixture.multipart"), TaskRef: &operationRef,
		Endpoint:            PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json", ContractVersion: 1},
		Transport:           PrimitiveRef{Kind: PrimitiveTransport, ID: "http", ContractVersion: 1},
		RequestCodec:        PrimitiveRef{Kind: PrimitiveRequestCodec, ID: MultipartRequestCodecID, ContractVersion: 1},
		RequestCodecOptions: json.RawMessage(`{"modelField":"model","payloadField":"metadata","streamField":"stream","artifacts":[{"role":"source-audio","field":"file","fileName":"audio.wav"}]}`),
		ResponseDecoder:     PrimitiveRef{Kind: PrimitiveResponseDecoder, ID: "openai-sse", ContractVersion: 1},
	}
	badBinding := validBinding
	badBinding.RequestCodecOptions = json.RawMessage(`{"modelField":"model","payloadField":"metadata","artifacts":[{"role":"wrong-role","field":"file"}]}`)
	definition := ProviderDefinition{
		ContractVersion: 1, ID: "multipart-manifest-provider", Version: "1", DisplayName: "Multipart manifest provider",
		Auth: PrimitiveRef{Kind: PrimitiveAuth, ID: "none", ContractVersion: 1},
		Operations: map[Operation]OperationBinding{
			Operation("audio.transcribe"):     validBinding,
			Operation("audio.transcribe.bad"): badBinding,
		},
	}
	if err := registry.Primitives.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	binding, err := NewRuntimeBindingBuilder(registry).Build(definition.ID, Operation("audio.transcribe"))
	if err != nil {
		t.Fatal(err)
	}
	adapter, ok := binding.Adapter.(kernel.ComposedAdapter)
	if !ok {
		t.Fatalf("adapter type=%T, want ComposedAdapter", binding.Adapter)
	}
	codec, ok := adapter.Request.(*MultipartRequestCodec)
	if !ok {
		t.Fatalf("request codec type=%T, want configured MultipartRequestCodec", adapter.Request)
	}
	if codec.options.ModelField != "model" || codec.options.PayloadField != "metadata" || codec.options.StreamField != "stream" || codec.artifactFields["source-audio"].Field != "file" {
		t.Fatalf("manifest requestCodecOptions were not bound: %#v", codec.options)
	}
	if _, err := NewRuntimeBindingBuilder(registry).Build(definition.ID, Operation("audio.transcribe.bad")); err == nil || !strings.Contains(err.Error(), "does not map declared artifact role") {
		t.Fatalf("manifest mapped an undeclared artifact role: %v", err)
	}
}

func TestManifestMultipartCodecFramesBinaryBodyThroughKernelAndTypedResult(t *testing.T) {
	runtime, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	providerRef := ProviderDefinitionRef("multipart-e2e-provider", 1)
	operationRef := extensions.Ref{Kind: "operation", ID: "audio.transcribe.e2e", ContractVersion: 1}
	inputSchemaRef := extensions.Ref{Kind: extensions.SchemaKind, ID: "audio.transcribe.e2e.input", ContractVersion: 1}
	resultSchemaRef := extensions.Ref{Kind: extensions.SchemaKind, ID: "audio.transcribe.e2e.result", ContractVersion: 1}
	eventSchemaRef := extensions.Ref{Kind: extensions.SchemaKind, ID: "audio.transcribe.e2e.event", ContractVersion: 1}
	for _, schema := range []struct {
		ref  extensions.Ref
		body json.RawMessage
	}{
		{inputSchemaRef, json.RawMessage(`{"type":"object"}`)},
		{resultSchemaRef, json.RawMessage(`{"type":"object","properties":{"transcript":{"type":"string"}},"required":["transcript"],"additionalProperties":false}`)},
		{eventSchemaRef, json.RawMessage(`{"type":"object","properties":{"kind":{"type":"string"},"sequence":{"type":"integer"},"itemId":{"type":"string"},"content":{"type":"object"}},"required":["kind","sequence","content"],"additionalProperties":true}`)},
	} {
		document, err := extensions.BindSchemaDocument(schema.ref, schema.body)
		if err != nil {
			t.Fatal(err)
		}
		if err := runtime.Primitives.catalog.RegisterSchema(schema.ref, document); err != nil {
			t.Fatal(err)
		}
	}
	artifactType := extensions.Ref{Kind: extensions.ArtifactKind, ID: "audio.e2e.input", ContractVersion: 1}
	policy := extensions.ArtifactPolicy{
		OwnerDomains: []string{"client"}, ReplayScopes: []extensions.ArtifactReplayScope{extensions.ArtifactReplayRequest},
		SensitivityLimit: extensions.ArtifactPrivate, AllowedMediaTypes: []string{"audio/wav"}, RecipientContracts: []extensions.Ref{providerRef},
	}
	if err := runtime.RegisterExtension(extensions.Descriptor{
		Ref: artifactType, ImplementationVersion: "1", DisplayName: "Audio input", Description: "Streamed WAV source.",
		ArtifactPolicy: &policy, ResourceBounds: extensions.ResourceBounds{MaxInputBytes: 1024},
	}, func(json.RawMessage) (any, error) { return struct{}{}, nil }); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Primitives.Operations.Register(operations.Definition{
		Ref: operationRef, DisplayName: "Transcribe audio", Description: "Transcribe one WAV artifact into a typed transcript.",
		InputSchemaRef: inputSchemaRef, ResultSchemaRef: &resultSchemaRef, EventSchemaRefs: []extensions.Ref{eventSchemaRef},
		ArtifactInputs: []operations.ArtifactInput{{Role: "source-audio", TypeRef: artifactType, MinCount: 1, MaxCount: 1}},
		ResourceBounds: operations.DefaultResourceBounds(), ReplaySafety: operations.ReplayNever,
		InputPayload:        func(normalize.Request) (json.RawMessage, error) { return json.RawMessage(`{}`), nil },
		CompileRequirements: func(normalize.Request) ([]normalize.FeatureRequirement, error) { return nil, nil },
		NewResultProjector: func(extensions.ResourceBounds) (operations.ResultProjector, error) {
			return &audioTranscriptProjector{}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	codecOptions := json.RawMessage(`{"modelField":"model","payloadFields":[{"property":"language","field":"language","required":true}],"artifacts":[{"role":"source-audio","field":"file","fileName":"source.wav"}]}`)
	providerDefinition := ProviderDefinition{
		ContractVersion: 1, ID: "multipart-e2e-provider", Version: "1", DisplayName: "Multipart E2E",
		Auth: PrimitiveRef{Kind: PrimitiveAuth, ID: "none", ContractVersion: 1},
		Operations: map[Operation]OperationBinding{Operation("audio.transcribe.e2e"): {
			Protocol: kernel.ProtocolOpenAIChat, TaskRef: &operationRef, ProviderFormat: normalize.FormatOpenAIChat,
			Endpoint:     PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json", ContractVersion: 1},
			Transport:    PrimitiveRef{Kind: PrimitiveTransport, ID: "http", ContractVersion: 1},
			RequestCodec: PrimitiveRef{Kind: PrimitiveRequestCodec, ID: MultipartRequestCodecID, ContractVersion: 1}, RequestCodecOptions: codecOptions,
			ResponseDecoder: PrimitiveRef{Kind: PrimitiveResponseDecoder, ID: "openai-sse", ContractVersion: 1},
		}},
	}
	if err := runtime.Primitives.RegisterDefinition(providerDefinition); err != nil {
		t.Fatal(err)
	}
	binding, err := NewRuntimeBindingBuilder(runtime).Build(providerDefinition.ID, Operation("audio.transcribe.e2e"))
	if err != nil {
		t.Fatal(err)
	}
	operationSnapshot, err := runtime.OperationSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	var receivedModel, receivedPayload, receivedFile string
	var receivedFileName, receivedFileType string
	var captureMu sync.Mutex
	var roundTrips int
	transport := kernel.HTTPTransport{Client: &http.Client{Transport: multipartRoundTripper(func(request *http.Request) (*http.Response, error) {
		captureMu.Lock()
		roundTrips++
		captureMu.Unlock()
		if request.URL.Path != "/v1/" {
			t.Errorf("multipart upstream path=%q", request.URL.Path)
			return nil, fmt.Errorf("multipart upstream path=%q", request.URL.Path)
		}
		mediaType, params, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
		if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
			return nil, fmt.Errorf("multipart request content type=%q err=%v", request.Header.Get("Content-Type"), err)
		}
		reader := multipart.NewReader(request.Body, params["boundary"])
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			content, err := io.ReadAll(part)
			if err != nil {
				return nil, err
			}
			captureMu.Lock()
			switch part.FormName() {
			case "model":
				receivedModel = string(content)
			case "language":
				receivedPayload = string(content)
			case "file":
				receivedFile = string(content)
				receivedFileName = part.FileName()
				receivedFileType = part.Header.Get("Content-Type")
			default:
				captureMu.Unlock()
				return nil, fmt.Errorf("unexpected multipart field %q", part.FormName())
			}
			captureMu.Unlock()
		}
		body := `{"id":"fixture-response","choices":[{"index":0,"message":{"role":"assistant","content":"decoded transcript"},"finish_reason":"stop"}]}`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}}
	composed := binding.Adapter.(kernel.ComposedAdapter)
	composed.Transport = transport
	route := kernel.Route{
		ID: "audio-route", DefinitionID: providerDefinition.ID, DefinitionRef: providerRef, ExternalModel: "physical-asr-7",
		BaseURL: "https://audio.example/v1", Protocol: kernel.ProtocolOpenAIChat, Enabled: true,
		OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{normalize.Operation(operationRef.ID): {ContractVersion: operationRef.ContractVersion, AdapterIDs: []string{binding.AdapterID}}},
	}
	runtimeSnapshot, err := kernel.BuildSnapshot(kernel.SnapshotInput{
		PublicModels: []kernel.PublicModel{{Name: "speech-role", TargetRef: "speech-role"}},
		Nodes:        []kernel.ModelNode{{ID: "speech-role", Kind: kernel.ModelPhysical, Members: []kernel.MemberRef{{Kind: kernel.MemberRoute, ID: route.ID, Fidelity: kernel.FidelityExact}}}},
		Routes:       []kernel.Route{route},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := kernel.New(runtimeSnapshot, kernel.AlwaysOpenGate{}, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	engine.Operations = operationSnapshot
	engine.Adapters[binding.AdapterID] = composed
	bodyStore, err := artifacts.NewStore(t.TempDir(), artifacts.Limits{MaxBytes: 1024, MaxBodyBytes: 1024, DefaultTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer bodyStore.Close()
	engine.ArtifactStore = bodyStore
	owner := extensions.ArtifactOwner{Domain: "client", ClientContract: "openai-chat"}
	const audioBody = "RIFFWAVE-fixture"
	bodyRef, err := bodyStore.Put(context.Background(), owner, "audio/wav", strings.NewReader(audioBody), 1024, time.Minute, true)
	if err != nil {
		t.Fatal(err)
	}
	request := kernel.NormalizedRequest{
		Model: "speech-role", Operation: normalize.Operation(operationRef.ID), OperationContractVersion: operationRef.ContractVersion,
		SourceFormat: normalize.FormatOpenAIChat, OperationPayload: json.RawMessage(`{"language":"vi"}`),
		Artifacts: []extensions.ArtifactRef{{TypeRef: artifactType, Role: "source-audio", Owner: owner, ReplayScope: extensions.ArtifactReplayRequest, Sensitivity: extensions.ArtifactPrivate, MediaType: bodyRef.MediaType, SizeBytes: bodyRef.SizeBytes, SHA256: bodyRef.SHA256, ExpiresAt: bodyRef.ExpiresAt, Body: &bodyRef}},
	}
	compatibilityPolicy := kernel.CompatibilityPolicy{RequiredFacets: kernel.RequiredRequestFacets(request)}
	for _, event := range kernel.RequiredResponseEvents(request) {
		compatibilityPolicy.RequiredFacets = append(compatibilityPolicy.RequiredFacets, kernel.ResponseEventFacet(event))
	}
	plan := composed.PlanCompatibility(kernel.CompatibilityContext{Request: request, Route: route, Operation: normalize.Operation(operationRef.ID), Requirements: kernel.CompileRequirements(request), Policy: compatibilityPolicy})
	if !plan.Supported {
		t.Fatalf("manifest-selected multipart adapter rejected the request plan: %#v", plan)
	}
	response := httptest.NewRecorder()
	if err := engine.Execute(context.Background(), request, kernel.Credential{}, response); err != nil {
		t.Fatalf("kernel multipart execution failed after %d upstream attempts: %v", roundTrips, err)
	}
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "decoded transcript") {
		t.Fatalf("client transcript response status=%d body=%s", response.Code, response.Body.String())
	}
	captureMu.Lock()
	defer captureMu.Unlock()
	if receivedModel != "physical-asr-7" || receivedPayload != "vi" || receivedFile != audioBody || receivedFileName != "source.wav" || receivedFileType != "audio/wav" {
		t.Fatalf("manifest multipart wire frame: model=%q payload=%q file=%q filename=%q type=%q", receivedModel, receivedPayload, receivedFile, receivedFileName, receivedFileType)
	}
}

func TestMultipartRequestCodecFailsClosedForUnmappedArtifactsAndStreamMode(t *testing.T) {
	codec, err := NewMultipartRequestCodec(MultipartRequestCodecOptions{
		ModelField: "model", PayloadField: "metadata",
		Artifacts: []MultipartArtifactField{{Role: "audio", Field: "file"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := codec.Prepare(context.Background(), normalize.Request{Model: "asr", Stream: true}, kernel.Route{}, kernel.Credential{}); err == nil {
		t.Fatal("streaming request was accepted without a configured stream field")
	}
	if _, err := codec.Prepare(context.Background(), normalize.Request{
		Model: "asr", Artifacts: []extensions.ArtifactRef{{Role: "unmapped"}},
	}, kernel.Route{}, kernel.Credential{}); err == nil {
		t.Fatal("artifact role without an upstream form-field mapping was accepted")
	}
	propertyCodec, err := NewMultipartRequestCodec(MultipartRequestCodecOptions{
		ModelField: "model", PayloadFields: []MultipartPayloadField{{Property: "language", Field: "language"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := propertyCodec.Prepare(context.Background(), normalize.Request{Model: "asr", OperationPayload: json.RawMessage(`{"language":"vi","prompt":"unmapped"}`)}, kernel.Route{}, kernel.Credential{}); err == nil {
		t.Fatal("unmapped operation payload property was silently dropped")
	}
}

func TestMultipartRequestCodecDeclaresExactArtifactRoleCompatibility(t *testing.T) {
	codec, err := NewMultipartRequestCodec(MultipartRequestCodecOptions{
		ModelField: "model", PayloadField: "metadata",
		Artifacts: []MultipartArtifactField{{Role: "source-audio", Field: "file"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := normalize.Request{Artifacts: []extensions.ArtifactRef{{Role: "source-audio"}, {Role: "unmapped"}}}
	requirements := kernel.RequiredRequestFacets(request)
	plan := codec.DescribeCompatibility(kernel.CompatibilityContext{Request: request, Policy: kernel.CompatibilityPolicy{RequiredFacets: requirements}})
	byFacet := make(map[string]kernel.FacetDisposition, len(plan))
	for _, item := range plan {
		byFacet[item.Facet] = item.Disposition
	}
	if byFacet[kernel.OperationArtifactFacet("source-audio")] != kernel.FacetTranslated || byFacet[kernel.OperationArtifactFacet("unmapped")] != kernel.FacetUnsupported {
		t.Fatalf("artifact role compatibility=%#v", byFacet)
	}
}
