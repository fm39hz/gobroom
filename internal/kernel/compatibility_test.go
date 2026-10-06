package kernel

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/operations"
)

type eventDeclarationTestDecoder struct{ events []ResponseEventKind }

func (d eventDeclarationTestDecoder) ID() string                           { return "fixture-events" }
func (d eventDeclarationTestDecoder) PossibleEvents() []ResponseEventKind  { return d.events }
func (d eventDeclarationTestDecoder) ClassifyError(int, []byte) ErrorClass { return ErrorRetryable }
func (d eventDeclarationTestDecoder) Decode(context.Context, UpstreamResponse, func(ResponseEvent) error, StreamHooks) error {
	return nil
}

type eventDeclarationTestRenderer struct{}

type eventDeclarationTestRequest struct{}

func (eventDeclarationTestRequest) ID() string { return "fixture-request" }
func (eventDeclarationTestRequest) DescribeCompatibility(CompatibilityContext) []FacetMapping {
	return []FacetMapping{{Facet: FacetWireRequest, Disposition: FacetPreserved}}
}
func (eventDeclarationTestRequest) Prepare(context.Context, NormalizedRequest, Route, Credential) (UpstreamRequest, error) {
	return UpstreamRequest{}, nil
}

func (eventDeclarationTestRenderer) ID() normalize.Format { return normalize.FormatOpenAIChat }
func (eventDeclarationTestRenderer) SupportsResponse(options ResponseRenderContext) CompatibilityPlan {
	report := []FacetMapping{{Facet: FacetWireResponse, Disposition: FacetTranslated}}
	policy := CompatibilityPolicy{RequiredFacets: []string{FacetWireResponse}}
	for _, event := range options.RequiredEvents {
		facet := ResponseEventFacet(event)
		report = append(report, FacetMapping{Facet: facet, Disposition: FacetTranslated})
		policy.RequiredFacets = append(policy.RequiredFacets, facet)
	}
	return ComposeCompatibilityPlan([][]FacetMapping{report}, policy)
}
func (eventDeclarationTestRenderer) Begin(context.Context, ResponseRenderContext, http.ResponseWriter) (ResponseRenderSession, error) {
	return nil, nil
}

func fixtureCompatibilityPlan(input CompatibilityContext, supportedFormat normalize.Format, fidelity CompatibilityFidelity) CompatibilityPlan {
	disposition := FacetTranslated
	if fidelity == FidelityNative {
		disposition = FacetPreserved
	}
	if input.Request.SourceFormat != supportedFormat {
		disposition = FacetUnsupported
	}
	reports := make([][]FacetMapping, 0, 2)
	requestReport := make([]FacetMapping, 0)
	for _, facet := range RequiredRequestFacets(input.Request) {
		requestDisposition := disposition
		if requestDisposition == FacetTranslated && facet == FacetWireRequest {
			requestDisposition = FacetPreserved
		}
		requestReport = append(requestReport, FacetMapping{Facet: facet, Paths: []string{"request"}, Disposition: requestDisposition})
	}
	reports = append(reports, requestReport)
	responseReport := []FacetMapping{{Facet: FacetWireResponse, Paths: []string{"response"}, Disposition: disposition}}
	for _, event := range RequiredResponseEvents(input.Request) {
		responseReport = append(responseReport, FacetMapping{Facet: ResponseEventFacet(event), Paths: []string{"response.events"}, Disposition: disposition})
	}
	reports = append(reports, responseReport)
	policy := CompatibilityPolicy{RequiredFacets: append(RequiredRequestFacets(input.Request), FacetWireResponse)}
	for _, event := range RequiredResponseEvents(input.Request) {
		policy.RequiredFacets = append(policy.RequiredFacets, ResponseEventFacet(event))
	}
	return ComposeCompatibilityPlan(reports, policy)
}

func TestCompatibilityPlanComposesFacetMappingsInStageOrder(t *testing.T) {
	reports := [][]FacetMapping{
		{{Facet: "reasoning.budget", Paths: []string{"request.thinking.budget"}, Disposition: FacetPreserved}},
		{{Facet: "reasoning.budget", Paths: []string{"upstream.thinking.budget"}, Disposition: FacetTranslated}},
		{{Facet: "reasoning.budget", Paths: []string{"client.usage.reasoning_tokens"}, Disposition: FacetPreserved}},
	}
	plan := ComposeCompatibilityPlan(reports, CompatibilityPolicy{RequiredFacets: []string{"reasoning.budget"}})
	if !plan.Supported || plan.Fidelity != FidelityTranslated || len(plan.Mappings) != 1 {
		t.Fatalf("unexpected composed plan: %#v", plan)
	}
	wantPaths := []string{"request.thinking.budget", "upstream.thinking.budget", "client.usage.reasoning_tokens"}
	if !reflect.DeepEqual(plan.Mappings[0].Paths, wantPaths) {
		t.Fatalf("composed semantic paths=%v want=%v", plan.Mappings[0].Paths, wantPaths)
	}
}

func TestCompatibilityPlanFailsClosedForRequiredUnknownOrUnsupportedFacet(t *testing.T) {
	cases := []struct {
		name    string
		reports [][]FacetMapping
	}{
		{name: "unknown"},
		{name: "unsupported", reports: [][]FacetMapping{{{Facet: "tools.history", Disposition: FacetUnsupported}}}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			plan := ComposeCompatibilityPlan(test.reports, CompatibilityPolicy{RequiredFacets: []string{"tools.history"}})
			if plan.Supported || plan.Fidelity != FidelityUnsupported || plan.Reason == "" {
				t.Fatalf("required %s facet was not rejected with an explanation: %#v", test.name, plan)
			}
		})
	}
}

func TestCompatibilityPlanRequiresNamedAndGrantedLosses(t *testing.T) {
	report := [][]FacetMapping{{{Facet: "reasoning.effort", Disposition: FacetDegraded, LossIDs: []string{"reasoning.clamped"}}}}
	policy := CompatibilityPolicy{RequiredFacets: []string{"reasoning.effort"}}
	denied := ComposeCompatibilityPlan(report, policy)
	if denied.Supported || denied.Fidelity != FidelityUnsupported {
		t.Fatalf("ungranted degradation must be rejected: %#v", denied)
	}
	allowed := ComposeCompatibilityPlan(report, CompatibilityPolicy{RequiredFacets: policy.RequiredFacets, AllowedLosses: []string{"reasoning.clamped"}})
	if !allowed.Supported || allowed.Fidelity != FidelityLossy || !reflect.DeepEqual(allowed.Losses, []string{"reasoning.clamped"}) {
		t.Fatalf("explicitly granted loss was not retained in plan: %#v", allowed)
	}
	deniedAgain := ComposeCompatibilityPlan(report, CompatibilityPolicy{RequiredFacets: policy.RequiredFacets, AllowedLosses: []string{"reasoning.clamped"}, DeniedLosses: []string{"reasoning.clamped"}})
	if deniedAgain.Supported {
		t.Fatalf("explicit denial must dominate a grant: %#v", deniedAgain)
	}
}

func TestCompatibilityPlanIgnoresUnrequestedUnsupportedFacet(t *testing.T) {
	plan := ComposeCompatibilityPlan([][]FacetMapping{{{Facet: "audio.input", Disposition: FacetUnsupported}}}, CompatibilityPolicy{})
	if !plan.Supported || plan.Fidelity != FidelityNative {
		t.Fatalf("unrequested facet should not reject a candidate: %#v", plan)
	}
}

func TestCompatibilityPlanRejectsUnscopedDegradationAndInvalidDeclarations(t *testing.T) {
	cases := []struct {
		name string
		mapv FacetMapping
	}{
		{name: "unnamed loss", mapv: FacetMapping{Facet: "tools", Disposition: FacetDegraded}},
		{name: "unknown disposition", mapv: FacetMapping{Facet: "tools", Disposition: "maybe"}},
		{name: "missing facet id", mapv: FacetMapping{Disposition: FacetPreserved}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			plan := ComposeCompatibilityPlan([][]FacetMapping{{test.mapv}}, CompatibilityPolicy{})
			if plan.Supported || plan.Reason == "" {
				t.Fatalf("invalid declaration was accepted: %#v", plan)
			}
		})
	}
}

func TestComposedAdapterRejectsRendererEventMissingFromDecoderContract(t *testing.T) {
	request := NormalizedRequest{Model: "test", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, SourceFormat: normalize.FormatOpenAIChat}
	input := CompatibilityContext{Request: request, Operation: request.Operation}
	input.Policy.RequiredFacets = append(RequiredRequestFacets(request), FacetWireResponse)
	for _, event := range RequiredResponseEvents(request) {
		input.Policy.RequiredFacets = append(input.Policy.RequiredFacets, ResponseEventFacet(event))
	}
	adapter := ComposedAdapter{
		AdapterID: "event-contract-test", Request: eventDeclarationTestRequest{},
		Response:       eventDeclarationTestDecoder{events: []ResponseEventKind{EventTextDelta}},
		Renderers:      map[normalize.Format]ResponseRenderer{normalize.FormatOpenAIChat: eventDeclarationTestRenderer{}},
		ProviderFormat: normalize.Format("fixture.provider"),
	}
	plan := adapter.PlanCompatibility(input)
	if plan.Supported || plan.Fidelity != FidelityUnsupported {
		t.Fatalf("renderer claim overrode missing decoder event contract: %#v", plan)
	}
	for _, mapping := range plan.Mappings {
		if mapping.Facet == ResponseEventFacet(EventResponseComplete) && mapping.Disposition == FacetUnsupported && mapping.Reason != "" {
			return
		}
	}
	t.Fatalf("plan did not retain the missing completion-event reason: %#v", plan)
}

func TestReplaySafetyAllowsPostDispatchReplayOnlyForConfirmedRejection(t *testing.T) {
	if inferUpstreamEffect(http.StatusTooManyRequests, CauseRateLimited) != EffectRejected || inferUpstreamEffect(http.StatusServiceUnavailable, CauseCapacity) != EffectUnknown || inferUpstreamEffect(http.StatusRequestTimeout, CauseRequestInvalid) != EffectUnknown {
		t.Fatal("effect inference must distinguish explicit client rejection from ambiguous server/network outcomes")
	}
	if !canReplayAfterDispatch(operations.ReplayOnRejection, EffectRejected) {
		t.Fatal("confirmed rejection should satisfy the declared replay contract")
	}
	for _, safety := range []operations.ReplaySafety{
		operations.ReplayBeforeDispatch, operations.ReplayWithKey, operations.ReplayNever,
	} {
		if canReplayAfterDispatch(safety, EffectRejected) {
			t.Fatalf("safety %q replayed after dispatch without its required proof/key", safety)
		}
	}
	for _, effect := range []UpstreamEffect{EffectUnknown, EffectAccepted} {
		if canReplayAfterDispatch(operations.ReplayOnRejection, effect) {
			t.Fatalf("replay-on-rejection accepted ambiguous/accepted effect %q", effect)
		}
	}
}
