package runtime

import (
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
)

func TestPerformanceKnownZeroIsAnObservedSample(t *testing.T) {
	book := NewPerformanceBook()
	route := kernel.Route{ID: "zero-observation"}
	at := time.Date(2026, time.October, 22, 12, 0, 0, 0, time.UTC)
	book.Observe(route, kernel.UsageEvent{At: at, FirstByteAt: at, Status: "ok", RequestClass: "text", Latency: time.Second, OutputTokensPerSecond: new(float64(0))})
	book.Observe(route, kernel.UsageEvent{At: at.Add(time.Second), FirstByteAt: at.Add(time.Second + 100*time.Millisecond), Status: "ok", RequestClass: "text", Latency: time.Second, TTFT: 100 * time.Millisecond, OutputTokensPerSecond: new(float64(10))})
	for _, class := range []string{"", "text"} {
		stats, ok := book.RouteSnapshot(route.ID)
		if class != "" {
			stats, ok = book.ClassSnapshot(route.ID, class)
		}
		if !ok || stats.EWMThroughput != 2 || stats.EWMTTFT != 20*time.Millisecond {
			t.Fatalf("known zero was treated as uninitialized for class %q: %#v", class, stats)
		}
	}
}

func TestPerformanceBookKeepsBoundedEWMA(t *testing.T) {
	book := NewPerformanceBook()
	route := kernel.Route{ID: "route"}
	now := time.Now()
	book.Observe(route, kernel.UsageEvent{At: now, Status: "ok", Latency: time.Second, TTFT: 200 * time.Millisecond, OutputTokensPerSecond: new(float64(10))})
	book.Observe(route, kernel.UsageEvent{At: now.Add(time.Second), Status: "ok", Latency: 2 * time.Second, TTFT: 400 * time.Millisecond, OutputTokensPerSecond: new(float64(5))})
	stats := book.Snapshot()[route.ID]
	if stats.Samples != 2 || stats.Successes != 2 {
		t.Fatalf("unexpected samples: %#v", stats)
	}
	if stats.EWMLatency <= time.Second || stats.EWMLatency >= 2*time.Second {
		t.Fatalf("latency is not an EWMA: %v", stats.EWMLatency)
	}
}

func TestPerformanceBookSeparatesRequestClasses(t *testing.T) {
	book := NewPerformanceBook()
	route := kernel.Route{ID: "route"}
	book.Observe(route, kernel.UsageEvent{At: time.Now(), Status: "ok", RequestClass: "vision"})
	book.Observe(route, kernel.UsageEvent{At: time.Now(), Status: "ok", RequestClass: "text"})
	if stats, ok := book.ClassSnapshot(route.ID, "vision"); !ok || stats.Samples != 1 {
		t.Fatalf("vision class missing: %#v %v", stats, ok)
	}
	if stats, ok := book.ClassSnapshot(route.ID, "text"); !ok || stats.Samples != 1 {
		t.Fatalf("text class missing: %#v %v", stats, ok)
	}
}

func TestPerformanceBookPreservesAbsentAndExplicitZeroUsage(t *testing.T) {
	book := NewPerformanceBook()
	route := kernel.Route{ID: "route"}
	now := time.Now()
	book.Observe(route, kernel.UsageEvent{At: now, Status: "ok", TTFT: time.Second, OutputTokensPerSecond: new(float64(10))})
	book.Observe(route, kernel.UsageEvent{At: now.Add(time.Second), Status: "ok"})
	stats := book.Snapshot()[route.ID]
	if stats.EWMTTFT != time.Second || stats.EWMThroughput != 10 {
		t.Fatalf("absent usage changed performance estimates: %#v", stats)
	}
	book.Observe(route, kernel.UsageEvent{At: now.Add(2 * time.Second), Status: "ok", TTFT: 2 * time.Second, OutputTokensPerSecond: new(float64(0))})
	stats = book.Snapshot()[route.ID]
	if stats.EWMTTFT <= time.Second || stats.EWMThroughput != 8 {
		t.Fatalf("explicit usage values were not sampled: %#v", stats)
	}
}
