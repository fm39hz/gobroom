package daemon

import (
	"context"
	"errors"
	"testing"
	"time"
)

const credentialLockSafetyTimeout = 2 * time.Second

func TestCredentialRefreshLockCanceledWaiterReturnsWhileOwnerHolds(t *testing.T) {
	d := &Daemon{}
	ownerUnlock, err := d.lockCredentialRefresh(context.Background(), "account-a")
	if err != nil {
		t.Fatalf("owner acquisition failed: %v", err)
	}
	defer ownerUnlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waiterStarted := make(chan struct{})
	waiterResult := make(chan error, 1)
	go func() {
		close(waiterStarted)
		unlock, err := d.lockCredentialRefresh(ctx, "account-a")
		if unlock != nil {
			unlock()
		}
		waiterResult <- err
	}()
	<-waiterStarted
	cancel()

	select {
	case err := <-waiterResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiter error = %v, want context.Canceled", err)
		}
	case <-time.After(credentialLockSafetyTimeout):
		t.Fatal("canceled waiter did not return while owner still held the lock")
	}
}

func TestCredentialRefreshLockPreCanceledContextDoesNotAcquire(t *testing.T) {
	d := &Daemon{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	unlock, err := d.lockCredentialRefresh(ctx, "account-a")
	if unlock != nil {
		unlock()
		t.Fatal("pre-canceled acquisition returned an unlock function")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("acquisition error = %v, want context.Canceled", err)
	}

	// If the canceled call took ownership, this acquisition will remain blocked.
	assertCredentialRefreshLockAcquires(t, d, "account-a")
}

func TestCredentialRefreshLockUnrelatedConnectionIsNotBlocked(t *testing.T) {
	d := &Daemon{}
	ownerUnlock, err := d.lockCredentialRefresh(context.Background(), "account-a")
	if err != nil {
		t.Fatalf("owner acquisition failed: %v", err)
	}
	defer ownerUnlock()

	assertCredentialRefreshLockAcquires(t, d, "account-b")
}

func TestCredentialRefreshLockReusableAfterCancellationAndRelease(t *testing.T) {
	d := &Daemon{}
	ownerUnlock, err := d.lockCredentialRefresh(context.Background(), "account-a")
	if err != nil {
		t.Fatalf("owner acquisition failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	waiterStarted := make(chan struct{})
	waiterResult := make(chan error, 1)
	go func() {
		close(waiterStarted)
		unlock, err := d.lockCredentialRefresh(ctx, "account-a")
		if unlock != nil {
			unlock()
		}
		waiterResult <- err
	}()
	<-waiterStarted
	cancel()
	select {
	case err := <-waiterResult:
		if !errors.Is(err, context.Canceled) {
			ownerUnlock()
			t.Fatalf("canceled waiter error = %v, want context.Canceled", err)
		}
	case <-time.After(credentialLockSafetyTimeout):
		ownerUnlock()
		t.Fatal("canceled waiter did not return")
	}

	ownerUnlock()
	assertCredentialRefreshLockAcquires(t, d, "account-a")
}

func assertCredentialRefreshLockAcquires(t *testing.T, d *Daemon, connectionID string) {
	t.Helper()
	type acquisition struct {
		unlock func()
		err    error
	}
	result := make(chan acquisition, 1)
	go func() {
		unlock, err := d.lockCredentialRefresh(context.Background(), connectionID)
		result <- acquisition{unlock: unlock, err: err}
	}()
	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("acquisition for %q failed: %v", connectionID, got.err)
		}
		if got.unlock == nil {
			t.Fatalf("acquisition for %q returned no unlock function", connectionID)
		}
		got.unlock()
	case <-time.After(credentialLockSafetyTimeout):
		t.Fatalf("acquisition for %q did not complete", connectionID)
	}
}
