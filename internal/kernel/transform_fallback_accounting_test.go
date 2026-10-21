package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/normalize"
)

type fallbackAccountingTransform struct {
	id     string
	marker string
	fail   bool
	calls  map[string]int
}

func (f fallbackAccountingTransform) Definition() TransformDefinition {
	modes := []TransformFailureMode(nil)
	if f.fail {
		modes = []TransformFailureMode{TransformSafeFailOpen}
	}
	return TransformDefinition{
		Ref:                   extensions.Ref{Kind: RequestTransformKind, ID: "fixture.fallback." + f.id, ContractVersion: 1},
		ImplementationVersion: "1", Label: f.id, Description: "Fallback accounting fixture.",
		Stage: TransformBeforeRequirements, Effects: []TransformEffect{TransformInput}, FailureModes: modes,
	}
}

func (f fallbackAccountingTransform) Apply(_ context.Context, request *NormalizedRequest, _ json.RawMessage) error {
	f.calls[f.id]++
	if len(request.Messages) > 0 {
		content, ok := request.Messages[0].Content.(string)
		if !ok {
			return errors.New("fixture requires canonical text content")
		}
		request.Messages[0].Content = content + "|" + f.marker
	}
	if f.fail {
		return errors.New("fixture transform failure")
	}
	return nil
}

type fallbackAccountingAdapter struct {
	planned          map[string]CompatibilityContext
	prepared         map[string]NormalizedRequest
	rejectAtDispatch bool
	executed         *[]string
}

func (fallbackAccountingAdapter) ID() string { return "fallback-accounting" }

func (a fallbackAccountingAdapter) PlanCompatibility(input CompatibilityContext) CompatibilityPlan {
	input.Request = normalize.CloneRequest(input.Request)
	input.RequestTransformSteps = cloneRequestTransformSteps(input.RequestTransformSteps)
	input.TransformFailures = append([]TransformFailure(nil), input.TransformFailures...)
	a.planned[input.Route.ID] = input
	plan := fixtureCompatibilityPlan(input, normalize.FormatOpenAIChat, FidelityNative)
	if input.Route.ID == "route-rejected" && !a.rejectAtDispatch {
		for i := range plan.Mappings {
			if plan.Mappings[i].Facet == FacetWireRequest {
				plan.Mappings[i].Disposition = FacetUnsupported
				plan.Mappings[i].Reason = "fixture rejects this candidate"
			}
		}
	}
	return plan
}

func (a fallbackAccountingAdapter) Prepare(_ context.Context, request NormalizedRequest, route Route, _ Credential) (UpstreamRequest, error) {
	a.prepared[route.ID] = normalize.CloneRequest(request)
	return UpstreamRequest{Method: http.MethodPost, URL: route.ID}, nil
}

func (a fallbackAccountingAdapter) Execute(_ context.Context, request UpstreamRequest) (UpstreamResponse, error) {
	*a.executed = append(*a.executed, request.URL)
	if a.rejectAtDispatch && request.URL == "route-rejected" {
		return UpstreamResponse{Status: http.StatusTooManyRequests, Body: io.NopCloser(strings.NewReader("confirmed rejection"))}, nil
	}
	return UpstreamResponse{Status: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok"))}, nil
}

func (fallbackAccountingAdapter) ClassifyOutcome(status int, _ http.Header, _ []byte) ClassifiedOutcome {
	return ClassifiedOutcome{StatusCode: status, Class: ErrorRetryable, Cause: CauseRateLimited, Effect: EffectRejected, Scope: ScopeRoute, Retry: RetryNow, Confidence: 1}
}

func (fallbackAccountingAdapter) ClassifyError(int, []byte) ErrorClass { return ErrorRetryable }

func (fallbackAccountingAdapter) RenderResponse(_ context.Context, _ UpstreamResponse, writer http.ResponseWriter, _ normalize.Format, hooks StreamHooks) error {
	if _, err := io.WriteString(writer, "ok"); err != nil {
		return err
	}
	if hooks.OnComplete != nil {
		hooks.OnComplete(UsageEvent{Status: "ok"})
	}
	return nil
}

func TestFallbackCompatibilityPlanAndUsageContainOnlySelectedTransforms(t *testing.T) {
	t.Run("compatibility rejection", func(t *testing.T) { testFallbackSelectedTransformAccounting(t, false) })
	t.Run("confirmed upstream rejection", func(t *testing.T) { testFallbackSelectedTransformAccounting(t, true) })
}

func testFallbackSelectedTransformAccounting(t *testing.T, rejectAtDispatch bool) {
	transforms := []fallbackAccountingTransform{
		{id: "invocation", marker: "invocation"},
		{id: "role-model", marker: "role-model"},
		{id: "rejected-model", marker: "rejected-model", fail: true},
		{id: "selected-model", marker: "selected-model"},
		{id: "rejected-attempt", marker: "rejected-attempt"},
		{id: "selected-attempt", marker: "selected-attempt"},
		{id: "selected-provider", marker: "selected-provider"},
		{id: "selected-connection", marker: "selected-connection"},
		{id: "skipped-connection", marker: "skipped-connection"},
	}
	calls := make(map[string]int, len(transforms))
	for i := range transforms {
		transforms[i].calls = calls
	}
	bindings := []TransformBinding{
		{ID: "invocation.binding", TransformRef: transforms[0].Definition().Ref, Enabled: true, Scope: daemonTransformScope()},
		{ID: "role-model.binding", TransformRef: transforms[1].Definition().Ref, Enabled: true, Scope: TransformScope{Kind: TransformScopeModel, ID: "role"}},
		{ID: "rejected-model.binding", TransformRef: transforms[2].Definition().Ref, Enabled: true, Scope: TransformScope{Kind: TransformScopeModel, ID: "physical-rejected"}, FailureMode: TransformSafeFailOpen},
		{ID: "selected-model.binding", TransformRef: transforms[3].Definition().Ref, Enabled: true, Scope: TransformScope{Kind: TransformScopeModel, ID: "physical-selected"}},
		{ID: "rejected-attempt.binding", TransformRef: transforms[4].Definition().Ref, Enabled: true, Scope: TransformScope{Kind: TransformScopeRoute, ID: "route-rejected"}},
		{ID: "selected-attempt.binding", TransformRef: transforms[5].Definition().Ref, Enabled: true, Scope: TransformScope{Kind: TransformScopeRoute, ID: "route-selected"}},
		{ID: "selected-provider.binding", TransformRef: transforms[6].Definition().Ref, Enabled: true, Scope: TransformScope{Kind: TransformScopeProvider, ID: "provider-selected"}},
		{ID: "selected-connection.binding", TransformRef: transforms[7].Definition().Ref, Enabled: true, Scope: TransformScope{Kind: TransformScopeConnection, ID: "connection-selected"}},
		{ID: "skipped-connection.binding", TransformRef: transforms[8].Definition().Ref, Enabled: true, Scope: TransformScope{Kind: TransformScopeConnection, ID: "connection-skipped"}},
	}
	operationBinding := func() map[normalize.Operation]RouteOperationBinding {
		return map[normalize.Operation]RouteOperationBinding{
			normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"fallback-accounting"}},
		}
	}
	snapshot, err := BuildSnapshot(SnapshotInput{
		PublicModels: []PublicModel{{Name: "public", TargetRef: "role"}},
		Nodes: []ModelNode{
			{ID: "role", Kind: ModelCombo, Strategy: StrategyFallback, Members: []MemberRef{{Kind: MemberModel, ID: "physical-rejected"}, {Kind: MemberModel, ID: "physical-selected"}}},
			{ID: "physical-rejected", Kind: ModelPhysical, Strategy: StrategyFallback, Members: []MemberRef{{Kind: MemberRoute, ID: "route-unavailable", Fidelity: FidelityExact}}},
			{ID: "physical-selected", Kind: ModelPhysical, Strategy: StrategyFallback, Members: []MemberRef{{Kind: MemberRoute, ID: "route-rejected", Fidelity: FidelityExact}, {Kind: MemberRoute, ID: "route-selected", Fidelity: FidelityExact}}},
		},
		Routes: []Route{
			{ID: "route-unavailable", NodeID: "provider-unavailable", CredentialID: "connection-unavailable", Enabled: false, OperationBindings: operationBinding()},
			{ID: "route-rejected", NodeID: "provider-rejected", CredentialID: "connection-rejected", Enabled: true, OperationBindings: operationBinding()},
			{ID: "route-selected", NodeID: "provider-selected", CredentialID: "connection-selected", Enabled: true, OperationBindings: operationBinding()},
		},
		TransformBindings: bindings,
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	k, err := New(snapshot, nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	for _, transform := range transforms {
		if err := k.Transforms.Register(transform); err != nil {
			t.Fatal(err)
		}
	}
	var executed []string
	adapter := fallbackAccountingAdapter{planned: map[string]CompatibilityContext{}, prepared: map[string]NormalizedRequest{}, rejectAtDispatch: rejectAtDispatch, executed: &executed}
	k.Adapters[adapter.ID()] = adapter
	request := NormalizedRequest{
		Model: "public", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1,
		SourceFormat: normalize.FormatOpenAIChat, Messages: []Message{{Role: "user", Content: "original"}},
	}
	if err := k.Execute(context.Background(), request, Credential{}, httptest.NewRecorder()); err != nil {
		t.Fatal(err)
	}
	wantExecuted := []string{"route-selected"}
	if rejectAtDispatch {
		wantExecuted = []string{"route-rejected", "route-selected"}
	}
	if !reflect.DeepEqual(executed, wantExecuted) {
		t.Fatalf("dispatches=%v want=%v", executed, wantExecuted)
	}

	const selectedInput = "original|invocation|role-model|selected-model|selected-attempt|selected-provider|selected-connection"
	if got := adapter.prepared["route-selected"].Messages[0].Content; got != selectedInput {
		t.Fatalf("selected candidate input=%q want=%q", got, selectedInput)
	}
	if got, ok := adapter.planned["route-rejected"].Request.Messages[0].Content.(string); !ok || !strings.Contains(got, "rejected-attempt") || strings.Contains(got, "selected-attempt") {
		t.Fatalf("rejected candidate did not receive only its own attempt transform: %q", got)
	}
	if got := adapter.planned["route-selected"].Request.Messages[0].Content; got != selectedInput {
		t.Fatalf("selected plan inherited rejected candidate state: %q", got)
	}
	if _, ok := adapter.planned["route-unavailable"]; ok {
		t.Fatal("disabled route unexpectedly reached compatibility planning")
	}
	wantSteps := []string{
		"invocation.binding", "role-model.binding", "selected-model.binding",
		"selected-attempt.binding", "selected-provider.binding", "selected-connection.binding",
	}
	gotSteps := make([]string, len(adapter.planned["route-selected"].RequestTransformSteps))
	for i, step := range adapter.planned["route-selected"].RequestTransformSteps {
		gotSteps[i] = step.BindingID
	}
	if !reflect.DeepEqual(gotSteps, wantSteps) {
		t.Fatalf("selected compatibility steps=%v want each selected-scope binding once: %v", gotSteps, wantSteps)
	}
	if len(adapter.planned["route-selected"].TransformFailures) != 0 {
		t.Fatalf("selected compatibility plan inherited rejected-branch failures: %#v", adapter.planned["route-selected"].TransformFailures)
	}
	wantCalls := map[string]int{
		"invocation": 1, "role-model": 1, "rejected-model": 1, "selected-model": 1,
		"rejected-attempt": 1, "selected-attempt": 1, "selected-provider": 1,
		"selected-connection": 1,
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("transform executions=%v want candidate-local executions=%v", calls, wantCalls)
	}

	select {
	case usage := <-k.Events:
		if usage.Status != "ok" || usage.CompatibilityPlan == nil {
			t.Fatalf("selected usage record missing successful compatibility summary: %#v", usage)
		}
		usageSteps := make([]string, len(usage.CompatibilityPlan.RequestTransformSteps))
		for i, step := range usage.CompatibilityPlan.RequestTransformSteps {
			usageSteps[i] = step.BindingID
		}
		if !reflect.DeepEqual(usageSteps, wantSteps) {
			t.Fatalf("usage compatibility steps=%v want=%v", usageSteps, wantSteps)
		}
		if len(usage.CompatibilityPlan.TransformFailures) != 0 {
			t.Fatalf("usage record contains rejected-branch failure: %#v", usage.CompatibilityPlan.TransformFailures)
		}
		if usage.LogicalModel != "public" || usage.ProviderNodeID != "provider-selected" || usage.ConnectionID != "connection-selected" {
			t.Fatalf("usage record identifies wrong fallback candidate: %#v", usage)
		}
	case <-time.After(time.Second):
		t.Fatal("fallback execution did not emit its usage record")
	}
}
