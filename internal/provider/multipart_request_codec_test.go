package provider

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/artifacts"
	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/operations"
)

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
