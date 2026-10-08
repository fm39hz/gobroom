package kernel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/artifacts"
	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/operations"
)

type responseArtifactRequestCodec struct{}

func (responseArtifactRequestCodec) ID() string { return "response-artifact-fixture" }
func (responseArtifactRequestCodec) DescribeCompatibility(input CompatibilityContext) []FacetMapping {
	result := make([]FacetMapping, 0)
	for _, facet := range RequiredRequestFacets(input.Request) {
		result = append(result, FacetMapping{Facet: facet, Paths: []string{"fixture.request"}, Disposition: FacetPreserved})
	}
	return result
}
func (responseArtifactRequestCodec) Prepare(context.Context, NormalizedRequest, Route, Credential) (UpstreamRequest, error) {
	return UpstreamRequest{Method: http.MethodPost, URL: "https://fixture.invalid/generate"}, nil
}

type responseArtifactDecoder struct{ typeRef extensions.Ref }

func (responseArtifactDecoder) ID() string { return "response-artifact-fixture" }
func (responseArtifactDecoder) PossibleEvents() []ResponseEventKind {
	return []ResponseEventKind{EventTextDelta, EventArtifact, EventResponseComplete}
}
func (responseArtifactDecoder) ClassifyError(int, []byte) ErrorClass { return ErrorRetryable }
func (d responseArtifactDecoder) Decode(ctx context.Context, _ UpstreamResponse, emit func(ResponseEvent) error, hooks StreamHooks) error {
	artifact, err := artifacts.PutResponseArtifactFromContext(ctx, d.typeRef, "image", "image/png", extensions.ArtifactPrivate, strings.NewReader("pixels"), 1024, 0)
	if err != nil {
		return err
	}
	if err := emit(ResponseEvent{Kind: EventArtifact, Artifacts: []extensions.ArtifactRef{artifact}}); err != nil {
		return err
	}
	if err := emit(ResponseEvent{Kind: EventTextDelta, Text: "rendered"}); err != nil {
		return err
	}
	if err := emit(ResponseEvent{Kind: EventResponseComplete}); err != nil {
		return err
	}
	if hooks.OnComplete != nil {
		hooks.OnComplete(UsageEvent{Status: "ok"})
	}
	return nil
}

type responseArtifactRenderer struct {
	got      *[]byte
	artifact *extensions.ArtifactRef
}

func (responseArtifactRenderer) ID() normalize.Format { return normalize.FormatOpenAIChat }
func (responseArtifactRenderer) SupportsResponse(options ResponseRenderContext) CompatibilityPlan {
	mappings := make([]FacetMapping, 0, len(options.RequiredEvents)+1)
	for _, event := range options.RequiredEvents {
		found := false
		for _, declared := range options.ProviderEvents {
			if event == declared {
				found = true
				break
			}
		}
		disposition := FacetUnsupported
		if found {
			disposition = FacetPreserved
		}
		mappings = append(mappings, FacetMapping{Facet: ResponseEventFacet(event), Paths: []string{"fixture.renderer"}, Disposition: disposition})
	}
	mappings = append(mappings, FacetMapping{Facet: FacetWireResponse, Paths: []string{"fixture.renderer"}, Disposition: FacetPreserved})
	return ComposeCompatibilityPlan([][]FacetMapping{mappings}, CompatibilityPolicy{RequiredFacets: func() []string {
		facets := make([]string, 0, len(options.RequiredEvents)+1)
		facets = append(facets, FacetWireResponse)
		for _, event := range options.RequiredEvents {
			facets = append(facets, ResponseEventFacet(event))
		}
		return facets
	}()})
}
func (r responseArtifactRenderer) Begin(_ context.Context, _ ResponseRenderContext, writer http.ResponseWriter) (ResponseRenderSession, error) {
	return responseArtifactSession{writer: writer, got: r.got, artifact: r.artifact}, nil
}

type responseArtifactSession struct {
	writer   http.ResponseWriter
	got      *[]byte
	artifact *extensions.ArtifactRef
}

func (s responseArtifactSession) Emit(ctx context.Context, event ResponseEvent) error {
	switch event.Kind {
	case EventArtifact:
		for _, artifact := range event.Artifacts {
			copy := artifact.Clone()
			*s.artifact = copy
			lease, err := artifacts.OpenArtifactFromContext(ctx, artifact)
			if err != nil {
				return err
			}
			body, readErr := io.ReadAll(lease)
			closeErr := lease.Close()
			if readErr != nil {
				return readErr
			}
			if closeErr != nil {
				return closeErr
			}
			*s.got = append(*s.got, body...)
		}
	case EventTextDelta:
		_, err := io.WriteString(s.writer, event.Text)
		return err
	}
	return nil
}
func (responseArtifactSession) Finish(context.Context, error) error { return nil }

type responseArtifactAdapter struct{ composed ComposedAdapter }

func (responseArtifactAdapter) ID() string { return "response-artifact-fixture" }
func (a responseArtifactAdapter) PlanCompatibility(input CompatibilityContext) CompatibilityPlan {
	return a.composed.PlanCompatibility(input)
}
func (responseArtifactAdapter) Prepare(context.Context, NormalizedRequest, Route, Credential) (UpstreamRequest, error) {
	return UpstreamRequest{Method: http.MethodPost, URL: "https://fixture.invalid/generate"}, nil
}
func (responseArtifactAdapter) Execute(context.Context, UpstreamRequest) (UpstreamResponse, error) {
	return UpstreamResponse{Status: http.StatusOK, Headers: make(http.Header), Body: io.NopCloser(strings.NewReader("fixture"))}, nil
}
func (responseArtifactAdapter) ClassifyError(int, []byte) ErrorClass { return ErrorRetryable }
func (a responseArtifactAdapter) RenderResponse(ctx context.Context, response UpstreamResponse, writer http.ResponseWriter, source normalize.Format, hooks StreamHooks) error {
	return a.composed.RenderResponse(ctx, response, writer, source, hooks)
}

func TestKernelTransfersProviderResponseArtifactThroughAuthorizedRendererLease(t *testing.T) {
	catalog := extensions.NewCatalog()
	operationRef := extensions.Ref{Kind: "operation", ID: "image.generate", ContractVersion: 1}
	artifactRef := extensions.Ref{Kind: extensions.ArtifactKind, ID: "image.output", ContractVersion: 1}
	inputSchema := extensions.Ref{Kind: extensions.SchemaKind, ID: "image.generate.input", ContractVersion: 1}
	eventSchema := extensions.Ref{Kind: extensions.SchemaKind, ID: "image.generate.event", ContractVersion: 1}
	for ref, shape := range map[extensions.Ref]json.RawMessage{
		inputSchema: json.RawMessage(`{"type":"object"}`),
		eventSchema: json.RawMessage(`{"type":"object","required":["kind"],"properties":{"kind":{"type":"string"},"sequence":{"type":"integer"},"itemId":{"type":"string"},"content":{"type":"object"}},"additionalProperties":true}`),
	} {
		document, err := extensions.BindSchemaDocument(ref, shape)
		if err != nil {
			t.Fatal(err)
		}
		if err := catalog.RegisterSchema(ref, document); err != nil {
			t.Fatal(err)
		}
	}
	policy := extensions.ArtifactPolicy{
		OwnerDomains: []string{"provider"}, ReplayScopes: []extensions.ArtifactReplayScope{extensions.ArtifactReplayRequest},
		SensitivityLimit: extensions.ArtifactPrivate, AllowedMediaTypes: []string{"image/png"}, RecipientContracts: []extensions.Ref{operationRef},
	}
	if err := catalog.Register(extensions.Descriptor{
		Ref: artifactRef, ImplementationVersion: "1", DisplayName: "Generated image", Description: "Response image output.",
		ArtifactPolicy: &policy, ResourceBounds: extensions.ResourceBounds{MaxInputBytes: 1024},
	}, func(json.RawMessage) (any, error) { return struct{}{}, nil }); err != nil {
		t.Fatal(err)
	}
	operationRegistry, err := operations.NewRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := operationRegistry.Register(operations.Definition{
		Ref: operationRef, DisplayName: "Image generation", Description: "Generate a response image.",
		InputSchemaRef: inputSchema, EventSchemaRefs: []extensions.Ref{eventSchema},
		ArtifactOutputs: []operations.ArtifactOutput{{Role: "image", TypeRef: artifactRef, MinCount: 1, MaxCount: 1}},
		ResourceBounds:  operations.DefaultResourceBounds(), ReplaySafety: operations.ReplayNever,
		InputPayload:        func(normalize.Request) (json.RawMessage, error) { return json.RawMessage(`{}`), nil },
		CompileRequirements: func(normalize.Request) ([]normalize.FeatureRequirement, error) { return nil, nil },
	}); err != nil {
		t.Fatal(err)
	}
	contractSnapshot, err := catalog.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	operationSnapshot, err := operationRegistry.Seal(contractSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	providerRef := extensions.Ref{Kind: "provider-definition", ID: "fixture.image-host", ContractVersion: 1}
	bindings := map[normalize.Operation]RouteOperationBinding{normalize.Operation(operationRef.ID): {ContractVersion: 1, AdapterIDs: []string{"artifact"}}}
	snapshot, err := BuildSnapshot(SnapshotInput{
		PublicModels: []PublicModel{{Name: "image", TargetRef: "physical-image"}},
		Routes:       []Route{{ID: "route-image", Enabled: true, Protocol: ProtocolOpenAIChat, DefinitionID: providerRef.ID, DefinitionRef: providerRef, OperationBindings: bindings, BaseURL: "https://fixture.invalid", ExternalModel: "image-v1", CredentialID: "conn-a"}},
		Nodes:        []ModelNode{{ID: "physical-image", Kind: ModelPhysical, Members: []MemberRef{{Kind: MemberRoute, ID: "route-image", Fidelity: FidelityExact}}}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	k, err := New(snapshot, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	k.Operations = operationSnapshot
	spool, err := artifacts.NewStore(t.TempDir(), artifacts.Limits{MaxBytes: 1 << 20, MaxBodyBytes: 1 << 16})
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	k.ArtifactStore = spool
	var got []byte
	var renderedArtifact extensions.ArtifactRef
	decoder := responseArtifactDecoder{typeRef: artifactRef}
	composed := ComposedAdapter{
		AdapterID: "artifact", Request: responseArtifactRequestCodec{}, Response: decoder, ProviderFormat: normalize.Format("fixture_provider"),
		Renderers: map[normalize.Format]ResponseRenderer{normalize.FormatOpenAIChat: responseArtifactRenderer{got: &got, artifact: &renderedArtifact}},
	}
	k.Adapters["artifact"] = responseArtifactAdapter{composed: composed}
	request := NormalizedRequest{Model: "image", Operation: normalize.Operation(operationRef.ID), OperationContractVersion: 1, SourceFormat: normalize.FormatOpenAIChat}
	writer := httptest.NewRecorder()
	if err := k.Execute(context.Background(), request, Credential{}, writer); err != nil {
		t.Fatal(err)
	}
	if string(got) != "pixels" || writer.Body.String() != "rendered" {
		t.Fatalf("renderer got artifact=%q client body=%q", got, writer.Body.String())
	}
	clientOwner := extensions.ArtifactOwner{Domain: "harness", ClientContract: string(normalize.FormatOpenAIChat)}
	if _, err := spool.OpenArtifact(context.Background(), contractSnapshot, renderedArtifact, clientOwner, operationRef); err == nil {
		t.Fatal("response artifact remained leased after rendering completed")
	}
}
