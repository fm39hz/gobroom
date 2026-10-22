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

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/operations"
)

type attemptUsageFixtureAdapter struct {
	id     string
	status int
	calls  *int
}

type failedAttemptUsageAdapter struct {
	attemptUsageFixtureAdapter
	failure error
	cancel  context.CancelFunc
}

func (a failedAttemptUsageAdapter) Execute(context.Context, UpstreamRequest) (UpstreamResponse, error) {
	*a.calls++
	if a.cancel != nil {
		a.cancel()
	}
	return UpstreamResponse{}, a.failure
}

func TestAttemptUsageTransportFailureAndCancellationRemainUnknown(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "transport_failure", true: "cancelled_dispatch"}[cancelled], func(t *testing.T) {
			k := responseFailureKernel(t, nil, "failed")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			adapter := failedAttemptUsageAdapter{attemptUsageFixtureAdapter: attemptUsageFixtureAdapter{id: "failed", calls: &calls}, failure: errors.New("fixture transport failure")}
			status := "error"
			if cancelled {
				adapter.cancel = cancel
				adapter.failure = context.Canceled
				status = "cancelled"
			}
			k.Adapters["failed"] = adapter
			if err := runResponseFailure(k, ctx); err == nil {
				t.Fatal("failed transport unexpectedly succeeded")
			}
			select {
			case event := <-k.Events:
				if event.Status != status || event.AttemptID == "" || event.InvocationID == "" || event.HasReportedUsage() || calls != 1 {
					t.Fatalf("wrong failed dispatch accounting: calls=%d event=%#v", calls, event)
				}
			default:
				t.Fatal("actual transport dispatch was omitted")
			}
			select {
			case duplicate := <-k.Events:
				t.Fatalf("failed dispatch counted twice: %#v", duplicate)
			default:
			}
		})
	}
}

func TestRequiredUsageFailureCannotReplayAcceptedEffect(t *testing.T) {
	gate := &responseFailureGate{}
	k := responseFailureKernel(t, gate, "first", "second")
	operationSnapshot, frozen, operation := fixtureUsageOperation(t, extensions.UsageRequirements{RequireReported: true}, operations.DefaultResourceBounds())
	k.Operations, k.UsageCatalog = operationSnapshot, frozen
	snapshot := k.Snapshots.Load()
	for id, route := range snapshot.Routes {
		binding := route.OperationBindings[normalize.OperationChatGenerate]
		route.OperationBindings = map[normalize.Operation]RouteOperationBinding{normalize.Operation(operation.ID): binding}
		snapshot.Routes[id] = route
	}
	if err := k.PublishSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	calls := 0
	k.Adapters["first"] = providerUsageFailureAdapter{id: "first", mode: "success", calls: &calls}
	k.Adapters["second"] = providerUsageFailureAdapter{id: "second", mode: "success", calls: &calls}
	err := k.Execute(context.Background(), NormalizedRequest{Model: "model", Operation: normalize.Operation(operation.ID), OperationContractVersion: 1, SourceFormat: normalize.FormatOpenAIChat, OperationPayload: json.RawMessage(`{}`)}, Credential{}, httptest.NewRecorder())
	var replay *ReplaySuppressedError
	if !errors.Is(err, ErrRequiredUsage) || !errors.As(err, &replay) || replay.Effect != EffectAccepted || calls != 1 {
		t.Fatalf("required accounting failure replayed accepted dispatch: calls=%d error=%v", calls, err)
	}
	if len(gate.outcomes) != 0 || len(gate.usage) != 0 {
		t.Fatalf("local accounting failure poisoned health/performance: %#v", gate)
	}
	event := <-k.Events
	if event.Status != "error" || event.InputTokens != nil || event.OutputTokens != nil || event.AttemptID == "" || len(event.AccountingIssues) == 0 {
		t.Fatalf("incomplete attempt evidence missing: %#v", event)
	}
	select {
	case duplicate := <-k.Events:
		t.Fatalf("duplicate finalization: %#v", duplicate)
	default:
	}
}

func TestOptionalUsageIssueDoesNotPoisonHealth(t *testing.T) {
	gate := &responseFailureGate{}
	k := responseFailureKernel(t, gate, "optional")
	calls := 0
	k.Adapters["optional"] = providerUsageFailureAdapter{id: "optional", mode: "success", calls: &calls, usage: UsageEvent{InputTokens: new(int64(7)), OutputTokens: new(int64(-1))}}
	if err := runResponseFailure(k, context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(gate.outcomes) != 1 || gate.outcomes[0].Cause != CauseSuccess || calls != 1 {
		t.Fatalf("optional accounting poisoned health: %#v", gate)
	}
	event := <-k.Events
	if event.InputTokens == nil || *event.InputTokens != 7 || event.OutputTokens != nil || len(event.AccountingIssues) == 0 {
		t.Fatalf("invalid optional metric not isolated: %#v", event)
	}
}

func (a attemptUsageFixtureAdapter) ID() string { return a.id }
func (attemptUsageFixtureAdapter) PlanCompatibility(input CompatibilityContext) CompatibilityPlan {
	return fixtureCompatibilityPlan(input, normalize.FormatOpenAIChat, FidelityNative)
}
func (attemptUsageFixtureAdapter) Prepare(context.Context, NormalizedRequest, Route, Credential) (UpstreamRequest, error) {
	return UpstreamRequest{Method: http.MethodPost, URL: "test://usage-attempt"}, nil
}
func (a attemptUsageFixtureAdapter) Execute(context.Context, UpstreamRequest) (UpstreamResponse, error) {
	*a.calls++
	return UpstreamResponse{Status: a.status, Headers: http.Header{"X-Usage-Input-Tokens": []string{"12"}}, Body: io.NopCloser(strings.NewReader(`{"usage":{"input":12}}`))}, nil
}
func (attemptUsageFixtureAdapter) ClassifyError(int, []byte) ErrorClass { return ErrorRetryable }
func (attemptUsageFixtureAdapter) RenderResponse(_ context.Context, _ UpstreamResponse, _ http.ResponseWriter, _ normalize.Format, hooks StreamHooks) error {
	hooks.OnProviderUsage(UsageEvent{InputTokens: new(int64(7)), InvocationID: "untrusted", AttemptID: "untrusted"})
	hooks.OnProviderUsage(UsageEvent{OutputTokens: new(int64(0))})
	hooks.OnComplete(UsageEvent{OutputTokens: new(int64(0))})
	return nil
}

type attemptUsageFixtureEnricher struct {
	statuses []int
	bodies   []json.RawMessage
}

func (s *attemptUsageFixtureEnricher) EnrichUsage(_ context.Context, _ Route, evidence UsageEvidence, event UsageEvent) (UsageEvent, error) {
	s.statuses = append(s.statuses, evidence.StatusCode)
	s.bodies = append(s.bodies, append(json.RawMessage(nil), evidence.ErrorBody...))
	if evidence.StatusCode == http.StatusTooManyRequests {
		event.InputTokens = new(int64(12))
	}
	event.InvocationID, event.AttemptID = "untrusted-enricher", "untrusted-enricher"
	return event, nil
}

func TestAttemptUsageFinalizesOnceAcrossFailureAndFallback(t *testing.T) {
	gate := &responseFailureGate{}
	k := responseFailureKernel(t, gate, "rejected", "accepted")
	ref := extensions.Ref{Kind: "usage_source", ID: "attempt-evidence", ContractVersion: 1}
	snapshot := k.Snapshots.Load()
	for id, route := range snapshot.Routes {
		binding := route.OperationBindings[normalize.OperationChatGenerate]
		binding.UsageSourceRef = ref
		route.OperationBindings = map[normalize.Operation]RouteOperationBinding{normalize.OperationChatGenerate: binding}
		snapshot.Routes[id] = route
	}
	if err := k.PublishSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	source := &attemptUsageFixtureEnricher{}
	k.UsageSources[ref] = source
	calls := 0
	k.Adapters["rejected"] = attemptUsageFixtureAdapter{id: "rejected", status: http.StatusTooManyRequests, calls: &calls}
	k.Adapters["accepted"] = attemptUsageFixtureAdapter{id: "accepted", status: http.StatusOK, calls: &calls}
	if err := runResponseFailure(k, context.Background()); err != nil {
		t.Fatal(err)
	}
	var events []UsageEvent
	for {
		select {
		case event := <-k.Events:
			events = append(events, event)
		default:
			goto drained
		}
	}
drained:
	if len(events) != 2 || calls != 2 {
		t.Fatalf("events=%#v upstreamCalls=%d; want two actual dispatches", events, calls)
	}
	if events[0].InvocationID == "" || events[0].InvocationID != events[1].InvocationID || events[0].InvocationID == "untrusted-enricher" ||
		events[0].AttemptID == "" || events[0].AttemptID == events[1].AttemptID || events[1].AttemptID == "untrusted-enricher" {
		t.Fatalf("dispatch identity not host-owned: %#v", events)
	}
	if events[0].Status != "error" || events[0].InputTokens == nil || *events[0].InputTokens != 12 || events[0].OutputTokens != nil ||
		events[1].Status != "ok" || events[1].InputTokens == nil || *events[1].InputTokens != 7 || events[1].OutputTokens == nil || *events[1].OutputTokens != 0 {
		t.Fatalf("rejection and success accounting mismatch: %#v", events)
	}
	if len(source.statuses) != 2 || source.statuses[0] != http.StatusTooManyRequests || source.statuses[1] != http.StatusOK ||
		string(source.bodies[0]) != `{"usage":{"input":12}}` || len(source.bodies[1]) != 0 {
		t.Fatalf("wrong response evidence: status=%v bodies=%q", source.statuses, source.bodies)
	}
}
