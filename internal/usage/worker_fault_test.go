package usage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/fm39hz/gobroom/internal/kernel"
)

type faultRepository struct {
	calls    int
	first    error
	failures int
}

func (r *faultRepository) SaveUsageEvent(_ context.Context, _ kernel.UsageEvent) error {
	r.calls++
	if r.calls <= r.failures {
		return r.first
	}
	return nil
}

func TestWorkerContinuesAfterPersistenceFailureWithoutLeakingError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		private := errors.New("private database detail")
		repository := &faultRepository{first: private, failures: 2}
		events := make(chan kernel.UsageEvent, 3)
		events <- kernel.UsageEvent{LogicalModel: "failed"}
		events <- kernel.UsageEvent{LogicalModel: "failed-again"}
		events <- kernel.UsageEvent{LogicalModel: "saved"}
		close(events)

		callbacks := 0
		worker := &Worker{Store: repository, Events: events, OnFailure: func() { callbacks++ }}
		err := worker.Run(context.Background())
		if !errors.Is(err, ErrPersistenceIncomplete) {
			t.Fatalf("Run error = %v, want ErrPersistenceIncomplete", err)
		}
		if errors.Is(err, private) || strings.Contains(err.Error(), private.Error()) {
			t.Fatalf("private repository error leaked: %v", err)
		}
		if repository.calls != 3 {
			t.Fatalf("save calls = %d, want 3", repository.calls)
		}
		if callbacks != 1 {
			t.Fatalf("failure callbacks = %d, want 1", callbacks)
		}
		want := (Status{Received: 3, Persisted: 1, Failed: 2})
		if got := worker.Snapshot(); got != want {
			t.Fatalf("Snapshot() = %+v, want %+v", got, want)
		}
	})
}

type blockingRepository struct {
	started chan struct{}
	ctxDone chan struct{}
}

func (r *blockingRepository) SaveUsageEvent(ctx context.Context, _ kernel.UsageEvent) error {
	close(r.started)
	<-ctx.Done()
	close(r.ctxDone)
	return ctx.Err()
}

func TestWorkerDeadlineAbandonsUnwrittenEventsAndReturnsDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		events := make(chan kernel.UsageEvent, 3)
		for _, name := range []string{"in-flight", "queued-1", "queued-2"} {
			events <- kernel.UsageEvent{LogicalModel: name}
		}
		close(events)
		store := &blockingRepository{started: make(chan struct{}), ctxDone: make(chan struct{})}
		ctx, cancel := context.WithCancelCause(context.Background())
		defer cancel(nil)
		worker := &Worker{Store: store, Events: events}
		done := make(chan error, 1)
		go func() { done <- worker.Run(ctx) }()
		<-store.started
		cancel(context.DeadlineExceeded)
		synctest.Wait()
		select {
		case <-store.ctxDone:
		default:
			t.Fatal("repository did not observe owner cancellation")
		}
		err := <-done
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Run error = %v, want context.DeadlineExceeded", err)
		}
		if !errors.Is(err, ErrPersistenceIncomplete) {
			t.Fatalf("Run error = %v, want ErrPersistenceIncomplete", err)
		}
		want := (Status{Received: 3, Abandoned: 3})
		if got := worker.Snapshot(); got != want {
			t.Fatalf("Snapshot() = %+v, want %+v", got, want)
		}
	})
}
