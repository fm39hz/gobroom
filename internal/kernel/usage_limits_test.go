package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/operations"
)

func TestAttemptUsageLimitsApplyToAccumulatedPartialUpdates(t *testing.T) {
	first := extensions.Ref{Kind: extensions.UsageKind, ID: "fixture.first", ContractVersion: 1}
	second := extensions.Ref{Kind: extensions.UsageKind, ID: "fixture.second", ContractVersion: 1}
	k := responseFailureKernel(t, nil, "unused")
	k.UsageCatalog = fixtureUsageCatalog(t, `{"type":"integer"}`, first, second)
	k.UsageLimits = UsageLimits{MaxValues: 1, MaxExtensionBytes: 1024}
	attempt := k.newUsageAttempt("invocation", Route{}, NormalizedRequest{})
	attempt.observe(UsageEvent{Extensions: []extensions.UsageValue{{TypeRef: first, Value: json.RawMessage(`1`)}}})
	final, err := attempt.complete(context.Background(), UsageEvent{Extensions: []extensions.UsageValue{{TypeRef: second, Value: json.RawMessage(`2`)}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(final.Extensions) != 1 || final.Extensions[0].TypeRef != first || len(final.AccountingIssues) == 0 {
		t.Fatalf("partial accumulation exceeded value bound: %#v", final)
	}
}

func TestAttemptUsageDirectCallbackRespectsOperationBound(t *testing.T) {
	bounds := operations.DefaultResourceBounds()
	bounds.MaxBufferedBytes = 256
	operationSnapshot, _, operation := fixtureUsageOperation(t, extensions.UsageRequirements{}, bounds)
	ref := extensions.Ref{Kind: extensions.UsageKind, ID: "fixture.operation-limited", ContractVersion: 1}
	k := &Kernel{Operations: operationSnapshot, UsageCatalog: fixtureUsageCatalog(t, `{"type":"string"}`, ref), UsageLimits: DefaultUsageLimits()}
	attempt := k.newUsageAttempt("invocation", Route{}, NormalizedRequest{Operation: normalize.Operation(operation.ID), OperationContractVersion: 1})
	payload, _ := json.Marshal(strings.Repeat("x", 1000))
	attempt.observe(UsageEvent{Extensions: []extensions.UsageValue{{TypeRef: ref, Value: payload}}})
	result, err := attempt.complete(context.Background(), UsageEvent{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Extensions) != 0 || len(result.AccountingIssues) == 0 {
		t.Fatalf("direct callback bypassed operation bound: extensions=%d issues=%v", len(result.Extensions), result.AccountingIssues)
	}
}

type callbackOnlyUsageDecoder struct{}

func (callbackOnlyUsageDecoder) ID() string { return "callback-only-usage" }
func (callbackOnlyUsageDecoder) PossibleEvents() []ResponseEventKind {
	return []ResponseEventKind{EventResponseComplete}
}
func (callbackOnlyUsageDecoder) ClassifyError(int, []byte) ErrorClass { return ErrorRetryable }
func (callbackOnlyUsageDecoder) Decode(_ context.Context, _ UpstreamResponse, emit func(ResponseEvent) error, hooks StreamHooks) error {
	hooks.OnProviderUsage(UsageEvent{InputTokens: new(int64(13))})
	hooks.OnComplete(UsageEvent{})
	return emit(ResponseEvent{Kind: EventResponseComplete})
}

func TestAttemptUsageDirectCallbackEntersCompositorAccumulator(t *testing.T) {
	adapter := ComposedAdapter{AdapterID: "callback-only", Response: callbackOnlyUsageDecoder{}, Renderers: map[normalize.Format]ResponseRenderer{normalize.FormatOpenAIChat: usageAccountingRenderer{}}}
	var completed UsageEvent
	if err := adapter.RenderResponse(context.Background(), UpstreamResponse{Status: http.StatusOK, Headers: make(http.Header), Body: http.NoBody}, httptest.NewRecorder(), normalize.FormatOpenAIChat, StreamHooks{OnComplete: func(event UsageEvent) { completed = event }}); err != nil {
		t.Fatal(err)
	}
	if completed.InputTokens == nil || *completed.InputTokens != 13 {
		t.Fatalf("direct callback omitted from terminal accounting: %#v", completed)
	}
}

func TestAttemptUsageCompletionDoesNotInventClientPayloadFields(t *testing.T) {
	adapter := ComposedAdapter{AdapterID: "callback-only", Response: callbackOnlyUsageDecoder{}, Renderers: map[normalize.Format]ResponseRenderer{normalize.FormatOpenAIChat: usageAccountingRenderer{}}}
	var completed UsageEvent
	err := adapter.RenderResponse(context.Background(), UpstreamResponse{Status: http.StatusOK, Headers: make(http.Header), Body: http.NoBody}, httptest.NewRecorder(), normalize.FormatOpenAIChat, StreamHooks{
		CompleteUsage: func(_ context.Context, event UsageEvent) (UsageEvent, error) {
			event.InputTokens = new(int64(99))
			return event, nil
		},
		ValidateEvent: func(_ context.Context, event ResponseEvent) error {
			if event.Kind == EventResponseComplete && event.Usage != nil {
				return errors.New("operation completion contract has no client usage field")
			}
			return nil
		},
		OnComplete: func(event UsageEvent) { completed = event },
	})
	if err != nil {
		t.Fatalf("operational accounting altered client event shape: %v", err)
	}
	if completed.InputTokens == nil || *completed.InputTokens != 99 {
		t.Fatalf("original enriched accounting was lost: %#v", completed)
	}
}

func TestAttemptUsageStagedTerminalHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var emitErr error
	decoder := pipelineFailureDecoder{decode: func(_ context.Context, emit func(ResponseEvent) error, _ StreamHooks) error {
		cancel()
		emitErr = emit(ResponseEvent{Kind: EventResponseComplete})
		return emitErr
	}}
	err := runProviderResponseErrorPipeline(ctx, decoder, pipelineFailureRenderer{}, StreamHooks{})
	if !errors.Is(emitErr, context.Canceled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("terminal callback ignored cancellation: emit=%v pipeline=%v", emitErr, err)
	}
}

func TestAttemptUsageBoundsIncludeSerializedEnvelopeAndRejectInvalidSiblings(t *testing.T) {
	ref := extensions.Ref{Kind: extensions.UsageKind, ID: "fixture.units", ContractVersion: 1}
	k := &Kernel{UsageCatalog: fixtureUsageCatalog(t, `{"type":"integer"}`, ref)}
	value := extensions.UsageValue{TypeRef: ref, Value: json.RawMessage(`0`)}
	encoded, err := json.Marshal([]extensions.UsageValue{value})
	if err != nil {
		t.Fatal(err)
	}
	for _, offset := range []int64{-1, 0, 1} {
		k.UsageLimits = UsageLimits{MaxValues: 32, MaxExtensionBytes: int64(len(encoded)) + offset}
		got := k.admitUsage(UsageEvent{InputTokens: new(int64(7)), Extensions: []extensions.UsageValue{value}})
		if got.InputTokens == nil || *got.InputTokens != 7 {
			t.Fatal("extension bound erased common sibling")
		}
		if offset < 0 && (len(got.Extensions) != 0 || len(got.AccountingIssues) == 0) {
			t.Fatalf("admitted oversized encoded envelope: %#v", got)
		}
		if offset >= 0 && (len(got.Extensions) != 1 || len(got.AccountingIssues) != 0) {
			t.Fatalf("rejected value at/under boundary: %#v", got)
		}
	}
	k.UsageLimits = DefaultUsageLimits()
	invalid := value
	invalid.Value = json.RawMessage(`"private-invalid-shape"`)
	got := k.admitUsage(UsageEvent{Extensions: []extensions.UsageValue{invalid, value}})
	if len(got.Extensions) != 1 || string(got.Extensions[0].Value) != `0` || len(got.AccountingIssues) == 0 {
		t.Fatalf("invalid schema poisoned valid sibling: %#v", got)
	}
	got = k.admitUsage(UsageEvent{Extensions: []extensions.UsageValue{value, value}})
	if len(got.Extensions) != 1 || len(got.AccountingIssues) == 0 {
		t.Fatalf("duplicate identity admitted: %#v", got)
	}
}

func TestAttemptUsageUsesCatalogIdentityAndEnvelopeBoundWithoutExtraNameLimit(t *testing.T) {
	ref := extensions.Ref{Kind: extensions.UsageKind, ID: "fixture." + strings.Repeat("unit", 100), ContractVersion: 1}
	k := &Kernel{UsageCatalog: fixtureUsageCatalog(t, `{"type":"integer"}`, ref)}
	got := k.admitUsage(UsageEvent{Extensions: []extensions.UsageValue{{TypeRef: ref, Value: json.RawMessage(`1`)}}})
	if len(got.Extensions) != 1 || len(got.AccountingIssues) != 0 {
		t.Fatalf("registered, bounded identity rejected by an extra name limit: extensions=%d issues=%v", len(got.Extensions), got.AccountingIssues)
	}
}

func TestAttemptUsageRetainedRawJSONFitsEncodedBudget(t *testing.T) {
	ref := extensions.Ref{Kind: extensions.UsageKind, ID: "fixture.retained", ContractVersion: 1}
	k := &Kernel{UsageCatalog: fixtureUsageCatalog(t, `{"type":"integer"}`, ref), UsageLimits: UsageLimits{MaxValues: 32, MaxExtensionBytes: 256}}
	got := k.admitUsage(UsageEvent{Extensions: []extensions.UsageValue{{TypeRef: ref, Value: json.RawMessage(strings.Repeat(" ", 170) + "1")}}})
	encoded, err := json.Marshal(got.Extensions)
	if err != nil {
		t.Fatal(err)
	}
	retained := len(encoded)
	for _, value := range got.Extensions {
		canonical, err := json.Marshal(value.Value)
		if err != nil {
			t.Fatal(err)
		}
		retained += len(value.Value) - len(canonical)
	}
	if retained > 256 {
		t.Fatalf("encoded envelope fit but retained raw buffers exceeded host budget: %d", retained)
	}
	if len(got.Extensions) != 1 {
		t.Fatalf("bounded numeric value lost: %#v", got)
	}
}
