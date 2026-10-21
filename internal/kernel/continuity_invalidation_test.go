package kernel

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/normalize"
)

func TestMemorySessionStoreInvalidateIsCompareAndSwap(t *testing.T) {
	route := sessionNamespaceTestRoute()
	store := NewMemorySessionStore()
	ctx := context.Background()

	t.Run("removes matching state", func(t *testing.T) {
		if err := store.Save(ctx, route, "client", SessionState{ResponseID: "observed"}); err != nil {
			t.Fatal(err)
		}
		state, ok, err := store.Load(ctx, route, "client")
		if err != nil || !ok {
			t.Fatalf("Load() ok=%v err=%v", ok, err)
		}
		removed, err := store.Invalidate(ctx, route, "client", state)
		if err != nil || !removed {
			t.Fatalf("Invalidate()=(%v,%v), want (true,nil)", removed, err)
		}
		if _, ok, err := store.Load(ctx, route, "client"); err != nil || ok {
			t.Fatalf("state remained after invalidation: ok=%v err=%v", ok, err)
		}
	})

	t.Run("preserves newer state", func(t *testing.T) {
		newer := SessionState{ResponseID: "newer"}
		if err := store.Save(ctx, route, "newer", SessionState{ResponseID: "observed"}); err != nil {
			t.Fatal(err)
		}
		observed, ok, err := store.Load(ctx, route, "newer")
		if err != nil || !ok {
			t.Fatalf("Load() ok=%v err=%v", ok, err)
		}
		if err := store.Save(ctx, route, "newer", newer); err != nil {
			t.Fatal(err)
		}
		removed, err := store.Invalidate(ctx, route, "newer", observed)
		got, ok, loadErr := store.Load(ctx, route, "newer")
		if err != nil || removed || loadErr != nil || !ok || got.ResponseID != newer.ResponseID {
			t.Fatalf("CAS removed newer state: removed=%v state=%#v ok=%v err=%v loadErr=%v", removed, got, ok, err, loadErr)
		}
	})

	t.Run("missing state", func(t *testing.T) {
		removed, err := store.Invalidate(ctx, route, "missing", SessionState{ResponseID: "old"})
		if err != nil || removed {
			t.Fatalf("Invalidate(missing)=(%v,%v), want (false,nil)", removed, err)
		}
	})

	t.Run("cancelled context", func(t *testing.T) {
		state := SessionState{ResponseID: "cancelled"}
		if err := store.Save(ctx, route, "cancelled", state); err != nil {
			t.Fatal(err)
		}
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		removed, err := store.Invalidate(cancelled, route, "cancelled", state)
		if !errors.Is(err, context.Canceled) || removed {
			t.Fatalf("Invalidate(cancelled)=(%v,%v), want (false, context.Canceled)", removed, err)
		}
		if _, ok, err := store.Load(ctx, route, "cancelled"); err != nil || !ok {
			t.Fatalf("cancelled invalidation changed state: ok=%v err=%v", ok, err)
		}
	})

	t.Run("principal and endpoint isolation", func(t *testing.T) {
		state := SessionState{ResponseID: "private"}
		if err := store.Save(ctx, route, "isolated", state); err != nil {
			t.Fatal(err)
		}
		otherPrincipal := route
		otherPrincipal.HealthGeneration.Connection = "principal-B"
		otherEndpoint := route
		otherEndpoint.BaseURL = "https://other.example.test/v1"
		for name, changed := range map[string]Route{"principal": otherPrincipal, "endpoint": otherEndpoint} {
			t.Run(name, func(t *testing.T) {
				removed, err := store.Invalidate(ctx, changed, "isolated", state)
				if err != nil || removed {
					t.Fatalf("cross-namespace Invalidate()=(%v,%v), want (false,nil)", removed, err)
				}
			})
		}
		if got, ok, err := store.Load(ctx, route, "isolated"); err != nil || !ok || got.ResponseID != state.ResponseID {
			t.Fatalf("original namespace was changed: state=%#v ok=%v err=%v", got, ok, err)
		}
	})
}

type continuityInvalidAdapter struct {
	*continuitySessionAdapter
	id      string
	outcome ClassifiedOutcome
	status  int
	calls   int
	store   *MemorySessionStore
	route   Route
	session string
	save    *SessionState
}

func (a *continuityInvalidAdapter) ID() string { return a.id }

func (a *continuityInvalidAdapter) Execute(ctx context.Context, _ UpstreamRequest) (UpstreamResponse, error) {
	a.calls++
	if a.save != nil {
		if err := a.store.Save(ctx, a.route, a.session, *a.save); err != nil {
			return UpstreamResponse{}, err
		}
	}
	return UpstreamResponse{Status: a.status, Body: http.NoBody}, nil
}

func (a *continuityInvalidAdapter) ClassifyError(int, []byte) ErrorClass { return a.outcome.Class }

func (a *continuityInvalidAdapter) ClassifyOutcome(int, http.Header, []byte) ClassifiedOutcome {
	return a.outcome
}

func validContinuityInvalidOutcome() ClassifiedOutcome {
	return ClassifiedOutcome{
		Class: ErrorTerminal, Cause: CauseContinuityInvalid, Effect: EffectRejected,
		Scope: ScopeRequest, Retry: RetryNever, Recovery: CredentialRecoveryNone,
		ContinuityRecovery: ContinuityRecoveryInvalidate,
	}
}

func newContinuityInvalidKernel(t *testing.T, first, second *continuityInvalidAdapter) (*Kernel, Route, extensions.Ref, string) {
	t.Helper()
	storeRef := extensions.Ref{Kind: "session_store", ID: "session", ContractVersion: 1}
	providerRef := extensions.Ref{Kind: "provider-definition", ID: "provider", ContractVersion: 1}
	operationRef := extensions.Ref{Kind: "operation", ID: string(normalize.OperationChatGenerate), ContractVersion: 1}
	routeFor := func(id, adapterID string, withStore bool) Route {
		binding := RouteOperationBinding{ContractVersion: 1, AdapterIDs: []string{adapterID}}
		if withStore {
			binding.SessionStoreRef = storeRef
		}
		route := Route{ID: id, NodeID: "provider", DefinitionID: "provider", DefinitionRef: providerRef,
			SessionOperationRef: operationRef, ExternalModel: "upstream-model", CredentialID: "account",
			Protocol: ProtocolOpenAIChat, Enabled: true,
			OperationBindings: map[normalize.Operation]RouteOperationBinding{normalize.OperationChatGenerate: binding}}
		route.HealthGeneration.Connection = "principal-A"
		return route
	}
	first.id, second.id = "continuity-invalid-first", "continuity-invalid-fallback"
	route := routeFor("first", first.ID(), true)
	secondRoute := routeFor("second", second.ID(), false)
	snapshot, err := BuildSnapshot(SnapshotInput{
		PublicModels: []PublicModel{{Name: "model", TargetRef: "model"}},
		Nodes: []ModelNode{{ID: "model", Kind: ModelPhysical, Strategy: StrategyFallback, Members: []MemberRef{
			{Kind: MemberRoute, ID: route.ID, Fidelity: FidelityExact}, {Kind: MemberRoute, ID: secondRoute.ID, Fidelity: FidelityExact},
		}}},
		Routes: []Route{route, secondRoute},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	k, err := New(snapshot, nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k.Close)
	first.continuitySessionAdapter = &continuitySessionAdapter{}
	second.continuitySessionAdapter = &continuitySessionAdapter{}
	first.route, second.route = route, secondRoute
	first.session, second.session = "client-session", "client-session"
	k.Adapters[first.ID()] = first
	k.Adapters[second.ID()] = second
	store := NewMemorySessionStore()
	k.SessionStores[storeRef] = store
	first.store, second.store = store, store
	return k, route, storeRef, "client-session"
}

func TestKernelContinuityInvalidationRequiresRestartWithoutFallback(t *testing.T) {
	valid := validContinuityInvalidOutcome()
	for name, change := range map[string]func(*ClassifiedOutcome){
		"wrong cause":  func(o *ClassifiedOutcome) { o.Cause = CauseRequestInvalid },
		"wrong retry":  func(o *ClassifiedOutcome) { o.Retry = RetryAfter },
		"wrong effect": func(o *ClassifiedOutcome) { o.Effect = EffectUnknown },
	} {
		t.Run(name, func(t *testing.T) {
			outcome := valid
			change(&outcome)
			first := &continuityInvalidAdapter{outcome: outcome, status: http.StatusBadRequest}
			second := &continuityInvalidAdapter{outcome: valid, status: http.StatusBadRequest}
			k, route, _, session := newContinuityInvalidKernel(t, first, second)
			if err := k.SessionStores[route.OperationBindings[normalize.OperationChatGenerate].SessionStoreRef].(SessionStore).Save(context.Background(), route, session, SessionState{ResponseID: "saved"}); err != nil {
				t.Fatal(err)
			}
			err := k.Execute(context.Background(), continuityInvalidRequest("model", session, ""), Credential{}, httptest.NewRecorder())
			if !errors.Is(err, ErrContinuityRecoveryInvalid) {
				t.Fatalf("Execute()=%v, want ErrContinuityRecoveryInvalid", err)
			}
			if first.calls != 1 || second.calls != 0 {
				t.Fatalf("dispatches first=%d fallback=%d, want 1 and 0", first.calls, second.calls)
			}
			state, ok, loadErr := k.SessionStores[route.OperationBindings[normalize.OperationChatGenerate].SessionStoreRef].(SessionStore).Load(context.Background(), route, session)
			if loadErr != nil || !ok || state.ResponseID != "saved" {
				t.Fatalf("invalid evidence reset state: %#v ok=%v err=%v", state, ok, loadErr)
			}
		})
	}
}

func TestKernelContinuityInvalidationAndCAS(t *testing.T) {
	t.Run("clears rejected loaded state and returns restart required", func(t *testing.T) {
		first := &continuityInvalidAdapter{outcome: validContinuityInvalidOutcome(), status: http.StatusBadRequest}
		second := &continuityInvalidAdapter{outcome: validContinuityInvalidOutcome(), status: http.StatusBadRequest}
		k, route, storeRef, session := newContinuityInvalidKernel(t, first, second)
		store := k.SessionStores[storeRef].(*MemorySessionStore)
		if err := store.Save(context.Background(), route, session, SessionState{ResponseID: "saved"}); err != nil {
			t.Fatal(err)
		}
		err := k.Execute(context.Background(), continuityInvalidRequest("model", session, ""), Credential{}, httptest.NewRecorder())
		if !errors.Is(err, ErrContinuityRestartRequired) {
			t.Fatalf("Execute()=%v, want ErrContinuityRestartRequired", err)
		}
		if first.calls != 1 || second.calls != 0 {
			t.Fatalf("dispatches first=%d fallback=%d, want 1 and 0", first.calls, second.calls)
		}
		if _, ok, err := store.Load(context.Background(), route, session); err != nil || ok {
			t.Fatalf("rejected continuation remained cached: ok=%v err=%v", ok, err)
		}
	})

	t.Run("explicit different parent preserves unrelated cache", func(t *testing.T) {
		first := &continuityInvalidAdapter{outcome: validContinuityInvalidOutcome(), status: http.StatusBadRequest}
		second := &continuityInvalidAdapter{outcome: validContinuityInvalidOutcome(), status: http.StatusBadRequest}
		k, route, storeRef, session := newContinuityInvalidKernel(t, first, second)
		store := k.SessionStores[storeRef].(*MemorySessionStore)
		if err := store.Save(context.Background(), route, session, SessionState{ResponseID: "saved-parent"}); err != nil {
			t.Fatal(err)
		}
		err := k.Execute(context.Background(), continuityInvalidRequest("model", session, "caller-parent"), Credential{}, httptest.NewRecorder())
		if !errors.Is(err, ErrContinuityRestartRequired) {
			t.Fatalf("Execute()=%v, want ErrContinuityRestartRequired", err)
		}
		state, ok, loadErr := store.Load(context.Background(), route, session)
		if loadErr != nil || !ok || state.ResponseID != "saved-parent" {
			t.Fatalf("unrelated cached state was invalidated: %#v ok=%v err=%v", state, ok, loadErr)
		}
	})

	t.Run("CAS preserves identical state re-saved during rejected dispatch", func(t *testing.T) {
		newer := SessionState{ResponseID: "observed", ExpiresAt: time.Now().Add(time.Hour)}
		first := &continuityInvalidAdapter{outcome: validContinuityInvalidOutcome(), status: http.StatusBadRequest, save: &newer}
		second := &continuityInvalidAdapter{outcome: validContinuityInvalidOutcome(), status: http.StatusBadRequest}
		k, route, storeRef, session := newContinuityInvalidKernel(t, first, second)
		store := k.SessionStores[storeRef].(*MemorySessionStore)
		if err := store.Save(context.Background(), route, session, newer); err != nil {
			t.Fatal(err)
		}
		err := k.Execute(context.Background(), continuityInvalidRequest("model", session, ""), Credential{}, httptest.NewRecorder())
		if !errors.Is(err, ErrContinuityRestartRequired) {
			t.Fatalf("Execute()=%v, want ErrContinuityRestartRequired", err)
		}
		state, ok, loadErr := store.Load(context.Background(), route, session)
		if loadErr != nil || !ok || state.ResponseID != newer.ResponseID {
			t.Fatalf("CAS did not preserve concurrent success: %#v ok=%v err=%v", state, ok, loadErr)
		}
	})
}

func TestMemorySessionStoreVersionProtectsIdenticalResave(t *testing.T) {
	store := NewMemorySessionStore()
	route := sessionNamespaceTestRoute()
	ctx := context.Background()
	state := SessionState{ResponseID: "identical", ExpiresAt: time.Now().Add(time.Hour)}
	if err := store.Save(ctx, route, "client", state); err != nil {
		t.Fatal(err)
	}
	observed, _, err := store.Load(ctx, route, "client")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, route, "client", observed); err != nil {
		t.Fatal(err)
	}
	removed, err := store.Invalidate(ctx, route, "client", observed)
	latest, found, loadErr := store.Load(ctx, route, "client")
	if err != nil || loadErr != nil || removed || !found || latest.ResponseID != observed.ResponseID || latest.StoreVersion == observed.StoreVersion || latest.StoreVersion == 0 {
		t.Fatalf("identical resave lost: removed=%v latest=%#v found=%v err=%v loadErr=%v", removed, latest, found, err, loadErr)
	}
}

func continuityInvalidRequest(model, session, previous string) NormalizedRequest {
	return NormalizedRequest{
		Model: model, Operation: normalize.OperationChatGenerate, OperationContractVersion: 1,
		SourceFormat: normalize.FormatOpenAIChat, Session: normalize.SessionContext{ID: session},
		Continuity: normalize.ContinuityState{PreviousResponse: previous},
	}
}
