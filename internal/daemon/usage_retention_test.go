package daemon

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

func TestUsageRetentionRunsAtStartupAndQuarterHourlyAndStops(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		var cutoffs []time.Time
		done := runUsageRetention(ctx, 24*time.Hour, func(ctx context.Context, before time.Time, limit int) (int64, error) {
			if ctx.Err() != nil || limit <= 0 {
				t.Fatal("invalid retention budget")
			}
			cutoffs = append(cutoffs, before)
			return 0, nil
		}, func() { t.Error("unexpected retention failure") })
		synctest.Wait()
		if len(cutoffs) != 1 || !cutoffs[0].Equal(time.Now().Add(-24*time.Hour)) {
			t.Fatalf("startup retention missing: %v", cutoffs)
		}
		time.Sleep(15 * time.Minute)
		synctest.Wait()
		if len(cutoffs) != 2 || !cutoffs[1].Equal(time.Now().Add(-24*time.Hour)) {
			t.Fatalf("periodic retention missing: %v", cutoffs)
		}
		cancel()
		<-done
		time.Sleep(15 * time.Minute)
		synctest.Wait()
		if len(cutoffs) != 2 {
			t.Fatal("retention ran after daemon cancellation")
		}
	})
}
