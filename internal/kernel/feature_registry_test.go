package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/operations"
)

type structuredOutputFeature struct{}

func (structuredOutputFeature) Evaluate(capability Capability, constraintData json.RawMessage) (bool, string) {
	var requested struct {
		Format string `json:"format"`
	}
	var supported struct {
		Formats []string `json:"formats"`
	}
	if err := json.Unmarshal(constraintData, &requested); err != nil || requested.Format == "" {
		return false, "invalid structured-output requirement"
	}
	if capability.State != SupportNative {
		return false, "structured output is not native"
	}
	if err := json.Unmarshal(capability.Constraints, &supported); err != nil {
		return false, "invalid route structured-output evidence"
	}
	for _, format := range supported.Formats {
		if format == requested.Format {
			return true, ""
		}
	}
	return false, fmt.Sprintf("format %q is unavailable", requested.Format)
}

type rejectingFeature struct{}

func (rejectingFeature) Evaluate(Capability, json.RawMessage) (bool, string) {
	return false, "this evaluator contract rejects the route"
}

func TestFeatureEvaluatorsBindFromVersionedExtensionCatalog(t *testing.T) {
	catalog := extensions.NewCatalog()
	constraintShape := json.RawMessage(`{"type":"object","properties":{"format":{"type":"string","minLength":1}},"required":["format"],"additionalProperties":false}`)
	featureID := "vendor.example.output-schema"
	featureV1 := extensions.Ref{Kind: FeatureEvaluatorKind, ID: featureID, ContractVersion: 1}
	featureV2 := extensions.Ref{Kind: FeatureEvaluatorKind, ID: featureID, ContractVersion: 2}
	for version, evaluator := range map[extensions.Ref]FeatureEvaluator{featureV1: structuredOutputFeature{}, featureV2: rejectingFeature{}} {
		schemaRef := extensions.Ref{Kind: extensions.SchemaKind, ID: "feature.constraints." + featureID, ContractVersion: version.ContractVersion}
		document, err := extensions.BindSchemaDocument(schemaRef, constraintShape)
		if err != nil {
			t.Fatal(err)
		}
		if err := catalog.RegisterSchema(schemaRef, document); err != nil {
			t.Fatal(err)
		}
		if err := catalog.Register(extensions.Descriptor{
			Ref: version, ImplementationVersion: "test", DisplayName: "Output schema", Description: "Fixture feature evaluator.", InputSchemaRef: &schemaRef,
		}, func(json.RawMessage) (any, error) { return evaluator, nil }); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := catalog.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewFeatureRegistryFromCatalog(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	route := Route{Profile: CapabilityProfile{featureID: {State: SupportNative, Constraints: json.RawMessage(`{"formats":["json"]}`)}}}
	constraints := json.RawMessage(`{"format":"json"}`)
	if ok, reason := registry.Evaluate(route, []FeatureRequirement{{Ref: featureV1, Constraints: constraints}}); !ok {
		t.Fatalf("v1 evaluator failed: %s", reason)
	}
	if ok, reason := registry.Evaluate(route, []FeatureRequirement{{Ref: featureV2, Constraints: constraints}}); ok || reason == "" {
		t.Fatalf("v2 request silently resolved to another evaluator version: %v %q", ok, reason)
	}
	if ok, reason := registry.Evaluate(route, []FeatureRequirement{{Ref: featureV1, Constraints: json.RawMessage(`{"unexpected":true}`)}}); ok || reason == "" {
		t.Fatalf("catalog input schema did not validate evaluator constraints: %v %q", ok, reason)
	}
}

func TestNewFeatureExtensionNegotiatesTypedConstraintsWithoutKernelBranch(t *testing.T) {
	const featureID = "vendor.example.output-schema.v1"
	featureRef := extensions.Ref{Kind: FeatureEvaluatorKind, ID: featureID, ContractVersion: 1}
	registry := NewFeatureRegistry()
	if err := registry.Register(featureRef, structuredOutputFeature{}); err != nil {
		t.Fatal(err)
	}
	requirement := normalize.FeatureRequirement{Ref: featureRef, Constraints: json.RawMessage(`{"format":"strict-json"}`)}
	request := normalize.Request{Requirements: []normalize.FeatureRequirement{requirement}}
	compiled := CompileRequirements(request)
	if len(compiled.Features) != 1 || compiled.Features[0].Ref != featureRef {
		t.Fatalf("extension requirement was not preserved: %#v", compiled.Features)
	}
	route := Route{Profile: CapabilityProfile{featureID: {
		State: SupportNative, Constraints: json.RawMessage(`{"formats":["json","strict-json"]}`),
	}}}
	if eligible, reason := registry.Evaluate(route, compiled.Features); !eligible {
		t.Fatalf("registered feature evaluator rejected supported route: %s", reason)
	}
	route.Profile[featureID] = Capability{State: SupportNative, Constraints: json.RawMessage(`{"formats":["json"]}`)}
	if eligible, reason := registry.Evaluate(route, compiled.Features); eligible || reason == "" {
		t.Fatalf("route missing requested feature constraint was admitted: %v %q", eligible, reason)
	}
}

func TestFeatureRegistryFailsClosedForUnknownExtension(t *testing.T) {
	const featureID = "future.audio.transcription.v1"
	registry := NewFeatureRegistry()
	ref := extensions.Ref{Kind: FeatureEvaluatorKind, ID: featureID, ContractVersion: 1}
	eligible, reason := registry.Evaluate(Route{Profile: CapabilityProfile{featureID: {State: SupportNative}}}, []FeatureRequirement{{Ref: ref}})
	if eligible || reason == "" {
		t.Fatalf("unregistered feature evaluator must fail closed: %v %q", eligible, reason)
	}
}

type featureRouteAdapter struct {
	id   string
	used *[]string
	seen *NormalizedRequest
}

func (a featureRouteAdapter) ID() string { return a.id }
func (a featureRouteAdapter) PlanCompatibility(input CompatibilityContext) CompatibilityPlan {
	return fixtureCompatibilityPlan(input, normalize.FormatAnthropic, FidelityNative)
}
func (a featureRouteAdapter) Prepare(_ context.Context, request NormalizedRequest, route Route, _ Credential) (UpstreamRequest, error) {
	*a.used = append(*a.used, route.ID)
	if a.seen != nil {
		*a.seen = request
	}
	return UpstreamRequest{Method: http.MethodPost, URL: "https://provider.test/generate"}, nil
}
func (a featureRouteAdapter) Execute(context.Context, UpstreamRequest) (UpstreamResponse, error) {
	return UpstreamResponse{Status: http.StatusOK, Headers: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
}
func (featureRouteAdapter) ClassifyError(int, []byte) ErrorClass { return ErrorRetryable }
func (featureRouteAdapter) RenderResponse(_ context.Context, _ UpstreamResponse, writer http.ResponseWriter, _ normalize.Format, hooks StreamHooks) error {
	if hooks.OnFirstByte != nil {
		hooks.OnFirstByte(time.Now())
	}
	_, err := io.WriteString(writer, "ok")
	if err == nil && hooks.OnComplete != nil {
		hooks.OnComplete(UsageEvent{Status: "ok"})
	}
	return err
}

func TestKernelRunsOnlyRouteWhoseRegisteredFeatureEvaluatorAcceptsRequest(t *testing.T) {
	const featureID = "vendor.example.output-schema.v1"
	featureRef := extensions.Ref{Kind: FeatureEvaluatorKind, ID: featureID, ContractVersion: 1}
	registry := NewFeatureRegistry()
	if err := registry.Register(featureRef, structuredOutputFeature{}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := BuildSnapshot(SnapshotInput{
		PublicModels: []PublicModel{{Name: "public", TargetRef: "physical"}},
		Nodes:        []ModelNode{{ID: "physical", Kind: ModelPhysical, Strategy: StrategyFallback, Members: []MemberRef{{Kind: MemberRoute, ID: "route-wrong", Fidelity: FidelityExact}, {Kind: MemberRoute, ID: "route-right", Fidelity: FidelityExact}}}},
		Routes: []Route{
			{ID: "route-wrong", OperationBindings: map[normalize.Operation]RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"wrong"}}}, Protocol: Protocol("vendor.protocol.one"), Enabled: true, Profile: CapabilityProfile{featureID: {State: SupportNative, Constraints: json.RawMessage(`{"formats":["json"]}`)}}},
			{ID: "route-right", OperationBindings: map[normalize.Operation]RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"right"}}}, Protocol: Protocol("vendor.protocol.two"), Enabled: true, Profile: CapabilityProfile{featureID: {State: SupportNative, Constraints: json.RawMessage(`{"formats":["strict-json"]}`)}}},
		},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	k, err := New(snapshot, nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	k.Features = registry
	var used []string
	k.Adapters["wrong"] = featureRouteAdapter{id: "wrong", used: &used}
	k.Adapters["right"] = featureRouteAdapter{id: "right", used: &used}
	request := NormalizedRequest{Model: "public", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, SourceFormat: normalize.FormatAnthropic, Requirements: []normalize.FeatureRequirement{{Ref: featureRef, Constraints: json.RawMessage(`{"format":"strict-json"}`)}}}
	writer := httptest.NewRecorder()
	if err := k.Execute(context.Background(), request, Credential{}, writer); err != nil {
		t.Fatal(err)
	}
	if len(used) != 1 || used[0] != "route-right" {
		t.Fatalf("attempted routes=%v; want only route-right", used)
	}
	if writer.Body.String() != "ok" {
		t.Fatalf("response=%q", writer.Body.String())
	}
}

func TestKernelRoutesNewOperationUsingGenericRouteTaskContract(t *testing.T) {
	const operation normalize.Operation = "audio.transcribe.v1"
	operationCatalog := extensions.NewCatalog()
	operationRegistry, err := operations.NewRegistry(operationCatalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := operations.RegisterChatGenerate(operationCatalog, operationRegistry); err != nil {
		t.Fatal(err)
	}
	if err := operationRegistry.RegisterRawPayload(extensions.Ref{Kind: "operation", ID: string(operation), ContractVersion: 1}, "Transcribe audio", "Audio payload contract", json.RawMessage(`{"type":"object","properties":{"audio":{"type":"string","minLength":1}},"required":["audio"],"additionalProperties":false}`), operations.ReplayNever); err != nil {
		t.Fatal(err)
	}
	extensionSnapshot, err := operationCatalog.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	operationSnapshot, err := operationRegistry.Seal(extensionSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := BuildSnapshot(SnapshotInput{
		PublicModels: []PublicModel{{Name: "transcriber", TargetRef: "physical"}},
		Nodes: []ModelNode{{ID: "physical", Kind: ModelPhysical, Strategy: StrategyFallback, Members: []MemberRef{
			{Kind: MemberRoute, ID: "chat-route", Fidelity: FidelityExact},
			{Kind: MemberRoute, ID: "audio-route", Fidelity: FidelityExact},
		}}},
		Routes: []Route{
			{ID: "chat-route", Protocol: Protocol("vendor.chat.v9"), OperationBindings: map[normalize.Operation]RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"chat"}}}, Enabled: true},
			{ID: "audio-route", Protocol: Protocol("vendor.audio.v1"), OperationBindings: map[normalize.Operation]RouteOperationBinding{operation: {ContractVersion: 1, AdapterIDs: []string{"audio"}}}, Enabled: true},
		},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	k, err := New(snapshot, nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	k.Operations = operationSnapshot
	var attempted []string
	k.Adapters["chat"] = featureRouteAdapter{id: "chat", used: &attempted}
	k.Adapters["audio"] = featureRouteAdapter{id: "audio", used: &attempted}
	writer := httptest.NewRecorder()
	request := NormalizedRequest{Model: "transcriber", Operation: operation, OperationContractVersion: 1, OperationPayload: json.RawMessage(`{"audio":"sample"}`), SourceFormat: normalize.FormatAnthropic}
	if err := k.Execute(context.Background(), request, Credential{}, writer); err != nil {
		t.Fatal(err)
	}
	if len(attempted) != 1 || attempted[0] != "audio-route" || writer.Body.String() != "ok" {
		t.Fatalf("attempted=%v response=%q", attempted, writer.Body.String())
	}
	attempted = nil
	invalid := request
	invalid.OperationPayload = json.RawMessage(`{"notAudio":"sample"}`)
	if err := k.Execute(context.Background(), invalid, Credential{}, httptest.NewRecorder()); err == nil {
		t.Fatal("payload that violates the registered operation schema was accepted")
	}
	if len(attempted) != 0 {
		t.Fatalf("invalid operation payload reached routes: %v", attempted)
	}
	wrongVersion := request
	wrongVersion.OperationContractVersion = 2
	if err := k.Execute(context.Background(), wrongVersion, Credential{}, httptest.NewRecorder()); err == nil {
		t.Fatal("unregistered operation version reached routing")
	}
}
