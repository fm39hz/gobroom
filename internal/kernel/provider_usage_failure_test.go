package kernel

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/normalize"
)

type providerUsageFailureAdapter struct {
	id     string
	mode   string
	usage  UsageEvent
	cancel context.CancelFunc
	calls  *int
}

func (a providerUsageFailureAdapter) ID() string { return a.id }
func (providerUsageFailureAdapter) PlanCompatibility(input CompatibilityContext) CompatibilityPlan {
	return fixtureCompatibilityPlan(input, normalize.FormatOpenAIChat, FidelityNative)
}
func (providerUsageFailureAdapter) Prepare(context.Context, NormalizedRequest, Route, Credential) (UpstreamRequest, error) {
	return UpstreamRequest{Method: http.MethodPost, URL: "test://provider-usage-failure"}, nil
}
func (providerUsageFailureAdapter) Execute(context.Context, UpstreamRequest) (UpstreamResponse, error) {
	return UpstreamResponse{Status: http.StatusOK, Body: io.NopCloser(strings.NewReader("provider body"))}, nil
}
func (providerUsageFailureAdapter) ClassifyError(int, []byte) ErrorClass { return ErrorRetryable }
func (a providerUsageFailureAdapter) RenderResponse(_ context.Context, _ UpstreamResponse, _ http.ResponseWriter, _ normalize.Format, hooks StreamHooks) error {
	if a.calls != nil {
		*a.calls++
	}
	if hooks.OnProviderUsage != nil {
		hooks.OnProviderUsage(a.usage)
	}
	switch a.mode {
	case "transform-fault":
		err := WrapResponseFailure(ResponseFailureTransform, errors.New("fixture transform fault"))
		if hooks.OnError != nil {
			hooks.OnError(err)
		}
		return err
	case "complete-then-fault":
		if hooks.OnComplete != nil {
			hooks.OnComplete(a.usage)
		}
		return WrapResponseFailure(ResponseFailureProvider, errors.New("fixture terminal fault"))
	case "complete-then-cancel":
		if hooks.OnComplete != nil {
			hooks.OnComplete(a.usage)
		}
		a.cancel()
		return nil
	case "success":
		if hooks.OnComplete != nil {
			hooks.OnComplete(a.usage)
		}
		return nil
	default:
		return errors.New("unknown fixture mode")
	}
}

func executeProviderUsageFailure(k *Kernel, ctx context.Context) error {
	return k.Execute(ctx, NormalizedRequest{Model: "model", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, SourceFormat: normalize.FormatOpenAIChat}, Credential{}, httptest.NewRecorder())
}

func assertOneUsageEvent(t *testing.T, k *Kernel, wantStatus string, want UsageEvent) {
	t.Helper()
	select {
	case got := <-k.Events:
		if got.Status != wantStatus || !sameUsageCounter(got.InputTokens, want.InputTokens) || !sameUsageCounter(got.OutputTokens, want.OutputTokens) || !sameUsageCost(got.EstimatedCost, want.EstimatedCost) {
			t.Fatalf("usage event=%+v, want status=%q and provider counters %+v", got, wantStatus, want)
		}
	default:
		t.Fatalf("missing %q usage event", wantStatus)
	}
	select {
	case extra := <-k.Events:
		t.Fatalf("duplicate usage event: %+v", extra)
	default:
	}
}

func assertNoUsageEvent(t *testing.T, k *Kernel) {
	t.Helper()
	select {
	case got := <-k.Events:
		t.Fatalf("unexpected fabricated usage event: %+v", got)
	default:
	}
}

func TestProviderUsageSurvivesTransformFaultWithoutSuccessObservation(t *testing.T) {
	gate := &responseFailureGate{}
	k := responseFailureKernel(t, gate, "first", "second")
	usage := UsageEvent{InputTokens: new(int64(17)), OutputTokens: new(int64(9)), EstimatedCost: new(float64(0.004))}
	calls := 0
	k.Adapters["first"] = providerUsageFailureAdapter{id: "first", mode: "transform-fault", usage: usage, calls: &calls}
	k.Adapters["second"] = providerUsageFailureAdapter{id: "second", calls: &calls}

	err := executeProviderUsageFailure(k, context.Background())
	var failure *ResponseFailure
	if !errors.As(err, &failure) || failure.Origin != ResponseFailureTransform {
		t.Fatalf("Execute error=%v, want transform failure", err)
	}
	if calls != 1 {
		t.Fatalf("rendered adapters=%d, want no fallback after accepted upstream effect", calls)
	}
	assertOneUsageEvent(t, k, "error", usage)
	for _, outcome := range gate.outcomes {
		if outcome.Cause == CauseSuccess {
			t.Fatalf("local response fault recorded success outcome: %+v", outcome)
		}
	}
	if len(gate.usage) != 0 {
		t.Fatalf("local response fault updated performance usage: %+v", gate.usage)
	}
}

func TestProviderUsageSurvivesCancellationAfterAdapterCompletion(t *testing.T) {
	gate := &responseFailureGate{}
	k := responseFailureKernel(t, gate, "provider-usage-failure")
	usage := UsageEvent{InputTokens: new(int64(23)), OutputTokens: new(int64(5)), EstimatedCost: new(float64(0.002))}
	ctx, cancel := context.WithCancel(context.Background())
	k.Adapters["provider-usage-failure"] = providerUsageFailureAdapter{id: "provider-usage-failure", mode: "complete-then-cancel", usage: usage, cancel: cancel}

	err := executeProviderUsageFailure(k, ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute error=%v, want context.Canceled", err)
	}
	assertOneUsageEvent(t, k, "cancelled", usage)
	for _, outcome := range gate.outcomes {
		if outcome.Cause == CauseSuccess {
			t.Fatalf("canceled request recorded success outcome: %+v", outcome)
		}
	}
	if len(gate.usage) != 0 {
		t.Fatalf("canceled request updated performance usage: %+v", gate.usage)
	}
}

func TestProviderCompletionIsStagedUntilTerminalReturn(t *testing.T) {
	gate := &responseFailureGate{}
	k := responseFailureKernel(t, gate, "provider-usage-failure")
	usage := UsageEvent{InputTokens: new(int64(31)), OutputTokens: new(int64(12)), EstimatedCost: new(float64(0.006))}
	k.Adapters["provider-usage-failure"] = providerUsageFailureAdapter{id: "provider-usage-failure", mode: "complete-then-fault", usage: usage}

	err := executeProviderUsageFailure(k, context.Background())
	if err == nil {
		t.Fatal("terminal provider fault was lost")
	}
	assertOneUsageEvent(t, k, "error", usage)
	for _, outcome := range gate.outcomes {
		if outcome.Cause == CauseSuccess {
			t.Fatalf("failed attempt recorded success outcome: %+v", outcome)
		}
	}
	if len(gate.usage) != 0 {
		t.Fatalf("failed attempt updated performance usage: %+v", gate.usage)
	}
}

func TestProviderUsageHappyCompletionStillEmitsOnce(t *testing.T) {
	gate := &responseFailureGate{}
	k := responseFailureKernel(t, gate, "provider-usage-failure")
	usage := UsageEvent{InputTokens: new(int64(8)), OutputTokens: new(int64(3)), EstimatedCost: new(float64(0.001))}
	k.Adapters["provider-usage-failure"] = providerUsageFailureAdapter{id: "provider-usage-failure", mode: "success", usage: usage}

	if err := executeProviderUsageFailure(k, context.Background()); err != nil {
		t.Fatalf("Execute error=%v", err)
	}
	assertOneUsageEvent(t, k, "ok", usage)
	if len(gate.usage) != 1 || !sameUsageCounter(gate.usage[0].InputTokens, usage.InputTokens) || !sameUsageCounter(gate.usage[0].OutputTokens, usage.OutputTokens) {
		t.Fatalf("successful performance usage=%+v, want one provider usage observation", gate.usage)
	}
	if len(gate.outcomes) != 1 || gate.outcomes[0].Cause != CauseSuccess {
		t.Fatalf("successful outcomes=%+v, want exactly one success", gate.outcomes)
	}
}

func sameUsageCounter(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameUsageCost(left, right *float64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func TestUnknownProviderUsageRecordsFailedDispatchWithoutFabricatingCounters(t *testing.T) {
	gate := &responseFailureGate{}
	k := responseFailureKernel(t, gate, "provider-usage-failure")
	k.Adapters["provider-usage-failure"] = providerUsageFailureAdapter{id: "provider-usage-failure", mode: "transform-fault"}

	if err := executeProviderUsageFailure(k, context.Background()); err == nil {
		t.Fatal("expected transform fault")
	}
	assertOneUsageEvent(t, k, "error", UsageEvent{})
}
