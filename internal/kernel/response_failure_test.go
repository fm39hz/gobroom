package kernel

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/fm39hz/gobroom/internal/normalize"
)

type responseFailureAdapter struct {
	id         string
	ctx        context.Context
	origin     ResponseFailureOrigin
	invoke     bool
	complete   bool
	write      bool
	ignore     bool
	waitExec   bool
	waitRender bool
	called     *int
}

func (a responseFailureAdapter) ID() string { return a.id }
func (responseFailureAdapter) PlanCompatibility(input CompatibilityContext) CompatibilityPlan {
	return fixtureCompatibilityPlan(input, normalize.FormatOpenAIChat, FidelityNative)
}
func (a responseFailureAdapter) Prepare(context.Context, NormalizedRequest, Route, Credential) (UpstreamRequest, error) {
	return UpstreamRequest{Method: http.MethodPost, URL: "test://response-failure"}, nil
}
func (a responseFailureAdapter) Execute(ctx context.Context, _ UpstreamRequest) (UpstreamResponse, error) {
	if a.waitExec {
		<-ctx.Done()
		return UpstreamResponse{}, ctx.Err()
	}
	return UpstreamResponse{Status: http.StatusOK, Body: io.NopCloser(strings.NewReader("provider body"))}, nil
}
func (responseFailureAdapter) ClassifyError(int, []byte) ErrorClass { return ErrorRetryable }
func (a responseFailureAdapter) RenderResponse(ctx context.Context, _ UpstreamResponse, w http.ResponseWriter, _ normalize.Format, hooks StreamHooks) error {
	if a.called != nil {
		*a.called++
	}
	if a.write {
		_, _ = io.WriteString(w, "partial")
	}
	if a.waitRender {
		<-ctx.Done()
		return WrapResponseFailure(ResponseFailureProvider, ctx.Err())
	}
	if a.ctx != nil {
		// Cancel synchronously inside rendering so the test has a deterministic
		// cancellation point relative to response commitment and completion.
		if cancel, ok := a.ctx.Value(responseFailureCancelKey{}).(context.CancelFunc); ok {
			cancel()
		}
	}
	err := errors.New("fixture render failure")
	if a.origin != ResponseFailureUnknown {
		err = WrapResponseFailure(a.origin, err)
	}
	if a.invoke && hooks.OnError != nil {
		hooks.OnError(err)
	}
	if a.complete && hooks.OnComplete != nil {
		hooks.OnComplete(UsageEvent{Status: "ok"})
	}
	if a.ignore {
		return nil
	}
	return err
}

type responseFailureCancelKey struct{}

type responseFailureGate struct {
	outcomes []ClassifiedOutcome
	usage    []UsageEvent
	acquired int
	released int
}

func (*responseFailureGate) Usable(Route, time.Time) bool { return true }
func (g *responseFailureGate) ObserveOutcome(_ Route, outcome ClassifiedOutcome) {
	g.outcomes = append(g.outcomes, outcome)
}
func (g *responseFailureGate) ObserveUsage(_ Route, event UsageEvent) {
	g.usage = append(g.usage, event)
}
func (g *responseFailureGate) Acquire(Route, time.Time) bool { g.acquired++; return true }
func (g *responseFailureGate) Release(Route)                 { g.released++ }

func responseFailureKernel(t *testing.T, gate Gate, adapterIDs ...string) *Kernel {
	t.Helper()
	members := make([]MemberRef, 0, len(adapterIDs))
	routes := make([]Route, 0, len(adapterIDs))
	for _, id := range adapterIDs {
		routeID := "route-" + id
		members = append(members, MemberRef{Kind: MemberRoute, ID: routeID, Fidelity: FidelityExact})
		routes = append(routes, Route{ID: routeID, Protocol: ProtocolOpenAIChat, Enabled: true, OperationBindings: map[normalize.Operation]RouteOperationBinding{
			normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{id}},
		}})
	}
	snapshot, err := BuildSnapshot(SnapshotInput{
		PublicModels: []PublicModel{{Name: "model", TargetRef: "model"}},
		Nodes:        []ModelNode{{ID: "model", Kind: ModelCombo, Strategy: StrategyFallback, Members: members}},
		Routes:       routes,
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	k, err := New(snapshot, gate, 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k.Close)
	return k
}

func runResponseFailure(k *Kernel, ctx context.Context) error {
	return k.Execute(ctx, NormalizedRequest{Model: "model", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, SourceFormat: normalize.FormatOpenAIChat}, Credential{}, httptest.NewRecorder())
}

func TestKernelCanceledRenderDoesNotCompleteUsage(t *testing.T) {
	for _, afterWrite := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_commit", true: "after_bytes"}[afterWrite], func(t *testing.T) {
			gate := &responseFailureGate{}
			k := responseFailureKernel(t, gate, "cancel")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx = context.WithValue(ctx, responseFailureCancelKey{}, context.CancelFunc(cancel))
			k.Adapters["cancel"] = responseFailureAdapter{id: "cancel", ctx: ctx, complete: true, write: afterWrite, ignore: true}
			err := runResponseFailure(k, ctx)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Execute error=%v, want context.Canceled", err)
			}
			if len(gate.usage) != 0 || len(gate.outcomes) != 0 {
				t.Fatalf("canceled request recorded completion: usage=%d outcomes=%d", len(gate.usage), len(gate.outcomes))
			}
			if gate.acquired != gate.released {
				t.Fatalf("scheduler admission leaked: acquired=%d released=%d", gate.acquired, gate.released)
			}
		})
	}
}

func TestKernelOperationDeadlineIsAttributedToOperation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := &responseFailureGate{}
		k := responseFailureKernel(t, gate, "deadline")
		k.Adapters["deadline"] = responseFailureAdapter{id: "deadline", waitExec: true}

		err := runResponseFailure(k, context.Background())
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Execute error=%v, want context.DeadlineExceeded", err)
		}
		if len(gate.outcomes) != 1 {
			t.Fatalf("deadline outcomes=%#v, want one operation timeout", gate.outcomes)
		}
		outcome := gate.outcomes[0]
		if outcome.Cause != CauseTimeout || outcome.Scope != ScopeOperation {
			t.Fatalf("deadline outcome=%#v, want timeout at operation scope", outcome)
		}
		if len(gate.usage) != 0 {
			t.Fatalf("deadline recorded %d usage events", len(gate.usage))
		}
		if gate.acquired != gate.released {
			t.Fatalf("scheduler admission leaked: acquired=%d released=%d", gate.acquired, gate.released)
		}
	})
}

func TestKernelCallerDeadlineIsNeutral(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := &responseFailureGate{}
		k := responseFailureKernel(t, gate, "caller-deadline")
		k.Adapters["caller-deadline"] = responseFailureAdapter{id: "caller-deadline", waitExec: true}
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		defer cancel()

		err := runResponseFailure(k, ctx)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Execute error=%v, want context.DeadlineExceeded", err)
		}
		if len(gate.outcomes) != 0 || len(gate.usage) != 0 {
			t.Fatalf("caller deadline affected health or usage: outcomes=%#v usage=%#v", gate.outcomes, gate.usage)
		}
		if gate.acquired != gate.released {
			t.Fatalf("scheduler admission leaked: acquired=%d released=%d", gate.acquired, gate.released)
		}
	})
}

func TestKernelProviderRenderTimeoutKeepsAcceptedEffect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := &responseFailureGate{}
		k := responseFailureKernel(t, gate, "render-deadline")
		k.Adapters["render-deadline"] = responseFailureAdapter{id: "render-deadline", waitRender: true}

		err := runResponseFailure(k, context.Background())
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Execute error=%v, want context.DeadlineExceeded", err)
		}
		if len(gate.outcomes) != 1 {
			t.Fatalf("render timeout outcomes=%#v, want one provider timeout", gate.outcomes)
		}
		outcome := gate.outcomes[0]
		if outcome.Cause != CauseTimeout || outcome.Scope != ScopeOperation || outcome.Effect != EffectAccepted {
			t.Fatalf("render timeout outcome=%#v, want timeout at operation scope with accepted effect", outcome)
		}
		if len(gate.usage) != 0 {
			t.Fatalf("render timeout recorded %d usage events", len(gate.usage))
		}
	})
}

func TestKernelTypedLocalRenderFailuresAreNeutralAndStopFallback(t *testing.T) {
	for _, origin := range []ResponseFailureOrigin{ResponseFailureTransform, ResponseFailureRenderer, ResponseFailureClient} {
		t.Run(string(origin), func(t *testing.T) {
			gate := &responseFailureGate{}
			k := responseFailureKernel(t, gate, "first", "second")
			calls := 0
			k.Adapters["first"] = responseFailureAdapter{id: "first", origin: origin, invoke: true, called: &calls}
			k.Adapters["second"] = responseFailureAdapter{id: "second", called: &calls}
			err := runResponseFailure(k, context.Background())
			var failure *ResponseFailure
			if !errors.As(err, &failure) || failure.Origin != origin {
				t.Fatalf("Execute error=%v, want typed origin %q", err, origin)
			}
			if calls != 1 {
				t.Fatalf("rendered adapters=%d, want no cross-route fallback", calls)
			}
			if len(gate.outcomes) != 0 || len(gate.usage) != 0 {
				t.Fatalf("local failure changed provider health or usage: outcomes=%d usage=%d", len(gate.outcomes), len(gate.usage))
			}
		})
	}
}

func TestKernelTypedProviderRenderFailureObservesOnceAndStopsFallback(t *testing.T) {
	gate := &responseFailureGate{}
	k := responseFailureKernel(t, gate, "provider", "second")
	calls := 0
	k.Adapters["provider"] = responseFailureAdapter{id: "provider", origin: ResponseFailureProvider, invoke: true, called: &calls}
	k.Adapters["second"] = responseFailureAdapter{id: "second", called: &calls}
	err := runResponseFailure(k, context.Background())
	var failure *ResponseFailure
	if !errors.As(err, &failure) || failure.Origin != ResponseFailureProvider {
		t.Fatalf("Execute error=%v, want provider failure", err)
	}
	if calls != 1 {
		t.Fatalf("rendered adapters=%d, want accepted response to stop fallback", calls)
	}
	if len(gate.outcomes) != 1 || gate.outcomes[0].Scope != ScopeOperation {
		t.Fatalf("provider failure outcomes=%#v, want exactly one operation observation", gate.outcomes)
	}
	if gate.acquired != gate.released {
		t.Fatalf("scheduler admission leaked: acquired=%d released=%d", gate.acquired, gate.released)
	}
}

func TestKernelRenderFailureDiscardsStagedCompletion(t *testing.T) {
	for _, test := range []struct {
		origin           ResponseFailureOrigin
		wantObservations int
	}{
		{origin: ResponseFailureProvider, wantObservations: 1},
		{origin: ResponseFailureRenderer, wantObservations: 0},
	} {
		t.Run(string(test.origin), func(t *testing.T) {
			gate := &responseFailureGate{}
			k := responseFailureKernel(t, gate, "failure")
			k.Adapters["failure"] = responseFailureAdapter{
				id: "failure", origin: test.origin, invoke: true, complete: true,
			}

			err := runResponseFailure(k, context.Background())
			var failure *ResponseFailure
			if !errors.As(err, &failure) || failure.Origin != test.origin {
				t.Fatalf("Execute error=%v, want typed %q failure", err, test.origin)
			}
			if len(gate.usage) != 0 {
				t.Fatalf("failed render recorded %d usage events", len(gate.usage))
			}
			if len(gate.outcomes) != test.wantObservations {
				t.Fatalf("failed render outcomes=%#v, want %d provider-failure observations", gate.outcomes, test.wantObservations)
			}
			for _, outcome := range gate.outcomes {
				if outcome.Cause != CauseStreamFailure && outcome.Cause != CauseProtocol {
					t.Fatalf("failed render recorded non-failure health outcome: %#v", outcome)
				}
			}
		})
	}
}

func TestKernelUntypedRenderFailureIsNeutralAndNeverReplayed(t *testing.T) {
	gate := &responseFailureGate{}
	k := responseFailureKernel(t, gate, "first", "second")
	calls := 0
	k.Adapters["first"] = responseFailureAdapter{id: "first", write: true, called: &calls}
	k.Adapters["second"] = responseFailureAdapter{id: "second", called: &calls}
	err := runResponseFailure(k, context.Background())
	if err == nil {
		t.Fatal("untyped render failure should remain an error")
	}
	if calls != 1 || len(gate.outcomes) != 0 || len(gate.usage) != 0 {
		t.Fatalf("untyped adapter failure was replayed or attributed upstream: calls=%d outcomes=%d usage=%d", calls, len(gate.outcomes), len(gate.usage))
	}
}
