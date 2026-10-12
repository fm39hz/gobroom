package kernel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/artifacts"
	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/operations"
)

type artifactConsumerAdapter struct {
	received     string
	features     []normalize.FeatureRequirement
	factsVisible bool
	planned      *CompatibilityContext
	plans        int
	executions   int
}

type pdfHeaderInterpreter struct{ inspections *int }

func (i pdfHeaderInterpreter) InspectArtifact(_ context.Context, _ extensions.ArtifactRef, source io.Reader) (json.RawMessage, error) {
	*i.inspections++
	header := make([]byte, 5)
	if _, err := io.ReadFull(source, header); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]string{"header": string(header)})
}

type pdfHeaderFeature struct{}

func (pdfHeaderFeature) Evaluate(capability Capability, requested json.RawMessage) (bool, string) {
	var want struct {
		Header string `json:"header"`
	}
	var have struct {
		Headers []string `json:"headers"`
	}
	if json.Unmarshal(requested, &want) != nil || json.Unmarshal(capability.Constraints, &have) != nil {
		return false, "invalid PDF-header feature evidence"
	}
	if capability.State != SupportNative {
		return false, "PDF-header inspection is not native"
	}
	for _, header := range have.Headers {
		if header == want.Header {
			return true, ""
		}
	}
	return false, "required document header is unsupported"
}

func (a *artifactConsumerAdapter) ID() string { return "artifact-consumer" }
func (a *artifactConsumerAdapter) PlanCompatibility(input CompatibilityContext) CompatibilityPlan {
	a.plans++
	if a.planned != nil {
		*a.planned = input
	}
	return fixtureCompatibilityPlan(input, normalize.FormatOpenAIChat, FidelityNative)
}
func (a *artifactConsumerAdapter) Prepare(ctx context.Context, request NormalizedRequest, _ Route, _ Credential) (UpstreamRequest, error) {
	a.features = append([]normalize.FeatureRequirement(nil), request.Requirements...)
	a.factsVisible = len(request.ArtifactFacts) != 0
	if len(request.Artifacts) != 1 {
		return UpstreamRequest{}, io.ErrUnexpectedEOF
	}
	lease, err := artifacts.OpenArtifactFromContext(ctx, request.Artifacts[0])
	if err != nil {
		return UpstreamRequest{}, err
	}
	defer lease.Close()
	body, err := io.ReadAll(lease)
	if err != nil {
		return UpstreamRequest{}, err
	}
	a.received = string(body)
	return UpstreamRequest{Method: http.MethodPost, URL: "test://artifact"}, nil
}
func (a *artifactConsumerAdapter) Execute(context.Context, UpstreamRequest) (UpstreamResponse, error) {
	a.executions++
	return UpstreamResponse{Status: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok"))}, nil
}
func (*artifactConsumerAdapter) ClassifyError(int, []byte) ErrorClass { return ErrorRetryable }
func (*artifactConsumerAdapter) RenderResponse(_ context.Context, response UpstreamResponse, writer http.ResponseWriter, _ normalize.Format, _ StreamHooks) error {
	defer response.Body.Close()
	_, err := io.Copy(writer, response.Body)
	return err
}

func TestKernelGrantsMultipartBodyOnlyToDeclaredOperationRecipient(t *testing.T) {
	catalog := extensions.NewCatalog()
	operationRegistry, err := operations.NewRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	operationRef := extensions.Ref{Kind: "operation", ID: "fixture.inspect", ContractVersion: 1}
	inputSchemaRef := extensions.Ref{Kind: extensions.SchemaKind, ID: "fixture.inspect.input", ContractVersion: 1}
	inputSchema, err := extensions.BindSchemaDocument(inputSchemaRef, json.RawMessage(`{"type":"object"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.RegisterSchema(inputSchemaRef, inputSchema); err != nil {
		t.Fatal(err)
	}
	providerRef := extensions.Ref{Kind: "provider-definition", ID: "fixture.provider", ContractVersion: 1}
	if err := catalog.Register(extensions.Descriptor{Ref: providerRef, ImplementationVersion: "1", DisplayName: "Fixture provider", Description: "Artifact recipient fixture."}, func(json.RawMessage) (any, error) { return struct{}{}, nil }); err != nil {
		t.Fatal(err)
	}
	artifactRef := extensions.Ref{Kind: extensions.ArtifactKind, ID: "fixture.document", ContractVersion: 1}
	factsSchemaRef := extensions.Ref{Kind: extensions.SchemaKind, ID: "fixture.document-facts", ContractVersion: 1}
	factsSchema, err := extensions.BindSchemaDocument(factsSchemaRef, json.RawMessage(`{"type":"object","properties":{"header":{"const":"%PDF-"}},"required":["header"],"additionalProperties":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.RegisterSchema(factsSchemaRef, factsSchema); err != nil {
		t.Fatal(err)
	}
	interpreterRef := extensions.Ref{Kind: extensions.ArtifactInterpreterKind, ID: "fixture.pdf-header", ContractVersion: 1}
	inspections := 0
	if err := catalog.Register(extensions.Descriptor{
		Ref: interpreterRef, ImplementationVersion: "1", DisplayName: "PDF header", Description: "Inspect only the bounded PDF magic header.",
		ResultSchemaRef: &factsSchemaRef, ResourceBounds: extensions.ResourceBounds{MaxInputBytes: 64, MaxOutputBytes: 128, MaxBufferedBytes: 128, DeadlineMillis: 1000},
	}, func(json.RawMessage) (any, error) { return pdfHeaderInterpreter{inspections: &inspections}, nil }); err != nil {
		t.Fatal(err)
	}
	featureRef := extensions.Ref{Kind: FeatureEvaluatorKind, ID: "fixture.document-header", ContractVersion: 1}
	policy := extensions.ArtifactPolicy{
		OwnerDomains: []string{"client"}, ReplayScopes: []extensions.ArtifactReplayScope{extensions.ArtifactReplayRequest, extensions.ArtifactReplayPortable},
		SensitivityLimit: extensions.ArtifactPrivate, AllowedMediaTypes: []string{"application/octet-stream"}, RecipientContracts: []extensions.Ref{providerRef, interpreterRef},
	}
	if err := catalog.Register(extensions.Descriptor{
		Ref: artifactRef, ImplementationVersion: "1", DisplayName: "Document", Description: "Fixture binary body.",
		ArtifactPolicy: &policy, ResourceBounds: extensions.ResourceBounds{MaxInputBytes: 64},
	}, func(json.RawMessage) (any, error) { return struct{}{}, nil }); err != nil {
		t.Fatal(err)
	}
	if err := operationRegistry.Register(operations.Definition{
		Ref: operationRef, DisplayName: "Inspect document", Description: "Consumes one scoped document.",
		InputSchemaRef: inputSchemaRef, ArtifactInputs: []operations.ArtifactInput{{Role: "source-document", TypeRef: artifactRef, MinCount: 1, MaxCount: 1}},
		ResourceBounds: operations.DefaultResourceBounds(), ReplaySafety: operations.ReplayNever,
		InputPayload:         func(normalize.Request) (json.RawMessage, error) { return json.RawMessage(`{}`), nil },
		CompileRequirements:  func(normalize.Request) ([]normalize.FeatureRequirement, error) { return nil, nil },
		ArtifactInterpreters: []extensions.ArtifactInterpreterBinding{{Role: "source-document", InterpreterRef: interpreterRef}},
		CompileArtifactRequirements: func(_ normalize.Request, facts []normalize.ArtifactFacts) ([]normalize.FeatureRequirement, error) {
			if len(facts) != 1 {
				return nil, io.ErrUnexpectedEOF
			}
			return []normalize.FeatureRequirement{{Ref: featureRef, Constraints: append(json.RawMessage(nil), facts[0].Value...)}}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	extensionSnapshot, err := catalog.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	operationSnapshot, err := operationRegistry.Seal(extensionSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := BuildSnapshot(SnapshotInput{
		PublicModels: []PublicModel{{Name: "fixture-model", TargetRef: "fixture-model"}},
		Nodes:        []ModelNode{{ID: "fixture-model", Kind: ModelPhysical, Members: []MemberRef{{Kind: MemberRoute, ID: "route", Fidelity: FidelityExact}}}},
		Routes: []Route{{ID: "route", NodeID: "provider", DefinitionID: providerRef.ID, DefinitionRef: providerRef, ExternalModel: "upstream-model", Protocol: ProtocolOpenAIChat, Enabled: true, Profile: CapabilityProfile{featureRef.ID: {State: SupportNative, Constraints: json.RawMessage(`{"headers":["%PDF-"]}`)}},
			OperationBindings: map[normalize.Operation]RouteOperationBinding{normalize.Operation(operationRef.ID): {ContractVersion: operationRef.ContractVersion, AdapterIDs: []string{"artifact-consumer"}}}}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := New(snapshot, AlwaysOpenGate{}, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	gateway.Operations = operationSnapshot
	gateway.Features = NewFeatureRegistry()
	if err := gateway.Features.Register(featureRef, pdfHeaderFeature{}); err != nil {
		t.Fatal(err)
	}
	spool, err := artifacts.NewStore(t.TempDir(), artifacts.Limits{MaxBytes: 128, MaxBodyBytes: 64, DefaultTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	gateway.ArtifactStore = spool
	var planned CompatibilityContext
	adapter := &artifactConsumerAdapter{planned: &planned}
	gateway.Adapters[adapter.ID()] = adapter
	owner := extensions.ArtifactOwner{Domain: "client", ClientContract: "fixture-multipart"}
	bodyData := "%PDF-private-document"
	bodyRef, err := spool.Put(context.Background(), owner, "application/octet-stream", strings.NewReader(bodyData), 64, time.Minute, true)
	if err != nil {
		t.Fatal(err)
	}
	request := NormalizedRequest{
		Model: "fixture-model", Operation: normalize.Operation(operationRef.ID), OperationContractVersion: operationRef.ContractVersion,
		SourceFormat: normalize.FormatOpenAIChat, OperationPayload: json.RawMessage(`{}`),
		Artifacts: []extensions.ArtifactRef{{TypeRef: artifactRef, Role: "source-document", Owner: owner, ReplayScope: extensions.ArtifactReplayRequest, Sensitivity: extensions.ArtifactPrivate, MediaType: bodyRef.MediaType, SizeBytes: bodyRef.SizeBytes, SHA256: bodyRef.SHA256, ExpiresAt: bodyRef.ExpiresAt, Body: &bodyRef}},
	}
	response := httptest.NewRecorder()
	if err := gateway.Execute(context.Background(), request, Credential{}, response); err != nil {
		t.Fatal(err)
	}
	if adapter.received != bodyData || response.Body.String() != "ok" || adapter.factsVisible {
		t.Fatalf("adapter body=%q response=%q derived facts visible=%v", adapter.received, response.Body.String(), adapter.factsVisible)
	}
	if len(adapter.features) != 1 || adapter.features[0].Ref != featureRef || string(adapter.features[0].Constraints) != `{"header":"%PDF-"}` || inspections != 1 {
		t.Fatalf("artifact-derived route requirement missing or body was rescanned: requirements=%#v inspections=%d", adapter.features, inspections)
	}
	if adapter.plans != 1 || adapter.executions != 1 {
		t.Fatalf("valid artifact route plans=%d executions=%d, want one each", adapter.plans, adapter.executions)
	}
	if len(planned.ArtifactTransfers) != 1 {
		t.Fatalf("compatibility plan omitted authorized artifact transfer: %#v", planned.ArtifactTransfers)
	}
	transfer := planned.ArtifactTransfers[0]
	if transfer.TypeRef != artifactRef || transfer.Role != "source-document" || transfer.OperationRef != operationRef || transfer.SourceDomain != "client" || transfer.TargetProvider != providerRef || transfer.Sensitivity != extensions.ArtifactPrivate || transfer.ReplayScope != extensions.ArtifactReplayRequest || transfer.MediaType != "application/octet-stream" || transfer.SizeBytes != int64(len(bodyData)) || !transfer.BodyLease {
		t.Fatalf("artifact transfer plan=%#v", transfer)
	}
	portableRequest := request
	portableRequest.Artifacts = append([]extensions.ArtifactRef(nil), request.Artifacts...)
	portableRequest.Artifacts[0].ReplayScope = extensions.ArtifactReplayPortable
	if err := gateway.Execute(context.Background(), portableRequest, Credential{}, httptest.NewRecorder()); err != nil {
		t.Fatalf("authorized portable artifact transfer failed: %v", err)
	}
	if adapter.received != bodyData || adapter.plans != 2 || adapter.executions != 2 || inspections != 2 {
		t.Fatalf("portable body was not consumed by the declared provider: body=%q plans=%d executions=%d", adapter.received, adapter.plans, adapter.executions)
	}
	if len(planned.ArtifactTransfers) != 1 || planned.ArtifactTransfers[0].ReplayScope != extensions.ArtifactReplayPortable || planned.ArtifactTransfers[0].TargetProvider != providerRef {
		t.Fatalf("portable transfer authorization was not represented in the plan: %#v", planned.ArtifactTransfers)
	}
	expiredRequest := request
	expiredRequest.Artifacts = append([]extensions.ArtifactRef(nil), request.Artifacts...)
	expiredAt := time.Now().Add(-time.Second)
	expiredRequest.Artifacts[0].ExpiresAt = expiredAt
	expiredBody := *expiredRequest.Artifacts[0].Body
	expiredBody.ExpiresAt = expiredAt
	expiredRequest.Artifacts[0].Body = &expiredBody
	if err := gateway.Execute(context.Background(), expiredRequest, Credential{}, httptest.NewRecorder()); err == nil {
		t.Fatal("expired artifact was accepted")
	}
	if adapter.plans != 2 || adapter.executions != 2 || inspections != 2 {
		t.Fatalf("expired artifact reached inspection/compatibility/dispatch: inspections=%d plans=%d executions=%d", inspections, adapter.plans, adapter.executions)
	}
	if err := spool.Release(owner, bodyRef); err != nil {
		t.Fatal(err)
	}
}
