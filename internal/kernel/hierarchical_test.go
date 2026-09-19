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

type attemptAdapter struct{ attempts *[]string }

type observingStrategy struct{ failedNodes *[]string }

type orderedObservingStrategy struct{ failedNodes *[]string }

func (orderedObservingStrategy) Plan(_ string, members []MemberRef, _ *StrategyState) []MemberRef {
	return members
}

func (s orderedObservingStrategy) OnFailure(failure StrategyFailure, _ *StrategyState) FailureAction {
	*s.failedNodes = append(*s.failedNodes, failure.NodeID)
	return FailureContinue
}

func (observingStrategy) Plan(_ string, members []MemberRef, state *StrategyState) []MemberRef {
	if len(members) < 2 {
		return members
	}
	start := state.Cursor % len(members)
	state.Cursor = (start + 1) % len(members)
	return rotateMembers(members, start)
}

func (s observingStrategy) OnFailure(failure StrategyFailure, _ *StrategyState) FailureAction {
	*s.failedNodes = append(*s.failedNodes, failure.NodeID)
	return FailureContinue
}

func (a attemptAdapter) ID() string         { return "test" }
func (a attemptAdapter) Protocol() Protocol { return ProtocolOpenAIChat }
func (a attemptAdapter) Prepare(_ context.Context, _ NormalizedRequest, route Route, _ Credential) (UpstreamRequest, error) {
	return UpstreamRequest{Method: http.MethodPost, URL: route.ID}, nil
}
func (a attemptAdapter) Execute(_ context.Context, request UpstreamRequest) (UpstreamResponse, error) {
	*a.attempts = append(*a.attempts, request.URL)
	if strings.HasPrefix(request.URL, "child-") {
		return UpstreamResponse{Status: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader("retry"))}, nil
	}
	return UpstreamResponse{Status: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok"))}, nil
}
func (attemptAdapter) ClassifyError(int, []byte) ErrorClass { return ErrorRetryable }
func (attemptAdapter) TranslateStream(_ context.Context, response UpstreamResponse, writer http.ResponseWriter, _ normalize.Format, _ StreamHooks) error {
	_, err := io.Copy(writer, response.Body)
	return err
}

func TestExecutePreservesHierarchicalFallbackBoundariesAndState(t *testing.T) {
	snapshot, err := BuildSnapshot(SnapshotInput{
		PublicModels: []PublicModel{{Name: "role", TargetRef: "role"}},
		Nodes: []ModelNode{
			{ID: "role", Kind: ModelCombo, Strategy: "observing-ordered", Members: []MemberRef{{Kind: MemberModel, ID: "physical"}, {Kind: MemberRoute, ID: "outer-route"}}},
			{ID: "physical", Kind: ModelPhysical, Strategy: "observing", Members: []MemberRef{{Kind: MemberRoute, ID: "child-a"}, {Kind: MemberRoute, ID: "child-b"}}},
		},
		Routes: []Route{
			{ID: "child-a", AdapterID: "test", Protocol: ProtocolOpenAIChat, Enabled: true},
			{ID: "child-b", AdapterID: "test", Protocol: ProtocolOpenAIChat, Enabled: true},
			{ID: "outer-route", AdapterID: "test", Protocol: ProtocolOpenAIChat, Enabled: true},
		},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(snapshot, nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer kernel.Close()
	attempts := []string{}
	failedNodes := []string{}
	kernel.Adapters["test"] = attemptAdapter{attempts: &attempts}
	kernel.Scheduler.RegisterStrategy("observing", observingStrategy{failedNodes: &failedNodes})
	kernel.Scheduler.RegisterStrategy("observing-ordered", orderedObservingStrategy{failedNodes: &failedNodes})
	request := NormalizedRequest{Model: "role", SourceFormat: normalize.FormatOpenAIChat}

	for i, want := range [][]string{{"child-a", "child-b", "outer-route"}, {"child-b", "child-a", "outer-route"}, {"child-a", "child-b", "outer-route"}} {
		attempts = attempts[:0]
		if err := kernel.Execute(context.Background(), request, Credential{}, httptest.NewRecorder()); err != nil {
			t.Fatalf("execute %d: %v", i, err)
		}
		if len(attempts) != len(want) {
			t.Fatalf("execute %d attempts=%v want=%v", i, attempts, want)
		}
		for j := range want {
			if attempts[j] != want[j] {
				t.Fatalf("execute %d attempts=%v want=%v", i, attempts, want)
			}
		}
	}
	if kernel.Scheduler.modelState["role"].Cursor != 0 || kernel.Scheduler.modelState["physical"].Cursor != 1 {
		t.Fatalf("state must be scoped and advanced independently per node: %#v", kernel.Scheduler.modelState)
	}
	if len(failedNodes) != 9 {
		t.Fatalf("expected each exhausted child then outer member to notify its own strategy, got %v", failedNodes)
	}
	for i := 0; i < len(failedNodes); i += 3 {
		if failedNodes[i] != "physical" || failedNodes[i+1] != "physical" || failedNodes[i+2] != "role" {
			t.Fatalf("failure transitions crossed policy boundaries: %v", failedNodes)
		}
	}
}

func TestTypedModelGraphRejectsCycles(t *testing.T) {
	_, err := BuildSnapshot(SnapshotInput{
		PublicModels: []PublicModel{{Name: "a", TargetRef: "a"}},
		Nodes: []ModelNode{
			{ID: "a", Members: []MemberRef{{Kind: MemberModel, ID: "b"}}},
			{ID: "b", Members: []MemberRef{{Kind: MemberModel, ID: "a"}}},
		},
	}, 1)
	if err == nil || errors.Is(err, ErrSnapshotInvalid) {
		t.Fatalf("expected descriptive model cycle error, got %v", err)
	}
}
