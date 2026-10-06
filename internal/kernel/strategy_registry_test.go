package kernel

import (
	"reflect"
	"testing"
)

type preferLastStrategy struct{}

func (preferLastStrategy) Plan(_ string, members []MemberRef, _ *StrategyState) []MemberRef {
	result := append([]MemberRef(nil), members...)
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
}
func (preferLastStrategy) OnFailure(StrategyFailure, *StrategyState) FailureAction {
	return FailureContinue
}

func TestStrategyCatalogDescribesOnlyCanonicalExecutablePrimitives(t *testing.T) {
	definitions := StrategyDefinitions()
	for _, builtin := range builtinStrategyDefinitions {
		if _, found := StrategyDefinitionByID(builtin.ID); !found {
			t.Fatalf("built-in strategy %q missing from catalog", builtin.ID)
		}
	}
	seen := map[string]bool{}
	for _, definition := range definitions {
		if definition.ID == "" || definition.Label == "" || definition.Description == "" || definition.Primitive == nil {
			t.Fatalf("incomplete strategy contract: %#v", definition)
		}
		if seen[definition.ID] {
			t.Fatalf("duplicate strategy id %q", definition.ID)
		}
		seen[definition.ID] = true
	}
	if _, ok := StrategyDefinitionByID("round_robin_fallback"); ok {
		t.Fatal("legacy spelling must not resolve as a second strategy ID")
	}
}

func TestStrategyOptionsAreValidatedAgainstPrimitiveSchema(t *testing.T) {
	if err := ValidateStrategyConfig("round-robin-fallback", map[string]any{"stickyLimit": float64(4)}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateStrategyConfig("round-robin-fallback", map[string]any{"stickyLimit": float64(0)}); err == nil {
		t.Fatal("stickyLimit below the declared minimum was accepted")
	}
	if err := ValidateStrategyConfig("round-robin-fallback", map[string]any{"surprise": true}); err == nil {
		t.Fatal("unknown primitive option was accepted")
	}
	if err := ValidateStrategyConfig("weighted-fallback", map[string]any{"stickyLimit": float64(2)}); err == nil {
		t.Fatal("an option belonging to another primitive was accepted")
	}
	defaults, err := DefaultStrategyConfig("round-robin-fallback")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(defaults, map[string]any{"stickyLimit": 1}) {
		t.Fatalf("sticky defaults=%#v", defaults)
	}
	limit, err := StrategyIntegerOption("round-robin-fallback", map[string]any{}, "stickyLimit")
	if err != nil || limit != 1 {
		t.Fatalf("effective default sticky limit=%d err=%v", limit, err)
	}
}

func TestStickyRoundRobinFallbackAppliesConfiguredLimitAtItsModelNode(t *testing.T) {
	scheduler := NewScheduler(AlwaysOpenGate{})
	node := ModelNode{ID: "role", Kind: ModelCombo, Strategy: StrategyRoundRobinFallback, StickyLimit: 2, Members: []MemberRef{{ID: "a"}, {ID: "b"}}}
	want := [][]string{{"a", "b"}, {"a", "b"}, {"b", "a"}, {"b", "a"}, {"a", "b"}}
	for index, expected := range want {
		planned := scheduler.Plan(node)
		if len(planned) != len(expected) || planned[0].ID != expected[0] || planned[1].ID != expected[1] {
			t.Fatalf("plan %d=%v, want %v", index, planned, expected)
		}
	}
}

func TestRoundRobinIsSingleAttemptAndLetsParentDecideFallback(t *testing.T) {
	scheduler := NewScheduler(AlwaysOpenGate{})
	node := ModelNode{ID: "single", Kind: ModelCombo, Strategy: StrategyRoundRobin, Members: []MemberRef{{ID: "a"}, {ID: "b"}}}
	for index, want := range []string{"a", "b", "a"} {
		planned := scheduler.Plan(node)
		if len(planned) != 1 || planned[0].ID != want {
			t.Fatalf("plan %d=%#v, want only %q", index, planned, want)
		}
	}
	if action := scheduler.OnFailure(node, StrategyFailure{Class: ErrorRetryable}); action != FailureContinue {
		t.Fatalf("retryable member failure action=%v, want return to parent policy", action)
	}
}

func TestStrategyValidationDoesNotAcceptLegacyAliases(t *testing.T) {
	if err := ValidateStrategyConfig("round_robin_fallback", nil); err == nil {
		t.Fatal("legacy strategy spelling unexpectedly resolved")
	}
	if _, err := DefaultStrategyConfig("weighted-fallback"); err != nil {
		t.Fatal(err)
	}
}

func TestNewStrategyExtensionAppearsInCatalogAndSchedulerWithoutKernelBranch(t *testing.T) {
	const id Strategy = "vendor.example.prefer-last.v1"
	definitionID := string(id)
	if err := RegisterStrategyDefinition(StrategyDefinition{ID: definitionID, Label: "Prefer last", Description: "Fixture strategy extension.", Runtime: id, Primitive: preferLastStrategy{}}); err != nil {
		t.Fatal(err)
	}
	if _, found := StrategyDefinitionByID(definitionID); !found {
		t.Fatal("registered strategy missing from catalog")
	}
	scheduler := NewScheduler(AlwaysOpenGate{})
	planned := scheduler.Plan(ModelNode{ID: "fixture", Strategy: id, Members: []MemberRef{{ID: "first"}, {ID: "last"}}})
	if len(planned) != 2 || planned[0].ID != "last" || planned[1].ID != "first" {
		t.Fatalf("registered strategy plan=%#v", planned)
	}
}
