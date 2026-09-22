package runtime

import (
	"sync"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
)

// PerformanceStats is an in-memory, bounded summary of real completed
// requests. It is deliberately not a request-history log.
type PerformanceStats struct {
	Samples       int
	Successes     int
	EWMLatency    time.Duration
	EWMTTFT       time.Duration
	EWMThroughput float64
	LastAt        time.Time
}

type PerformanceBook struct {
	mu      sync.RWMutex
	alpha   float64
	byRoute map[string]PerformanceStats
}

func NewPerformanceBook() *PerformanceBook {
	return &PerformanceBook{alpha: 0.2, byRoute: map[string]PerformanceStats{}}
}

func (b *PerformanceBook) Observe(route kernel.Route, event kernel.UsageEvent) {
	if b == nil {
		return
	}
	b.mu.Lock()
	stats := b.byRoute[route.ID]
	stats.Samples++
	if event.Status == "ok" {
		stats.Successes++
	}
	stats.EWMLatency = ewmaDuration(stats.EWMLatency, event.Latency, b.alpha)
	stats.EWMTTFT = ewmaDuration(stats.EWMTTFT, event.TTFT, b.alpha)
	if event.OutputTokensPerSecond > 0 {
		stats.EWMThroughput = ewmaFloat(stats.EWMThroughput, event.OutputTokensPerSecond, b.alpha)
	}
	stats.LastAt = event.At
	b.byRoute[route.ID] = stats
	b.mu.Unlock()
}

func (b *PerformanceBook) Snapshot() map[string]PerformanceStats {
	b.mu.RLock()
	defer b.mu.RUnlock()
	result := make(map[string]PerformanceStats, len(b.byRoute))
	for id, stats := range b.byRoute {
		result[id] = stats
	}
	return result
}

func ewmaDuration(previous, value time.Duration, alpha float64) time.Duration {
	if value <= 0 {
		return previous
	}
	if previous <= 0 {
		return value
	}
	return time.Duration(float64(previous)*(1-alpha) + float64(value)*alpha)
}

func ewmaFloat(previous, value, alpha float64) float64 {
	if previous <= 0 {
		return value
	}
	return previous*(1-alpha) + value*alpha
}
