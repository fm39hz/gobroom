package kernel

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/normalize"
)

type committedFailureAdapter struct{ attempts *atomic.Int32 }

type cancelAdapter struct{ started chan struct{} }

func (a cancelAdapter) ID() string         { return "cancel" }
func (a cancelAdapter) Protocol() Protocol { return ProtocolOpenAIChat }
func (a cancelAdapter) Prepare(context.Context, NormalizedRequest, Route, Credential) (UpstreamRequest, error) {
	return UpstreamRequest{Method: http.MethodPost, URL: "test://cancel"}, nil
}
func (a cancelAdapter) Execute(ctx context.Context, _ UpstreamRequest) (UpstreamResponse, error) {
	close(a.started)
	<-ctx.Done()
	return UpstreamResponse{}, ctx.Err()
}
func (a cancelAdapter) ClassifyError(int, []byte) ErrorClass { return ErrorRetryable }
func (a cancelAdapter) TranslateStream(context.Context, UpstreamResponse, http.ResponseWriter, normalize.Format, StreamHooks) error {
	return nil
}

func (a committedFailureAdapter) ID() string         { return "committed-failure" }
func (a committedFailureAdapter) Protocol() Protocol { return ProtocolOpenAIChat }
func (a committedFailureAdapter) Prepare(context.Context, NormalizedRequest, Route, Credential) (UpstreamRequest, error) {
	return UpstreamRequest{Method: http.MethodPost, URL: "test://"}, nil
}
func (a committedFailureAdapter) Execute(context.Context, UpstreamRequest) (UpstreamResponse, error) {
	a.attempts.Add(1)
	return UpstreamResponse{Status: http.StatusOK, Body: io.NopCloser(strings.NewReader("partial"))}, nil
}
func (a committedFailureAdapter) ClassifyError(int, []byte) ErrorClass { return ErrorRetryable }
func (a committedFailureAdapter) TranslateStream(_ context.Context, response UpstreamResponse, writer http.ResponseWriter, _ normalize.Format, hooks StreamHooks) error {
	_, _ = io.Copy(writer, response.Body)
	err := errors.New("post-commit stream failure")
	if hooks.OnError != nil {
		hooks.OnError(err)
	}
	return err
}

func TestKernelDoesNotSwitchAfterResponseCommitment(t *testing.T) {
	attempts := &atomic.Int32{}
	snapshot, err := BuildSnapshot(SnapshotInput{
		PublicModels: []PublicModel{{Name: "role", TargetRef: "role"}},
		Nodes:        []ModelNode{{ID: "role", Kind: ModelCombo, Strategy: StrategyFallback, Members: []MemberRef{{Kind: MemberRoute, ID: "a"}, {Kind: MemberRoute, ID: "b"}}}},
		Routes:       []Route{{ID: "a", AdapterID: "committed-failure", Protocol: ProtocolOpenAIChat, Enabled: true}, {ID: "b", AdapterID: "committed-failure", Protocol: ProtocolOpenAIChat, Enabled: true}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	k, err := New(snapshot, nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	k.Adapters["committed-failure"] = committedFailureAdapter{attempts: attempts}
	err = k.Execute(context.Background(), NormalizedRequest{Model: "role", SourceFormat: normalize.FormatOpenAIChat}, Credential{}, httptest.NewRecorder())
	if err == nil || !strings.Contains(err.Error(), "post-commit") {
		t.Fatalf("err=%v", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("route switched after commitment: attempts=%d", attempts.Load())
	}
}

func TestKernelPropagatesCancellationDuringUpstreamExecution(t *testing.T) {
	started := make(chan struct{})
	snapshot, err := BuildSnapshot(SnapshotInput{PublicModels: []PublicModel{{Name: "model", TargetRef: "model"}}, Nodes: []ModelNode{{ID: "model", Kind: ModelPhysical, Members: []MemberRef{{Kind: MemberRoute, ID: "route"}}}}, Routes: []Route{{ID: "route", AdapterID: "cancel", Protocol: ProtocolOpenAIChat, Enabled: true}}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	k, err := New(snapshot, nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	k.Adapters["cancel"] = cancelAdapter{started: started}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- k.Execute(ctx, NormalizedRequest{Model: "model", SourceFormat: normalize.FormatOpenAIChat}, Credential{}, httptest.NewRecorder())
	}()
	<-started
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("kernel did not propagate cancellation")
	}
}
