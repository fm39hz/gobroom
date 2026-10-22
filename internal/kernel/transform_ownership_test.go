package kernel

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/normalize"
)

func TestResponseTransformPreservesAndIsolatesArtifactReferences(t *testing.T) {
	issuer := extensions.Ref{Kind: "provider-definition", ID: "fixture", ContractVersion: 1}
	artifact := extensions.ArtifactRef{TypeRef: extensions.Ref{Kind: "artifact", ID: "fixture", ContractVersion: 1}, Role: "result", Value: []byte("original"), Owner: extensions.ArtifactOwner{IssuerRef: &issuer}, Body: &extensions.BodyRef{ID: "original-body"}}
	for _, mutate := range []bool{false, true} {
		t.Run(map[bool]string{false: "preserve", true: "reject mutation"}[mutate], func(t *testing.T) {
			event := ResponseEvent{Kind: EventArtifact, Artifacts: []extensions.ArtifactRef{artifact.Clone()}}
			transform := responseMutationFixture{id: "fixture.artifact-ownership", effects: []ResponseTransformEffect{ResponseEffectText}, mutate: func(event ResponseEvent) ResponseEvent {
				if !reflect.DeepEqual(event.Artifacts, []extensions.ArtifactRef{artifact}) {
					t.Error("transform received altered artifact references")
					return event
				}
				if mutate {
					event.Artifacts[0].Value[0] = 'X'
					event.Artifacts[0].Owner.IssuerRef.ID = "forged"
					event.Artifacts[0].Body.ID = "forged-body"
				}
				return event
			}}
			registry := NewResponseTransformRegistry()
			if err := registry.Register(transform); err != nil {
				t.Fatal(err)
			}
			updated, err := registry.ApplyScopes(context.Background(), event, []TransformBinding{{ID: "binding", TransformRef: transform.Definition().Ref, Enabled: true, Scope: daemonTransformScope()}}, daemonTransformScope())
			if mutate {
				if !errors.Is(err, ErrTransformSafetyViolation) {
					t.Fatalf("artifact mutation: %v", err)
				}
			} else {
				if err != nil || !reflect.DeepEqual(updated.Artifacts, event.Artifacts) {
					t.Fatalf("artifact lost: updated=%#v err=%v", updated.Artifacts, err)
				}
				updated.Artifacts[0].Value[0] = 'Y'
				updated.Artifacts[0].Owner.IssuerRef.ID = "changed after return"
				updated.Artifacts[0].Body.ID = "changed after return"
			}
			if !reflect.DeepEqual(event.Artifacts[0], artifact) {
				t.Fatalf("input artifact mutated: %#v", event.Artifacts[0])
			}
		})
	}
}

func TestResponseTransformOwnsNestedUsageEvidence(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(map[bool]string{false: "undeclared", true: "declared"}[allowed], func(t *testing.T) {
			event := ResponseEvent{Kind: EventUsage, Usage: &UsageEvent{
				InputTokens:         new(int64(7)),
				CompatibilityLosses: []LossRecord{{ID: "loss", Requested: map[string]any{"value": "original"}}},
				CompatibilityPlan:   &CompatibilityPlanSummary{Mappings: []CompatibilityMappingSummary{{Paths: []string{"original"}}}, RequestTransformSteps: []RequestTransformPlanStep{{Effects: []TransformEffect{TransformInput}}}},
			}}
			effects := []ResponseTransformEffect{ResponseEffectText}
			if allowed {
				effects = []ResponseTransformEffect{ResponseEffectUsage}
			}
			transform := responseMutationFixture{id: "fixture.nested-usage", effects: effects, mutate: func(event ResponseEvent) ResponseEvent {
				event.Usage.CompatibilityLosses[0].Requested.(map[string]any)["value"] = "changed"
				event.Usage.CompatibilityPlan.Mappings[0].Paths[0] = "changed"
				event.Usage.CompatibilityPlan.RequestTransformSteps[0].Effects[0] = TransformTools
				return event
			}}
			registry := NewResponseTransformRegistry()
			if err := registry.Register(transform); err != nil {
				t.Fatal(err)
			}
			updated, err := registry.ApplyScopes(context.Background(), event, []TransformBinding{{ID: "binding", TransformRef: transform.Definition().Ref, Enabled: true, Scope: daemonTransformScope()}}, daemonTransformScope())
			if allowed && (err != nil || updated.Usage.CompatibilityPlan.Mappings[0].Paths[0] != "changed") {
				t.Fatalf("declared mutation: %v", err)
			}
			if !allowed && err == nil {
				t.Fatal("undeclared nested usage mutation accepted")
			}
			if event.Usage.CompatibilityLosses[0].Requested.(map[string]any)["value"] != "original" || event.Usage.CompatibilityPlan.Mappings[0].Paths[0] != "original" || event.Usage.CompatibilityPlan.RequestTransformSteps[0].Effects[0] != TransformInput {
				t.Fatalf("provider usage evidence changed: %#v", event.Usage)
			}
		})
	}
}

func TestRequestTransformCannotChangeOperationContractVersion(t *testing.T) {
	transform := requestMutationFixture{id: "fixture.operation-version", effects: []TransformEffect{TransformInput}, mutate: func(request *NormalizedRequest) { request.OperationContractVersion = 2 }}
	registry := NewRequestTransformRegistry()
	if err := registry.Register(transform); err != nil {
		t.Fatal(err)
	}
	request := NormalizedRequest{Model: "role", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1}
	err := registry.ApplyScopes(context.Background(), &request, []TransformBinding{{ID: "binding", TransformRef: transform.Definition().Ref, Enabled: true, Scope: daemonTransformScope()}}, daemonTransformScope())
	if !errors.Is(err, ErrTransformSafetyViolation) || request.OperationContractVersion != 1 {
		t.Fatalf("operation contract changed: %#v err=%v", request, err)
	}
}
