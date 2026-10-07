package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/operations"
)

type operationEventSchemaAdapter struct {
	validationCalls int
	events          []ResponseEvent
}

func (*operationEventSchemaAdapter) ID() string { return "operation-event-schema" }
func (*operationEventSchemaAdapter) PlanCompatibility(input CompatibilityContext) CompatibilityPlan {
	return fixtureCompatibilityPlan(input, normalize.FormatOpenAIChat, FidelityNative)
}
func (*operationEventSchemaAdapter) Prepare(context.Context, NormalizedRequest, Route, Credential) (UpstreamRequest, error) {
	return UpstreamRequest{Method: http.MethodPost, URL: "test://event-schema"}, nil
}
func (*operationEventSchemaAdapter) Execute(context.Context, UpstreamRequest) (UpstreamResponse, error) {
	return UpstreamResponse{Status: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok"))}, nil
}
func (*operationEventSchemaAdapter) ClassifyError(int, []byte) ErrorClass { return ErrorRetryable }
func (a *operationEventSchemaAdapter) RenderResponse(ctx context.Context, _ UpstreamResponse, _ http.ResponseWriter, _ normalize.Format, hooks StreamHooks) error {
	if hooks.ValidateEvent == nil {
		return errors.New("kernel did not bind the operation event validator")
	}
	events := a.events
	if len(events) == 0 {
		events = []ResponseEvent{{Kind: EventToolCallDelta, ToolCallID: "call-1", ToolName: "lookup"}}
	}
	for _, event := range events {
		a.validationCalls++
		if err := hooks.ValidateEvent(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

type invalidTranscriptProjector struct{}

func (invalidTranscriptProjector) ConsumeEvent(json.RawMessage) error { return nil }
func (invalidTranscriptProjector) Finalize() (json.RawMessage, error) {
	return json.RawMessage(`{"wrong":"shape"}`), nil
}

func TestKernelEnforcesExactOperationEventSchemaAtAdapterBoundary(t *testing.T) {
	catalog := extensions.NewCatalog()
	operationRegistry, err := operations.NewRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	operationRef := extensions.Ref{Kind: "operation", ID: "fixture.text-only", ContractVersion: 1}
	inputSchemaRef := extensions.Ref{Kind: extensions.SchemaKind, ID: "fixture.text-only.input", ContractVersion: 1}
	eventSchemaRef := extensions.Ref{Kind: extensions.SchemaKind, ID: "fixture.text-only.event", ContractVersion: 1}
	for _, item := range []struct {
		ref   extensions.Ref
		shape json.RawMessage
	}{
		{ref: inputSchemaRef, shape: json.RawMessage(`{"type":"object"}`)},
		{ref: eventSchemaRef, shape: json.RawMessage(`{"type":"object","properties":{"kind":{"enum":["text_delta","response_complete"]},"sequence":{"type":"integer"},"itemId":{"type":"string"},"content":{"type":"object"}},"required":["kind","sequence","content"],"additionalProperties":false}`)},
	} {
		document, err := extensions.BindSchemaDocument(item.ref, item.shape)
		if err != nil {
			t.Fatal(err)
		}
		if err := catalog.RegisterSchema(item.ref, document); err != nil {
			t.Fatal(err)
		}
	}
	if err := operationRegistry.Register(operations.Definition{
		Ref: operationRef, DisplayName: "Text-only fixture", Description: "Rejects tool-call events.",
		InputSchemaRef: inputSchemaRef, EventSchemaRefs: []extensions.Ref{eventSchemaRef},
		ResourceBounds: operations.DefaultResourceBounds(), ReplaySafety: operations.ReplayOnRejection,
		InputPayload:        func(normalize.Request) (json.RawMessage, error) { return json.RawMessage(`{}`), nil },
		CompileRequirements: func(normalize.Request) ([]normalize.FeatureRequirement, error) { return nil, nil },
	}); err != nil {
		t.Fatal(err)
	}
	extensionsSnapshot, err := catalog.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	operationSnapshot, err := operationRegistry.Seal(extensionsSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	runtimeSnapshot, err := BuildSnapshot(SnapshotInput{
		PublicModels: []PublicModel{{Name: "text-only", TargetRef: "text-only"}},
		Nodes:        []ModelNode{{ID: "text-only", Kind: ModelPhysical, Members: []MemberRef{{Kind: MemberRoute, ID: "route", Fidelity: FidelityExact}}}},
		Routes: []Route{{ID: "route", Protocol: ProtocolOpenAIChat, Enabled: true, OperationBindings: map[normalize.Operation]RouteOperationBinding{
			normalize.Operation(operationRef.ID): {ContractVersion: operationRef.ContractVersion, AdapterIDs: []string{"operation-event-schema"}},
		}}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := New(runtimeSnapshot, AlwaysOpenGate{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	gateway.Operations = operationSnapshot
	adapter := &operationEventSchemaAdapter{}
	gateway.Adapters[adapter.ID()] = adapter
	err = gateway.Execute(context.Background(), NormalizedRequest{
		Model: "text-only", Operation: normalize.Operation(operationRef.ID), OperationContractVersion: operationRef.ContractVersion,
		OperationPayload: json.RawMessage(`{}`), SourceFormat: normalize.FormatOpenAIChat,
	}, Credential{}, httptest.NewRecorder())
	if err == nil || !strings.Contains(err.Error(), "violates every declared event schema") || adapter.validationCalls != 1 {
		t.Fatalf("invalid operation event was not rejected: err=%v validation-calls=%d", err, adapter.validationCalls)
	}
}

func TestKernelValidatesProjectedOperationResultAtCompleteEvent(t *testing.T) {
	catalog := extensions.NewCatalog()
	operationRegistry, err := operations.NewRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	operationRef := extensions.Ref{Kind: "operation", ID: "fixture.transcript", ContractVersion: 1}
	inputSchemaRef := extensions.Ref{Kind: extensions.SchemaKind, ID: "fixture.transcript.input", ContractVersion: 1}
	eventSchemaRef := extensions.Ref{Kind: extensions.SchemaKind, ID: "fixture.transcript.event", ContractVersion: 1}
	resultSchemaRef := extensions.Ref{Kind: extensions.SchemaKind, ID: "fixture.transcript.result", ContractVersion: 1}
	for _, item := range []struct {
		ref   extensions.Ref
		shape json.RawMessage
	}{
		{inputSchemaRef, json.RawMessage(`{"type":"object"}`)},
		{eventSchemaRef, json.RawMessage(`{"type":"object","properties":{"kind":{"enum":["text_delta","response_complete"]},"sequence":{"type":"integer"},"itemId":{"type":"string"},"content":{"type":"object"}},"required":["kind","sequence","content"],"additionalProperties":false}`)},
		{resultSchemaRef, json.RawMessage(`{"type":"object","properties":{"transcript":{"type":"string"}},"required":["transcript"],"additionalProperties":false}`)},
	} {
		document, err := extensions.BindSchemaDocument(item.ref, item.shape)
		if err != nil {
			t.Fatal(err)
		}
		if err := catalog.RegisterSchema(item.ref, document); err != nil {
			t.Fatal(err)
		}
	}
	if err := operationRegistry.Register(operations.Definition{
		Ref: operationRef, DisplayName: "Transcript", Description: "Validates the final operation result.",
		InputSchemaRef: inputSchemaRef, ResultSchemaRef: &resultSchemaRef, EventSchemaRefs: []extensions.Ref{eventSchemaRef},
		ResourceBounds: operations.DefaultResourceBounds(), ReplaySafety: operations.ReplayOnRejection,
		InputPayload:        func(normalize.Request) (json.RawMessage, error) { return json.RawMessage(`{}`), nil },
		CompileRequirements: func(normalize.Request) ([]normalize.FeatureRequirement, error) { return nil, nil },
		NewResultProjector: func(extensions.ResourceBounds) (operations.ResultProjector, error) {
			return invalidTranscriptProjector{}, nil
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
	runtimeSnapshot, err := BuildSnapshot(SnapshotInput{
		PublicModels: []PublicModel{{Name: "transcript", TargetRef: "transcript"}},
		Nodes:        []ModelNode{{ID: "transcript", Kind: ModelPhysical, Members: []MemberRef{{Kind: MemberRoute, ID: "route", Fidelity: FidelityExact}}}},
		Routes: []Route{{ID: "route", Protocol: ProtocolOpenAIChat, Enabled: true, OperationBindings: map[normalize.Operation]RouteOperationBinding{
			normalize.Operation(operationRef.ID): {ContractVersion: operationRef.ContractVersion, AdapterIDs: []string{"operation-event-schema"}},
		}}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := New(runtimeSnapshot, AlwaysOpenGate{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	gateway.Operations = operationSnapshot
	adapter := &operationEventSchemaAdapter{events: []ResponseEvent{{Kind: EventTextDelta, Text: "decoded"}, {Kind: EventResponseComplete}}}
	gateway.Adapters[adapter.ID()] = adapter
	err = gateway.Execute(context.Background(), NormalizedRequest{
		Model: "transcript", Operation: normalize.Operation(operationRef.ID), OperationContractVersion: operationRef.ContractVersion,
		OperationPayload: json.RawMessage(`{}`), SourceFormat: normalize.FormatOpenAIChat,
	}, Credential{}, httptest.NewRecorder())
	if err == nil || !strings.Contains(err.Error(), "result violates schema") || adapter.validationCalls != 2 {
		t.Fatalf("invalid projected result was not rejected at completion: err=%v validation-calls=%d", err, adapter.validationCalls)
	}
}
