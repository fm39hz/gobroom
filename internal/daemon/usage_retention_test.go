package daemon

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestUsageRetentionRunsAtStartupAndQuarterHourlyAndStops(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		var cutoffs []time.Time
		var mu sync.Mutex
		snapshot := func() []time.Time { mu.Lock(); defer mu.Unlock(); return append([]time.Time(nil), cutoffs...) }
		done := runUsageRetention(ctx, 24*time.Hour, func(ctx context.Context, before time.Time, limit int) (int64, error) {
			if ctx.Err() != nil || limit <= 0 {
				t.Fatal("invalid retention budget")
			}
			mu.Lock()
			cutoffs = append(cutoffs, before)
			mu.Unlock()
			return 0, nil
		}, func() { t.Error("unexpected retention failure") })
		synctest.Wait()
		observed := snapshot()
		if len(observed) != 1 || !observed[0].Equal(time.Now().Add(-24*time.Hour)) {
			t.Fatalf("startup retention missing: %v", observed)
		}
		time.Sleep(15 * time.Minute)
		synctest.Wait()
		observed = snapshot()
		if len(observed) != 2 || !observed[1].Equal(time.Now().Add(-24*time.Hour)) {
			t.Fatalf("periodic retention missing: %v", observed)
		}
		cancel()
		<-done
		time.Sleep(15 * time.Minute)
		synctest.Wait()
		if len(snapshot()) != 2 {
			t.Fatal("retention ran after daemon cancellation")
		}
	})
}
