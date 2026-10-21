package daemon

import (
	"context"
	"errors"
	"time"

	"github.com/fm39hz/gobroom/internal/usage"
)

const defaultUsageDrainTimeout = 5 * time.Second

type UsagePersistenceStatus = usage.Status

var ErrUsagePersistenceIncomplete = usage.ErrPersistenceIncomplete

// Producers have already closed. The worker's repository must honor context;
// never detach a live writer and close its database underneath it.
func drainUsage(ctx context.Context, budget time.Duration, cancel context.CancelCauseFunc, done <-chan error) error {
	if budget == 0 {
		budget = defaultUsageDrainTimeout
	}
	timer := time.AfterFunc(budget, func() { cancel(context.DeadlineExceeded) })
	stopCaller := context.AfterFunc(ctx, func() { cancel(ctx.Err()) })
	defer timer.Stop()
	defer stopCaller()
	defer cancel(nil)
	err := <-done
	return errors.Join(err, ctx.Err())
}
