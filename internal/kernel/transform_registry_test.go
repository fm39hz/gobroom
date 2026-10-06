package kernel

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/normalize"
)

type promptCompressionFixture struct{}

func (promptCompressionFixture) Definition() TransformDefinition {
	return TransformDefinition{ID: "fixture.prompt-compression.v1", Label: "Prompt compression", Description: "Replace verbose fixture text.", Stage: TransformBeforeRequirements, Order: 10, Effects: []TransformEffect{TransformInput}}
}
func (promptCompressionFixture) Apply(_ context.Context, request *NormalizedRequest) error {
	if len(request.Messages) > 0 {
		request.Messages[0].Content = "compressed-context"
	}
	return nil
}

type invalidIdentityTransform struct{}

func (invalidIdentityTransform) Definition() TransformDefinition {
	return TransformDefinition{ID: "fixture.invalid-identity.v1", Label: "Invalid identity", Description: "Must be rejected.", Stage: TransformBeforeRequirements, Effects: []TransformEffect{TransformOptions}}
}
func (invalidIdentityTransform) Apply(_ context.Context, request *NormalizedRequest) error {
	request.Operation = "vendor.other.operation"
	return nil
}

func TestRequestTransformRegistryRunsBeforeKernelRequirementsAndProviderEncoding(t *testing.T) {
	snapshot, err := BuildSnapshot(SnapshotInput{
		PublicModels: []PublicModel{{Name: "public", TargetRef: "physical"}},
		Nodes:        []ModelNode{{ID: "physical", Kind: ModelPhysical, Members: []MemberRef{{Kind: MemberRoute, ID: "route", Fidelity: FidelityExact}}}},
		Routes:       []Route{{ID: "route", Enabled: true, OperationBindings: map[normalize.Operation]RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"fixture"}}}}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	k, err := New(snapshot, nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	var attempted []string
	var encoded NormalizedRequest
	k.Adapters["fixture"] = featureRouteAdapter{id: "fixture", used: &attempted, seen: &encoded}
	if err := k.Transforms.Register(promptCompressionFixture{}); err != nil {
		t.Fatal(err)
	}
	request := NormalizedRequest{Model: "public", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, SourceFormat: normalize.FormatAnthropic, Messages: []Message{{Role: "user", Content: "very long fixture context"}}}
	if err := k.Execute(context.Background(), request, Credential{}, httptest.NewRecorder()); err != nil {
		t.Fatal(err)
	}
	if len(attempted) != 1 || encoded.Messages[0].Content != "compressed-context" {
		t.Fatalf("transform did not run before provider encoding: routes=%v request=%#v", attempted, encoded)
	}
}

func TestRequestTransformCannotChangeResolvedModelOrOperation(t *testing.T) {
	registry := NewRequestTransformRegistry()
	if err := registry.Register(invalidIdentityTransform{}); err != nil {
		t.Fatal(err)
	}
	request := NormalizedRequest{Model: "chosen-role", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1}
	err := registry.Apply(context.Background(), &request)
	if err == nil || !strings.Contains(err.Error(), "immutable request identity") {
		t.Fatalf("identity mutation error=%v", err)
	}
}
