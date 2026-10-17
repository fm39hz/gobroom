package kernel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fm39hz/gobroom/internal/normalize"
)

type pipelineFailureDecoder struct {
	decode func(context.Context, func(ResponseEvent) error, StreamHooks) error
}

func (pipelineFailureDecoder) ID() string { return "pipeline-failure-fixture" }
func (pipelineFailureDecoder) PossibleEvents() []ResponseEventKind {
	return []ResponseEventKind{EventTextDelta, EventResponseComplete}
}
func (pipelineFailureDecoder) ClassifyError(int, []byte) ErrorClass { return ErrorRetryable }
func (d pipelineFailureDecoder) Decode(ctx context.Context, _ UpstreamResponse, emit func(ResponseEvent) error, hooks StreamHooks) error {
	return d.decode(ctx, emit, hooks)
}

type pipelineFailureRenderer struct {
	emitErr   error
	finishErr error
}

func (pipelineFailureRenderer) ID() normalize.Format { return normalize.FormatOpenAIChat }
func (pipelineFailureRenderer) SupportsResponse(options ResponseRenderContext) CompatibilityPlan {
	return eventDeclarationTestRenderer{}.SupportsResponse(options)
}
func (r pipelineFailureRenderer) Begin(context.Context, ResponseRenderContext, http.ResponseWriter) (ResponseRenderSession, error) {
	return pipelineFailureSession{emitErr: r.emitErr, finishErr: r.finishErr}, nil
}

type pipelineFailureSession struct {
	emitErr   error
	finishErr error
}

func (s pipelineFailureSession) Emit(context.Context, ResponseEvent) error { return s.emitErr }
func (s pipelineFailureSession) Finish(context.Context, error) error       { return s.finishErr }

func runPipelineFailureFixture(decoder ResponseDecoder, renderer ResponseRenderer, hooks StreamHooks) error {
	adapter := ComposedAdapter{
		AdapterID: "pipeline-failure-fixture",
		Response:  decoder,
		Renderers: map[normalize.Format]ResponseRenderer{normalize.FormatOpenAIChat: renderer},
	}
	return adapter.RenderResponse(context.Background(), UpstreamResponse{Status: http.StatusOK, Headers: make(http.Header), Body: http.NoBody}, httptest.NewRecorder(), normalize.FormatOpenAIChat, hooks)
}

func assertResponseFailureOrigin(t *testing.T, err error, want ResponseFailureOrigin, cause error) {
	t.Helper()
	var failure *ResponseFailure
	if !errors.As(err, &failure) {
		t.Fatalf("error %v does not contain ResponseFailure", err)
	}
	if failure.Origin != want {
		t.Fatalf("failure origin=%q, want %q (error %v)", failure.Origin, want, err)
	}
	if cause != nil && !errors.Is(err, cause) {
		t.Fatalf("error %v does not preserve cause %v", err, cause)
	}
}

func TestComposedAdapterResponseFailurePreservesTransformOriginThroughDecoderWrap(t *testing.T) {
	transformErr := errors.New("transform rejected event")
	decoder := pipelineFailureDecoder{decode: func(_ context.Context, emit func(ResponseEvent) error, _ StreamHooks) error {
		if err := emit(ResponseEvent{Kind: EventTextDelta}); err != nil {
			return fmt.Errorf("decoder wrapper: %w", err)
		}
		return nil
	}}
	err := runPipelineFailureFixture(decoder, pipelineFailureRenderer{}, StreamHooks{
		TransformResponse: func(context.Context, ResponseEvent) (ResponseEvent, error) { return ResponseEvent{}, transformErr },
	})
	assertResponseFailureOrigin(t, err, ResponseFailureTransform, transformErr)
}

func TestComposedAdapterLatchesIgnoredTransformCallbackFailure(t *testing.T) {
	transformErr := errors.New("transform rejected event")
	completed := false
	decoder := pipelineFailureDecoder{decode: func(_ context.Context, emit func(ResponseEvent) error, hooks StreamHooks) error {
		_ = emit(ResponseEvent{Kind: EventTextDelta})
		_ = emit(ResponseEvent{Kind: EventResponseComplete})
		if hooks.OnComplete != nil {
			hooks.OnComplete(UsageEvent{Status: "ok"})
		}
		return nil
	}}
	err := runPipelineFailureFixture(decoder, pipelineFailureRenderer{}, StreamHooks{
		TransformResponse: func(context.Context, ResponseEvent) (ResponseEvent, error) { return ResponseEvent{}, transformErr },
		OnComplete:        func(UsageEvent) { completed = true },
	})
	assertResponseFailureOrigin(t, err, ResponseFailureTransform, transformErr)
	if completed {
		t.Fatal("completion hook ran after a latched transform callback failure")
	}
}

func TestComposedAdapterTagsRendererEmitAndFinishFailures(t *testing.T) {
	for _, test := range []struct {
		name     string
		renderer pipelineFailureRenderer
		cause    error
	}{
		{name: "emit", renderer: pipelineFailureRenderer{emitErr: errors.New("emit failed")}},
		{name: "finish", renderer: pipelineFailureRenderer{finishErr: errors.New("finish failed")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			decoder := pipelineFailureDecoder{decode: func(_ context.Context, emit func(ResponseEvent) error, _ StreamHooks) error {
				return emit(ResponseEvent{Kind: EventResponseComplete})
			}}
			err := runPipelineFailureFixture(decoder, test.renderer, StreamHooks{})
			assertResponseFailureOrigin(t, err, ResponseFailureRenderer, test.cause)
		})
	}
}

func TestComposedAdapterTagsUnexpectedEOFAsProviderFailure(t *testing.T) {
	decoder := pipelineFailureDecoder{decode: func(context.Context, func(ResponseEvent) error, StreamHooks) error {
		return io.ErrUnexpectedEOF
	}}
	err := runPipelineFailureFixture(decoder, pipelineFailureRenderer{}, StreamHooks{})
	assertResponseFailureOrigin(t, err, ResponseFailureProvider, io.ErrUnexpectedEOF)
}

func TestComposedAdapterCanceledContextDoesNotComplete(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	completed := false
	decoder := pipelineFailureDecoder{decode: func(ctx context.Context, emit func(ResponseEvent) error, hooks StreamHooks) error {
		_ = emit(ResponseEvent{Kind: EventResponseComplete})
		if hooks.OnComplete != nil {
			hooks.OnComplete(UsageEvent{Status: "ok"})
		}
		return nil
	}}
	adapter := ComposedAdapter{
		AdapterID: "pipeline-failure-fixture",
		Response:  decoder,
		Renderers: map[normalize.Format]ResponseRenderer{normalize.FormatOpenAIChat: pipelineFailureRenderer{}},
	}
	err := adapter.RenderResponse(ctx, UpstreamResponse{Status: http.StatusOK, Headers: make(http.Header), Body: http.NoBody}, httptest.NewRecorder(), normalize.FormatOpenAIChat, StreamHooks{
		OnComplete: func(UsageEvent) { completed = true },
	})
	if err == nil {
		t.Fatal("canceled response unexpectedly succeeded")
	}
	if completed {
		t.Fatal("completion hook ran for a canceled response")
	}
}
