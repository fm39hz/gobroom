package kernel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/normalize"
)

var errPrivateSessionSave = errors.New("private session repository detail")

type saveFailureSessionStore struct{ saveErr error }

func (saveFailureSessionStore) ID() string { return "save-failure-session" }
func (saveFailureSessionStore) Load(context.Context, Route, string) (SessionState, bool, error) {
	return SessionState{}, false, nil
}
func (s saveFailureSessionStore) Save(context.Context, Route, string, SessionState) error {
	return s.saveErr
}
func (saveFailureSessionStore) Invalidate(context.Context, Route, string, SessionState) (bool, error) {
	return false, nil
}

type saveFailureFeedbackGate struct {
	failures  int
	successes int
}

func (*saveFailureFeedbackGate) Usable(Route, time.Time) bool           { return true }
func (g *saveFailureFeedbackGate) MarkFailure(Route, ErrorClass, error) { g.failures++ }
func (g *saveFailureFeedbackGate) MarkFailureAfter(Route, ErrorClass, error, time.Duration) {
	g.failures++
}
func (g *saveFailureFeedbackGate) MarkSuccess(Route) { g.successes++ }

type saveFailureAdapter struct {
	*continuitySessionAdapter
	dispatches int
}

func (*saveFailureAdapter) ID() string { return "save-failure-adapter" }

func (a *saveFailureAdapter) Execute(context.Context, UpstreamRequest) (UpstreamResponse, error) {
	a.dispatches++
	return UpstreamResponse{Status: http.StatusOK, Body: http.NoBody}, nil
}

func (*saveFailureAdapter) RenderResponse(_ context.Context, _ UpstreamResponse, _ http.ResponseWriter, _ normalize.Format, hooks StreamHooks) error {
	if hooks.OnEvent != nil {
		hooks.OnEvent(ResponseEvent{Kind: EventResponseComplete, ResponseID: "response-after-save-failure"})
	}
	if hooks.OnComplete != nil {
		hooks.OnComplete(UsageEvent{Status: "ok", InputTokens: new(int64(13)), OutputTokens: new(int64(7))})
	}
	return nil
}

func TestKernelSessionSaveFailureIsObservedWithoutReplayOrHealthPenalty(t *testing.T) {
	for _, test := range []struct {
		name              string
		saveErr           error
		wantNotifications int
	}{
		{name: "private save failure", saveErr: errPrivateSessionSave, wantNotifications: 1},
		{name: "wrapped superseded state", saveErr: fmt.Errorf("write rejected: %w", ErrSessionStateSuperseded)},
		{name: "successful save"},
	} {
		t.Run(test.name, func(t *testing.T) {
			storeRef := extensions.Ref{Kind: "session_store", ID: "save-failure-session", ContractVersion: 1}
			providerRef := extensions.Ref{Kind: "provider-definition", ID: "save-failure-provider", ContractVersion: 1}
			operationRef := extensions.Ref{Kind: "operation", ID: string(normalize.OperationChatGenerate), ContractVersion: 1}
			route := Route{
				ID: "save-failure-route", NodeID: "provider", DefinitionID: providerRef.ID,
				DefinitionRef: providerRef, SessionOperationRef: operationRef,
				ExternalModel: "upstream-model", CredentialID: "account", Protocol: ProtocolOpenAIChat,
				Enabled: true, OperationBindings: map[normalize.Operation]RouteOperationBinding{
					normalize.OperationChatGenerate: {
						ContractVersion: 1, AdapterIDs: []string{"save-failure-adapter"}, SessionStoreRef: storeRef,
					},
				},
			}
			route.HealthGeneration.Connection = "principal-A"
			snapshot, err := BuildSnapshot(SnapshotInput{
				PublicModels: []PublicModel{{Name: "model", TargetRef: "model"}},
				Nodes:        []ModelNode{{ID: "model", Kind: ModelPhysical, Members: []MemberRef{{Kind: MemberRoute, ID: route.ID, Fidelity: FidelityExact}}}},
				Routes:       []Route{route},
			}, 1)
			if err != nil {
				t.Fatal(err)
			}
			gate := &saveFailureFeedbackGate{}
			k, err := New(snapshot, gate, 4)
			if err != nil {
				t.Fatal(err)
			}
			defer k.Close()
			adapter := &saveFailureAdapter{continuitySessionAdapter: &continuitySessionAdapter{}}
			k.Adapters[adapter.ID()] = adapter
			k.SessionStores[storeRef] = saveFailureSessionStore{saveErr: test.saveErr}
			notifications := 0
			k.OnSessionSaveFailure = func() { notifications++ }

			request := NormalizedRequest{
				Model: "model", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1,
				SourceFormat: normalize.FormatOpenAIChat, Session: normalize.SessionContext{ID: "client-session"},
			}
			if err := k.Execute(context.Background(), request, Credential{}, httptest.NewRecorder()); err != nil {
				t.Fatalf("Execute() error = %v; post-response persistence issue must not fail the response", err)
			}
			if adapter.dispatches != 1 {
				t.Fatalf("upstream dispatches = %d, want 1", adapter.dispatches)
			}
			if notifications != test.wantNotifications {
				t.Fatalf("session save failure notifications = %d, want %d", notifications, test.wantNotifications)
			}
			if gate.failures != 0 {
				t.Fatalf("provider failure feedback = %d, want 0", gate.failures)
			}
			if gate.successes != 1 {
				t.Fatalf("provider success feedback = %d, want 1", gate.successes)
			}
			k.Close()
			var completion UsageEvent
			for event := range k.Events {
				if event.Status == "ok" {
					completion = event
				}
			}
			if completion.Status != "ok" || completion.InputTokens == nil || *completion.InputTokens != 13 || completion.OutputTokens == nil || *completion.OutputTokens != 7 {
				t.Fatalf("successful completion usage = %#v, want status ok and known usage 13/7", completion)
			}
		})
	}
}
