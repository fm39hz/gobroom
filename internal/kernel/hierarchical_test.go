package kernel

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/normalize"
)

type attemptAdapter struct{ attempts *[]string }
type undeclaredCompatibilityAdapter struct{ attemptAdapter }
type ambiguousExecutionAdapter struct{ attemptAdapter }
type namedLossAdapter struct{ attemptAdapter }

func (undeclaredCompatibilityAdapter) PlanCompatibility(CompatibilityContext) CompatibilityPlan {
	return CompatibilityPlan{Supported: true, Fidelity: FidelityNative}
}
func (a ambiguousExecutionAdapter) Execute(_ context.Context, request UpstreamRequest) (UpstreamResponse, error) {
	*a.attempts = append(*a.attempts, request.URL)
	return UpstreamResponse{}, errors.New("connection closed after upstream dispatch")
}

type observingStrategy struct{ failedNodes *[]string }

type orderedObservingStrategy struct{ failedNodes *[]string }

func (orderedObservingStrategy) Plan(_ string, members []MemberRef, _ *StrategyState) []MemberRef {
	return members
}

func (s orderedObservingStrategy) OnFailure(failure StrategyFailure, _ *StrategyState) FailureAction {
	*s.failedNodes = append(*s.failedNodes, failure.NodeID)
	return FailureContinue
}

func (observingStrategy) Plan(_ string, members []MemberRef, state *StrategyState) []MemberRef {
	if len(members) < 2 {
		return members
	}
	start := state.Cursor % len(members)
	state.Cursor = (start + 1) % len(members)
	return rotateMembers(members, start)
}

func (s observingStrategy) OnFailure(failure StrategyFailure, _ *StrategyState) FailureAction {
	*s.failedNodes = append(*s.failedNodes, failure.NodeID)
	return FailureContinue
}

func (a attemptAdapter) ID() string { return "test" }
func (a attemptAdapter) PlanCompatibility(input CompatibilityContext) CompatibilityPlan {
	return fixtureCompatibilityPlan(input, normalize.FormatOpenAIChat, FidelityNative)
}
func (a namedLossAdapter) PlanCompatibility(input CompatibilityContext) CompatibilityPlan {
	base := fixtureCompatibilityPlan(input, normalize.FormatOpenAIChat, FidelityNative)
	return ComposeCompatibilityPlan([][]FacetMapping{
		base.Mappings,
		{{Facet: "generation.temperature", Paths: []string{"request.temperature"}, Disposition: FacetDegraded, Losses: []LossRecord{{
			ID: "generation.temperature.clamped", Requested: map[string]any{"temperature": 0.8}, Effective: map[string]any{"temperature": 0.3}, SemanticPaths: []string{"request.temperature"},
		}}, Reason: "temperature was clamped to the provider range"}},
	}, input.Policy)
}
func (namedLossAdapter) RenderResponse(_ context.Context, response UpstreamResponse, writer http.ResponseWriter, _ normalize.Format, hooks StreamHooks) error {
	if _, err := io.Copy(writer, response.Body); err != nil {
		return err
	}
	if hooks.OnComplete != nil {
		hooks.OnComplete(UsageEvent{Status: "ok"})
	}
	return nil
}
func (a attemptAdapter) Prepare(_ context.Context, _ NormalizedRequest, route Route, _ Credential) (UpstreamRequest, error) {
	return UpstreamRequest{Method: http.MethodPost, URL: route.ID}, nil
}
func (a attemptAdapter) Execute(_ context.Context, request UpstreamRequest) (UpstreamResponse, error) {
	*a.attempts = append(*a.attempts, request.URL)
	if strings.HasPrefix(request.URL, "child-") {
		return UpstreamResponse{Status: http.StatusTooManyRequests, Body: io.NopCloser(strings.NewReader("retry"))}, nil
	}
	return UpstreamResponse{Status: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok"))}, nil
}
func (attemptAdapter) ClassifyError(int, []byte) ErrorClass { return ErrorRetryable }
func (attemptAdapter) RenderResponse(_ context.Context, response UpstreamResponse, writer http.ResponseWriter, _ normalize.Format, _ StreamHooks) error {
	_, err := io.Copy(writer, response.Body)
	return err
}

func TestExecutePreservesHierarchicalFallbackBoundariesAndState(t *testing.T) {
	snapshot, err := BuildSnapshot(SnapshotInput{
		PublicModels: []PublicModel{{Name: "role", TargetRef: "role"}},
		Nodes: []ModelNode{
			{ID: "role", Kind: ModelCombo, Strategy: "observing-ordered", Members: []MemberRef{{Kind: MemberModel, ID: "physical"}, {Kind: MemberRoute, ID: "outer-route"}}},
			{ID: "physical", Kind: ModelPhysical, Strategy: "observing", Members: []MemberRef{{Kind: MemberRoute, ID: "child-a", Fidelity: FidelityExact}, {Kind: MemberRoute, ID: "child-b", Fidelity: FidelityExact}}},
		},
		Routes: []Route{
			{ID: "child-a", OperationBindings: map[normalize.Operation]RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"test"}}}, Protocol: ProtocolOpenAIChat, Enabled: true},
			{ID: "child-b", OperationBindings: map[normalize.Operation]RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"test"}}}, Protocol: ProtocolOpenAIChat, Enabled: true},
			{ID: "outer-route", OperationBindings: map[normalize.Operation]RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"test"}}}, Protocol: ProtocolOpenAIChat, Enabled: true},
		},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(snapshot, nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer kernel.Close()
	attempts := []string{}
	failedNodes := []string{}
	kernel.Adapters["test"] = attemptAdapter{attempts: &attempts}
	kernel.Scheduler.RegisterStrategy("observing", observingStrategy{failedNodes: &failedNodes})
	kernel.Scheduler.RegisterStrategy("observing-ordered", orderedObservingStrategy{failedNodes: &failedNodes})
	request := NormalizedRequest{Model: "role", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, SourceFormat: normalize.FormatOpenAIChat}

	for i, want := range [][]string{{"child-a", "child-b", "outer-route"}, {"child-b", "child-a", "outer-route"}, {"child-a", "child-b", "outer-route"}} {
		attempts = attempts[:0]
		if err := kernel.Execute(context.Background(), request, Credential{}, httptest.NewRecorder()); err != nil {
			t.Fatalf("execute %d: %v", i, err)
		}
		if len(attempts) != len(want) {
			t.Fatalf("execute %d attempts=%v want=%v", i, attempts, want)
		}
		for j := range want {
			if attempts[j] != want[j] {
				t.Fatalf("execute %d attempts=%v want=%v", i, attempts, want)
			}
		}
	}
	roleState := kernel.Scheduler.modelState[strategyStateKey("role", StrategyRef("ordered-fallback", 1))]
	physicalState := kernel.Scheduler.modelState[strategyStateKey("physical", StrategyRef("ordered-fallback", 1))]
	if roleState == nil || physicalState == nil || roleState.Cursor != 0 || physicalState.Cursor != 1 {
		t.Fatalf("state must be scoped and advanced independently per node: %#v", kernel.Scheduler.modelState)
	}
	if len(failedNodes) != 9 {
		t.Fatalf("expected each exhausted child then outer member to notify its own strategy, got %v", failedNodes)
	}
	for i := 0; i < len(failedNodes); i += 3 {
		if failedNodes[i] != "physical" || failedNodes[i+1] != "physical" || failedNodes[i+2] != "role" {
			t.Fatalf("failure transitions crossed policy boundaries: %v", failedNodes)
		}
	}
}

func TestNodeLossPoliciesFlowDownAndPermittedLossIsRecordedInUsage(t *testing.T) {
	makeGateway := func(comboPolicy, physicalPolicy LossPolicy, ceiling LossPolicyCeiling) (*Kernel, *[]string) {
		attempts := []string{}
		snapshot, err := BuildSnapshot(SnapshotInput{
			LossCeiling:  ceiling,
			PublicModels: []PublicModel{{Name: "role", TargetRef: "combo"}},
			Nodes: []ModelNode{
				{ID: "combo", Kind: ModelCombo, Strategy: StrategyFallback, Members: []MemberRef{{Kind: MemberModel, ID: "physical"}}, LossPolicy: comboPolicy},
				{ID: "physical", Kind: ModelPhysical, Strategy: StrategyFallback, Members: []MemberRef{{Kind: MemberRoute, ID: "route", Fidelity: FidelityExact}}, LossPolicy: physicalPolicy},
			},
			Routes: []Route{{ID: "route", Enabled: true, Protocol: ProtocolOpenAIChat, OperationBindings: map[normalize.Operation]RouteOperationBinding{
				normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"loss"}},
			}}},
		}, 1)
		if err != nil {
			t.Fatal(err)
		}
		gateway, err := New(snapshot, nil, 8)
		if err != nil {
			t.Fatal(err)
		}
		gateway.Adapters["loss"] = namedLossAdapter{attemptAdapter{attempts: &attempts}}
		return gateway, &attempts
	}

	request := NormalizedRequest{Model: "role", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, SourceFormat: normalize.FormatOpenAIChat}
	denied, deniedAttempts := makeGateway(LossPolicy{}, LossPolicy{}, LossPolicyCeiling{})
	if err := denied.Execute(context.Background(), request, Credential{}, httptest.NewRecorder()); err == nil || len(*deniedAttempts) != 0 {
		t.Fatalf("ungranted loss was dispatched: err=%v attempts=%v", err, *deniedAttempts)
	}
	denied.Close()

	allowed, attempts := makeGateway(LossPolicy{Allow: []string{"generation.temperature.clamped"}}, LossPolicy{}, LossPolicyCeiling{AllowOnly: true, Allow: []string{"generation.temperature.clamped"}})
	if err := allowed.Execute(context.Background(), request, Credential{}, httptest.NewRecorder()); err != nil {
		t.Fatalf("ancestor grant did not reach candidate: %v", err)
	}
	if len(*attempts) != 1 {
		t.Fatalf("allowed route attempts=%v", *attempts)
	}
	select {
	case event := <-allowed.Events:
		if event.CompatibilityFidelity != FidelityLossy || len(event.CompatibilityLosses) != 1 || event.CompatibilityLosses[0].ID != "generation.temperature.clamped" || !reflect.DeepEqual(event.CompatibilityLosses[0].Requested, map[string]any{"temperature": 0.8}) || !reflect.DeepEqual(event.CompatibilityLosses[0].Effective, map[string]any{"temperature": 0.3}) || !reflect.DeepEqual(event.CompatibilityLosses[0].SemanticPaths, []string{"request.temperature"}) || !reflect.DeepEqual(event.CompatibilityLosses[0].PolicySources, []string{"combo"}) {
			t.Fatalf("compatibility outcome missing from usage event: %#v", event)
		}
		if event.CompatibilityPlan == nil || !event.CompatibilityPlan.Supported || event.CompatibilityPlan.Fidelity != FidelityLossy {
			t.Fatalf("selected compatibility plan missing from usage event: %#v", event.CompatibilityPlan)
		}
		foundTemperature := false
		for _, mapping := range event.CompatibilityPlan.Mappings {
			if mapping.Facet == "generation.temperature" && mapping.Disposition == FacetDegraded && mapping.Reason == "temperature was clamped to the provider range" {
				foundTemperature = true
			}
		}
		if !foundTemperature {
			t.Fatalf("usage compatibility plan omitted facet explanation: %#v", event.CompatibilityPlan.Mappings)
		}
	default:
		t.Fatal("usage event was not emitted")
	}
	allowed.Close()

	deniedByChild, childAttempts := makeGateway(LossPolicy{Allow: []string{"generation.temperature.clamped"}}, LossPolicy{Deny: []string{"generation.temperature.clamped"}}, LossPolicyCeiling{AllowOnly: true, Allow: []string{"generation.temperature.clamped"}})
	if err := deniedByChild.Execute(context.Background(), request, Credential{}, httptest.NewRecorder()); err == nil || len(*childAttempts) != 0 {
		t.Fatalf("child denial did not dominate ancestor grant: err=%v attempts=%v", err, *childAttempts)
	}
	deniedByChild.Close()

	deniedByServer, serverAttempts := makeGateway(LossPolicy{Allow: []string{"generation.temperature.clamped"}}, LossPolicy{}, LossPolicyCeiling{AllowOnly: true, Allow: []string{"reasoning.effort.clamped"}})
	if err := deniedByServer.Execute(context.Background(), request, Credential{}, httptest.NewRecorder()); err == nil || len(*serverAttempts) != 0 {
		t.Fatalf("model grant exceeded server loss ceiling: err=%v attempts=%v", err, *serverAttempts)
	}
	deniedByServer.Close()
}

func TestKernelRejectsAdapterWithoutRequiredFacetDeclarationBeforeDispatch(t *testing.T) {
	snapshot, err := BuildSnapshot(SnapshotInput{
		PublicModels: []PublicModel{{Name: "role", TargetRef: "role"}},
		Nodes:        []ModelNode{{ID: "role", Kind: ModelCombo, Strategy: StrategyFallback, Members: []MemberRef{{Kind: MemberRoute, ID: "route"}}}},
		Routes: []Route{{ID: "route", Enabled: true, OperationBindings: map[normalize.Operation]RouteOperationBinding{
			normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"undeclared"}},
		}}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	k, err := New(snapshot, nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	attempts := []string{}
	k.Adapters["undeclared"] = undeclaredCompatibilityAdapter{attemptAdapter{attempts: &attempts}}
	request := NormalizedRequest{Model: "role", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, SourceFormat: normalize.FormatOpenAIChat}
	err = k.Execute(context.Background(), request, Credential{}, httptest.NewRecorder())
	if !errors.Is(err, ErrNoRoute) || len(attempts) != 0 {
		t.Fatalf("adapter without a required facet declaration dispatched: err=%v attempts=%v", err, attempts)
	}
}

func TestKernelDoesNotFallbackAfterAmbiguousDispatchedRequest(t *testing.T) {
	snapshot, err := BuildSnapshot(SnapshotInput{
		PublicModels: []PublicModel{{Name: "role", TargetRef: "role"}},
		Nodes:        []ModelNode{{ID: "role", Kind: ModelCombo, Strategy: StrategyFallback, Members: []MemberRef{{Kind: MemberRoute, ID: "first"}, {Kind: MemberRoute, ID: "second"}}}},
		Routes: []Route{
			{ID: "first", Enabled: true, OperationBindings: map[normalize.Operation]RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"ambiguous"}}}},
			{ID: "second", Enabled: true, OperationBindings: map[normalize.Operation]RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"ambiguous"}}}},
		},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	k, err := New(snapshot, nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	attempts := []string{}
	k.Adapters["ambiguous"] = ambiguousExecutionAdapter{attemptAdapter{attempts: &attempts}}
	request := NormalizedRequest{Model: "role", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, SourceFormat: normalize.FormatOpenAIChat}
	err = k.Execute(context.Background(), request, Credential{}, httptest.NewRecorder())
	var suppressed *ReplaySuppressedError
	if !errors.As(err, &suppressed) || suppressed.Effect != EffectUnknown {
		t.Fatalf("ambiguous dispatched attempt did not suppress replay: %v", err)
	}
	if len(attempts) != 1 || attempts[0] != "first" {
		t.Fatalf("ambiguous request may have executed twice: attempts=%v", attempts)
	}
}

func TestTypedModelGraphRejectsCycles(t *testing.T) {
	_, err := BuildSnapshot(SnapshotInput{
		PublicModels: []PublicModel{{Name: "a", TargetRef: "a"}},
		Nodes: []ModelNode{
			{ID: "a", Members: []MemberRef{{Kind: MemberModel, ID: "b"}}},
			{ID: "b", Members: []MemberRef{{Kind: MemberModel, ID: "a"}}},
		},
	}, 1)
	if err == nil || errors.Is(err, ErrSnapshotInvalid) {
		t.Fatalf("expected descriptive model cycle error, got %v", err)
	}
}
