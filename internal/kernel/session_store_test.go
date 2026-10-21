package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/normalize"
)

type failingSessionStore struct{}

func (failingSessionStore) ID() string { return "failing-session" }
func (failingSessionStore) Load(context.Context, Route, string) (SessionState, bool, error) {
	return SessionState{}, false, errors.New("private storage path detail")
}
func (failingSessionStore) Save(context.Context, Route, string, SessionState) error { return nil }

type continuitySessionAdapter struct {
	plannedPreviousResponse string
	plannedContinuityFacet  bool
	previousResponse        string
	sessionOperationRef     extensions.Ref
}

func (*continuitySessionAdapter) ID() string { return "continuity-session" }
func (a *continuitySessionAdapter) PlanCompatibility(input CompatibilityContext) CompatibilityPlan {
	a.plannedPreviousResponse = input.Request.Continuity.PreviousResponse
	for _, facet := range input.Policy.RequiredFacets {
		a.plannedContinuityFacet = a.plannedContinuityFacet || facet == FacetContinuity
	}
	return fixtureCompatibilityPlan(input, normalize.FormatOpenAIChat, FidelityNative)
}
func (a *continuitySessionAdapter) Prepare(_ context.Context, request NormalizedRequest, route Route, _ Credential) (UpstreamRequest, error) {
	a.previousResponse = request.Continuity.PreviousResponse
	a.sessionOperationRef = route.SessionOperationRef
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
	sessionStoreRef := extensions.Ref{Kind: "session_store", ID: "session", ContractVersion: 1}
	providerRef := extensions.Ref{Kind: "provider-definition", ID: "responses-provider", ContractVersion: 1}
	chatOperationRef := extensions.Ref{Kind: "operation", ID: string(normalize.OperationChatGenerate), ContractVersion: 1}
	route := Route{ID: "route", NodeID: "provider", DefinitionID: "responses-provider", DefinitionRef: providerRef, SessionOperationRef: chatOperationRef, ExternalModel: "upstream-model", CredentialID: "account", OperationBindings: map[normalize.Operation]RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"continuity-session"}, SessionStoreRef: sessionStoreRef}}, Protocol: ProtocolOpenAIChat, Enabled: true}
	route.HealthGeneration.Connection = "principal-A"
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
	k.SessionStores[sessionStoreRef] = sessions
	if err := sessions.Save(context.Background(), route, "client-session", SessionState{ResponseID: "response-previous"}); err != nil {
		t.Fatal(err)
	}
	request := NormalizedRequest{Model: "model", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, SourceFormat: normalize.FormatOpenAIChat, Session: normalize.SessionContext{ID: "client-session"}}
	if err := k.Execute(context.Background(), request, Credential{}, httptest.NewRecorder()); err != nil {
		t.Fatal(err)
	}
	if adapter.previousResponse != "response-previous" {
		t.Fatalf("previous response was not loaded into normalized request: %q", adapter.previousResponse)
	}
	if adapter.sessionOperationRef != chatOperationRef {
		t.Fatalf("adapter did not receive exact operation-scoped session ref: %#v", adapter.sessionOperationRef)
	}
	if adapter.plannedPreviousResponse != "response-previous" {
		t.Fatalf("compatibility admission did not receive loaded continuation state: %q", adapter.plannedPreviousResponse)
	}
	if !adapter.plannedContinuityFacet {
		t.Fatal("compatibility admission omitted the stored continuity facet")
	}
	state, ok, err := sessions.Load(context.Background(), route, "client-session")
	if err != nil || !ok || state.ResponseID != "response-next" {
		t.Fatalf("saved session state=%#v ok=%v err=%v", state, ok, err)
	}
}

func TestKernelFailsClosedWhenSessionStateCannotBeLoaded(t *testing.T) {
	sessionStoreRef := extensions.Ref{Kind: "session_store", ID: "failing-session", ContractVersion: 1}
	route := Route{ID: "route", DefinitionID: "provider", ExternalModel: "model", Protocol: ProtocolOpenAIChat, Enabled: true, OperationBindings: map[normalize.Operation]RouteOperationBinding{
		normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"continuity-session"}, SessionStoreRef: sessionStoreRef},
	}}
	snapshot, err := BuildSnapshot(SnapshotInput{
		PublicModels: []PublicModel{{Name: "model", TargetRef: "model"}},
		Nodes:        []ModelNode{{ID: "model", Kind: ModelPhysical, Members: []MemberRef{{Kind: MemberRoute, ID: "route", Fidelity: FidelityExact}}}},
		Routes:       []Route{route},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := New(snapshot, nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	adapter := &continuitySessionAdapter{}
	engine.Adapters[adapter.ID()] = adapter
	engine.SessionStores[sessionStoreRef] = failingSessionStore{}
	request := NormalizedRequest{
		Model: "model", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1,
		SourceFormat: normalize.FormatOpenAIChat, Session: normalize.SessionContext{ID: "client-session"},
	}
	err = engine.Execute(context.Background(), request, Credential{}, httptest.NewRecorder())
	if !errors.Is(err, ErrSessionStateUnavailable) {
		t.Fatalf("session load failure=%v, want ErrSessionStateUnavailable", err)
	}
	if strings.Contains(err.Error(), "private storage path detail") {
		t.Fatalf("session backend detail escaped to request boundary: %v", err)
	}
	if adapter.plannedPreviousResponse != "" || adapter.previousResponse != "" {
		t.Fatalf("kernel planned or dispatched request after failed continuity load: planned=%q dispatched=%q", adapter.plannedPreviousResponse, adapter.previousResponse)
	}
}

func TestMemorySessionStoreExpiresStateAndCopiesOpaqueData(t *testing.T) {
	route := Route{DefinitionID: "provider", DefinitionRef: extensions.Ref{Kind: "provider-definition", ID: "provider", ContractVersion: 1}, SessionOperationRef: extensions.Ref{Kind: "operation", ID: "chat.generate", ContractVersion: 1}, ExternalModel: "model", CredentialID: "account"}
	route.HealthGeneration.Connection = "principal-A"
	store := NewMemorySessionStore()
	providerExpiry := time.Now().Add(12 * time.Hour)
	data := json.RawMessage(`{"opaque":"value"}`)
	if err := store.Save(context.Background(), route, "session", SessionState{ProviderData: data, ExpiresAt: providerExpiry}); err != nil {
		t.Fatal(err)
	}
	data[2] = 'X'
	state, ok, err := store.Load(context.Background(), route, "session")
	if err != nil || !ok || string(state.ProviderData) != `{"opaque":"value"}` || !state.ExpiresAt.Equal(providerExpiry) {
		t.Fatalf("state=%#v ok=%v err=%v", state, ok, err)
	}
	longExpiry := time.Now().Add(48 * time.Hour)
	if err := store.Save(context.Background(), route, "long-expiry", SessionState{ResponseID: "bounded", ExpiresAt: longExpiry}); err != nil {
		t.Fatal(err)
	}
	bounded, ok, err := store.Load(context.Background(), route, "long-expiry")
	if err != nil || !ok || !bounded.ExpiresAt.Before(longExpiry) || time.Until(bounded.ExpiresAt) > store.TTL {
		t.Fatalf("provider state expiry was not capped at the memory-store TTL: state=%#v ok=%v err=%v", bounded, ok, err)
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
	otherOperation := route
	otherOperation.SessionOperationRef = extensions.Ref{Kind: "operation", ID: "responses.generate", ContractVersion: 1}
	if _, ok, err := store.Load(context.Background(), otherOperation, "session"); err != nil || ok {
		t.Fatalf("session state leaked across provider operations: ok=%v err=%v", ok, err)
	}
	otherOperationVersion := route
	otherOperationVersion.SessionOperationRef.ContractVersion = 2
	if _, ok, err := store.Load(context.Background(), otherOperationVersion, "session"); err != nil || ok {
		t.Fatalf("session state leaked across operation contract versions: ok=%v err=%v", ok, err)
	}
	otherProviderVersion := route
	otherProviderVersion.DefinitionRef.ContractVersion = 2
	if _, ok, err := store.Load(context.Background(), otherProviderVersion, "session"); err != nil || ok {
		t.Fatalf("session state leaked across provider-definition versions: ok=%v err=%v", ok, err)
	}
}
