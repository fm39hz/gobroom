package kernel

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
)

type preferLastStrategy struct{}

func (preferLastStrategy) Plan(_ string, members []MemberRef, _ *StrategyState) []MemberRef {
	result := append([]MemberRef(nil), members...)
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
}

type configurableOrderStrategy struct{}

func (configurableOrderStrategy) Plan(_ string, members []MemberRef, state *StrategyState) []MemberRef {
	var options struct {
		PreferLast int `json:"preferLast"`
	}
	_ = json.Unmarshal(state.Config, &options)
	result := append([]MemberRef(nil), members...)
	if options.PreferLast == 1 {
		for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
			result[left], result[right] = result[right], result[left]
		}
	}
	return result
}
func (configurableOrderStrategy) OnFailure(StrategyFailure, *StrategyState) FailureAction {
	return FailureContinue
}
func (preferLastStrategy) OnFailure(StrategyFailure, *StrategyState) FailureAction {
	return FailureContinue
}

func TestStrategyCatalogDescribesOnlyCanonicalExecutablePrimitives(t *testing.T) {
	catalog, err := NewBuiltinStrategyCatalog()
	if err != nil {
		t.Fatal(err)
	}
	definitions := catalog.Definitions()
	for _, builtin := range builtinStrategyDefinitions {
		if _, found := catalog.Definition(builtin.Ref); !found {
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
	if _, ok := catalog.Definition(StrategyRef("round_robin_fallback", 1)); ok {
		t.Fatal("legacy spelling must not resolve as a second strategy ID")
	}
}

func TestStrategyOptionsAreValidatedAgainstPrimitiveSchema(t *testing.T) {
	catalog, err := NewBuiltinStrategyCatalog()
	if err != nil {
		t.Fatal(err)
	}
	ref := StrategyRef("round-robin-fallback", 1)
	if err := catalog.Validate(ref, map[string]any{"stickyLimit": float64(4)}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Validate(ref, map[string]any{"stickyLimit": float64(0)}); err == nil {
		t.Fatal("stickyLimit below the declared minimum was accepted")
	}
	if err := catalog.Validate(ref, map[string]any{"surprise": true}); err == nil {
		t.Fatal("unknown primitive option was accepted")
	}
	if err := catalog.Validate(StrategyRef("weighted-fallback", 1), map[string]any{"stickyLimit": float64(2)}); err == nil {
		t.Fatal("an option belonging to another primitive was accepted")
	}
	defaults, err := catalog.DefaultOptions(ref)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(defaults, map[string]any{"stickyLimit": 1}) {
		t.Fatalf("sticky defaults=%#v", defaults)
	}
	definition, limit, err := catalog.Resolve(ref, map[string]any{})
	if err != nil || definition.Ref != ref || limit != 1 {
		t.Fatalf("effective strategy/default sticky limit=%#v %d err=%v", definition.Ref, limit, err)
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

func TestStrategyStateIsScopedToExactContractRefAcrossReloads(t *testing.T) {
	scheduler := NewScheduler(AlwaysOpenGate{})
	candidates := []Route{{ID: "a", Enabled: true}, {ID: "b", Enabled: true}}
	v1 := ResolvedModel{PublicName: "junior", Strategy: StrategyRoundRobinFallback, StrategyRef: StrategyRef("round-robin-fallback", 1), StickyLimit: 1, Candidates: candidates}
	v2 := ResolvedModel{PublicName: "junior", Strategy: StrategyRoundRobinFallback, StrategyRef: StrategyRef("round-robin-fallback", 2), StickyLimit: 1, Candidates: candidates}
	firstV1 := scheduler.Order(v1, time.Now())
	firstV2 := scheduler.Order(v2, time.Now())
	if len(firstV1) != 2 || len(firstV2) != 2 || firstV1[0].ID != "a" || firstV2[0].ID != "a" {
		t.Fatalf("strategy contract versions shared mutable cursor state: v1=%#v v2=%#v", firstV1, firstV2)
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
	catalog, err := NewBuiltinStrategyCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := catalog.Resolve(StrategyRef("round_robin_fallback", 1), nil); err == nil {
		t.Fatal("legacy strategy spelling unexpectedly resolved")
	}
	if _, _, err := catalog.Resolve(StrategyRef("weighted-fallback", 1), nil); err != nil {
		t.Fatal(err)
	}
}

func TestNewStrategyExtensionAppearsInCatalogAndSchedulerWithoutKernelBranch(t *testing.T) {
	const id Strategy = "vendor.example.prefer-last.v1"
	definitionID := string(id)
	ref := extensions.Ref{Kind: StrategyExtensionKind, ID: definitionID, ContractVersion: 1}
	catalog := extensions.NewCatalog()
	if err := RegisterStrategyExtension(catalog, StrategyDefinition{Ref: ref, ID: definitionID, Label: "Prefer last", Description: "Fixture strategy extension.", Runtime: id, Primitive: preferLastStrategy{}}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	definitions, err := StrategyDefinitionsFromCatalog(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 1 || definitions[0].Ref != ref {
		t.Fatal("registered strategy missing from catalog")
	}
	scheduler := NewScheduler(AlwaysOpenGate{})
	scheduler.SetStrategyDefinitions(definitions)
	planned := scheduler.Plan(ModelNode{ID: "fixture", Strategy: id, Members: []MemberRef{{ID: "first"}, {ID: "last"}}})
	if len(planned) != 2 || planned[0].ID != "last" || planned[1].ID != "first" {
		t.Fatalf("registered strategy plan=%#v", planned)
	}
}

func TestExactStrategyRefOptionsReachTopLevelSchedulerPrimitive(t *testing.T) {
	ref := StrategyRef("vendor.configurable-order", 2)
	catalog := extensions.NewCatalog()
	definition := StrategyDefinition{
		Ref: ref, ID: ref.ID, Label: "Configurable order", Description: "Reorders members from typed options.",
		Runtime: "vendor.configurable-order.v2", Primitive: configurableOrderStrategy{},
		Options: []StrategyOptionDefinition{{Key: "preferLast", Label: "Prefer last", Type: StrategyOptionInteger, Minimum: 0, Maximum: 1}},
	}
	if err := RegisterStrategyExtension(catalog, definition); err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewStrategyCatalog(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	options := map[string]any{"preferLast": float64(1)}
	resolved, _, err := registry.Resolve(ref, options)
	if err != nil {
		t.Fatal(err)
	}
	scheduler := NewScheduler(AlwaysOpenGate{})
	scheduler.SetStrategyDefinitions(registry.Definitions())
	ordered := scheduler.Order(ResolvedModel{
		PublicName: "junior", Strategy: resolved.Runtime, StrategyRef: ref,
		StrategyConfig: json.RawMessage(`{"preferLast":1}`),
		Candidates:     []Route{{ID: "first", Enabled: true}, {ID: "last", Enabled: true}},
	}, time.Now())
	if len(ordered) != 2 || ordered[0].ID != "last" || ordered[1].ID != "first" {
		t.Fatalf("catalog-bound strategy order=%#v", ordered)
	}
}
