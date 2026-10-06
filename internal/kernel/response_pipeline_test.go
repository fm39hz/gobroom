package kernel

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/normalize"
)

type precommitFailureAdapter struct {
	name     string
	attempts *[]string
	fail     bool
}

func (a precommitFailureAdapter) ID() string { return a.name }
func (a precommitFailureAdapter) PlanCompatibility(input CompatibilityContext) CompatibilityPlan {
	return fixtureCompatibilityPlan(input, normalize.FormatOpenAIChat, FidelityTranslated)
}
func (a precommitFailureAdapter) Prepare(_ context.Context, _ NormalizedRequest, route Route, _ Credential) (UpstreamRequest, error) {
	*a.attempts = append(*a.attempts, route.ID)
	return UpstreamRequest{Method: http.MethodPost, URL: "fixture://" + route.ID}, nil
}
func (precommitFailureAdapter) Execute(context.Context, UpstreamRequest) (UpstreamResponse, error) {
	return UpstreamResponse{Status: http.StatusOK, Body: io.NopCloser(strings.NewReader("body"))}, nil
}
func (precommitFailureAdapter) ClassifyError(int, []byte) ErrorClass { return ErrorRetryable }
func (a precommitFailureAdapter) RenderResponse(_ context.Context, _ UpstreamResponse, writer http.ResponseWriter, _ normalize.Format, hooks StreamHooks) error {
	if a.fail {
		return errors.New("semantic response decode failed")
	}
	if hooks.OnFirstByte != nil {
		hooks.OnFirstByte(time.Now())
	}
	if _, err := io.WriteString(writer, "rendered"); err != nil {
		return err
	}
	if hooks.OnComplete != nil {
		hooks.OnComplete(UsageEvent{Status: "ok"})
	}
	return nil
}

func TestKernelDoesNotReplayAcceptedResponseAfterSemanticFailure(t *testing.T) {
	snapshot, err := BuildSnapshot(SnapshotInput{
		PublicModels: []PublicModel{{Name: "role", TargetRef: "role"}},
		Nodes:        []ModelNode{{ID: "role", Kind: ModelCombo, Strategy: StrategyFallback, Members: []MemberRef{{Kind: MemberRoute, ID: "first"}, {Kind: MemberRoute, ID: "second"}}}},
		Routes: []Route{
			{ID: "first", Enabled: true, OperationBindings: map[normalize.Operation]RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"decoder-fails"}}}},
			{ID: "second", Enabled: true, OperationBindings: map[normalize.Operation]RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"renderer-succeeds"}}}},
		},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	k, err := New(snapshot, nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	var attempts []string
	k.Adapters["decoder-fails"] = precommitFailureAdapter{name: "decoder-fails", attempts: &attempts, fail: true}
	k.Adapters["renderer-succeeds"] = precommitFailureAdapter{name: "renderer-succeeds", attempts: &attempts}
	writer := httptest.NewRecorder()
	request := NormalizedRequest{Model: "role", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, SourceFormat: normalize.FormatOpenAIChat}
	err = k.Execute(context.Background(), request, Credential{}, writer)
	var suppressed *ReplaySuppressedError
	if !errors.As(err, &suppressed) || suppressed.Effect != EffectAccepted {
		t.Fatalf("accepted upstream response should suppress retry, got %v", err)
	}
	if len(attempts) != 1 || attempts[0] != "first" || writer.Body.String() != "" {
		t.Fatalf("attempts=%v body=%q", attempts, writer.Body.String())
	}
}
