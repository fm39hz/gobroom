package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
type artifactMutationTransform struct{}
type safeFailOpenRequestTransform struct{}
type safeFailOpenEffectTransform struct{}
type safeFailOpenResponseTransform struct{}
type boundedSlowResponseTransform struct{}
type boundedRequestTransform struct{}
type oversizedRequestOutputTransform struct{}
type oversizedResponseOutputTransform struct{}
type cancelRequestTransform struct{ cancel context.CancelFunc }
type cancelResponseTransform struct{ cancel context.CancelFunc }
type requestMutationFixture struct {
	id      string
	effects []TransformEffect
	mutate  func(*NormalizedRequest)
}
type responseOpaqueMutationFixture struct{}
type responseMutationFixture struct {
	id      string
	effects []ResponseTransformEffect
	mutate  func(ResponseEvent) ResponseEvent
}
type cancellingKernelResponseTransform struct {
	cancel       context.CancelFunc
	cancelOnCall int
	calls        int
}
type kernelResponseTransformAdapter struct{ attempts *[]string }

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

func (safeFailOpenRequestTransform) Definition() TransformDefinition {
	return TransformDefinition{Ref: extensions.Ref{Kind: RequestTransformKind, ID: "fixture.fail-open.apply", ContractVersion: 1}, ImplementationVersion: "1", Label: "Best effort request", Description: "Fixture failure-open request transform.", Stage: TransformBeforeRequirements, Effects: []TransformEffect{TransformInput}, FailureModes: []TransformFailureMode{TransformSafeFailOpen}}
}
func (safeFailOpenRequestTransform) Apply(_ context.Context, request *NormalizedRequest, _ json.RawMessage) error {
	request.Messages[0].Content = "partial mutation that must roll back"
	return errors.New("sensitive internal detail must not enter the report")
}

func (safeFailOpenEffectTransform) Definition() TransformDefinition {
	return TransformDefinition{Ref: extensions.Ref{Kind: RequestTransformKind, ID: "fixture.fail-open.effect", ContractVersion: 1}, ImplementationVersion: "1", Label: "Best effort effect", Description: "Fixture undeclared effect.", Stage: TransformBeforeRequirements, Effects: []TransformEffect{TransformInput}, FailureModes: []TransformFailureMode{TransformSafeFailOpen}}
}
func (safeFailOpenEffectTransform) Apply(_ context.Context, request *NormalizedRequest, _ json.RawMessage) error {
	value := 0.5
	request.Generation.Temperature = &value
	return nil
}

func (safeFailOpenResponseTransform) Definition() ResponseTransformDefinition {
	return ResponseTransformDefinition{Ref: extensions.Ref{Kind: ResponseTransformKind, ID: "fixture.fail-open.response", ContractVersion: 1}, ImplementationVersion: "1", Label: "Best effort response", Description: "Fixture response safe failure.", Effects: []ResponseTransformEffect{ResponseEffectText}, FailureModes: []TransformFailureMode{TransformSafeFailOpen}}
}
func (safeFailOpenResponseTransform) ApplyResponse(_ context.Context, event ResponseEvent, _ json.RawMessage) (ResponseEvent, error) {
	event.Text = "partially rewritten"
	return event, errors.New("rendering failed")
}

func (boundedSlowResponseTransform) Definition() ResponseTransformDefinition {
	return ResponseTransformDefinition{Ref: extensions.Ref{Kind: ResponseTransformKind, ID: "fixture.bounded-slow-response", ContractVersion: 1}, ImplementationVersion: "1", Label: "Bounded slow response", Description: "Fixture for transform deadlines.", ResourceBounds: extensions.ResourceBounds{MaxInputBytes: 1024, MaxOutputBytes: 1024, MaxBufferedBytes: 1024, DeadlineMillis: 1}, Effects: []ResponseTransformEffect{ResponseEffectText}, FailureModes: []TransformFailureMode{TransformSafeFailOpen}}
}

func (boundedSlowResponseTransform) ApplyResponse(ctx context.Context, event ResponseEvent, _ json.RawMessage) (ResponseEvent, error) {
	timer := time.NewTimer(5 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return event, ctx.Err()
	case <-timer.C:
		return event, nil
	}
}

func (boundedRequestTransform) Definition() TransformDefinition {
	return TransformDefinition{Ref: extensions.Ref{Kind: RequestTransformKind, ID: "fixture.bounded-request", ContractVersion: 1}, ImplementationVersion: "1", Label: "Bounded request", Description: "Fixture for transform input bounds.", Stage: TransformBeforeRequirements, ResourceBounds: extensions.ResourceBounds{MaxInputBytes: 1, MaxOutputBytes: 1024, MaxBufferedBytes: 1024, DeadlineMillis: 50}, Effects: []TransformEffect{TransformInput}, FailureModes: []TransformFailureMode{TransformSafeFailOpen}}
}

func (boundedRequestTransform) Apply(_ context.Context, request *NormalizedRequest, _ json.RawMessage) error {
	request.Messages[0].Content = "must not be applied"
	return nil
}

func (oversizedRequestOutputTransform) Definition() TransformDefinition {
	return TransformDefinition{Ref: extensions.Ref{Kind: RequestTransformKind, ID: "fixture.oversized-request-output", ContractVersion: 1}, ImplementationVersion: "1", Label: "Oversized request output", Description: "Fixture for transform output bounds.", Stage: TransformBeforeRequirements, ResourceBounds: extensions.ResourceBounds{MaxInputBytes: 1024, MaxOutputBytes: 80, MaxBufferedBytes: 1024, DeadlineMillis: 50}, Effects: []TransformEffect{TransformInput}, FailureModes: []TransformFailureMode{TransformSafeFailOpen}}
}

func (oversizedRequestOutputTransform) Apply(_ context.Context, request *NormalizedRequest, _ json.RawMessage) error {
	request.Messages[0].Content = strings.Repeat("x", 256)
	return nil
}

func (oversizedResponseOutputTransform) Definition() ResponseTransformDefinition {
	return ResponseTransformDefinition{Ref: extensions.Ref{Kind: ResponseTransformKind, ID: "fixture.oversized-response-output", ContractVersion: 1}, ImplementationVersion: "1", Label: "Oversized response output", Description: "Fixture for transform output bounds.", ResourceBounds: extensions.ResourceBounds{MaxInputBytes: 1024, MaxOutputBytes: 80, MaxBufferedBytes: 1024, DeadlineMillis: 50}, Effects: []ResponseTransformEffect{ResponseEffectText}, FailureModes: []TransformFailureMode{TransformSafeFailOpen}}
}

func (oversizedResponseOutputTransform) ApplyResponse(_ context.Context, event ResponseEvent, _ json.RawMessage) (ResponseEvent, error) {
	event.Text = strings.Repeat("x", 256)
	return event, nil
}

func (transform cancelRequestTransform) Definition() TransformDefinition {
	return TransformDefinition{Ref: extensions.Ref{Kind: RequestTransformKind, ID: "fixture.cancel-request", ContractVersion: 1}, ImplementationVersion: "1", Label: "Cancel request chain", Description: "Fixture for atomic cancellation.", Stage: TransformBeforeRequirements, Effects: []TransformEffect{TransformInput}}
}

func (transform cancelRequestTransform) Apply(_ context.Context, request *NormalizedRequest, _ json.RawMessage) error {
	request.Messages[0].Content = "partial mutation"
	transform.cancel()
	return nil
}

func (transform cancelResponseTransform) Definition() ResponseTransformDefinition {
	return ResponseTransformDefinition{Ref: extensions.Ref{Kind: ResponseTransformKind, ID: "fixture.cancel-response", ContractVersion: 1}, ImplementationVersion: "1", Label: "Cancel response transform", Description: "Fixture for cancellation fail-closed behavior.", Effects: []ResponseTransformEffect{ResponseEffectText}}
}

func (transform cancelResponseTransform) ApplyResponse(_ context.Context, event ResponseEvent, _ json.RawMessage) (ResponseEvent, error) {
	event.Text = "partial mutation"
	transform.cancel()
	return event, nil
}

func (transform requestMutationFixture) Definition() TransformDefinition {
	return TransformDefinition{Ref: extensions.Ref{Kind: RequestTransformKind, ID: transform.id, ContractVersion: 1}, ImplementationVersion: "1", Label: "Request mutation fixture", Description: "Fixture for exhaustive transform effect validation.", Stage: TransformBeforeRequirements, Effects: transform.effects}
}

func (transform requestMutationFixture) Apply(_ context.Context, request *NormalizedRequest, _ json.RawMessage) error {
	transform.mutate(request)
	return nil
}

func (responseOpaqueMutationFixture) Definition() ResponseTransformDefinition {
	return ResponseTransformDefinition{Ref: extensions.Ref{Kind: ResponseTransformKind, ID: "fixture.opaque-response-mutation", ContractVersion: 1}, ImplementationVersion: "1", Label: "Opaque response mutation", Description: "Fixture for opaque response protection.", Effects: []ResponseTransformEffect{ResponseEffectText}, FailureModes: []TransformFailureMode{TransformSafeFailOpen}}
}

func (responseOpaqueMutationFixture) ApplyResponse(_ context.Context, event ResponseEvent, _ json.RawMessage) (ResponseEvent, error) {
	event.Opaque = json.RawMessage(`{"signature":"rewritten"}`)
	return event, nil
}

func (transform responseMutationFixture) Definition() ResponseTransformDefinition {
	return ResponseTransformDefinition{Ref: extensions.Ref{Kind: ResponseTransformKind, ID: transform.id, ContractVersion: 1}, ImplementationVersion: "1", Label: "Response mutation fixture", Description: "Fixture for response effect validation.", Effects: transform.effects}
}

func (transform responseMutationFixture) ApplyResponse(_ context.Context, event ResponseEvent, _ json.RawMessage) (ResponseEvent, error) {
	return transform.mutate(event), nil
}

func (kernelResponseTransformAdapter) ID() string { return "kernel-response-transform-cancel" }
func (kernelResponseTransformAdapter) PlanCompatibility(input CompatibilityContext) CompatibilityPlan {
	return fixtureCompatibilityPlan(input, normalize.FormatOpenAIChat, FidelityNative)
}
func (kernelResponseTransformAdapter) Prepare(_ context.Context, _ NormalizedRequest, route Route, _ Credential) (UpstreamRequest, error) {
	return UpstreamRequest{Method: http.MethodPost, URL: route.ID}, nil
}
func (adapter kernelResponseTransformAdapter) Execute(_ context.Context, request UpstreamRequest) (UpstreamResponse, error) {
	*adapter.attempts = append(*adapter.attempts, request.URL)
	return UpstreamResponse{Status: http.StatusOK, Body: io.NopCloser(strings.NewReader("response"))}, nil
}
func (kernelResponseTransformAdapter) ClassifyError(int, []byte) ErrorClass { return ErrorRetryable }
func (kernelResponseTransformAdapter) RenderResponse(ctx context.Context, _ UpstreamResponse, writer http.ResponseWriter, _ normalize.Format, hooks StreamHooks) error {
	for index, text := range []string{"first", "second"} {
		transformCtx := ctx
		if index > 0 {
			transformCtx = withResponseOutputStarted(ctx)
		}
		event := ResponseEvent{At: time.Now(), Kind: EventTextDelta, Text: text}
		if hooks.TransformResponse != nil {
			updated, err := hooks.TransformResponse(transformCtx, event)
			if err != nil {
				if hooks.OnError != nil {
					hooks.OnError(err)
				}
				return err
			}
			event = updated
		}
		if index == 0 && hooks.OnFirstByte != nil {
			hooks.OnFirstByte(event.At)
		}
		if hooks.OnEvent != nil {
			hooks.OnEvent(event)
		}
		if _, err := io.WriteString(writer, event.Text); err != nil {
			return err
		}
	}
	if hooks.OnComplete != nil {
		hooks.OnComplete(UsageEvent{Status: "ok"})
	}
	return nil
}

func (transform *cancellingKernelResponseTransform) Definition() ResponseTransformDefinition {
	return ResponseTransformDefinition{Ref: extensions.Ref{Kind: ResponseTransformKind, ID: "fixture.kernel-response-cancel", ContractVersion: 1}, ImplementationVersion: "1", Label: "Kernel response cancellation", Description: "Cancel at a selected semantic event.", Effects: []ResponseTransformEffect{ResponseEffectText}, FailureModes: []TransformFailureMode{TransformSafeFailOpen}}
}

func (transform *cancellingKernelResponseTransform) ApplyResponse(_ context.Context, event ResponseEvent, _ json.RawMessage) (ResponseEvent, error) {
	transform.calls++
	event.Text += "!"
	if transform.calls == transform.cancelOnCall {
		transform.cancel()
	}
	return event, nil
}

func TestTransformsEnforcePayloadAndDeadlineBounds(t *testing.T) {
	scope := daemonTransformScope()
	requestTransform := boundedRequestTransform{}
	requestRegistry := NewRequestTransformRegistry()
	if err := requestRegistry.Register(requestTransform); err != nil {
		t.Fatal(err)
	}
	request := NormalizedRequest{Model: "m", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, Messages: []Message{{Role: "user", Content: "x"}}}
	requestBindings := []TransformBinding{{ID: "bounded-request", TransformRef: requestTransform.Definition().Ref, Enabled: true, Scope: scope, FailureMode: TransformSafeFailOpen}}
	report, err := requestRegistry.ApplyScopesWithReport(context.Background(), &request, requestBindings, scope)
	if err != nil || request.Messages[0].Content != "x" || len(report.Failures) != 1 || report.Failures[0].Reason != TransformFailureBudget {
		t.Fatalf("request input bound not applied transactionally: request=%#v report=%#v err=%v", request, report, err)
	}

	responseTransform := boundedSlowResponseTransform{}
	responseRegistry := NewResponseTransformRegistry()
	if err := responseRegistry.Register(responseTransform); err != nil {
		t.Fatal(err)
	}
	event := ResponseEvent{Kind: EventTextDelta, Text: "original"}
	responseBindings := []TransformBinding{{ID: "bounded-response", TransformRef: responseTransform.Definition().Ref, Enabled: true, Scope: scope, FailureMode: TransformSafeFailOpen}}
	updated, report, err := responseRegistry.ApplyScopesWithReport(context.Background(), event, responseBindings, scope)
	if err != nil || updated.Text != event.Text || len(report.Failures) != 1 || report.Failures[0].Reason != TransformFailureDeadline {
		t.Fatalf("response deadline not handled as safe fail-open: updated=%#v report=%#v err=%v", updated, report, err)
	}
}

func TestTransformsRejectOversizedOutputsTransactionally(t *testing.T) {
	scope := daemonTransformScope()
	requestTransform := oversizedRequestOutputTransform{}
	requestRegistry := NewRequestTransformRegistry()
	if err := requestRegistry.Register(requestTransform); err != nil {
		t.Fatal(err)
	}
	request := NormalizedRequest{Model: "m", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, Messages: []Message{{Role: "user", Content: "original"}}}
	requestBindings := []TransformBinding{{ID: "large-request-output", TransformRef: requestTransform.Definition().Ref, Enabled: true, Scope: scope, FailureMode: TransformSafeFailOpen}}
	report, err := requestRegistry.ApplyScopesWithReport(context.Background(), &request, requestBindings, scope)
	if err != nil || request.Messages[0].Content != "original" || len(report.Failures) != 1 || report.Failures[0].Reason != TransformFailureBudget {
		t.Fatalf("oversized request output was published: request=%#v report=%#v err=%v", request, report, err)
	}

	responseTransform := oversizedResponseOutputTransform{}
	responseRegistry := NewResponseTransformRegistry()
	if err := responseRegistry.Register(responseTransform); err != nil {
		t.Fatal(err)
	}
	event := ResponseEvent{Kind: EventTextDelta, Text: "original"}
	responseBindings := []TransformBinding{{ID: "large-response-output", TransformRef: responseTransform.Definition().Ref, Enabled: true, Scope: scope, FailureMode: TransformSafeFailOpen}}
	updated, report, err := responseRegistry.ApplyScopesWithReport(context.Background(), event, responseBindings, scope)
	if err != nil || updated.Text != event.Text || len(report.Failures) != 1 || report.Failures[0].Reason != TransformFailureBudget {
		t.Fatalf("oversized response output was published: updated=%#v report=%#v err=%v", updated, report, err)
	}
}

func TestTransformCancellationIsFailClosedAndAtomicAcrossChains(t *testing.T) {
	scope := daemonTransformScope()
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	requestTransform := cancelRequestTransform{cancel: cancelRequest}
	requestRegistry := NewRequestTransformRegistry()
	for _, transform := range []RequestTransform{promptCompressionFixture{}, requestTransform} {
		if err := requestRegistry.Register(transform); err != nil {
			t.Fatal(err)
		}
	}
	request := NormalizedRequest{Model: "m", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, Messages: []Message{{Role: "user", Content: "original"}}}
	requestBindings := []TransformBinding{
		{ID: "first-success", TransformRef: promptCompressionFixture{}.Definition().Ref, Enabled: true, Scope: scope},
		{ID: "cancel-request", TransformRef: requestTransform.Definition().Ref, Enabled: true, Scope: scope, FailureMode: TransformSafeFailOpen, Order: 1},
	}
	if report, err := requestRegistry.ApplyScopesWithReport(requestCtx, &request, requestBindings, scope); !errors.Is(err, context.Canceled) || len(report.Failures) != 0 || request.Messages[0].Content != "original" {
		t.Fatalf("request cancellation published a partial result or failed open: request=%#v report=%#v err=%v", request, report, err)
	}

	responseCtx, cancelResponse := context.WithCancel(context.Background())
	responseTransform := cancelResponseTransform{cancel: cancelResponse}
	responseRegistry := NewResponseTransformRegistry()
	if err := responseRegistry.Register(responseTransform); err != nil {
		t.Fatal(err)
	}
	event := ResponseEvent{Kind: EventTextDelta, Text: "original"}
	responseBindings := []TransformBinding{{ID: "cancel-response", TransformRef: responseTransform.Definition().Ref, Enabled: true, Scope: scope, FailureMode: TransformSafeFailOpen}}
	if updated, report, err := responseRegistry.ApplyScopesWithReport(responseCtx, event, responseBindings, scope); !errors.Is(err, context.Canceled) || len(report.Failures) != 0 || updated.Text != "" {
		t.Fatalf("response cancellation was published or failed open: updated=%#v report=%#v err=%v", updated, report, err)
	}
}

func TestKernelDoesNotDispatchWhenRequestTransformCancels(t *testing.T) {
	transformCtx, cancel := context.WithCancel(context.Background())
	transform := cancelRequestTransform{cancel: cancel}
	snapshot, err := BuildSnapshot(SnapshotInput{
		PublicModels: []PublicModel{{Name: "physical", TargetRef: "physical"}},
		Nodes:        []ModelNode{{ID: "physical", Kind: ModelPhysical, Strategy: StrategyFallback, Members: []MemberRef{{Kind: MemberRoute, ID: "route", Fidelity: FidelityExact}}}},
		Routes: []Route{{ID: "route", Enabled: true, OperationBindings: map[normalize.Operation]RouteOperationBinding{
			normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"scoped-probe"}},
		}}},
		TransformBindings: []TransformBinding{{ID: "daemon.cancel", TransformRef: transform.Definition().Ref, Enabled: true, Scope: daemonTransformScope(), FailureMode: TransformSafeFailOpen}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	k, err := New(snapshot, nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	if err := k.Transforms.Register(transform); err != nil {
		t.Fatal(err)
	}
	var attempts []string
	k.Adapters["scoped-probe"] = scopedProbeAdapter{attempts: &attempts}
	request := NormalizedRequest{Model: "physical", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, SourceFormat: normalize.FormatOpenAIChat, Messages: []Message{{Role: "user", Content: "hello"}}}
	if err := k.Execute(transformCtx, request, Credential{}, httptest.NewRecorder()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled request transform returned %v", err)
	}
	if len(attempts) != 0 {
		t.Fatalf("kernel dispatched after transform cancellation: %v", attempts)
	}
}

func TestKernelResponseTransformCancellationStopsFallbackBeforeAndAfterCommit(t *testing.T) {
	for _, test := range []struct {
		name         string
		cancelOnCall int
		wantBody     string
	}{
		{name: "before commit", cancelOnCall: 1, wantBody: ""},
		{name: "after first event commit", cancelOnCall: 2, wantBody: "first!"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			transform := &cancellingKernelResponseTransform{cancel: cancel, cancelOnCall: test.cancelOnCall}
			binding := TransformBinding{ID: "daemon.cancel-response", TransformRef: transform.Definition().Ref, Enabled: true, Scope: daemonTransformScope(), FailureMode: TransformSafeFailOpen}
			snapshot, err := BuildSnapshot(SnapshotInput{
				PublicModels: []PublicModel{{Name: "role", TargetRef: "role"}},
				Nodes:        []ModelNode{{ID: "role", Kind: ModelCombo, Strategy: StrategyFallback, Members: []MemberRef{{Kind: MemberRoute, ID: "route-a"}, {Kind: MemberRoute, ID: "route-b"}}}},
				Routes: []Route{
					{ID: "route-a", Enabled: true, Protocol: ProtocolOpenAIChat, OperationBindings: map[normalize.Operation]RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"kernel-response-transform-cancel"}}}},
					{ID: "route-b", Enabled: true, Protocol: ProtocolOpenAIChat, OperationBindings: map[normalize.Operation]RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"kernel-response-transform-cancel"}}}},
				},
				TransformBindings: []TransformBinding{binding},
			}, 1)
			if err != nil {
				t.Fatal(err)
			}
			engine, err := New(snapshot, nil, 4)
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			if err := engine.ResponseTransforms.Register(transform); err != nil {
				t.Fatal(err)
			}
			var attempts []string
			engine.Adapters["kernel-response-transform-cancel"] = kernelResponseTransformAdapter{attempts: &attempts}
			writer := httptest.NewRecorder()
			err = engine.Execute(ctx, NormalizedRequest{Model: "role", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, SourceFormat: normalize.FormatOpenAIChat}, Credential{}, writer)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("response transform cancellation returned %v", err)
			}
			if !reflect.DeepEqual(attempts, []string{"route-a"}) {
				t.Fatalf("kernel switched fallback after cancellation: %v", attempts)
			}
			if writer.Body.String() != test.wantBody {
				t.Fatalf("response bytes=%q want %q", writer.Body.String(), test.wantBody)
			}
			select {
			case usage := <-engine.Events:
				t.Fatalf("cancelled response emitted a usage completion: %#v", usage)
			default:
			}
		})
	}
}

func TestRequestTransformSafeFailOpenRollsBackAndReportsWithoutErrorText(t *testing.T) {
	registry := NewRequestTransformRegistry()
	for _, transform := range []RequestTransform{safeFailOpenRequestTransform{}, safeFailOpenEffectTransform{}} {
		if err := registry.Register(transform); err != nil {
			t.Fatal(err)
		}
	}
	scope := daemonTransformScope()
	request := NormalizedRequest{Model: "model", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, Messages: []Message{{Role: "user", Content: "original"}}, Raw: map[string]any{"model": "model", "messages": "original"}}
	bindings := []TransformBinding{
		{ID: "apply-failure", TransformRef: safeFailOpenRequestTransform{}.Definition().Ref, Enabled: true, Scope: scope, FailureMode: TransformSafeFailOpen},
		{ID: "effect-failure", TransformRef: safeFailOpenEffectTransform{}.Definition().Ref, Enabled: true, Scope: scope, FailureMode: TransformSafeFailOpen, Order: 1},
	}
	if err := registry.ValidateBindings(bindings); err != nil {
		t.Fatal(err)
	}
	report, err := registry.ApplyScopesWithReport(context.Background(), &request, bindings, scope)
	if err != nil {
		t.Fatal(err)
	}
	if request.Messages[0].Content != "original" || request.Generation.Temperature != nil {
		t.Fatalf("safe fail-open published partial mutations: %#v", request)
	}
	if len(report.Failures) != 2 || report.Failures[0].Reason != TransformFailureApply || report.Failures[1].Reason != TransformFailureEffect {
		t.Fatalf("safe failure report=%#v", report)
	}
	if strings.Contains(fmt.Sprint(report), "sensitive internal detail") {
		t.Fatalf("safe failure report exposed transform error text: %#v", report)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	cancelledRequest := NormalizedRequest{Model: "model", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, Messages: []Message{{Role: "user", Content: "original"}}}
	if report, err := registry.ApplyScopesWithReport(cancelled, &cancelledRequest, bindings[:1], scope); err == nil || len(report.Failures) != 0 || cancelledRequest.Messages[0].Content != "original" {
		t.Fatalf("cancellation was allowed to fail open: request=%#v report=%#v err=%v", cancelledRequest, report, err)
	}
}

func TestTransformCannotFailOpenProtectedIdentityOrUndeclaredDescriptorMode(t *testing.T) {
	registry := NewRequestTransformRegistry()
	transform := transformFailureModeFixture{}
	if err := registry.Register(transform); err != nil {
		t.Fatal(err)
	}
	scope := daemonTransformScope()
	request := NormalizedRequest{Model: "before", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, Raw: map[string]any{"model": "before"}}
	bindings := []TransformBinding{{ID: "identity", TransformRef: transform.Definition().Ref, Enabled: true, Scope: scope, FailureMode: TransformSafeFailOpen}}
	if err := registry.ValidateBindings(bindings); err == nil {
		t.Fatal("safe fail-open was accepted when the implementation did not declare it")
	}
	identityRegistry := NewRequestTransformRegistry()
	identity := failOpenIdentityTransform{}
	if err := identityRegistry.Register(identity); err != nil {
		t.Fatal(err)
	}
	bindings[0].TransformRef = identity.Definition().Ref
	if err := identityRegistry.ValidateBindings(bindings); err != nil {
		t.Fatal(err)
	}
	if report, err := identityRegistry.ApplyScopesWithReport(context.Background(), &request, bindings, scope); err == nil || len(report.Failures) != 0 || request.Model != "before" {
		t.Fatalf("identity violation did not fail closed transactionally: request=%#v report=%#v err=%v", request, report, err)
	}
}

type transformFailureModeFixture struct{}

func (transformFailureModeFixture) Definition() TransformDefinition {
	return TransformDefinition{Ref: extensions.Ref{Kind: RequestTransformKind, ID: "fixture.no-fail-open", ContractVersion: 1}, ImplementationVersion: "1", Label: "No fail open", Description: "Fixture without fail-open permission.", Stage: TransformBeforeRequirements, Effects: []TransformEffect{TransformInput}}
}
func (transformFailureModeFixture) Apply(context.Context, *NormalizedRequest, json.RawMessage) error {
	return nil
}

type failOpenIdentityTransform struct{}

func (failOpenIdentityTransform) Definition() TransformDefinition {
	return TransformDefinition{Ref: extensions.Ref{Kind: RequestTransformKind, ID: "fixture.fail-open-identity", ContractVersion: 1}, ImplementationVersion: "1", Label: "Identity mutation", Description: "Must fail closed despite the binding mode.", Stage: TransformBeforeRequirements, Effects: []TransformEffect{TransformInput}, FailureModes: []TransformFailureMode{TransformSafeFailOpen}}
}
func (failOpenIdentityTransform) Apply(_ context.Context, request *NormalizedRequest, _ json.RawMessage) error {
	request.Model = "after"
	return nil
}

func TestResponseTransformSafeFailOpenOnlyBeforeOutput(t *testing.T) {
	registry := NewResponseTransformRegistry()
	transform := safeFailOpenResponseTransform{}
	if err := registry.Register(transform); err != nil {
		t.Fatal(err)
	}
	scope := daemonTransformScope()
	bindings := []TransformBinding{{ID: "response-best-effort", TransformRef: transform.Definition().Ref, Enabled: true, Scope: scope, FailureMode: TransformSafeFailOpen}}
	if err := registry.ValidateBindings(bindings); err != nil {
		t.Fatal(err)
	}
	original := ResponseEvent{Kind: EventTextDelta, Text: "original"}
	updated, report, err := registry.ApplyScopesWithReport(context.Background(), original, bindings, scope)
	if err != nil || updated.Text != "original" || len(report.Failures) != 1 {
		t.Fatalf("pre-output safe fail-open result=%#v report=%#v err=%v", updated, report, err)
	}
	_, report, err = registry.ApplyScopesWithReport(withResponseOutputStarted(context.Background()), original, bindings, scope)
	if err == nil || len(report.Failures) != 0 {
		t.Fatalf("post-output transform failure did not fail closed: report=%#v err=%v", report, err)
	}
}

func TestKernelRecordsRequestTransformSafeFailOpenInUsagePlan(t *testing.T) {
	transform := safeFailOpenRequestTransform{}
	bindings := []TransformBinding{{
		ID: "daemon.best-effort", TransformRef: transform.Definition().Ref, Enabled: true,
		Scope: daemonTransformScope(), FailureMode: TransformSafeFailOpen,
	}}
	snapshot, err := BuildSnapshot(SnapshotInput{
		PublicModels: []PublicModel{{Name: "junior", TargetRef: "junior"}},
		Routes: []Route{{ID: "route", Enabled: true, Protocol: ProtocolOpenAIChat, OperationBindings: map[normalize.Operation]RouteOperationBinding{
			normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"scoped-probe"}},
		}}},
		Nodes:             []ModelNode{{ID: "junior", Kind: ModelPhysical, Members: []MemberRef{{Kind: MemberRoute, ID: "route", Fidelity: FidelityExact}}}},
		TransformBindings: bindings,
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	k, err := New(snapshot, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	if err := k.Transforms.Register(transform); err != nil {
		t.Fatal(err)
	}
	attempts := []string{}
	k.Adapters["scoped-probe"] = scopedProbeAdapter{attempts: &attempts}
	request := NormalizedRequest{Model: "junior", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, SourceFormat: normalize.FormatOpenAIChat, Messages: []Message{{Role: "user", Content: "original"}}}
	if err := k.Execute(context.Background(), request, Credential{}, httptest.NewRecorder()); err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0] != "route" {
		t.Fatalf("safe fail-open changed the selected request branch: %v", attempts)
	}
	select {
	case event := <-k.Events:
		if event.CompatibilityPlan == nil || len(event.CompatibilityPlan.TransformFailures) != 1 {
			t.Fatalf("usage plan omitted safe-fail-open evidence: %#v", event.CompatibilityPlan)
		}
		failure := event.CompatibilityPlan.TransformFailures[0]
		if failure.BindingID != "daemon.best-effort" || failure.Mode != TransformSafeFailOpen || failure.Reason != TransformFailureApply {
			t.Fatalf("unexpected transform failure evidence: %#v", failure)
		}
	case <-time.After(time.Second):
		t.Fatal("safe fail-open request did not produce a usage plan")
	}
}

func TestFallbackUsageDoesNotIncludeTransformFailureFromRejectedModelBranch(t *testing.T) {
	transform := safeFailOpenRequestTransform{}
	bindings := []TransformBinding{{
		ID: "model.rejected.best-effort", TransformRef: transform.Definition().Ref, Enabled: true,
		Scope: TransformScope{Kind: TransformScopeModel, ID: "physical-a"}, FailureMode: TransformSafeFailOpen,
	}}
	snapshot, err := BuildSnapshot(SnapshotInput{
		PublicModels: []PublicModel{{Name: "role", TargetRef: "role"}},
		Nodes: []ModelNode{
			{ID: "role", Kind: ModelCombo, Strategy: StrategyFallback, Members: []MemberRef{{Kind: MemberModel, ID: "physical-a"}, {Kind: MemberModel, ID: "physical-b"}}},
			{ID: "physical-a", Kind: ModelPhysical, Strategy: StrategyFallback, Members: []MemberRef{{Kind: MemberRoute, ID: "unavailable", Fidelity: FidelityExact}}},
			{ID: "physical-b", Kind: ModelPhysical, Strategy: StrategyFallback, Members: []MemberRef{{Kind: MemberRoute, ID: "selected", Fidelity: FidelityExact}}},
		},
		Routes: []Route{
			{ID: "unavailable", Enabled: false, OperationBindings: map[normalize.Operation]RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"scoped-probe"}}}},
			{ID: "selected", Enabled: true, OperationBindings: map[normalize.Operation]RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"scoped-probe"}}}},
		},
		TransformBindings: bindings,
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	k, err := New(snapshot, nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	if err := k.Transforms.Register(transform); err != nil {
		t.Fatal(err)
	}
	var attempts []string
	k.Adapters["scoped-probe"] = scopedProbeAdapter{attempts: &attempts}
	request := NormalizedRequest{Model: "role", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, SourceFormat: normalize.FormatOpenAIChat, Messages: []Message{{Role: "user", Content: "original"}}}
	if err := k.Execute(context.Background(), request, Credential{}, httptest.NewRecorder()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(attempts, []string{"selected"}) {
		t.Fatalf("fallback selected unexpected provider attempts: %v", attempts)
	}
	select {
	case usage := <-k.Events:
		if usage.CompatibilityPlan == nil || len(usage.CompatibilityPlan.TransformFailures) != 0 {
			t.Fatalf("selected branch inherited transform failure from rejected branch: %#v", usage.CompatibilityPlan)
		}
	case <-time.After(time.Second):
		t.Fatal("selected fallback branch did not emit usage")
	}
}

func (artifactMutationTransform) Definition() ResponseTransformDefinition {
	return ResponseTransformDefinition{Ref: extensions.Ref{Kind: ResponseTransformKind, ID: "fixture.artifact-mutation", ContractVersion: 1}, ImplementationVersion: "1", Label: "Artifact mutation", Description: "Must not rewrite provider artifact provenance.", Effects: []ResponseTransformEffect{ResponseEffectText}}
}
func (artifactMutationTransform) ApplyResponse(_ context.Context, event ResponseEvent, _ json.RawMessage) (ResponseEvent, error) {
	if len(event.Artifacts) > 0 {
		event.Artifacts[0].Role = "rewritten"
	}
	return event, nil
}

func TestResponseTransformCannotRewriteArtifactReference(t *testing.T) {
	registry := NewResponseTransformRegistry()
	transform := artifactMutationTransform{}
	if err := registry.Register(transform); err != nil {
		t.Fatal(err)
	}
	scope := daemonTransformScope()
	event := ResponseEvent{Kind: EventArtifact, Artifacts: []extensions.ArtifactRef{{
		TypeRef: extensions.Ref{Kind: extensions.ArtifactKind, ID: "image", ContractVersion: 1}, Role: "image",
	}}}
	_, err := registry.ApplyScopes(context.Background(), event, []TransformBinding{{
		ID: "mutate-artifact", TransformRef: transform.Definition().Ref, Enabled: true, Scope: scope,
	}}, scope)
	if err == nil || !strings.Contains(err.Error(), "immutable artifact references") {
		t.Fatalf("response transform rewrote artifact provenance: %v", err)
	}
}

func TestTransformRegistriesProjectScopedCompatibilitySteps(t *testing.T) {
	requestRegistry := NewRequestTransformRegistry()
	if err := requestRegistry.Register(promptCompressionFixture{}); err != nil {
		t.Fatal(err)
	}
	responseRegistry := NewResponseTransformRegistry()
	if err := responseRegistry.Register(responseMarkTransform{}); err != nil {
		t.Fatal(err)
	}
	daemonScope := daemonTransformScope()
	modelScope := TransformScope{Kind: TransformScopeModel, ID: "physical"}
	routeScope := TransformScope{Kind: TransformScopeRoute, ID: "route"}
	requestSteps := requestRegistry.Plan([]TransformBinding{
		{ID: "model.compress", TransformRef: promptCompressionFixture{}.Definition().Ref, Enabled: true, Scope: modelScope, Order: 2},
		{ID: "daemon.compress", TransformRef: promptCompressionFixture{}.Definition().Ref, Enabled: true, Scope: daemonScope, Order: 1},
	}, daemonScope, modelScope)
	if len(requestSteps) != 2 || requestSteps[0].BindingID != "daemon.compress" || requestSteps[1].BindingID != "model.compress" || requestSteps[0].Stage != TransformBeforeRequirements || !reflect.DeepEqual(requestSteps[0].Effects, []TransformEffect{TransformInput}) {
		t.Fatalf("request transform compatibility chain=%#v", requestSteps)
	}
	responseSteps := responseRegistry.Plan([]TransformBinding{{
		ID: "route.mark", TransformRef: responseMarkTransform{}.Definition().Ref, Enabled: true, Scope: routeScope,
	}}, daemonScope, routeScope)
	if len(responseSteps) != 1 || responseSteps[0].BindingID != "route.mark" || responseSteps[0].Scope != routeScope || !reflect.DeepEqual(responseSteps[0].Effects, []ResponseTransformEffect{ResponseEffectText}) {
		t.Fatalf("response transform compatibility chain=%#v", responseSteps)
	}
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
		PublicModels: []PublicModel{{Name: "public", TargetRef: "physical"}},
		Nodes:        []ModelNode{{ID: "physical", Kind: ModelPhysical, Members: []MemberRef{{Kind: MemberRoute, ID: "route", Fidelity: FidelityExact}}}},
		Routes:       []Route{{ID: "route", Enabled: true, OperationBindings: map[normalize.Operation]RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"fixture"}}}}},
		TransformBindings: []TransformBinding{
			{ID: "daemon.compress", TransformRef: extensions.Ref{Kind: RequestTransformKind, ID: "fixture.prompt-compression.v1", ContractVersion: 1}, Enabled: true, Scope: daemonTransformScope()},
			{ID: "daemon.response-mark", TransformRef: extensions.Ref{Kind: ResponseTransformKind, ID: "fixture.response-mark.v1", ContractVersion: 1}, Enabled: true, Scope: daemonTransformScope()},
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
	var attempted []string
	var encoded NormalizedRequest
	var planned CompatibilityContext
	k.Adapters["fixture"] = featureRouteAdapter{id: "fixture", used: &attempted, seen: &encoded, planned: &planned}
	if err := k.Transforms.Register(promptCompressionFixture{}); err != nil {
		t.Fatal(err)
	}
	if err := k.ResponseTransforms.Register(responseMarkTransform{}); err != nil {
		t.Fatal(err)
	}
	request := NormalizedRequest{Model: "public", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, SourceFormat: normalize.FormatAnthropic, Messages: []Message{{Role: "user", Content: "very long fixture context"}}}
	if err := k.Execute(context.Background(), request, Credential{}, httptest.NewRecorder()); err != nil {
		t.Fatal(err)
	}
	if len(attempted) != 1 || encoded.Messages[0].Content != "compressed-context" {
		t.Fatalf("transform did not run before provider encoding: routes=%v request=%#v", attempted, encoded)
	}
	if len(planned.RequestTransformSteps) != 1 || planned.RequestTransformSteps[0].BindingID != "daemon.compress" || planned.RequestTransformSteps[0].TransformRef.ID != "fixture.prompt-compression.v1" || planned.RequestTransformSteps[0].Scope != daemonTransformScope() || !reflect.DeepEqual(planned.RequestTransformSteps[0].Effects, []TransformEffect{TransformInput}) {
		t.Fatalf("compatibility context lost the scoped request-transform chain: %#v", planned.RequestTransformSteps)
	}
	if len(planned.ResponseTransformSteps) != 1 || planned.ResponseTransformSteps[0].BindingID != "daemon.response-mark" || planned.ResponseTransformSteps[0].TransformRef.ID != "fixture.response-mark.v1" || planned.ResponseTransformSteps[0].Scope != daemonTransformScope() || !reflect.DeepEqual(planned.ResponseTransformSteps[0].Effects, []ResponseTransformEffect{ResponseEffectText}) {
		t.Fatalf("compatibility context lost the scoped response-transform chain: %#v", planned.ResponseTransformSteps)
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

func TestRequestTransformEffectsAndOpaqueFacetsAreExhaustivelyGuarded(t *testing.T) {
	setMessageContent := func(request *NormalizedRequest) {
		request.Messages[0].Content.([]normalize.ContentPart)[0].Text = "rewritten"
	}
	setPromptContent := func(request *NormalizedRequest) { request.Prompt.Layers[0].Parts[0].Text = "rewritten" }
	setToolDefinition := func(request *NormalizedRequest) {
		request.Tools[0].Function = map[string]any{"name": "search", "description": "updated"}
	}
	setToolArguments := func(request *NormalizedRequest) {
		request.Messages[0].ToolCalls[0].Arguments.(map[string]any)["query"] = "updated"
	}
	setThinkingEffort := func(request *NormalizedRequest) { request.Thinking.Effort = "high" }
	setContinuity := func(request *NormalizedRequest) { request.Continuity.PreviousResponse = "response-1" }
	setOperationInput := func(request *NormalizedRequest) { request.OperationPayload = json.RawMessage(`{"prompt":"rewritten"}`) }
	setModality := func(request *NormalizedRequest) { request.Modalities.Vision = true }
	setRequirement := func(request *NormalizedRequest) {
		request.Requirements = append(request.Requirements, normalize.FeatureRequirement{Ref: extensions.Ref{Kind: "feature", ID: "image", ContractVersion: 1}})
	}
	setExtension := func(request *NormalizedRequest) {
		request.Extensions = map[string]any{"vendor.example/options": map[string]any{"mode": "fast"}}
	}
	setTemperature := func(request *NormalizedRequest) {
		value := 0.4
		request.Generation.Temperature = &value
	}
	setToolChoice := func(request *NormalizedRequest) {
		request.ToolChoice = normalize.ToolChoice{Mode: "required", Set: true, Metadata: request.ToolChoice.Metadata}
	}
	tests := []struct {
		name      string
		effects   []TransformEffect
		mutate    func(*NormalizedRequest)
		wantError string
	}{
		{name: "generation requires options", effects: []TransformEffect{TransformInput}, mutate: setTemperature, wantError: "generation options"},
		{name: "tool choice requires tools", effects: []TransformEffect{TransformInput}, mutate: setToolChoice, wantError: "tool choice"},
		{name: "generation options declared", effects: []TransformEffect{TransformOptions}, mutate: setTemperature},
		{name: "tool choice declared", effects: []TransformEffect{TransformTools}, mutate: setToolChoice},
		{name: "message input declared", effects: []TransformEffect{TransformInput}, mutate: setMessageContent},
		{name: "prompt declared", effects: []TransformEffect{TransformPrompt}, mutate: setPromptContent},
		{name: "tool definition declared", effects: []TransformEffect{TransformTools}, mutate: setToolDefinition},
		{name: "tool arguments declared", effects: []TransformEffect{TransformTools}, mutate: setToolArguments},
		{name: "thinking intent declared", effects: []TransformEffect{TransformThinking}, mutate: setThinkingEffort},
		{name: "continuity declared", effects: []TransformEffect{TransformContinuity}, mutate: setContinuity},
		{name: "operation payload declared", effects: []TransformEffect{TransformInput}, mutate: setOperationInput},
		{name: "modality declared", effects: []TransformEffect{TransformInput}, mutate: setModality},
		{name: "derived requirement declared", effects: []TransformEffect{TransformInput}, mutate: setRequirement},
		{name: "opaque extension immutable", effects: []TransformEffect{TransformOptions}, mutate: setExtension, wantError: "opaque source request extensions"},
		{name: "unsupported evidence immutable", effects: []TransformEffect{TransformInput}, mutate: func(request *NormalizedRequest) { request.UnsupportedFacets = nil }, wantError: "unsupported-facet evidence"},
		{name: "unsupported generation evidence immutable", effects: []TransformEffect{TransformOptions}, mutate: func(request *NormalizedRequest) { request.Generation.Unsupported = nil }, wantError: "unsupported generation-option evidence"},
		{name: "thinking source immutable", effects: []TransformEffect{TransformThinking}, mutate: func(request *NormalizedRequest) { request.Thinking.Source = "rewritten" }, wantError: "thinking provenance"},
		{name: "provider session state opaque", effects: []TransformEffect{TransformContinuity}, mutate: func(request *NormalizedRequest) {
			request.Session.ProviderState = json.RawMessage(`{"state":"rewritten"}`)
		}, wantError: "opaque provider session state"},
		{name: "opaque raw extension immutable", effects: []TransformEffect{TransformOptions}, mutate: func(request *NormalizedRequest) {
			request.Raw["vendor_extension"].(map[string]any)["opaque"] = "rewritten"
		}, wantError: "raw client request is immutable"},
		{name: "content metadata immutable", effects: []TransformEffect{TransformInput}, mutate: func(request *NormalizedRequest) {
			request.Messages[0].Content.([]normalize.ContentPart)[0].Metadata["cache_control"] = "rewritten"
		}, wantError: "opaque content structure or metadata"},
		{name: "prompt metadata immutable", effects: []TransformEffect{TransformPrompt}, mutate: func(request *NormalizedRequest) {
			request.Prompt.Layers[0].Parts[0].Metadata["cache_control"] = "rewritten"
		}, wantError: "opaque prompt metadata or content structure"},
		{name: "tool metadata immutable", effects: []TransformEffect{TransformTools}, mutate: func(request *NormalizedRequest) { request.Tools[0].Metadata["vendor"] = "rewritten" }, wantError: "opaque tool metadata"},
		{name: "tool choice metadata immutable", effects: []TransformEffect{TransformTools}, mutate: func(request *NormalizedRequest) { request.ToolChoice.Metadata["vendor"] = "rewritten" }, wantError: "opaque tool-choice metadata"},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transform := requestMutationFixture{id: fmt.Sprintf("fixture.effect-matrix-%d", index), effects: test.effects, mutate: test.mutate}
			registry := NewRequestTransformRegistry()
			if err := registry.Register(transform); err != nil {
				t.Fatal(err)
			}
			request := NormalizedRequest{
				Model: "model", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1,
				Raw:               map[string]any{"model": "model", "vendor_extension": map[string]any{"opaque": "original"}},
				Messages:          []Message{{Role: "user", Content: []normalize.ContentPart{{Type: "text", Text: "original", Metadata: map[string]any{"cache_control": "ephemeral"}}}, ToolCalls: []normalize.ToolCall{{ID: "call-1", Type: "function", Name: "search", Arguments: map[string]any{"query": "before"}}}}},
				Prompt:            normalize.PromptPlan{Layers: []normalize.PromptLayer{{Origin: normalize.PromptProvider, Role: "system", Parts: []normalize.ContentPart{{Type: "text", Text: "instruction", Metadata: map[string]any{"cache_control": "ephemeral"}}}}}},
				Tools:             []normalize.Tool{{Name: "search", Metadata: map[string]any{"vendor": "original"}}},
				ToolChoice:        normalize.ToolChoice{Metadata: map[string]any{"vendor": "original"}},
				UnsupportedFacets: []string{"content.opaque"},
				Generation:        normalize.GenerationOptions{Unsupported: []string{"vendor_option"}},
				Thinking:          normalize.ThinkingIntent{Mode: "level", Effort: "medium", Source: "ingress"},
				Session:           normalize.SessionContext{ProviderState: json.RawMessage(`{"state":"provider-owned"}`)},
			}
			binding := TransformBinding{ID: "matrix", TransformRef: transform.Definition().Ref, Enabled: true, Scope: daemonTransformScope()}
			err := registry.ApplyScopes(context.Background(), &request, []TransformBinding{binding}, daemonTransformScope())
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("declared effect was rejected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("mutation error=%v want substring %q", err, test.wantError)
			}
			if len(request.UnsupportedFacets) != 1 || request.UnsupportedFacets[0] != "content.opaque" || request.Generation.Unsupported[0] != "vendor_option" || request.Messages[0].Content.([]normalize.ContentPart)[0].Metadata["cache_control"] != "ephemeral" || request.Prompt.Layers[0].Parts[0].Metadata["cache_control"] != "ephemeral" || request.Tools[0].Metadata["vendor"] != "original" || request.Raw["vendor_extension"].(map[string]any)["opaque"] != "original" || string(request.Session.ProviderState) != `{"state":"provider-owned"}` {
				t.Fatalf("rejected transform leaked a mutation: %#v", request)
			}
		})
	}
}

func TestRequestTransformCannotAppendOpaqueMessageMetadata(t *testing.T) {
	tests := []struct {
		name    string
		message Message
	}{
		{name: "source message", message: Message{Role: "user", Content: "added", Metadata: map[string]any{"vendor": "opaque"}}},
		{name: "content part", message: Message{Role: "user", Content: []normalize.ContentPart{{Type: "text", Text: "added", Metadata: map[string]any{"cache_control": "ephemeral"}}}}},
		{name: "tool call metadata", message: Message{Role: "assistant", ToolCalls: []normalize.ToolCall{{ID: "call-new", Type: "function", Name: "lookup", Arguments: map[string]any{}, Metadata: map[string]any{"vendor": "opaque"}}}}},
		{name: "tool call provider data", message: Message{Role: "assistant", ToolCalls: []normalize.ToolCall{{ID: "call-new", Type: "function", Name: "lookup", Arguments: map[string]any{}, ProviderData: []byte(`{"opaque":true}`)}}}},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transform := requestMutationFixture{
				id: fmt.Sprintf("fixture.add-opaque-message-%d", index), effects: []TransformEffect{TransformInput},
				mutate: func(request *NormalizedRequest) { request.Messages = append(request.Messages, test.message) },
			}
			registry := NewRequestTransformRegistry()
			if err := registry.Register(transform); err != nil {
				t.Fatal(err)
			}
			request := NormalizedRequest{Model: "model", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, Messages: []Message{{Role: "user", Content: "original"}}}
			original := normalize.CloneRequest(request)
			binding := TransformBinding{ID: "add-opaque", TransformRef: transform.Definition().Ref, Enabled: true, Scope: daemonTransformScope()}
			err := registry.ApplyScopes(context.Background(), &request, []TransformBinding{binding}, daemonTransformScope())
			if err == nil || !strings.Contains(err.Error(), "added message") {
				t.Fatalf("opaque addition was accepted: %v", err)
			}
			if len(request.Messages) != 1 || !reflect.DeepEqual(request.Messages[0], original.Messages[0]) {
				t.Fatalf("rejected message addition leaked into request: %#v", request.Messages)
			}
		})
	}
}

func TestRequestTransformAcceptsCanonicalMessageSuffix(t *testing.T) {
	transform := requestMutationFixture{
		id: "fixture.add-canonical-message", effects: []TransformEffect{TransformInput},
		mutate: func(request *NormalizedRequest) {
			request.Messages = append(request.Messages, Message{Role: "assistant", Content: "added"})
		},
	}
	registry := NewRequestTransformRegistry()
	if err := registry.Register(transform); err != nil {
		t.Fatal(err)
	}
	request := NormalizedRequest{Model: "model", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, Messages: []Message{{Role: "user", Content: "original"}}}
	binding := TransformBinding{ID: "append-canonical", TransformRef: transform.Definition().Ref, Enabled: true, Scope: daemonTransformScope()}
	if err := registry.ApplyScopes(context.Background(), &request, []TransformBinding{binding}, daemonTransformScope()); err != nil {
		t.Fatal(err)
	}
	if len(request.Messages) != 2 || request.Messages[1].Content != "added" || !request.Mutations.Messages {
		t.Fatalf("canonical message suffix was not published: %#v", request)
	}
}

func TestRequestTransformPublishesKernelOwnedTypedMutationMarkers(t *testing.T) {
	transform := requestMutationFixture{
		id: "fixture.track-mutations", effects: []TransformEffect{TransformTools, TransformOptions},
		mutate: func(request *NormalizedRequest) {
			request.ToolChoice = normalize.ToolChoice{Mode: "required", Set: true}
			value := 0.6
			request.Generation.Temperature = &value
		},
	}
	registry := NewRequestTransformRegistry()
	if err := registry.Register(transform); err != nil {
		t.Fatal(err)
	}
	request := NormalizedRequest{Model: "m", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, Raw: map[string]any{"model": "m"}}
	err := registry.ApplyScopes(context.Background(), &request, []TransformBinding{{ID: "track", TransformRef: transform.Definition().Ref, Enabled: true, Scope: daemonTransformScope()}}, daemonTransformScope())
	if err != nil {
		t.Fatal(err)
	}
	if !request.Mutations.ToolChoice || !request.Mutations.GenerationTemperature || request.Mutations.Messages || request.Mutations.Tools {
		t.Fatalf("kernel published incorrect typed mutation set: %#v", request.Mutations)
	}
}

func TestResponseTransformCannotMutateOpaquePayloadEvenWithFailOpen(t *testing.T) {
	registry := NewResponseTransformRegistry()
	transform := responseOpaqueMutationFixture{}
	if err := registry.Register(transform); err != nil {
		t.Fatal(err)
	}
	updated, report, err := registry.ApplyScopesWithReport(context.Background(), ResponseEvent{Kind: EventTextDelta, Text: "visible", Opaque: json.RawMessage(`{"signature":"provider-owned"}`)}, []TransformBinding{{
		ID: "opaque", TransformRef: transform.Definition().Ref, Enabled: true, Scope: daemonTransformScope(), FailureMode: TransformSafeFailOpen,
	}}, daemonTransformScope())
	if err == nil || !errors.Is(err, ErrTransformSafetyViolation) || len(report.Failures) != 0 || len(updated.Opaque) != 0 {
		t.Fatalf("opaque response mutation was accepted or failed open: updated=%#v report=%#v err=%v", updated, report, err)
	}
}

func TestResponseTransformEffectMatrixAcceptsOnlyDeclaredSemanticPayload(t *testing.T) {
	tests := []struct {
		name   string
		event  ResponseEvent
		mutate func(ResponseEvent) ResponseEvent
		effect ResponseTransformEffect
	}{
		{name: "text", event: ResponseEvent{Kind: EventTextDelta, Text: "before"}, mutate: func(event ResponseEvent) ResponseEvent { event.Text = "after"; return event }, effect: ResponseEffectText},
		{name: "thinking", event: ResponseEvent{Kind: EventThinkingDelta, Text: "before"}, mutate: func(event ResponseEvent) ResponseEvent { event.Text = "after"; return event }, effect: ResponseEffectThinking},
		{name: "tool arguments", event: ResponseEvent{Kind: EventToolCallDelta, ToolCallID: "call-1", ToolName: "search", ToolArguments: `{}`}, mutate: func(event ResponseEvent) ResponseEvent { event.ToolArguments = `{"q":"x"}`; return event }, effect: ResponseEffectToolArguments},
		{name: "usage", event: ResponseEvent{Kind: EventUsage, Usage: &UsageEvent{InputTokens: 1}}, mutate: func(event ResponseEvent) ResponseEvent { event.Usage.OutputTokens = 2; return event }, effect: ResponseEffectUsage},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transform := responseMutationFixture{id: fmt.Sprintf("fixture.response-effect-%d", index), effects: []ResponseTransformEffect{test.effect}, mutate: test.mutate}
			registry := NewResponseTransformRegistry()
			if err := registry.Register(transform); err != nil {
				t.Fatal(err)
			}
			updated, err := registry.ApplyScopes(context.Background(), test.event, []TransformBinding{{ID: "effect", TransformRef: transform.Definition().Ref, Enabled: true, Scope: daemonTransformScope()}}, daemonTransformScope())
			if err != nil || reflect.DeepEqual(updated, test.event) {
				t.Fatalf("declared response effect failed to apply: updated=%#v err=%v", updated, err)
			}
		})
	}
}

func TestResponseTransformRejectsUndeclaredSemanticEffect(t *testing.T) {
	transform := responseMutationFixture{
		id: "fixture.response-undeclared-effect", effects: []ResponseTransformEffect{ResponseEffectUsage},
		mutate: func(event ResponseEvent) ResponseEvent { event.Text = "rewritten"; return event },
	}
	registry := NewResponseTransformRegistry()
	if err := registry.Register(transform); err != nil {
		t.Fatal(err)
	}
	updated, err := registry.ApplyScopes(context.Background(), ResponseEvent{Kind: EventTextDelta, Text: "original"}, []TransformBinding{{
		ID: "undeclared", TransformRef: transform.Definition().Ref, Enabled: true, Scope: daemonTransformScope(),
	}}, daemonTransformScope())
	if err == nil || !strings.Contains(err.Error(), "undeclared event content") || updated.Text != "" {
		t.Fatalf("undeclared response effect was accepted or published: updated=%#v err=%v", updated, err)
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
