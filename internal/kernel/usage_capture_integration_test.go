package kernel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/normalize"
)

type rawUsageFixtureDecoder struct{}

func (rawUsageFixtureDecoder) ID() string { return "raw-fixture" }
func (rawUsageFixtureDecoder) PossibleEvents() []ResponseEventKind {
	return []ResponseEventKind{EventUsage, EventResponseComplete}
}
func (rawUsageFixtureDecoder) ClassifyError(int, []byte) ErrorClass { return ErrorRetryable }
func (rawUsageFixtureDecoder) Decode(_ context.Context, _ UpstreamResponse, emit func(ResponseEvent) error, hooks StreamHooks) error {
	usage := UsageEvent{InputTokens: new(int64(7)), RawUsage: &RawUsageFragment{ProviderDefinition: hooks.ProviderIssuer, API: "fixture", Value: []byte(`{"input_tokens":7}`)}}
	if err := emit(ResponseEvent{Kind: EventUsage, Usage: &usage}); err != nil {
		return err
	}
	return emit(ResponseEvent{Kind: EventResponseComplete, Usage: &usage})
}

func TestRawUsageStaysOriginalAndNeverReachesClientTransforms(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			var rendered UsageEvent
			adapter := ComposedAdapter{AdapterID: "raw", Response: rawUsageFixtureDecoder{}, Renderers: map[normalize.Format]ResponseRenderer{normalize.FormatOpenAIChat: usageAccountingRenderer{rendered: &rendered}}}
			var original UsageEvent
			clientLeak := false
			check := func(event ResponseEvent) {
				if event.Usage != nil && event.Usage.RawUsage != nil {
					clientLeak = true
				}
			}
			err := adapter.RenderResponse(context.Background(), UpstreamResponse{Status: http.StatusOK, Headers: make(http.Header), Body: http.NoBody}, httptest.NewRecorder(), normalize.FormatOpenAIChat, StreamHooks{
				ProviderIssuer: extensions.Ref{Kind: "provider-definition", ID: "fixture", ContractVersion: 1}, UsageCapture: UsageCapturePolicy{Enabled: enabled, MaxBytes: 1024},
				TransformResponse: func(_ context.Context, event ResponseEvent) (ResponseEvent, error) { check(event); return event, nil }, OnEvent: check,
				OnComplete: func(event UsageEvent) { original = event },
			})
			if err != nil {
				t.Fatal(err)
			}
			if clientLeak {
				t.Fatal("raw original usage reached the client transform/event path")
			}
			if (original.RawUsage != nil) != enabled || original.InputTokens == nil || *original.InputTokens != 7 {
				t.Fatalf("raw host policy/original counters lost: %#v", original)
			}
		})
	}
}
