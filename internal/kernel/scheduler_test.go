package kernel

import (
	"sync"
	"testing"
	"time"
)

func TestSchedulerStickyLimitKeepsRouteForConfiguredWindow(t *testing.T) {
	scheduler := NewScheduler(nil)
	model := ResolvedModel{PublicName: "sticky", Strategy: StrategyRoundRobinFallback, StickyLimit: 3, Candidates: []Route{{ID: "a", Enabled: true}, {ID: "b", Enabled: true}}}
	for index, want := range []string{"a", "a", "a", "b", "b", "b"} {
		got, err := scheduler.Select(model, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if got.ID != want {
			t.Fatalf("select %d=%s want %s", index, got.ID, want)
		}
	}
}

func TestSchedulerWeightedOrderPreservesFallbackCandidates(t *testing.T) {
	scheduler := NewScheduler(nil)
	model := ResolvedModel{PublicName: "weighted", Strategy: StrategyWeighted, Candidates: []Route{{ID: "a", Weight: 3, Enabled: true}, {ID: "b", Weight: 1, Enabled: true}}}
	counts := map[string]int{}
	for i := 0; i < 40; i++ {
		ordered := scheduler.Order(model, time.Now())
		if len(ordered) != 2 {
			t.Fatalf("ordered=%#v", ordered)
		}
		counts[ordered[0].ID]++
	}
	if counts["a"] <= counts["b"] {
		t.Fatalf("weights not reflected: %#v", counts)
	}
}

func TestSchedulerConcurrentSelectionIsRaceSafe(t *testing.T) {
	scheduler := NewScheduler(nil)
	model := ResolvedModel{PublicName: "concurrent", Strategy: StrategyRoundRobinFallback, StickyLimit: 2, Candidates: []Route{{ID: "a", Enabled: true}, {ID: "b", Enabled: true}, {ID: "c", Enabled: true}}}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := scheduler.Select(model, time.Now()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}
