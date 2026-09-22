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
	byClass map[string]map[string]PerformanceStats
}

func NewPerformanceBook() *PerformanceBook {
	return &PerformanceBook{alpha: 0.2, byRoute: map[string]PerformanceStats{}, byClass: map[string]map[string]PerformanceStats{}}
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
	if event.RequestClass != "" {
		classes := b.byClass[route.ID]
		if classes == nil {
			classes = map[string]PerformanceStats{}
			b.byClass[route.ID] = classes
		}
		classStats := classes[event.RequestClass]
		classStats.Samples++
		if event.Status == "ok" {
			classStats.Successes++
		}
		classStats.EWMLatency = ewmaDuration(classStats.EWMLatency, event.Latency, b.alpha)
		classStats.EWMTTFT = ewmaDuration(classStats.EWMTTFT, event.TTFT, b.alpha)
		if event.OutputTokensPerSecond > 0 {
			classStats.EWMThroughput = ewmaFloat(classStats.EWMThroughput, event.OutputTokensPerSecond, b.alpha)
		}
		classStats.LastAt = event.At
		classes[event.RequestClass] = classStats
	}
	b.mu.Unlock()
}

func (b *PerformanceBook) ClassSnapshot(routeID, requestClass string) (PerformanceStats, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	stats, ok := b.byClass[routeID][requestClass]
	return stats, ok
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
