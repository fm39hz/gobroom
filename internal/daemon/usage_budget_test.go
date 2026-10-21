package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"
)

func TestUsageDrainTimeoutRejectsNegativeBeforeStateInitialization(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state.db")
	d := New(Config{DBPath: path, IPCPath: filepath.Join(root, "daemon.sock"), UsageDrainTimeout: -time.Second})
	if err := d.Start(context.Background()); err == nil {
		_ = d.Stop(context.Background())
		t.Fatal("negative usage drain budget was accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("invalid deployment config initialized state: %v", err)
	}
}

func TestDrainUsageBudgetCancelsOperationAndJoinsBeforeReturn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		operationCtx, cancelOperation := context.WithCancelCause(context.Background())
		defer cancelOperation(nil)
		joined := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			<-operationCtx.Done()
			close(joined)
			done <- context.Cause(operationCtx)
		}()

		result := make(chan error, 1)
		go func() {
			result <- drainUsage(context.Background(), time.Second, cancelOperation, done)
		}()
		time.Sleep(time.Second)
		err := <-result
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("drainUsage error = %v, want deadline", err)
		}
		select {
		case <-joined:
		default:
			t.Fatal("drainUsage returned before the canceled operation joined")
		}
		if !errors.Is(context.Cause(operationCtx), context.DeadlineExceeded) {
			t.Fatalf("operation cause = %v, want deadline", context.Cause(operationCtx))
		}
	})
}

func TestDrainUsageCallerCancellationCancelsOperationAndJoins(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		callerCtx, cancelCaller := context.WithCancel(context.Background())
		defer cancelCaller()
		operationCtx, cancelOperation := context.WithCancelCause(context.Background())
		defer cancelOperation(nil)
		joined := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			<-operationCtx.Done()
			close(joined)
			done <- context.Cause(operationCtx)
		}()

		result := make(chan error, 1)
		go func() {
			result <- drainUsage(callerCtx, time.Hour, cancelOperation, done)
		}()
		cancelCaller()
		err := <-result
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("drainUsage error = %v, want context.Canceled", err)
		}
		select {
		case <-joined:
		default:
			t.Fatal("drainUsage returned before the canceled operation joined")
		}
		if !errors.Is(context.Cause(operationCtx), context.Canceled) {
			t.Fatalf("operation cause = %v, want context.Canceled", context.Cause(operationCtx))
		}
	})
}

func TestDrainUsageHealthyCompletionStopsBudgetTimer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		operationCtx, cancelOperation := context.WithCancelCause(context.Background())
		defer cancelOperation(nil)
		done := make(chan error, 1)
		done <- nil

		if err := drainUsage(context.Background(), time.Second, cancelOperation, done); err != nil {
			t.Fatalf("drainUsage error = %v, want nil", err)
		}
		if !errors.Is(context.Cause(operationCtx), context.Canceled) {
			t.Fatalf("operation cause after drain = %v, want cleanup cancellation", context.Cause(operationCtx))
		}
		time.Sleep(2 * time.Second)
		if !errors.Is(context.Cause(operationCtx), context.Canceled) {
			t.Fatalf("stopped budget timer changed operation cause to %v", context.Cause(operationCtx))
		}
	})
}
