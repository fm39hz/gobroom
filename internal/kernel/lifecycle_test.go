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

	"github.com/fm39hz/gobroom/internal/normalize"
)

type committedFailureAdapter struct{ attempts *atomic.Int32 }

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
