package kernel

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/normalize"
)

type mutatingUsageEnricher struct{}

func (mutatingUsageEnricher) EnrichUsage(_ context.Context, _ Route, _ UsageEvidence, event UsageEvent) (UsageEvent, error) {
	*event.InputTokens = 999
	if len(event.Extensions) > 0 {
		event.Extensions[0].Value = []byte("mutated")
	}
	return event, errors.New("enrichment rejected")
}

type ownedUsageAdapter struct {
	fail          bool
	unknownOutput bool
}

func (ownedUsageAdapter) ID() string { return "owned-usage" }
func (ownedUsageAdapter) PlanCompatibility(input CompatibilityContext) CompatibilityPlan {
	return fixtureCompatibilityPlan(input, normalize.FormatOpenAIChat, FidelityNative)
}
func (ownedUsageAdapter) Prepare(context.Context, NormalizedRequest, Route, Credential) (UpstreamRequest, error) {
	return UpstreamRequest{Method: http.MethodPost, URL: "test://owned-usage"}, nil
}
func (ownedUsageAdapter) Execute(context.Context, UpstreamRequest) (UpstreamResponse, error) {
	return UpstreamResponse{Status: http.StatusOK, Headers: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
}
func (ownedUsageAdapter) ClassifyError(int, []byte) ErrorClass { return ErrorRetryable }
func (a ownedUsageAdapter) RenderResponse(_ context.Context, _ UpstreamResponse, _ http.ResponseWriter, _ normalize.Format, hooks StreamHooks) error {
	usage := UsageEvent{InputTokens: new(int64(17)), Extensions: []extensions.UsageValue{{TypeRef: extensions.Ref{Kind: extensions.UsageKind, ID: "test", ContractVersion: 1}, Value: []byte(`"original"`)}}}
	if !a.unknownOutput {
		usage.OutputTokens = new(int64(9))
	}
	if hooks.OnProviderUsage != nil {
		hooks.OnProviderUsage(usage)
	}
	if a.fail {
		return errors.New("render failed")
	}
	if hooks.OnComplete != nil {
		hooks.OnComplete(usage)
	}
	return nil
}

func ownedUsageKernel(t *testing.T, adapter ownedUsageAdapter, enricher UsageEnricher) *Kernel {
	t.Helper()
	usageRef := extensions.Ref{Kind: "usage_source", ID: "mutating", ContractVersion: 1}
	snapshot, err := BuildSnapshot(SnapshotInput{
		PublicModels: []PublicModel{{Name: "model", TargetRef: "model"}},
		Nodes:        []ModelNode{{ID: "model", Kind: ModelPhysical, Members: []MemberRef{{Kind: MemberRoute, ID: "route", Fidelity: FidelityExact}}}},
		Routes: []Route{{ID: "route", Protocol: ProtocolOpenAIChat, Enabled: true, OperationBindings: map[normalize.Operation]RouteOperationBinding{
			normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"owned-usage"}, UsageSourceRef: usageRef},
		}}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	k, err := New(snapshot, nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k.Close)
	k.Adapters["owned-usage"] = adapter
	k.UsageCatalog = fixtureUsageCatalog(t, `{"type":"string"}`, extensions.Ref{Kind: extensions.UsageKind, ID: "test", ContractVersion: 1})
	if enricher != nil {
		k.UsageSources[usageRef] = enricher
	}
	return k
}

func runOwnedUsage(t *testing.T, k *Kernel) error {
	t.Helper()
	return k.Execute(context.Background(), NormalizedRequest{Model: "model", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, SourceFormat: normalize.FormatOpenAIChat}, Credential{}, httptest.NewRecorder())
}

func TestUsageEnrichmentMutationRejectedOnSuccess(t *testing.T) {
	k := ownedUsageKernel(t, ownedUsageAdapter{}, mutatingUsageEnricher{})
	if err := runOwnedUsage(t, k); err != nil {
		t.Fatal(err)
	}
	got := <-k.Events
	if got.InputTokens == nil || *got.InputTokens != 17 || len(got.Extensions) != 1 || string(got.Extensions[0].Value) != `"original"` {
		t.Fatalf("rejected enrichment mutated successful usage: %+v", got)
	}
}

func TestUsageEnrichmentMutationRejectedOnRenderFailure(t *testing.T) {
	k := ownedUsageKernel(t, ownedUsageAdapter{fail: true}, mutatingUsageEnricher{})
	if err := runOwnedUsage(t, k); err == nil {
		t.Fatal("expected render failure")
	}
	got := <-k.Events
	if got.InputTokens == nil || *got.InputTokens != 17 || len(got.Extensions) != 1 || string(got.Extensions[0].Value) != `"original"` {
		t.Fatalf("rejected enrichment mutated failed usage: %+v", got)
	}
}

type directUsageCallbackDecoder struct{}

func (directUsageCallbackDecoder) ID() string { return "direct-usage-callback" }
func (directUsageCallbackDecoder) PossibleEvents() []ResponseEventKind {
	return []ResponseEventKind{EventResponseComplete}
}
func (directUsageCallbackDecoder) ClassifyError(int, []byte) ErrorClass { return ErrorRetryable }
func (directUsageCallbackDecoder) Decode(_ context.Context, _ UpstreamResponse, emit func(ResponseEvent) error, hooks StreamHooks) error {
	usage := UsageEvent{InputTokens: new(int64(13))}
	hooks.OnProviderUsage(usage)
	return emit(ResponseEvent{Kind: EventResponseComplete, Usage: &usage})
}

func TestDirectProviderUsageCallbackCannotMutateDecoderCompletion(t *testing.T) {
	adapter := ComposedAdapter{AdapterID: "direct-callback", Response: directUsageCallbackDecoder{}, Renderers: map[normalize.Format]ResponseRenderer{normalize.FormatOpenAIChat: usageAccountingRenderer{}}}
	var rendered UsageEvent
	err := adapter.RenderResponse(context.Background(), UpstreamResponse{Status: http.StatusOK, Headers: make(http.Header), Body: http.NoBody}, httptest.NewRecorder(), normalize.FormatOpenAIChat, StreamHooks{
		OnProviderUsage: func(event UsageEvent) { *event.InputTokens = 99 },
		OnComplete:      func(event UsageEvent) { rendered = event },
	})
	if err != nil {
		t.Fatal(err)
	}
	if rendered.InputTokens == nil || *rendered.InputTokens != 13 {
		t.Fatalf("decoder completion was changed through provider callback: %+v", rendered)
	}
}

type prefilledThroughputEnricher struct{}

func (prefilledThroughputEnricher) EnrichUsage(_ context.Context, _ Route, _ UsageEvidence, event UsageEvent) (UsageEvent, error) {
	event.OutputTokensPerSecond = new(float64(123))
	return event, nil
}

func TestSuccessClearsThroughputWhenOutputUsageUnknown(t *testing.T) {
	k := ownedUsageKernel(t, ownedUsageAdapter{unknownOutput: true}, prefilledThroughputEnricher{})
	if err := runOwnedUsage(t, k); err != nil {
		t.Fatal(err)
	}
	got := <-k.Events
	if got.OutputTokensPerSecond != nil {
		t.Fatalf("throughput=%v, want unknown when output tokens are unknown", *got.OutputTokensPerSecond)
	}
}
