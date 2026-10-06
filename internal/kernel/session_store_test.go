package kernel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/normalize"
)

type continuitySessionAdapter struct {
	previousResponse string
}

func (*continuitySessionAdapter) ID() string         { return "continuity-session" }
func (*continuitySessionAdapter) NegotiateClientFormat(format normalize.Format, _ bool) CompatibilityDecision { return CompatibilityDecision{Supported: format == normalize.FormatOpenAIChat, Fidelity: FidelityNative} }
func (a *continuitySessionAdapter) Prepare(_ context.Context, request NormalizedRequest, _ Route, _ Credential) (UpstreamRequest, error) {
	a.previousResponse = request.Continuity.PreviousResponse
	return UpstreamRequest{Method: http.MethodPost, URL: "test://continuity"}, nil
}
func (*continuitySessionAdapter) Execute(context.Context, UpstreamRequest) (UpstreamResponse, error) {
	return UpstreamResponse{Status: http.StatusOK, Body: http.NoBody}, nil
}
func (*continuitySessionAdapter) ClassifyError(int, []byte) ErrorClass { return ErrorRetryable }
func (*continuitySessionAdapter) RenderResponse(_ context.Context, _ UpstreamResponse, _ http.ResponseWriter, _ normalize.Format, hooks StreamHooks) error {
	if hooks.OnEvent != nil {
		hooks.OnEvent(ResponseEvent{Kind: EventResponseComplete, ResponseID: "response-next"})
	}
	if hooks.OnComplete != nil {
		hooks.OnComplete(UsageEvent{Status: "ok"})
	}
	return nil
}

func TestKernelLoadsAndSavesProviderSessionContinuity(t *testing.T) {
	route := Route{ID: "route", NodeID: "provider", DefinitionID: "responses-provider", ExternalModel: "upstream-model", CredentialID: "account", OperationBindings: map[normalize.Operation]RouteOperationBinding{normalize.OperationChatGenerate: {AdapterIDs: []string{"continuity-session"}, SessionStoreID: "session"}}, Protocol: ProtocolOpenAIChat, Enabled: true}
	snapshot, err := BuildSnapshot(SnapshotInput{
		PublicModels: []PublicModel{{Name: "model", TargetRef: "model"}},
		Nodes:        []ModelNode{{ID: "model", Kind: ModelPhysical, Members: []MemberRef{{Kind: MemberRoute, ID: "route", Fidelity: FidelityExact}}}},
		Routes:       []Route{route},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	k, err := New(snapshot, nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	adapter := &continuitySessionAdapter{}
	k.Adapters[adapter.ID()] = adapter
	sessions := NewMemorySessionStore()
	k.SessionStores["session"] = sessions
	if err := sessions.Save(context.Background(), route, "client-session", SessionState{ResponseID: "response-previous"}); err != nil {
		t.Fatal(err)
	}
	request := NormalizedRequest{Model: "model", Operation: normalize.OperationChatGenerate, SourceFormat: normalize.FormatOpenAIChat, Session: normalize.SessionContext{ID: "client-session"}}
	if err := k.Execute(context.Background(), request, Credential{}, httptest.NewRecorder()); err != nil {
		t.Fatal(err)
	}
	if adapter.previousResponse != "response-previous" {
		t.Fatalf("previous response was not loaded into normalized request: %q", adapter.previousResponse)
	}
	state, ok, err := sessions.Load(context.Background(), route, "client-session")
	if err != nil || !ok || state.ResponseID != "response-next" {
		t.Fatalf("saved session state=%#v ok=%v err=%v", state, ok, err)
	}
}

func TestMemorySessionStoreExpiresStateAndCopiesOpaqueData(t *testing.T) {
	route := Route{DefinitionID: "provider", ExternalModel: "model", CredentialID: "account"}
	store := NewMemorySessionStore()
	data := json.RawMessage(`{"opaque":"value"}`)
	if err := store.Save(context.Background(), route, "session", SessionState{ProviderData: data}); err != nil {
		t.Fatal(err)
	}
	data[2] = 'X'
	state, ok, err := store.Load(context.Background(), route, "session")
	if err != nil || !ok || string(state.ProviderData) != `{"opaque":"value"}` {
		t.Fatalf("state=%#v ok=%v err=%v", state, ok, err)
	}
	if err := store.Save(context.Background(), route, "expired", SessionState{ResponseID: "old", ExpiresAt: time.Now().Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.Load(context.Background(), route, "expired"); err != nil || ok {
		t.Fatalf("expired state remained: ok=%v err=%v", ok, err)
	}
	otherModel := route
	otherModel.ExternalModel = "different-model"
	if _, ok, err := store.Load(context.Background(), otherModel, "session"); err != nil || ok {
		t.Fatalf("session state leaked across physical models: ok=%v err=%v", ok, err)
	}
}
