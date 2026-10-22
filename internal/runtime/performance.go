package runtime

import (
	"sync"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
)

// PerformanceStats is an in-memory, bounded summary of real completed
// requests. It is deliberately not a request-history log.
type PerformanceStats struct {
	Samples           int
	Successes         int
	EWMLatency        time.Duration
	EWMTTFT           time.Duration
	EWMThroughput     float64
	TTFTSamples       int
	ThroughputSamples int
	LastAt            time.Time
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
	b.byRoute[route.ID] = observePerformanceStats(b.byRoute[route.ID], event, b.alpha)
	if event.RequestClass != "" {
		classes := b.byClass[route.ID]
		if classes == nil {
			classes = map[string]PerformanceStats{}
			b.byClass[route.ID] = classes
		}
		classes[event.RequestClass] = observePerformanceStats(classes[event.RequestClass], event, b.alpha)
	}
	b.mu.Unlock()
}

func observePerformanceStats(stats PerformanceStats, event kernel.UsageEvent, alpha float64) PerformanceStats {
	stats.Samples++
	if event.Status == "ok" {
		stats.Successes++
	}
	stats.EWMLatency = ewmaDuration(stats.EWMLatency, event.Latency, alpha)
	if event.TTFT > 0 || !event.FirstByteAt.IsZero() {
		if stats.TTFTSamples == 0 {
			stats.EWMTTFT = event.TTFT
		} else {
			stats.EWMTTFT = time.Duration(ewmaFloat(float64(stats.EWMTTFT), float64(event.TTFT), alpha))
		}
		stats.TTFTSamples++
	}
	if event.OutputTokensPerSecond != nil {
		if stats.ThroughputSamples == 0 {
			stats.EWMThroughput = *event.OutputTokensPerSecond
		} else {
			stats.EWMThroughput = ewmaFloat(stats.EWMThroughput, *event.OutputTokensPerSecond, alpha)
		}
		stats.ThroughputSamples++
	}
	stats.LastAt = event.At
	return stats
}

func (b *PerformanceBook) ClassSnapshot(routeID, requestClass string) (PerformanceStats, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	stats, ok := b.byClass[routeID][requestClass]
	return stats, ok
}

func (b *PerformanceBook) RouteSnapshot(routeID string) (PerformanceStats, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	stats, ok := b.byRoute[routeID]
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

func (b *PerformanceBook) ResetRoutes(routeIDs []string) {
	if b == nil || len(routeIDs) == 0 {
		return
	}
	b.mu.Lock()
	for _, routeID := range routeIDs {
		delete(b.byRoute, routeID)
		delete(b.byClass, routeID)
	}
	b.mu.Unlock()
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
	return previous*(1-alpha) + value*alpha
}
