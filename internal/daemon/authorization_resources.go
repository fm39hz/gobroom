package daemon

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"

	"github.com/fm39hz/gobroom/internal/provider"
)

type authorizationCallbackReservation struct {
	requested   provider.AuthorizationCallback
	resolved    provider.AuthorizationCallback
	mode        provider.AuthorizationCallbackMode
	bindAddress string
}

const maxAuthorizationResourceRounds = 8

func (d *Daemon) validateSetupTransition(transition provider.AuthorizationTransition) error {
	if transition.Validate(time.Now()) != nil {
		return fmt.Errorf("provider returned an invalid authorization transition")
	}
	for _, action := range transition.Actions {
		if action.InputSchemaRef == nil {
			continue
		}
		if d.setupSchemas == nil {
			return fmt.Errorf("authorization schema catalog is unavailable")
		}
		if _, ok := d.setupSchemas.Schema(*action.InputSchemaRef); !ok {
			return fmt.Errorf("authorization action schema is not registered")
		}
	}
	return nil
}

// Resource negotiation belongs to the already admitted setup step. Intermediate
// states/actions never become a public revision; the final transition alone is
// applied through the normal owner/revision/CAS boundary.
func (d *Daemon) prepareSetupCallbackResources(ctx context.Context, id string, transition provider.AuthorizationTransition) (provider.AuthorizationTransition, error) {
	for round := 0; ; round++ {
		if err := ctx.Err(); err != nil {
			return provider.AuthorizationTransition{}, err
		}
		if err := d.validateSetupTransition(transition); err != nil {
			return provider.AuthorizationTransition{}, err
		}
		needs := transition.PrepareCallbacks
		for _, callback := range transition.Callbacks {
			needs = needs || callback.NeedsAllocation()
		}
		if !needs {
			return transition, nil
		}
		if round >= maxAuthorizationResourceRounds {
			return provider.AuthorizationTransition{}, fmt.Errorf("authorization resource negotiation exceeded its round limit")
		}
		sessions := d.authorizationSessions()
		sessions.mu.Lock()
		session, exists := sessions.byID[id]
		if sessions.closed || !exists || !sessions.inFlight[id] || (session.status != "starting" && session.status != "awaiting_user") || ctx.Err() != nil {
			sessions.mu.Unlock()
			return provider.AuthorizationTransition{}, context.Canceled
		}
		if !session.expiresAt.After(time.Now()) || transition.ExpiresAt.After(session.expiresAt) {
			sessions.mu.Unlock()
			return provider.AuthorizationTransition{}, fmt.Errorf("authorization preparation cannot extend its deadline")
		}
		session.expiresAt = transition.ExpiresAt
		session.callbackKey = id
		sessions.byID[id] = session
		resources, err := d.reserveAuthorizationCallbacks(ctx, id, transition.Callbacks)
		sessions.mu.Unlock()
		if err != nil {
			return provider.AuthorizationTransition{}, err
		}
		current, ok := d.store.ConnectionCredentialByID(session.connectionID)
		if !ok || current.Owner != session.expectedCredential.Owner || current.Revision != session.expectedCredential.Revision || current.PrincipalGeneration != session.expectedCredential.PrincipalGeneration {
			return provider.AuthorizationTransition{}, fmt.Errorf("connection changed during authorization preparation")
		}
		if err := ctx.Err(); err != nil {
			return provider.AuthorizationTransition{}, err
		}
		stepCtx, cancel := context.WithDeadline(ctx, session.expiresAt)
		if err := stepCtx.Err(); err != nil {
			cancel()
			return provider.AuthorizationTransition{}, err
		}
		if d.runCtx != nil && d.runCtx.Err() != nil {
			cancel()
			return provider.AuthorizationTransition{}, d.runCtx.Err()
		}
		next, err := session.setupFlow.AdvanceAuthorization(stepCtx, transition.State, provider.AuthorizationEvent{Kind: provider.AuthorizationCallbacksReady, Callbacks: resources})
		if stepCtx.Err() != nil {
			err = stepCtx.Err()
		}
		cancel()
		if err != nil {
			return provider.AuthorizationTransition{}, err
		}
		transition = next
	}
}

// Reservation grants a real socket, not permission to dispatch a callback.
// The driver receives its final URI before it can publish a consent action.
func (d *Daemon) reserveAuthorizationCallbacks(ctx context.Context, owner string, specs []provider.AuthorizationCallback) ([]provider.AuthorizationCallbackResource, error) {
	if owner == "" || len(specs) == 0 || len(specs) > 8 {
		return nil, fmt.Errorf("invalid callback resource request")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(specs))
	for _, spec := range specs {
		if spec.Validate() != nil || seen[spec.ActionID] {
			return nil, fmt.Errorf("invalid or duplicate callback resource request")
		}
		seen[spec.ActionID] = true
	}
	d.callbackMu.Lock()
	if d.callbacksClosed {
		d.callbackMu.Unlock()
		return nil, fmt.Errorf("callback registry is stopped")
	}
	if d.callbackListeners == nil {
		d.callbackListeners = make(map[string]*authorizationCallbackListener)
	}
	if d.callbackReservations == nil {
		d.callbackReservations = make(map[authorizationCallbackKey]authorizationCallbackReservation)
	}
	created := make(map[string]*authorizationCallbackListener)
	pending := make(map[authorizationCallbackKey]authorizationCallbackReservation, len(specs))
	resources := make([]provider.AuthorizationCallbackResource, 0, len(specs))
	rollback := func(err error) ([]provider.AuthorizationCallbackResource, error) {
		for _, listener := range created {
			_ = listener.listener.Close()
		}
		d.callbackMu.Unlock()
		return nil, err
	}
	for _, spec := range specs {
		if err := ctx.Err(); err != nil {
			return rollback(err)
		}
		key := authorizationCallbackKey{owner: owner, action: spec.ActionID}
		if existing, ok := d.callbackReservations[key]; ok && sameCallbackGrant(existing.requested, spec) {
			pending[key] = existing
			resources = append(resources, provider.AuthorizationCallbackResource{ActionID: spec.ActionID, URL: existing.resolved.URL, Mode: existing.mode})
			continue
		}
		target, _ := url.Parse(spec.URL)
		_, address, ok := loopbackCallbackAddress(target, true)
		if !ok {
			return rollback(fmt.Errorf("callback allocation target is not local"))
		}
		resolved := spec
		resolved.ResponseParameters = append([]string(nil), spec.ResponseParameters...)
		mode := provider.AuthorizationCallbackLoopback
		if spec.NeedsAllocation() || (d.callbackListeners[address] == nil && created[address] == nil) {
			var config net.ListenConfig
			listener, err := config.Listen(ctx, "tcp", address)
			if err != nil {
				if ctx.Err() != nil {
					return rollback(ctx.Err())
				}
				if spec.Required || spec.NeedsAllocation() {
					return rollback(fmt.Errorf("callback resource could not be allocated"))
				}
				mode = provider.AuthorizationCallbackManual
			} else {
				if spec.NeedsAllocation() {
					allocated := *target
					allocated.Host = net.JoinHostPort(target.Hostname(), strconv.Itoa(listener.Addr().(*net.TCPAddr).Port))
					resolved.URL = allocated.String()
					_, address, _ = loopbackCallbackAddress(&allocated, false)
				}
				created[address] = &authorizationCallbackListener{server: d.newAuthorizationCallbackServer(), listener: listener}
			}
		}
		requested := spec
		requested.ResponseParameters = append([]string(nil), spec.ResponseParameters...)
		pending[key] = authorizationCallbackReservation{requested: requested, resolved: resolved, mode: mode, bindAddress: address}
		resources = append(resources, provider.AuthorizationCallbackResource{ActionID: spec.ActionID, URL: resolved.URL, Mode: mode})
	}
	if err := ctx.Err(); err != nil {
		return rollback(err)
	}
	for key := range d.callbackReservations {
		if key.owner == owner {
			delete(d.callbackReservations, key)
		}
	}
	for key, reservation := range pending {
		d.callbackReservations[key] = reservation
	}
	for address, listener := range created {
		d.callbackListeners[address] = listener
	}
	retired := d.pruneAuthorizationCallbackListenersLocked()
	d.callbackMu.Unlock()
	for _, listener := range retired {
		shutdownCallbackListener(listener)
	}
	for _, listener := range created {
		d.serveAuthorizationCallback(listener)
	}
	return resources, nil
}
