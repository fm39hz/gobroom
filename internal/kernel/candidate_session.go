package kernel

import (
	"context"

	"github.com/fm39hz/gobroom/internal/normalize"
)

// restoreCandidateSession applies the same scoped, read-only continuation
// evidence to execution and preflight. State never becomes explanation output.
func (k *Kernel) restoreCandidateSession(ctx context.Context, route Route, request *NormalizedRequest) (SessionState, bool, error) {
	var state SessionState
	loaded := false
	if key := requestSessionKey(*request); key != "" && route.SessionStoreRef.ID != "" {
		store := k.SessionStores[route.SessionStoreRef]
		if store == nil {
			return state, false, ErrSessionStateUnavailable
		}
		saved, found, err := store.Load(ctx, route, key)
		if err != nil {
			return state, false, ErrSessionStateUnavailable
		}
		if found {
			if saved.StoreVersion == 0 {
				return state, false, ErrSessionStateUnavailable
			}
			state, loaded = saved, true
			request.Session.ProviderState = append([]byte(nil), saved.ProviderData...)
			if request.Continuity.PreviousResponse == "" {
				request.Continuity.PreviousResponse = saved.ResponseID
			}
		}
	}
	if requestHasToolSignature(request.Messages) {
		*request = normalize.CloneRequest(*request)
		attestToolCallSignatures(request, state.Signatures, route.DefinitionRef)
	}
	return state, loaded, nil
}
