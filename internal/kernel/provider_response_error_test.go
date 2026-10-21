package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/normalize"
)

func TestNewProviderResponseErrorCopiesAndBoundsData(t *testing.T) {
	input := []byte(`{"error":{"code":"private-marker","message":"provider detail"}}`)
	got, err := NewProviderResponseError(input)
	if err != nil {
		t.Fatal(err)
	}
	evidence := got
	if evidence == nil {
		t.Fatalf("constructor returned %T, want *ProviderResponseError", got)
	}
	input[0] = 'x'
	first := evidence.Data()
	if !json.Valid(first) || !strings.Contains(string(first), "private-marker") {
		t.Fatalf("constructor did not preserve a private copy: %q", first)
	}
	first[0] = 'x'
	if second := evidence.Data(); !json.Valid(second) || !strings.Contains(string(second), "private-marker") {
		t.Fatalf("Data exposed its backing bytes: %q", second)
	}
	if strings.Contains(evidence.Error(), "private-marker") || strings.Contains(evidence.Error(), "provider detail") {
		t.Fatalf("Error exposed provider payload: %q", evidence.Error())
	}
}

func TestNewProviderResponseErrorRejectsInvalidEnvelopes(t *testing.T) {
	for _, test := range []struct {
		name string
		data []byte
	}{
		{name: "invalid", data: []byte(`{`)},
		{name: "null", data: []byte(`null`)},
		{name: "array", data: []byte(`[]`)},
		{name: "nonobject", data: []byte(`"error"`)},
		{name: "missing error", data: []byte(`{"message":"no error"}`)},
		{name: "null error", data: []byte(`{"error":null}`)},
		{name: "oversize", data: []byte(`{"error":{"message":"` + strings.Repeat("x", MaxProviderResponseErrorBytes) + `"}}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewProviderResponseError(test.data); err == nil {
				t.Fatal("constructor accepted invalid evidence")
			}
		})
	}
}

type providerResponseErrorRenderer struct {
	published []SessionState
}

func (providerResponseErrorRenderer) ID() normalize.Format { return normalize.FormatOpenAIChat }
func (providerResponseErrorRenderer) SupportsResponse(options ResponseRenderContext) CompatibilityPlan {
	return eventDeclarationTestRenderer{}.SupportsResponse(options)
}
func (r *providerResponseErrorRenderer) Begin(context.Context, ResponseRenderContext, http.ResponseWriter) (ResponseRenderSession, error) {
	return providerResponseErrorSession{published: &r.published}, nil
}

type providerResponseErrorSession struct{ published *[]SessionState }

func (providerResponseErrorSession) Emit(context.Context, ResponseEvent) error { return nil }
func (providerResponseErrorSession) Finish(context.Context, error) error       { return nil }

func runProviderResponseErrorPipeline(ctx context.Context, decoder ResponseDecoder, renderer ResponseRenderer, hooks StreamHooks) error {
	adapter := ComposedAdapter{
		AdapterID: "provider-response-error-fixture", Response: decoder,
		Renderers: map[normalize.Format]ResponseRenderer{normalize.FormatOpenAIChat: renderer},
	}
	return adapter.RenderResponse(ctx, UpstreamResponse{Status: http.StatusOK, Headers: make(http.Header), Body: http.NoBody}, httptest.NewRecorder(), normalize.FormatOpenAIChat, hooks)
}

func TestComposedAdapterNormalizesTypedDecoderFailureOnceAndDiscardsSessionUpdates(t *testing.T) {
	evidence, err := NewProviderResponseError([]byte(`{"error":{"code":"provider-terminal"}}`))
	if err != nil {
		t.Fatal(err)
	}
	renderer := &providerResponseErrorRenderer{}
	normalizeCalls, completeCalls := 0, 0
	decoder := pipelineFailureDecoder{decode: func(_ context.Context, _ func(ResponseEvent) error, hooks StreamHooks) error {
		if hooks.NormalizeProviderFailure != nil {
			t.Fatal("decoder received host normalization hook")
		}
		if hooks.OnSessionState == nil {
			t.Fatal("decoder did not receive private session update hook")
		}
		hooks.OnSessionState(SessionState{ResponseID: "private-unpublished"})
		return evidence
	}}
	gotErr := runProviderResponseErrorPipeline(context.Background(), decoder, renderer, StreamHooks{
		NormalizeProviderFailure: func(got *ProviderResponseError) error {
			normalizeCalls++
			if string(got.Data()) != string(evidence.Data()) {
				t.Fatal("normalizer received different evidence")
			}
			return nil
		},
		OnSessionState: func(SessionState) { t.Fatal("failed decoder session state was published") },
		OnComplete:     func(UsageEvent) { completeCalls++ },
	})
	if gotErr == nil {
		t.Fatal("normalizer returning nil erased the decoder failure")
	}
	if normalizeCalls != 1 {
		t.Fatalf("normalizer calls=%d, want exactly one", normalizeCalls)
	}
	if completeCalls != 0 {
		t.Fatalf("successful completion calls=%d, want zero", completeCalls)
	}
	if len(renderer.published) != 0 {
		t.Fatalf("renderer observed private session updates: %#v", renderer.published)
	}
}

func TestComposedAdapterDoesNotNormalizeSpoofedOrCanceledFailures(t *testing.T) {
	evidence, err := NewProviderResponseError([]byte(`{"error":{"code":"spoof"}}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		ctx       func() (context.Context, context.CancelFunc)
		dec       func(func(ResponseEvent) error) error
		renderer  ResponseRenderer
		transform bool
	}{
		{name: "emit failure", ctx: func() (context.Context, context.CancelFunc) { return context.Background(), func() {} }, renderer: pipelineFailureRenderer{emitErr: evidence}, dec: func(emit func(ResponseEvent) error) error {
			if err := emit(ResponseEvent{Kind: EventResponseComplete}); err != nil {
				return err
			}
			return evidence
		}},
		{name: "transform failure", ctx: func() (context.Context, context.CancelFunc) { return context.Background(), func() {} }, transform: true, dec: func(emit func(ResponseEvent) error) error {
			if err := emit(ResponseEvent{Kind: EventTextDelta}); err != nil {
				return evidence
			}
			return evidence
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := test.ctx()
			defer cancel()
			calls := 0
			decoder := pipelineFailureDecoder{decode: func(_ context.Context, emit func(ResponseEvent) error, _ StreamHooks) error { return test.dec(emit) }}
			hooks := StreamHooks{NormalizeProviderFailure: func(*ProviderResponseError) error { calls++; return nil }}
			if test.transform {
				hooks.TransformResponse = func(context.Context, ResponseEvent) (ResponseEvent, error) {
					return ResponseEvent{}, errors.New("transform fault")
				}
			}
			renderer := test.renderer
			if renderer == nil {
				renderer = pipelineFailureRenderer{}
			}
			if err := runProviderResponseErrorPipeline(ctx, decoder, renderer, hooks); err == nil {
				t.Fatal("expected pipeline failure")
			}
			if calls != 0 {
				t.Fatalf("normalizer calls=%d, want zero for non-decoder failure", calls)
			}
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	decoder := pipelineFailureDecoder{decode: func(context.Context, func(ResponseEvent) error, StreamHooks) error { return evidence }}
	if err := runProviderResponseErrorPipeline(ctx, decoder, pipelineFailureRenderer{}, StreamHooks{NormalizeProviderFailure: func(*ProviderResponseError) error { calls++; return nil }}); err == nil {
		t.Fatal("canceled pipeline unexpectedly succeeded")
	}
	if calls != 0 {
		t.Fatalf("normalizer calls after cancellation=%d, want zero", calls)
	}
}

func TestComposedAdapterBoundsProviderResponseErrorByMaxEventBytes(t *testing.T) {
	evidence, err := NewProviderResponseError([]byte(`{"error":{"code":"too-large-for-event"}}`))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	decoder := pipelineFailureDecoder{decode: func(context.Context, func(ResponseEvent) error, StreamHooks) error { return evidence }}
	gotErr := runProviderResponseErrorPipeline(context.Background(), decoder, pipelineFailureRenderer{}, StreamHooks{
		MaxEventBytes:            int64(len(evidence.Data()) - 1),
		NormalizeProviderFailure: func(*ProviderResponseError) error { calls++; return nil },
	})
	if gotErr == nil {
		t.Fatal("oversize evidence unexpectedly succeeded")
	}
	if calls != 0 {
		t.Fatalf("normalizer calls=%d for oversized evidence, want zero", calls)
	}
}
