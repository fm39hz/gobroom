package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/normalize"
)

type precommitFailureAdapter struct {
	name     string
	attempts *[]string
	fail     bool
}

type usageAccountingDecoder struct{}
type usageAccountingRenderer struct{ rendered *UsageEvent }
type usageAccountingTransform struct{}

func (usageAccountingDecoder) ID() string { return "usage-accounting-decoder" }
func (usageAccountingDecoder) PossibleEvents() []ResponseEventKind {
	return []ResponseEventKind{EventTextDelta, EventUsage, EventResponseComplete}
}
func (usageAccountingDecoder) ClassifyError(int, []byte) ErrorClass { return ErrorRetryable }
func (usageAccountingDecoder) Decode(_ context.Context, _ UpstreamResponse, emit func(ResponseEvent) error, _ StreamHooks) error {
	usage := UsageEvent{InputTokens: 11, OutputTokens: 7, EstimatedCost: 0.25, Status: "ok"}
	if err := emit(ResponseEvent{Kind: EventUsage, Usage: &usage}); err != nil {
		return err
	}
	return emit(ResponseEvent{Kind: EventResponseComplete, Usage: &usage})
}
func (usageAccountingRenderer) ID() normalize.Format { return normalize.FormatOpenAIChat }
func (r usageAccountingRenderer) SupportsResponse(options ResponseRenderContext) CompatibilityPlan {
	var mappings [][]FacetMapping
	response := []FacetMapping{{Facet: FacetWireResponse, Disposition: FacetPreserved}}
	policy := CompatibilityPolicy{RequiredFacets: []string{FacetWireResponse}}
	for _, event := range options.RequiredEvents {
		response = append(response, FacetMapping{Facet: ResponseEventFacet(event), Disposition: FacetPreserved})
		policy.RequiredFacets = append(policy.RequiredFacets, ResponseEventFacet(event))
	}
	mappings = append(mappings, response)
	return ComposeCompatibilityPlan(mappings, policy)
}
func (r usageAccountingRenderer) Begin(context.Context, ResponseRenderContext, http.ResponseWriter) (ResponseRenderSession, error) {
	return usageAccountingSession{rendered: r.rendered}, nil
}

type usageAccountingSession struct{ rendered *UsageEvent }

func (s usageAccountingSession) Emit(_ context.Context, event ResponseEvent) error {
	if event.Kind == EventUsage && event.Usage != nil {
		usage := *event.Usage
		*s.rendered = usage
	}
	return nil
}
func (usageAccountingSession) Finish(context.Context, error) error { return nil }
func (usageAccountingTransform) Definition() ResponseTransformDefinition {
	return ResponseTransformDefinition{Ref: extensions.Ref{Kind: ResponseTransformKind, ID: "fixture.usage-projection", ContractVersion: 1}, ImplementationVersion: "1", Label: "Usage projection", Description: "Rewrite client-visible usage only.", Effects: []ResponseTransformEffect{ResponseEffectUsage}}
}
func (usageAccountingTransform) ApplyResponse(_ context.Context, event ResponseEvent, _ json.RawMessage) (ResponseEvent, error) {
	if event.Usage != nil {
		usage := *event.Usage
		usage.InputTokens, usage.OutputTokens, usage.EstimatedCost = 900, 800, 99
		event.Usage = &usage
	}
	return event, nil
}

func TestResponseTransformDoesNotRewriteProviderUsageAccounting(t *testing.T) {
	registry := NewResponseTransformRegistry()
	transform := usageAccountingTransform{}
	if err := registry.Register(transform); err != nil {
		t.Fatal(err)
	}
	transformScope := daemonTransformScope()
	bindings := []TransformBinding{{ID: "usage-projection", TransformRef: transform.Definition().Ref, Enabled: true, Scope: transformScope}}
	var rendered, accounted UsageEvent
	adapter := ComposedAdapter{
		AdapterID: "usage-accounting-fixture", Request: eventDeclarationTestRequest{}, Response: usageAccountingDecoder{},
		Renderers: map[normalize.Format]ResponseRenderer{normalize.FormatOpenAIChat: usageAccountingRenderer{rendered: &rendered}},
	}
	err := adapter.RenderResponse(context.Background(), UpstreamResponse{Status: http.StatusOK, Headers: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, httptest.NewRecorder(), normalize.FormatOpenAIChat, StreamHooks{
		TransformResponse: func(ctx context.Context, event ResponseEvent) (ResponseEvent, error) {
			return registry.ApplyScopes(ctx, event, bindings, transformScope)
		},
		OnComplete: func(event UsageEvent) { accounted = event },
	})
	if err != nil {
		t.Fatal(err)
	}
	if rendered.InputTokens != 900 || rendered.OutputTokens != 800 || rendered.EstimatedCost != 99 {
		t.Fatalf("client projection was not transformed: %#v", rendered)
	}
	if accounted.InputTokens != 11 || accounted.OutputTokens != 7 || accounted.EstimatedCost != 0.25 {
		t.Fatalf("provider accounting was changed by client projection: %#v", accounted)
	}
}

func (a precommitFailureAdapter) ID() string { return a.name }
func (a precommitFailureAdapter) PlanCompatibility(input CompatibilityContext) CompatibilityPlan {
	return fixtureCompatibilityPlan(input, normalize.FormatOpenAIChat, FidelityTranslated)
}
func (a precommitFailureAdapter) Prepare(_ context.Context, _ NormalizedRequest, route Route, _ Credential) (UpstreamRequest, error) {
	*a.attempts = append(*a.attempts, route.ID)
	return UpstreamRequest{Method: http.MethodPost, URL: "fixture://" + route.ID}, nil
}
func (precommitFailureAdapter) Execute(context.Context, UpstreamRequest) (UpstreamResponse, error) {
	return UpstreamResponse{Status: http.StatusOK, Body: io.NopCloser(strings.NewReader("body"))}, nil
}
func (precommitFailureAdapter) ClassifyError(int, []byte) ErrorClass { return ErrorRetryable }
func (a precommitFailureAdapter) RenderResponse(_ context.Context, _ UpstreamResponse, writer http.ResponseWriter, _ normalize.Format, hooks StreamHooks) error {
	if a.fail {
		return errors.New("semantic response decode failed")
	}
	if hooks.OnFirstByte != nil {
		hooks.OnFirstByte(time.Now())
	}
	if _, err := io.WriteString(writer, "rendered"); err != nil {
		return err
	}
	if hooks.OnComplete != nil {
		hooks.OnComplete(UsageEvent{Status: "ok"})
	}
	return nil
}

func TestKernelDoesNotReplayAcceptedResponseAfterSemanticFailure(t *testing.T) {
	snapshot, err := BuildSnapshot(SnapshotInput{
		PublicModels: []PublicModel{{Name: "role", TargetRef: "role"}},
		Nodes:        []ModelNode{{ID: "role", Kind: ModelCombo, Strategy: StrategyFallback, Members: []MemberRef{{Kind: MemberRoute, ID: "first"}, {Kind: MemberRoute, ID: "second"}}}},
		Routes: []Route{
			{ID: "first", Enabled: true, OperationBindings: map[normalize.Operation]RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"decoder-fails"}}}},
			{ID: "second", Enabled: true, OperationBindings: map[normalize.Operation]RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"renderer-succeeds"}}}},
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
	var attempts []string
	k.Adapters["decoder-fails"] = precommitFailureAdapter{name: "decoder-fails", attempts: &attempts, fail: true}
	k.Adapters["renderer-succeeds"] = precommitFailureAdapter{name: "renderer-succeeds", attempts: &attempts}
	writer := httptest.NewRecorder()
	request := NormalizedRequest{Model: "role", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, SourceFormat: normalize.FormatOpenAIChat}
	err = k.Execute(context.Background(), request, Credential{}, writer)
	var suppressed *ReplaySuppressedError
	if !errors.As(err, &suppressed) || suppressed.Effect != EffectAccepted {
		t.Fatalf("accepted upstream response should suppress retry, got %v", err)
	}
	if len(attempts) != 1 || attempts[0] != "first" || writer.Body.String() != "" {
		t.Fatalf("attempts=%v body=%q", attempts, writer.Body.String())
	}
}
