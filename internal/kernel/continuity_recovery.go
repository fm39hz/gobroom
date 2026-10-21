package kernel

import "context"

func validContinuityRecovery(outcome ClassifiedOutcome, effect UpstreamEffect) bool {
	return outcome.ContinuityRecovery == ContinuityRecoveryInvalidate && outcome.Cause == CauseContinuityInvalid && outcome.Effect == effect && outcome.Scope == ScopeRequest && outcome.Class == ErrorTerminal && outcome.Retry == RetryNever && outcome.Recovery.Effective() == CredentialRecoveryNone && outcome.RetryAt.IsZero() && len(outcome.Limits) == 0
}

func (k *Kernel) invalidateLoadedContinuity(ctx context.Context, route Route, request NormalizedRequest, sessionID string, saved SessionState, loaded bool) error {
	if !loaded || emptySessionState(saved) || saved.ResponseID != "" && saved.ResponseID != request.Continuity.PreviousResponse {
		return nil
	}
	store := k.SessionStores[route.SessionStoreRef]
	if store == nil {
		return ErrSessionStateUnavailable
	}
	if _, err := store.Invalidate(ctx, route, sessionID, saved); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrSessionStateUnavailable
	}
	return nil
}
