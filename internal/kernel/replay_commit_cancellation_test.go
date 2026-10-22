package kernel

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/fm39hz/gobroom/internal/normalize"
)

type replayCancellationBody struct {
	io.ReadCloser
	closed atomic.Bool
}

func (b *replayCancellationBody) Close() error {
	b.closed.Store(true)
	return b.ReadCloser.Close()
}

type replayCancellationAdapter struct {
	*issuerReplayAdapter
	entered  chan struct{}
	commit   bool
	request  *replayCancellationBody
	response *replayCancellationBody
}

func (a *replayCancellationAdapter) Prepare(ctx context.Context, request NormalizedRequest, route Route, credential Credential) (UpstreamRequest, error) {
	upstream, err := a.issuerReplayAdapter.Prepare(ctx, request, route, credential)
	if err == nil {
		body := &replayCancellationBody{ReadCloser: io.NopCloser(upstream.Body)}
		a.request = body
		upstream.Body = body
	}
	return upstream, err
}

func (a *replayCancellationAdapter) Execute(ctx context.Context, request UpstreamRequest) (UpstreamResponse, error) {
	// Like HTTPTransport, this fixture transport consumes and closes the body
	// it accepted. The kernel separately owns abandoned, undispatched bodies.
	if body, ok := request.Body.(io.Closer); ok {
		defer body.Close()
	}
	response, err := a.issuerReplayAdapter.Execute(ctx, request)
	if err == nil {
		a.response = &replayCancellationBody{ReadCloser: response.Body}
		response.Body = a.response
	}
	return response, err
}

func (a *replayCancellationAdapter) RenderResponse(ctx context.Context, response UpstreamResponse, writer http.ResponseWriter, format normalize.Format, hooks StreamHooks) error {
	if a.commit {
		if _, err := io.WriteString(writer, "visible"); err != nil {
			return err
		}
	}
	close(a.entered)
	<-ctx.Done()
	if a.commit && hooks.OnComplete != nil {
		hooks.OnComplete(UsageEvent{Status: "ok", OutputTokens: new(int64(1))})
	}
	return ctx.Err()
}

func TestReplayWithKeyCancellationAfterAcceptedResponseBeforeCommit(t *testing.T) {
	testReplayWithKeyCancellationAtRender(t, false)
}

func TestReplayWithKeyCancellationAfterClientCommit(t *testing.T) {
	testReplayWithKeyCancellationAtRender(t, true)
}

func testReplayWithKeyCancellationAtRender(t *testing.T, committed bool) {
	synctest.Test(t, func(t *testing.T) {
		first := &issuerReplayAdapter{id: "issuer-a-adapter"}
		second := &issuerReplayAdapter{id: "issuer-b-adapter"}
		k := newReplayKeyKernel(t, first, second)
		adapter := &replayCancellationAdapter{
			issuerReplayAdapter: first,
			entered:             make(chan struct{}),
			commit:              committed,
		}
		k.Adapters[first.id] = adapter
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		writer := httptest.NewRecorder()
		done := make(chan error, 1)
		go func() { done <- k.Execute(ctx, replayKeyRequest("cancel-after-response"), Credential{}, writer) }()

		<-adapter.entered
		cancel()
		err := <-done
		var suppressed *ReplaySuppressedError
		if errors.As(err, &suppressed) {
			t.Fatalf("accepted upstream response became a replay rejection: %v", err)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled render returned %v, want context.Canceled", err)
		}
		if first.calls != 1 || first.prepareCalls != 1 || first.replayCalls != 0 || second.calls != 0 {
			t.Fatalf("cancellation replayed or crossed issuer: A calls=%d prepares=%d replay factories=%d B calls=%d", first.calls, first.prepareCalls, first.replayCalls, second.calls)
		}
		if adapter.response == nil || !adapter.response.closed.Load() {
			t.Fatal("accepted response body was not closed after cancellation")
		}
		if adapter.request == nil || !adapter.request.closed.Load() {
			t.Fatal("upstream request body was not closed after cancellation")
		}
		if committed && writer.Body.String() != "visible" {
			t.Fatalf("committed bytes were lost or changed: %q", writer.Body.String())
		}
		if !committed && writer.Body.Len() != 0 {
			t.Fatalf("response committed output before cancellation: %q", writer.Body.String())
		}
		select {
		case usage := <-k.Events:
			if usage.Status == "ok" {
				t.Fatalf("canceled response emitted successful usage: %#v", usage)
			}
			if usage.Status != "cancelled" || usage.AttemptID == "" || usage.InvocationID == "" {
				t.Fatalf("known failed usage was lost or misclassified: %#v", usage)
			}
			if committed && (usage.OutputTokens == nil || *usage.OutputTokens != 1) || !committed && usage.OutputTokens != nil {
				t.Fatalf("cancelled dispatch did not retain known versus unknown output: %#v", usage)
			}
		default:
			t.Fatal("cancelled actual dispatch has no accounting record")
		}
	})
}
