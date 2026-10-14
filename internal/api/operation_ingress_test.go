package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/artifacts"
	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/operations"
	"github.com/fm39hz/gobroom/internal/store"
)

type mismatchedClientIngress struct{}

func (mismatchedClientIngress) ID() string { return "fixture-mismatch" }
func (mismatchedClientIngress) IngressRef() extensions.Ref {
	return extensions.Ref{Kind: extensions.ClientIngressKind, ID: "fixture-mismatch", ContractVersion: 1}
}
func (mismatchedClientIngress) OperationRef() extensions.Ref {
	return extensions.Ref{Kind: "operation", ID: string(normalize.OperationChatGenerate), ContractVersion: 1}
}
func (mismatchedClientIngress) Routes() []IngressRoute {
	return []IngressRoute{{Method: http.MethodPost, Path: "/v1/fixture/mismatch"}}
}
func (mismatchedClientIngress) Decode(*http.Request) (normalize.Request, error) {
	return normalize.Request{Model: "fixture", Operation: "unregistered.operation", OperationContractVersion: 1}, nil
}

func TestClientIngressCannotReturnDifferentOperationThanDeclared(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/mismatched-ingress.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := NewServer(db)
	if err := server.RegisterOperationIngress(mismatchedClientIngress{}); err != nil {
		t.Fatal(err)
	}
	executorCalls := 0
	server.SetExecutor(func(context.Context, normalize.Request, http.ResponseWriter) error {
		executorCalls++
		return nil
	})
	response := httptest.NewRecorder()
	server.HandlerWithOptions(HandlerOptions{DataPlane: true}).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/fixture/mismatch", nil))
	if response.Code != http.StatusBadRequest || executorCalls != 0 {
		t.Fatalf("mismatched operation crossed ingress boundary: status=%d calls=%d body=%q", response.Code, executorCalls, response.Body.String())
	}
}

func TestMultipartOperationIngressSpoolsDeclaredArtifact(t *testing.T) {
	store, err := artifacts.NewStore(t.TempDir(), artifacts.Limits{MaxBytes: 128, MaxBodyBytes: 128, DefaultTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	requestPart, err := writer.CreateFormField("request")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := requestPart.Write([]byte(`{"model":"fixture-model","operationPayload":{"model":"fixture-model"}}`)); err != nil {
		t.Fatal(err)
	}
	imagePart, err := writer.CreateFormFile("source", "scan.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := imagePart.Write([]byte("binary-fixture")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	codec := MultipartOperationIngress{
		CodecID: "fixture-multipart", IngressContractVersion: 1, Operation: normalize.OperationChatGenerate, OperationContractVersion: 1,
		Endpoints: []IngressRoute{{Method: http.MethodPost, Path: "/v1/fixture"}},
		Bindings: []MultipartArtifactBinding{{
			Field: "source", Role: "source-document",
			TypeRef:  extensions.Ref{Kind: extensions.ArtifactKind, ID: "fixture.document", ContractVersion: 1},
			MaxBytes: 32, TTL: time.Minute,
		}},
	}
	request, err := http.NewRequest(http.MethodPost, "/v1/fixture", bytes.NewReader(body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request = request.WithContext(artifacts.WithStore(context.Background(), store))
	decoded, err := codec.Decode(request)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Model != "fixture-model" || decoded.Operation != normalize.OperationChatGenerate || len(decoded.Artifacts) != 1 {
		t.Fatalf("decoded request did not preserve operation and artifact: %#v", decoded)
	}
	artifact := decoded.Artifacts[0]
	if artifact.Role != "source-document" || artifact.TypeRef.ID != "fixture.document" || artifact.Owner.Domain != "client" || artifact.Owner.ClientContract != codec.IngressRef().Key() || artifact.Body == nil {
		t.Fatalf("artifact provenance/role/body not preserved: %#v", artifact)
	}
	lease, err := store.Open(context.Background(), artifact.Owner, *artifact.Body)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(lease)
	if err != nil || string(got) != "binary-fixture" {
		t.Fatalf("spooled multipart body=%q err=%v", got, err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validateOperationIngress(codec); err != nil {
		t.Fatalf("valid multipart operation ingress rejected: %v", err)
	}
}

func TestMultipartOperationIngressReleasesPartialSpoolsOnDecodeFailure(t *testing.T) {
	store, err := artifacts.NewStore(t.TempDir(), artifacts.Limits{MaxBytes: 128, MaxBodyBytes: 128, DefaultTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("source", "scan.bin")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("partial"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	codec := MultipartOperationIngress{
		CodecID: "fixture-multipart", IngressContractVersion: 1, Operation: normalize.OperationChatGenerate, OperationContractVersion: 1,
		Endpoints: []IngressRoute{{Method: http.MethodPost, Path: "/v1/fixture"}},
		Bindings:  []MultipartArtifactBinding{{Field: "source", Role: "source-document", TypeRef: extensions.Ref{Kind: extensions.ArtifactKind, ID: "fixture.document", ContractVersion: 1}, MaxBytes: 32}},
	}
	request, err := http.NewRequest(http.MethodPost, "/v1/fixture", bytes.NewReader(body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request = request.WithContext(artifacts.WithStore(context.Background(), store))
	if _, err := codec.Decode(request); err == nil {
		t.Fatal("multipart request without metadata field was accepted")
	}
	ref, err := store.Put(context.Background(), extensions.ArtifactOwner{Domain: "test"}, "application/octet-stream", strings.NewReader(strings.Repeat("x", 128)), 128, time.Minute, true)
	if err != nil {
		t.Fatalf("partial decode did not release spool capacity: %v", err)
	}
	if err := store.Release(extensions.ArtifactOwner{Domain: "test"}, ref); err != nil {
		t.Fatal(err)
	}
}

func TestMultipartIngressBindingsMustMatchOperationArtifactPorts(t *testing.T) {
	snapshot, artifactRef, _ := multipartTestOperationSnapshot(t)
	codec := MultipartOperationIngress{
		CodecID: "fixture-multipart", IngressContractVersion: 1, Operation: "fixture.inspect", OperationContractVersion: 1,
		Endpoints: []IngressRoute{{Method: http.MethodPost, Path: "/v1/fixture"}},
		Bindings:  []MultipartArtifactBinding{{Field: "source", Role: "source-document", TypeRef: artifactRef, MaxBytes: 32}},
	}
	server := &Server{operations: snapshot, ingress: map[string]OperationIngress{}}
	if err := server.RegisterOperationIngress(codec); err != nil {
		t.Fatalf("matching operation artifact port rejected: %v", err)
	}

	bad := codec
	bad.CodecID = "wrong-role"
	bad.Endpoints = []IngressRoute{{Method: http.MethodPost, Path: "/v1/wrong-role"}}
	bad.Bindings = append([]MultipartArtifactBinding(nil), codec.Bindings...)
	bad.Bindings[0].Role = "unbound-role"
	other := &Server{operations: snapshot, ingress: map[string]OperationIngress{}}
	if err := other.RegisterOperationIngress(bad); err == nil {
		t.Fatal("ingress bound an artifact role absent from the operation contract")
	}
}

type multipartE2EAdapter struct {
	received      string
	features      []normalize.FeatureRequirement
	planCalls     int
	prepareCalls  int
	prepareErr    error
	compatibility kernel.CompatibilityPlan
}

type binaryPrefixInterpreter struct{}

func (binaryPrefixInterpreter) InspectArtifact(_ context.Context, _ extensions.ArtifactRef, body io.Reader) (json.RawMessage, error) {
	var header [6]byte
	if _, err := io.ReadFull(body, header[:]); err != nil {
		return nil, err
	}
	if string(header[:]) != "binary" {
		return nil, fmt.Errorf("unsupported binary signature %q", header[:])
	}
	return json.RawMessage(`{"kind":"binary"}`), nil
}

type binaryKindFeature struct{ evaluations *int }

func (feature binaryKindFeature) Evaluate(capability kernel.Capability, constraints json.RawMessage) (bool, string) {
	*feature.evaluations++
	var requested struct {
		Kind string `json:"kind"`
	}
	var supported struct {
		Kinds []string `json:"kinds"`
	}
	if json.Unmarshal(constraints, &requested) != nil || json.Unmarshal(capability.Constraints, &supported) != nil || capability.State != kernel.SupportNative {
		return false, "invalid binary-kind evidence"
	}
	for _, kind := range supported.Kinds {
		if kind == requested.Kind {
			return true, ""
		}
	}
	return false, "binary kind is not supported"
}

type transcriptProjector struct {
	text strings.Builder
	max  int64
}

func (p *transcriptProjector) ConsumeEvent(raw json.RawMessage) error {
	var event struct {
		Content struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		return err
	}
	if int64(len(event.Content.Text)) > p.max-int64(p.text.Len()) {
		return fmt.Errorf("projected transcript exceeds %d-byte bound", p.max)
	}
	_, _ = p.text.WriteString(event.Content.Text)
	return nil
}

func (p *transcriptProjector) Finalize() (json.RawMessage, error) {
	return json.Marshal(struct {
		Transcript string `json:"transcript"`
	}{Transcript: p.text.String()})
}

func (*multipartE2EAdapter) ID() string { return "multipart-e2e" }
func (a *multipartE2EAdapter) PlanCompatibility(input kernel.CompatibilityContext) kernel.CompatibilityPlan {
	a.planCalls++
	report := make([]kernel.FacetMapping, 0, len(input.Policy.RequiredFacets))
	for _, facet := range input.Policy.RequiredFacets {
		report = append(report, kernel.FacetMapping{Facet: facet, Paths: []string{"fixture"}, Disposition: kernel.FacetPreserved})
	}
	report = append(report, kernel.FacetMapping{Facet: kernel.FacetWireResponse, Paths: []string{"fixture.response"}, Disposition: kernel.FacetPreserved})
	a.compatibility = kernel.ComposeCompatibilityPlan([][]kernel.FacetMapping{report}, input.Policy)
	return a.compatibility
}
func (a *multipartE2EAdapter) Prepare(ctx context.Context, request kernel.NormalizedRequest, _ kernel.Route, _ kernel.Credential) (kernel.UpstreamRequest, error) {
	a.prepareCalls++
	a.features = append([]normalize.FeatureRequirement(nil), request.Requirements...)
	if len(request.Artifacts) != 1 {
		a.prepareErr = fmt.Errorf("expected one operation artifact, got %d", len(request.Artifacts))
		return kernel.UpstreamRequest{}, a.prepareErr
	}
	lease, err := artifacts.OpenArtifactFromContext(ctx, request.Artifacts[0])
	if err != nil {
		a.prepareErr = err
		return kernel.UpstreamRequest{}, a.prepareErr
	}
	defer lease.Close()
	body, err := io.ReadAll(lease)
	if err == nil {
		a.received = string(body)
	} else {
		a.prepareErr = err
	}
	return kernel.UpstreamRequest{Method: http.MethodPost, URL: "test://multipart", Body: strings.NewReader(a.received)}, err
}
func (*multipartE2EAdapter) Execute(_ context.Context, request kernel.UpstreamRequest) (kernel.UpstreamResponse, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return kernel.UpstreamResponse{}, err
	}
	result, err := json.Marshal(map[string]string{"transcript": string(body)})
	if err != nil {
		return kernel.UpstreamResponse{}, err
	}
	return kernel.UpstreamResponse{Status: http.StatusOK, Body: io.NopCloser(bytes.NewReader(result))}, nil
}
func (*multipartE2EAdapter) ClassifyError(int, []byte) kernel.ErrorClass {
	return kernel.ErrorRetryable
}
func (*multipartE2EAdapter) RenderResponse(ctx context.Context, response kernel.UpstreamResponse, writer http.ResponseWriter, _ normalize.Format, hooks kernel.StreamHooks) error {
	defer response.Body.Close()
	resultBody, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	var result struct {
		Transcript string `json:"transcript"`
	}
	if err := json.Unmarshal(resultBody, &result); err != nil {
		return err
	}
	if hooks.ValidateEvent != nil {
		for _, event := range []kernel.ResponseEvent{{Kind: kernel.EventTextDelta, Text: result.Transcript}, {Kind: kernel.EventResponseComplete}} {
			if err := hooks.ValidateEvent(ctx, event); err != nil {
				return err
			}
			if hooks.OnEvent != nil {
				hooks.OnEvent(event)
			}
		}
	}
	_, err = writer.Write(resultBody)
	return err
}

func TestMultipartOperationExecutesThroughHTTPKernelAndAdapter(t *testing.T) {
	operationSnapshot, artifactType, featureRef := multipartTestOperationSnapshot(t)
	recipient := extensions.Ref{Kind: "provider-encoder", ID: "fixture.document-reader", ContractVersion: 1}
	runtimeSnapshot, err := kernel.BuildSnapshot(kernel.SnapshotInput{
		PublicModels: []kernel.PublicModel{{Name: "document-inspection", TargetRef: "document-inspection"}},
		Nodes:        []kernel.ModelNode{{ID: "document-inspection", Kind: kernel.ModelPhysical, Members: []kernel.MemberRef{{Kind: kernel.MemberRoute, ID: "fixture-route", Fidelity: kernel.FidelityExact}}}},
		Routes: []kernel.Route{{ID: "fixture-route", NodeID: "fixture-provider", DefinitionID: recipient.ID, DefinitionRef: recipient, ExternalModel: "document-reader", Protocol: kernel.Protocol("fixture.binary"), Enabled: true, Profile: kernel.CapabilityProfile{featureRef.ID: {State: kernel.SupportNative, Constraints: json.RawMessage(`{"kinds":["binary"]}`)}},
			OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{"fixture.inspect": {ContractVersion: 1, AdapterIDs: []string{"multipart-e2e"}}}}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := kernel.New(runtimeSnapshot, kernel.AlwaysOpenGate{}, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	gateway.Operations = operationSnapshot
	evaluations := 0
	gateway.Features = kernel.NewFeatureRegistry()
	if err := gateway.Features.Register(featureRef, binaryKindFeature{evaluations: &evaluations}); err != nil {
		t.Fatal(err)
	}
	adapter := &multipartE2EAdapter{}
	gateway.Adapters[adapter.ID()] = adapter
	bodyStore, err := artifacts.NewStore(t.TempDir(), artifacts.Limits{MaxBytes: 128, MaxBodyBytes: 64, DefaultTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer bodyStore.Close()
	gateway.ArtifactStore = bodyStore

	codec := MultipartOperationIngress{
		CodecID: "fixture-inspection-multipart", IngressContractVersion: 1, Operation: "fixture.inspect", OperationContractVersion: 1,
		Endpoints: []IngressRoute{{Method: http.MethodPost, Path: "/v1/fixture/inspect"}},
		Bindings:  []MultipartArtifactBinding{{Field: "source", Role: "source-document", TypeRef: artifactType, MaxBytes: 32}},
	}
	server := &Server{ingress: map[string]OperationIngress{}, operations: operationSnapshot}
	server.SetArtifactStore(bodyStore)
	server.SetExecutor(func(ctx context.Context, request normalize.Request, writer http.ResponseWriter) error {
		return gateway.Execute(ctx, request, kernel.Credential{}, writer)
	})
	if err := server.RegisterOperationIngress(codec); err != nil {
		t.Fatal(err)
	}

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	metadata, err := form.CreateFormField("request")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := metadata.Write([]byte(`{"model":"document-inspection","operationPayload":{}}`)); err != nil {
		t.Fatal(err)
	}
	file, err := form.CreateFormFile("source", "document.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("binary-input")); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/fixture/inspect", &body)
	request.Header.Set("Content-Type", form.FormDataContentType())
	response := httptest.NewRecorder()
	server.HandlerWithOptions(HandlerOptions{DataPlane: true}).ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != `{"transcript":"binary-input"}` || adapter.received != "binary-input" {
		t.Fatalf("multipart endpoint status=%d body=%q adapter-input=%q plan-calls=%d prepare-calls=%d prepare-error=%v compatibility=%#v", response.Code, response.Body.String(), adapter.received, adapter.planCalls, adapter.prepareCalls, adapter.prepareErr, adapter.compatibility)
	}
	if len(adapter.features) != 1 || adapter.features[0].Ref != featureRef || string(adapter.features[0].Constraints) != `{"kind":"binary"}` || evaluations != 1 {
		t.Fatalf("body-derived route requirement did not reach eligibility and provider: requirements=%#v evaluator-calls=%d", adapter.features, evaluations)
	}
}

func multipartTestOperationSnapshot(t *testing.T) (*operations.Snapshot, extensions.Ref, extensions.Ref) {
	t.Helper()
	catalog := extensions.NewCatalog()
	registry, err := operations.NewRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	operationRef := extensions.Ref{Kind: "operation", ID: "fixture.inspect", ContractVersion: 1}
	inputSchemaRef := extensions.Ref{Kind: extensions.SchemaKind, ID: "fixture.inspect.input", ContractVersion: 1}
	resultSchemaRef := extensions.Ref{Kind: extensions.SchemaKind, ID: "fixture.inspect.result", ContractVersion: 1}
	eventSchemaRef := extensions.Ref{Kind: extensions.SchemaKind, ID: "fixture.inspect.event", ContractVersion: 1}
	inputSchema, err := extensions.BindSchemaDocument(inputSchemaRef, json.RawMessage(`{"type":"object"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.RegisterSchema(inputSchemaRef, inputSchema); err != nil {
		t.Fatal(err)
	}
	resultSchema, err := extensions.BindSchemaDocument(resultSchemaRef, json.RawMessage(`{"type":"object","properties":{"transcript":{"type":"string"}},"required":["transcript"],"additionalProperties":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.RegisterSchema(resultSchemaRef, resultSchema); err != nil {
		t.Fatal(err)
	}
	eventSchema, err := extensions.BindSchemaDocument(eventSchemaRef, json.RawMessage(`{"type":"object","properties":{"kind":{"enum":["text_delta","response_complete"]},"sequence":{"type":"integer"},"itemId":{"type":"string"},"content":{"type":"object"}},"required":["kind","sequence","content"],"additionalProperties":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.RegisterSchema(eventSchemaRef, eventSchema); err != nil {
		t.Fatal(err)
	}
	artifactRef := extensions.Ref{Kind: extensions.ArtifactKind, ID: "fixture.document", ContractVersion: 1}
	recipientRef := extensions.Ref{Kind: "provider-encoder", ID: "fixture.document-reader", ContractVersion: 1}
	factsSchemaRef := extensions.Ref{Kind: extensions.SchemaKind, ID: "fixture.binary-facts", ContractVersion: 1}
	factsSchema, err := extensions.BindSchemaDocument(factsSchemaRef, json.RawMessage(`{"type":"object","properties":{"kind":{"const":"binary"}},"required":["kind"],"additionalProperties":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.RegisterSchema(factsSchemaRef, factsSchema); err != nil {
		t.Fatal(err)
	}
	interpreterRef := extensions.Ref{Kind: extensions.ArtifactInterpreterKind, ID: "fixture.binary-prefix", ContractVersion: 1}
	if err := catalog.Register(extensions.Descriptor{
		Ref: interpreterRef, ImplementationVersion: "1", DisplayName: "Binary prefix", Description: "Inspect a bounded six-byte media signature.",
		ResultSchemaRef: &factsSchemaRef, ResourceBounds: extensions.ResourceBounds{MaxInputBytes: 32, MaxOutputBytes: 64, MaxBufferedBytes: 64, DeadlineMillis: 500},
	}, func(json.RawMessage) (any, error) { return binaryPrefixInterpreter{}, nil }); err != nil {
		t.Fatal(err)
	}
	featureRef := extensions.Ref{Kind: kernel.FeatureEvaluatorKind, ID: "fixture.binary-input", ContractVersion: 1}
	if err := catalog.Register(extensions.Descriptor{Ref: recipientRef, ImplementationVersion: "1", DisplayName: "Document reader", Description: "Fixture consumer."}, func(json.RawMessage) (any, error) { return struct{}{}, nil }); err != nil {
		t.Fatal(err)
	}
	policy := extensions.ArtifactPolicy{
		OwnerDomains: []string{"client"}, ReplayScopes: []extensions.ArtifactReplayScope{extensions.ArtifactReplayRequest},
		SensitivityLimit: extensions.ArtifactPrivate, AllowedMediaTypes: []string{"application/octet-stream"}, RecipientContracts: []extensions.Ref{recipientRef, interpreterRef},
	}
	if err := catalog.Register(extensions.Descriptor{
		Ref: artifactRef, ImplementationVersion: "1", DisplayName: "Document body", Description: "Fixture binary input.",
		ArtifactPolicy: &policy, ResourceBounds: extensions.ResourceBounds{MaxInputBytes: 32},
	}, func(json.RawMessage) (any, error) { return struct{}{}, nil }); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(operations.Definition{
		Ref: operationRef, DisplayName: "Inspect fixture", Description: "Consumes one document body.",
		InputSchemaRef: inputSchemaRef, ResultSchemaRef: &resultSchemaRef, EventSchemaRefs: []extensions.Ref{eventSchemaRef},
		ArtifactInputs:       []operations.ArtifactInput{{Role: "source-document", TypeRef: artifactRef, MinCount: 1, MaxCount: 1}},
		ArtifactInterpreters: []extensions.ArtifactInterpreterBinding{{Role: "source-document", InterpreterRef: interpreterRef}},
		ResourceBounds:       operations.DefaultResourceBounds(), ReplaySafety: operations.ReplayNever,
		InputPayload:        func(normalize.Request) (json.RawMessage, error) { return json.RawMessage(`{}`), nil },
		CompileRequirements: func(normalize.Request) ([]normalize.FeatureRequirement, error) { return nil, nil },
		CompileArtifactRequirements: func(_ normalize.Request, facts []normalize.ArtifactFacts) ([]normalize.FeatureRequirement, error) {
			if len(facts) != 1 {
				return nil, io.ErrUnexpectedEOF
			}
			return []normalize.FeatureRequirement{{Ref: featureRef, Constraints: append(json.RawMessage(nil), facts[0].Value...)}}, nil
		},
		NewResultProjector: func(bounds extensions.ResourceBounds) (operations.ResultProjector, error) {
			return &transcriptProjector{max: bounds.MaxBufferedBytes}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	extensionSnapshot, err := catalog.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := registry.Seal(extensionSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot, artifactRef, featureRef
}
