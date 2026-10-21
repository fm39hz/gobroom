package usage

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/fm39hz/gobroom/internal/kernel"
)

var ErrPersistenceIncomplete = errors.New("usage persistence incomplete")

// Status is operational evidence, never a provider failure or a billing claim.
// Live snapshots are independently sampled counters; completed drains are exact.
type Status struct {
	Received     uint64 `json:"received"`
	Persisted    uint64 `json:"persisted"`
	Failed       uint64 `json:"failed"`
	Abandoned    uint64 `json:"abandoned"`
	QueueDropped uint64 `json:"queueDropped"`
}

type Repository interface {
	SaveUsageEvent(context.Context, kernel.UsageEvent) error
}

type Worker struct {
	Store     Repository
	Events    <-chan kernel.UsageEvent
	OnFailure func()
	received  atomic.Uint64
	persisted atomic.Uint64
	failed    atomic.Uint64
	abandoned atomic.Uint64
	notified  atomic.Bool
}

// Run owns one consumer until Events closes. Its context belongs to persistence,
// not to requests: cancel it only to bound the owner's shutdown drain. The
// repository must cooperate with cancellation. Remaining closed-stream events
// are counted as abandoned rather than silently discarded or retried.
func (w *Worker) Run(ctx context.Context) error {
	for event := range w.Events {
		w.received.Add(1)
		if ctx.Err() != nil {
			w.abandoned.Add(1)
			w.notifyFailure()
			continue
		}
		if w.Store == nil {
			w.failed.Add(1)
			w.notifyFailure()
			continue
		}
		if err := w.Store.SaveUsageEvent(ctx, event); err != nil {
			if ctx.Err() != nil {
				w.abandoned.Add(1)
			} else {
				w.failed.Add(1)
			}
			w.notifyFailure()
		} else {
			w.persisted.Add(1)
		}
	}
	status := w.Snapshot()
	var result error
	if status.Failed > 0 || status.Abandoned > 0 {
		result = fmt.Errorf("%w: failed=%d abandoned=%d", ErrPersistenceIncomplete, status.Failed, status.Abandoned)
	}
	// Keep private repository errors and arbitrary context causes out of control
	// clients and logs. Only standard cancellation/deadline identity is exposed.
	if errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
		result = errors.Join(result, context.DeadlineExceeded)
	} else if ctx.Err() != nil {
		result = errors.Join(result, ctx.Err())
	}
	return result
}

func (w *Worker) notifyFailure() {
	if !w.notified.Swap(true) && w.OnFailure != nil {
		w.OnFailure()
	}
}

func (w *Worker) Snapshot() Status {
	return Status{Received: w.received.Load(), Persisted: w.persisted.Load(), Failed: w.failed.Load(), Abandoned: w.abandoned.Load()}
}
