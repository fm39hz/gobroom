package runtime

import (
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
)

func TestPerformanceBookKeepsBoundedEWMA(t *testing.T) {
	book := NewPerformanceBook()
	route := kernel.Route{ID: "route"}
	now := time.Now()
	book.Observe(route, kernel.UsageEvent{At: now, Status: "ok", Latency: time.Second, TTFT: 200 * time.Millisecond, OutputTokensPerSecond: 10})
	book.Observe(route, kernel.UsageEvent{At: now.Add(time.Second), Status: "ok", Latency: 2 * time.Second, TTFT: 400 * time.Millisecond, OutputTokensPerSecond: 5})
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
