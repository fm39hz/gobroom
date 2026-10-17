package daemon

import (
	"context"
	"fmt"
	"time"

	"github.com/fm39hz/gobroom/internal/store"
)

const credentialActivationAttempts = 5
const credentialActivationInterval = time.Second

type CredentialActivationProgress = store.CredentialActivationProgress
type CredentialTransition = store.CredentialTransition

const (
	CredentialRenewal    = store.CredentialRenewal
	PrincipalReplacement = store.PrincipalReplacement
)

type CredentialActivationView struct {
	ConnectionID        string                       `json:"connectionId"`
	Transition          store.CredentialTransition   `json:"transition"`
	CredentialCommitted bool                         `json:"credentialCommitted"`
	Status              string                       `json:"status"`
	Attempts            int                          `json:"attempts"`
	NextAttempt         time.Time                    `json:"nextAttempt,omitzero"`
	ErrorCode           string                       `json:"errorCode,omitempty"`
	Progress            CredentialActivationProgress `json:"progress"`
}

func activationView(a store.CredentialActivation) CredentialActivationView {
	return CredentialActivationView{ConnectionID: a.Commit.ConnectionID, Transition: a.Commit.Transition, CredentialCommitted: true, Status: a.Status, Attempts: a.Attempts, NextAttempt: a.NextAttempt, ErrorCode: a.ErrorCode, Progress: a.Progress}
}

func commitMatchesCredential(key store.CredentialCommit, current store.Credential) bool {
	return key.ConnectionID == current.ID && key.CredentialType == current.Type && key.Revision == current.Revision && key.PrincipalGeneration == current.PrincipalGeneration && key.Owner == current.Owner
}

func (d *Daemon) credentialActivationView(ctx context.Context, key store.CredentialCommit) CredentialActivationView {
	a, ok, err := d.store.CredentialActivation(ctx, key.ConnectionID)
	if err == nil && ok && a.Commit == key {
		return d.currentActivationView(a)
	}
	status := store.ActivationSuperseded
	code := ""
	if err != nil {
		status, code = store.ActivationPending, "activation_status_unavailable"
	}
	return CredentialActivationView{ConnectionID: key.ConnectionID, Transition: key.Transition, CredentialCommitted: true, Status: status, ErrorCode: code}
}

func (d *Daemon) currentActivationView(a store.CredentialActivation) CredentialActivationView {
	view := activationView(a)
	current, exists := d.store.ConnectionCredentialByID(a.Commit.ConnectionID)
	if !exists || !commitMatchesCredential(a.Commit, current) {
		view.Status, view.NextAttempt, view.ErrorCode = store.ActivationSuperseded, time.Time{}, ""
	}
	return view
}

// Only local activation runs here: no auth exchange or provider request is
// repeated. Callers hold the connection's cancellable credential lock.
func (d *Daemon) activateCredentialLocked(ctx context.Context, key store.CredentialCommit) (CredentialActivationView, error) {
	a, ok, err := d.store.CredentialActivation(ctx, key.ConnectionID)
	if err != nil {
		return d.credentialActivationView(ctx, key), err
	}
	if !ok || a.Commit != key {
		return CredentialActivationView{ConnectionID: key.ConnectionID, CredentialCommitted: true, Status: store.ActivationSuperseded}, nil
	}
	if a.Status != store.ActivationPending || ctx.Err() != nil {
		return activationView(a), ctx.Err()
	}
	current, exists := d.store.ConnectionCredentialByID(key.ConnectionID)
	if !exists || !commitMatchesCredential(key, current) {
		a.Status, a.NextAttempt, a.ErrorCode = store.ActivationSuperseded, time.Time{}, ""
	} else {
		a.Attempts++
		a.ErrorCode = ""
		if !a.Progress.RoutingReset {
			reset, resetErr := d.store.ResetCredentialRoutingState(ctx, key)
			if resetErr != nil {
				a.ErrorCode = "routing_reset"
			} else if !reset {
				a.Status = store.ActivationSuperseded
			} else {
				a.Progress.RoutingReset = true
			}
		}
		if a.Status == store.ActivationPending && a.ErrorCode == "" {
			if d.server == nil {
				a.ErrorCode = "serving_unavailable"
			} else {
				receipt, reloadErr := d.server.Reload()
				a.Progress.ControlVersion, a.Progress.ControlPublished = receipt.Control.Version, receipt.Control.Published
				a.Progress.VersionPersisted = receipt.Control.VersionPersisted
				a.Progress.ServingVersion, a.Progress.ServingPublished = receipt.ServingVersion, receipt.ServingPublished
				if reloadErr != nil {
					a.ErrorCode = receipt.FailureStage
					if a.ErrorCode == "" {
						a.ErrorCode = "reload"
					}
				} else {
					a.Status = store.ActivationActive
				}
			}
		}
		// Reconfiguration may race publication. Never report the old token as
		// current, nor overwrite a replacement job installed by a newer CAS.
		current, exists = d.store.ConnectionCredentialByID(key.ConnectionID)
		if !exists || !commitMatchesCredential(key, current) {
			a.Status, a.ErrorCode = store.ActivationSuperseded, ""
		}
		if a.Status == store.ActivationPending {
			if a.Attempts >= credentialActivationAttempts {
				a.Status = store.ActivationExhausted
			} else {
				a.NextAttempt = time.Now().Add(credentialActivationInterval)
			}
		}
		if a.Status != store.ActivationPending {
			a.NextAttempt = time.Time{}
		}
	}
	accepted, err := d.store.SaveCredentialActivation(ctx, a)
	if err != nil {
		return activationView(a), err
	}
	if !accepted {
		return d.credentialActivationView(ctx, key), nil
	}
	if a.ErrorCode != "" {
		d.appendLog("warn", "credential committed; activation pending: connection="+key.ConnectionID+" stage="+a.ErrorCode)
	}
	return activationView(a), nil
}

func (d *Daemon) signalCredentialActivation() {
	select {
	case d.activationWake <- struct{}{}:
	default:
	}
}

func (d *Daemon) activationContext() context.Context {
	if d.runCtx != nil {
		return d.runCtx
	}
	return context.Background()
}

func (d *Daemon) retryCredentialActivation(ctx context.Context, id string) (CredentialActivationView, error) {
	unlock, err := d.lockCredentialRefresh(ctx, id)
	if err != nil {
		return CredentialActivationView{}, err
	}
	defer unlock()
	a, ok, err := d.store.CredentialActivation(ctx, id)
	if err != nil {
		return CredentialActivationView{}, err
	}
	if !ok {
		return CredentialActivationView{}, fmt.Errorf("credential activation not found")
	}
	current, exists := d.store.ConnectionCredentialByID(id)
	if !exists || !commitMatchesCredential(a.Commit, current) {
		return CredentialActivationView{}, fmt.Errorf("credential activation is superseded")
	}
	if a.Status == store.ActivationActive {
		return activationView(a), nil
	}
	a.Status, a.Attempts, a.NextAttempt, a.ErrorCode = store.ActivationPending, 0, time.Now(), ""
	accepted, err := d.store.SaveCredentialActivation(ctx, a)
	if err != nil {
		return CredentialActivationView{}, err
	}
	if !accepted {
		return CredentialActivationView{}, fmt.Errorf("credential activation changed during retry")
	}
	view, err := d.activateCredentialLocked(d.activationContext(), a.Commit)
	d.signalCredentialActivation()
	return view, err
}

// A persisted per-connection outbox survives a crash between CAS and wakeup.
// The timer is disabled when idle; transient infrastructure failures have a
// bounded in-memory retry budget even if progress itself cannot be persisted.
func (d *Daemon) runCredentialActivations(ctx context.Context) {
	defer close(d.activationDone)
	timer := time.NewTimer(0)
	defer timer.Stop()
	tick := timer.C
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.activationWake:
			failures = 0
		case <-tick:
		}
		if ctx.Err() != nil {
			return
		}
		batch, err := d.store.PendingCredentialActivations(ctx, time.Now(), 32)
		failed := err != nil
		for _, a := range batch {
			unlock, lockErr := d.lockCredentialRefresh(ctx, a.Commit.ConnectionID)
			if lockErr != nil {
				return
			}
			_, applyErr := d.activateCredentialLocked(ctx, a.Commit)
			unlock()
			failed = failed || applyErr != nil
			if ctx.Err() != nil {
				return
			}
		}
		var next time.Time
		var pending bool
		if !failed {
			next, pending, err = d.store.NextCredentialActivation(ctx)
			failed = err != nil
		}
		if failed {
			failures++
			if failures >= credentialActivationAttempts {
				d.appendLog("error", "credential activation storage unavailable; explicit retry or restart required")
				timer.Stop()
				tick = nil
				continue
			}
			timer.Reset(credentialActivationInterval)
			tick = timer.C
			continue
		}
		failures = 0
		if pending {
			timer.Reset(max(time.Until(next), 0))
			tick = timer.C
		} else {
			timer.Stop()
			tick = nil
		}
	}
}
