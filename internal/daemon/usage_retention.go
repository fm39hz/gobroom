package daemon

import (
	"context"
	"time"
)

// Retention is local housekeeping, not a provider probe. Short transactional
// batches and a per-pass deadline keep it cooperative with live persistence.
func runUsageRetention(ctx context.Context, retention time.Duration, prune func(context.Context, time.Time, int) (int64, error), onFailure func()) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()
		prunePass := func() {
			if ctx.Err() != nil {
				return
			}
			passCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			before := time.Now().Add(-retention)
			for passCtx.Err() == nil {
				deleted, err := prune(passCtx, before, 1000)
				if err != nil {
					if ctx.Err() == nil && onFailure != nil {
						onFailure()
					}
					return
				}
				if deleted < 1000 {
					return
				}
			}
			if ctx.Err() == nil && onFailure != nil {
				onFailure()
			}
		}
		prunePass()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				prunePass()
			}
		}
	}()
	return done
}
