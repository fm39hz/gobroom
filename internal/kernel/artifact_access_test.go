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
	received string
	planned  *CompatibilityContext
}

func (a *artifactConsumerAdapter) ID() string { return "artifact-consumer" }
func (a *artifactConsumerAdapter) PlanCompatibility(input CompatibilityContext) CompatibilityPlan {
	if a.planned != nil {
		*a.planned = input
	}
	return fixtureCompatibilityPlan(input, normalize.FormatOpenAIChat, FidelityNative)
}
func (a *artifactConsumerAdapter) Prepare(ctx context.Context, request NormalizedRequest, _ Route, _ Credential) (UpstreamRequest, error) {
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
func (*artifactConsumerAdapter) Execute(context.Context, UpstreamRequest) (UpstreamResponse, error) {
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
	policy := extensions.ArtifactPolicy{
		OwnerDomains: []string{"client"}, ReplayScopes: []extensions.ArtifactReplayScope{extensions.ArtifactReplayRequest},
		SensitivityLimit: extensions.ArtifactPrivate, AllowedMediaTypes: []string{"application/octet-stream"}, RecipientContracts: []extensions.Ref{providerRef},
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
		InputPayload:        func(normalize.Request) (json.RawMessage, error) { return json.RawMessage(`{}`), nil },
		CompileRequirements: func(normalize.Request) ([]normalize.FeatureRequirement, error) { return nil, nil },
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
		Routes: []Route{{ID: "route", NodeID: "provider", DefinitionID: providerRef.ID, DefinitionRef: providerRef, ExternalModel: "upstream-model", Protocol: ProtocolOpenAIChat, Enabled: true,
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
	bodyRef, err := spool.Put(context.Background(), owner, "application/octet-stream", strings.NewReader("private-document"), 64, time.Minute, true)
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
	if adapter.received != "private-document" || response.Body.String() != "ok" {
		t.Fatalf("adapter body=%q response=%q", adapter.received, response.Body.String())
	}
	if len(planned.ArtifactTransfers) != 1 {
		t.Fatalf("compatibility plan omitted authorized artifact transfer: %#v", planned.ArtifactTransfers)
	}
	transfer := planned.ArtifactTransfers[0]
	if transfer.TypeRef != artifactRef || transfer.Role != "source-document" || transfer.OperationRef != operationRef || transfer.SourceDomain != "client" || transfer.TargetProvider != providerRef || transfer.Sensitivity != extensions.ArtifactPrivate || transfer.ReplayScope != extensions.ArtifactReplayRequest || transfer.MediaType != "application/octet-stream" || transfer.SizeBytes != int64(len("private-document")) || !transfer.BodyLease {
		t.Fatalf("artifact transfer plan=%#v", transfer)
	}
	if err := spool.Release(owner, bodyRef); err != nil {
		t.Fatal(err)
	}
}
