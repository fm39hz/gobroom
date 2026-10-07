package kernel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/normalize"
)

type promptCompressionFixture struct{}

func (promptCompressionFixture) Definition() TransformDefinition {
	return TransformDefinition{Ref: extensions.Ref{Kind: RequestTransformKind, ID: "fixture.prompt-compression.v1", ContractVersion: 1}, ImplementationVersion: "1", Label: "Prompt compression", Description: "Replace verbose fixture text.", Stage: TransformBeforeRequirements, Effects: []TransformEffect{TransformInput}}
}
func (promptCompressionFixture) Apply(_ context.Context, request *NormalizedRequest, _ json.RawMessage) error {
	if len(request.Messages) > 0 {
		request.Messages[0].Content = "compressed-context"
	}
	return nil
}

type invalidIdentityTransform struct{}
type undeclaredPromptMutation struct{}

type responseMarkTransform struct{}

type scopedProbeAdapter struct{ attempts *[]string }

func (scopedProbeAdapter) ID() string { return "scoped-probe" }
func (a scopedProbeAdapter) PlanCompatibility(input CompatibilityContext) CompatibilityPlan {
	plan := fixtureCompatibilityPlan(input, normalize.FormatOpenAIChat, FidelityNative)
	if len(input.Request.Messages) > 0 && input.Request.Messages[0].Content == "compressed-context" {
		for i := range plan.Mappings {
			if plan.Mappings[i].Facet == FacetWireRequest {
				plan.Mappings[i].Disposition = FacetUnsupported
				plan.Mappings[i].Reason = "fixture rejects transformed branch"
			}
		}
	}
	return plan
}
func (scopedProbeAdapter) Prepare(_ context.Context, _ NormalizedRequest, route Route, _ Credential) (UpstreamRequest, error) {
	return UpstreamRequest{Method: http.MethodPost, URL: route.ID}, nil
}
func (a scopedProbeAdapter) Execute(_ context.Context, request UpstreamRequest) (UpstreamResponse, error) {
	*a.attempts = append(*a.attempts, request.URL)
	return UpstreamResponse{Status: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok"))}, nil
}
func (scopedProbeAdapter) ClassifyError(int, []byte) ErrorClass { return ErrorRetryable }
func (scopedProbeAdapter) RenderResponse(_ context.Context, _ UpstreamResponse, writer http.ResponseWriter, _ normalize.Format, hooks StreamHooks) error {
	if _, err := io.WriteString(writer, "ok"); err != nil {
		return err
	}
	if hooks.OnComplete != nil {
		hooks.OnComplete(UsageEvent{Status: "ok"})
	}
	return nil
}

func (responseMarkTransform) Definition() ResponseTransformDefinition {
	return ResponseTransformDefinition{Ref: extensions.Ref{Kind: ResponseTransformKind, ID: "fixture.response-mark.v1", ContractVersion: 1}, ImplementationVersion: "1", Label: "Mark response", Description: "Add a fixture suffix to text.", Effects: []ResponseTransformEffect{ResponseEffectText}}
}
func (responseMarkTransform) ApplyResponse(_ context.Context, event ResponseEvent, _ json.RawMessage) (ResponseEvent, error) {
	if event.Kind == EventTextDelta {
		event.Text += "!"
	}
	return event, nil
}

func (invalidIdentityTransform) Definition() TransformDefinition {
	return TransformDefinition{Ref: extensions.Ref{Kind: RequestTransformKind, ID: "fixture.invalid-identity.v1", ContractVersion: 1}, ImplementationVersion: "1", Label: "Invalid identity", Description: "Must be rejected.", Stage: TransformBeforeRequirements, Effects: []TransformEffect{TransformOptions}}
}
func (invalidIdentityTransform) Apply(_ context.Context, request *NormalizedRequest, _ json.RawMessage) error {
	request.Operation = "vendor.other.operation"
	return nil
}

func (undeclaredPromptMutation) Definition() TransformDefinition {
	return TransformDefinition{Ref: extensions.Ref{Kind: RequestTransformKind, ID: "fixture.undeclared-prompt.v1", ContractVersion: 1}, ImplementationVersion: "1", Label: "Undeclared prompt mutation", Description: "Prove effect enforcement and rollback.", Stage: TransformBeforeRequirements, Effects: []TransformEffect{TransformOptions}}
}
func (undeclaredPromptMutation) Apply(_ context.Context, request *NormalizedRequest, _ json.RawMessage) error {
	request.Messages[0].Content = "mutated"
	return nil
}

func TestRequestTransformRegistryRunsBeforeKernelRequirementsAndProviderEncoding(t *testing.T) {
	snapshot, err := BuildSnapshot(SnapshotInput{
		PublicModels:      []PublicModel{{Name: "public", TargetRef: "physical"}},
		Nodes:             []ModelNode{{ID: "physical", Kind: ModelPhysical, Members: []MemberRef{{Kind: MemberRoute, ID: "route", Fidelity: FidelityExact}}}},
		Routes:            []Route{{ID: "route", Enabled: true, OperationBindings: map[normalize.Operation]RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"fixture"}}}}},
		TransformBindings: []TransformBinding{{ID: "daemon.compress", TransformRef: extensions.Ref{Kind: RequestTransformKind, ID: "fixture.prompt-compression.v1", ContractVersion: 1}, Enabled: true, Scope: daemonTransformScope()}},
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

func TestModelScopedTransformUsesBranchLocalInputBeforeFallback(t *testing.T) {
	snapshot, err := BuildSnapshot(SnapshotInput{
		PublicModels: []PublicModel{{Name: "role", TargetRef: "role"}},
		Nodes: []ModelNode{
			{ID: "role", Kind: ModelCombo, Strategy: StrategyFallback, Members: []MemberRef{{Kind: MemberModel, ID: "physical-a"}, {Kind: MemberModel, ID: "physical-b"}}},
			{ID: "physical-a", Kind: ModelPhysical, Strategy: StrategyFallback, Members: []MemberRef{{Kind: MemberRoute, ID: "route-a", Fidelity: FidelityExact}}},
			{ID: "physical-b", Kind: ModelPhysical, Strategy: StrategyFallback, Members: []MemberRef{{Kind: MemberRoute, ID: "route-b", Fidelity: FidelityExact}}},
		},
		Routes: []Route{
			{ID: "route-a", Enabled: true, OperationBindings: map[normalize.Operation]RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"scoped-probe"}}}},
			{ID: "route-b", Enabled: true, OperationBindings: map[normalize.Operation]RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"scoped-probe"}}}},
		},
		TransformBindings: []TransformBinding{{ID: "model.physical-a.compress", TransformRef: extensions.Ref{Kind: RequestTransformKind, ID: "fixture.prompt-compression.v1", ContractVersion: 1}, Enabled: true, Scope: TransformScope{Kind: TransformScopeModel, ID: "physical-a"}}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	k, err := New(snapshot, nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	if err := k.Transforms.Register(promptCompressionFixture{}); err != nil {
		t.Fatal(err)
	}
	attempts := []string{}
	k.Adapters["scoped-probe"] = scopedProbeAdapter{attempts: &attempts}
	request := NormalizedRequest{Model: "role", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, SourceFormat: normalize.FormatOpenAIChat, Messages: []Message{{Role: "user", Content: "original"}}}
	if err := k.Execute(context.Background(), request, Credential{}, httptest.NewRecorder()); err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0] != "route-b" {
		t.Fatalf("transform leaked from physical-a branch into fallback: attempts=%v", attempts)
	}
	if request.Messages[0].Content != "original" {
		t.Fatalf("kernel mutated caller-owned input: %#v", request.Messages)
	}
}

func TestConnectionScopedTransformIsCandidateLocal(t *testing.T) {
	snapshot, err := BuildSnapshot(SnapshotInput{
		PublicModels: []PublicModel{{Name: "physical", TargetRef: "physical"}},
		Nodes:        []ModelNode{{ID: "physical", Kind: ModelPhysical, Strategy: StrategyFallback, Members: []MemberRef{{Kind: MemberRoute, ID: "route-a", Fidelity: FidelityExact}, {Kind: MemberRoute, ID: "route-b", Fidelity: FidelityExact}}}},
		Routes: []Route{
			{ID: "route-a", CredentialID: "connection-a", Enabled: true, OperationBindings: map[normalize.Operation]RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"scoped-probe"}}}},
			{ID: "route-b", CredentialID: "connection-b", Enabled: true, OperationBindings: map[normalize.Operation]RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"scoped-probe"}}}},
		},
		TransformBindings: []TransformBinding{{ID: "connection.a.compress", TransformRef: extensions.Ref{Kind: RequestTransformKind, ID: "fixture.prompt-compression.v1", ContractVersion: 1}, Enabled: true, Scope: TransformScope{Kind: TransformScopeConnection, ID: "connection-a"}}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	k, err := New(snapshot, nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	if err := k.Transforms.Register(promptCompressionFixture{}); err != nil {
		t.Fatal(err)
	}
	attempts := []string{}
	k.Adapters["scoped-probe"] = scopedProbeAdapter{attempts: &attempts}
	request := NormalizedRequest{Model: "physical", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, SourceFormat: normalize.FormatOpenAIChat, Messages: []Message{{Role: "user", Content: "original"}}}
	if err := k.Execute(context.Background(), request, Credential{}, httptest.NewRecorder()); err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0] != "route-b" {
		t.Fatalf("connection-scoped transform leaked to another candidate: attempts=%v", attempts)
	}
}

func TestRequestTransformCannotChangeResolvedModelOrOperation(t *testing.T) {
	registry := NewRequestTransformRegistry()
	if err := registry.Register(invalidIdentityTransform{}); err != nil {
		t.Fatal(err)
	}
	binding := TransformBinding{ID: "daemon.invalid", TransformRef: extensions.Ref{Kind: RequestTransformKind, ID: "fixture.invalid-identity.v1", ContractVersion: 1}, Enabled: true, Scope: daemonTransformScope()}
	request := NormalizedRequest{Model: "chosen-role", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1}
	err := registry.ApplyScopes(context.Background(), &request, []TransformBinding{binding}, daemonTransformScope())
	if err == nil || !strings.Contains(err.Error(), "immutable request identity") {
		t.Fatalf("identity mutation error=%v", err)
	}
}

func TestRegisteringTransformDoesNotActivateItWithoutEnabledBinding(t *testing.T) {
	registry := NewRequestTransformRegistry()
	if err := registry.Register(promptCompressionFixture{}); err != nil {
		t.Fatal(err)
	}
	request := NormalizedRequest{Messages: []Message{{Role: "user", Content: "original"}}}
	disabledBinding := TransformBinding{ID: "daemon.disabled", TransformRef: extensions.Ref{Kind: RequestTransformKind, ID: "fixture.prompt-compression.v1", ContractVersion: 1}, Scope: daemonTransformScope()}
	if registry.Active([]TransformBinding{disabledBinding}, daemonTransformScope()) {
		t.Fatal("registering an implementation activated it without a binding")
	}
	if err := registry.ApplyScopes(context.Background(), &request, []TransformBinding{disabledBinding}, daemonTransformScope()); err != nil {
		t.Fatal(err)
	}
	if request.Messages[0].Content != "original" {
		t.Fatalf("unbound transform changed input: %#v", request.Messages)
	}
	if registry.Active([]TransformBinding{disabledBinding}, daemonTransformScope()) {
		t.Fatal("disabled binding activated transform")
	}
	if err := registry.ApplyScopes(context.Background(), &request, []TransformBinding{disabledBinding}, daemonTransformScope()); err != nil {
		t.Fatal(err)
	}
	if request.Messages[0].Content != "original" {
		t.Fatalf("disabled binding changed input: %#v", request.Messages)
	}
	modelScope := TransformScope{Kind: TransformScopeModel, ID: "junior"}
	modelBinding := TransformBinding{ID: "model.junior.compress", TransformRef: extensions.Ref{Kind: RequestTransformKind, ID: "fixture.prompt-compression.v1", ContractVersion: 1}, Enabled: true, Scope: modelScope}
	if registry.Active([]TransformBinding{modelBinding}, daemonTransformScope()) || !registry.Active([]TransformBinding{modelBinding}, modelScope) {
		t.Fatal("active model-scoped binding leaked into daemon scope")
	}
	if err := registry.ApplyScopes(context.Background(), &request, []TransformBinding{modelBinding}, daemonTransformScope()); err != nil {
		t.Fatal(err)
	}
	if request.Messages[0].Content != "original" {
		t.Fatalf("model-scoped binding ran in daemon scope: %#v", request.Messages)
	}
	if err := registry.ApplyScopes(context.Background(), &request, []TransformBinding{modelBinding}, modelScope); err != nil {
		t.Fatal(err)
	}
	if request.Messages[0].Content != "compressed-context" {
		t.Fatalf("model-scoped binding did not run in its scope: %#v", request.Messages)
	}
}

func TestRequestTransformRejectsUndeclaredMutationTransactionally(t *testing.T) {
	registry := NewRequestTransformRegistry()
	if err := registry.Register(undeclaredPromptMutation{}); err != nil {
		t.Fatal(err)
	}
	binding := TransformBinding{ID: "daemon.bad-mutation", TransformRef: extensions.Ref{Kind: RequestTransformKind, ID: "fixture.undeclared-prompt.v1", ContractVersion: 1}, Enabled: true, Scope: daemonTransformScope()}
	request := NormalizedRequest{Model: "junior", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, Messages: []Message{{Role: "user", Content: "original"}}}
	err := registry.ApplyScopes(context.Background(), &request, []TransformBinding{binding}, daemonTransformScope())
	if err == nil || !strings.Contains(err.Error(), "without declaring effect") {
		t.Fatalf("undeclared mutation was accepted: %v", err)
	}
	if request.Messages[0].Content != "original" {
		t.Fatalf("failed transform leaked its partial mutation: %#v", request.Messages)
	}
}

func TestResponseTransformRegistrationNeedsAnEnabledMatchingScope(t *testing.T) {
	registry := NewResponseTransformRegistry()
	if err := registry.Register(responseMarkTransform{}); err != nil {
		t.Fatal(err)
	}
	modelScope := TransformScope{Kind: TransformScopeModel, ID: "junior"}
	binding := TransformBinding{ID: "model.junior.mark", TransformRef: extensions.Ref{Kind: ResponseTransformKind, ID: "fixture.response-mark.v1", ContractVersion: 1}, Enabled: true, Scope: modelScope}
	if registry.Active([]TransformBinding{binding}, daemonTransformScope()) || !registry.Active([]TransformBinding{binding}, modelScope) {
		t.Fatal("response transform activation ignored its model scope")
	}
	event := ResponseEvent{Kind: EventTextDelta, Text: "answer"}
	unchanged, err := registry.ApplyScopes(context.Background(), event, []TransformBinding{binding}, daemonTransformScope())
	if err != nil || unchanged.Text != "answer" {
		t.Fatalf("unmatched response transform ran: event=%#v err=%v", unchanged, err)
	}
	updated, err := registry.ApplyScopes(context.Background(), event, []TransformBinding{binding}, modelScope)
	if err != nil || updated.Text != "answer!" {
		t.Fatalf("matching response transform did not run: event=%#v err=%v", updated, err)
	}
}
