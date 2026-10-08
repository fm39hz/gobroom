package kernel

import (
	"context"
	"reflect"
	"testing"

	"github.com/fm39hz/gobroom/internal/normalize"
)

func TestExplainCompatibilityUsesModelPathAndDoesNotDispatch(t *testing.T) {
	bindings := map[normalize.Operation]RouteOperationBinding{
		normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"fixture"}},
	}
	snapshot, err := BuildSnapshot(SnapshotInput{
		PublicModels: []PublicModel{{Name: "junior", TargetRef: "combo-junior"}},
		Routes:       []Route{{ID: "route-a", Enabled: true, Protocol: ProtocolOpenAIChat, OperationBindings: bindings}},
		Nodes: []ModelNode{
			{ID: "physical-qwen", Kind: ModelPhysical, Members: []MemberRef{{Kind: MemberRoute, ID: "route-a", Fidelity: FidelityExact}}},
			{ID: "combo-junior", Kind: ModelCombo, Members: []MemberRef{{Kind: MemberModel, ID: "physical-qwen"}}},
		},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	k, err := New(snapshot, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	attempts := []string{}
	k.Adapters["fixture"] = attemptAdapter{attempts: &attempts}

	request := NormalizedRequest{
		Model: "junior", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1,
		SourceFormat: normalize.FormatOpenAIChat, Messages: []normalize.Message{{Role: "user", Content: "fixture prompt"}},
	}
	result, err := k.ExplainCompatibility(context.Background(), "junior", request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || result[0].RouteID != "route-a" || !result[0].Compatible {
		t.Fatalf("unexpected compatibility explanation: %#v", result)
	}
	if got, want := result[0].ModelPath, []string{"combo-junior", "physical-qwen"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("model path=%v want=%v", got, want)
	}
	if result[0].Plan.Fidelity != FidelityNative || len(result[0].Plan.Mappings) == 0 {
		t.Fatalf("compatibility plan missing: %#v", result[0].Plan)
	}
	if len(attempts) != 0 {
		t.Fatalf("dry-run dispatched upstream request(s): %v", attempts)
	}
}
