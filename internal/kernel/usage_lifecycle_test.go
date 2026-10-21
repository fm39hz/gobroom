package kernel

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestKernelUsageCloseSerializesConcurrentProducers(t *testing.T) {
	for range 32 {
		engine := &Kernel{Events: make(chan UsageEvent, 32)}
		var accepted atomic.Int64
		if !engine.EmitUsage(UsageEvent{InputTokens: 1}) {
			t.Fatal("initial event rejected")
		}
		accepted.Add(1)
		drained := make(chan int64, 1)
		go func() {
			var count int64
			for range engine.Events {
				count++
			}
			drained <- count
		}()
		start := make(chan struct{})
		var producers sync.WaitGroup
		for range 16 {
			producers.Go(func() {
				<-start
				for range 64 {
					if engine.EmitUsage(UsageEvent{InputTokens: 1}) {
						accepted.Add(1)
					}
				}
			})
		}
		closed := make(chan struct{})
		go func() { <-start; engine.Close(); close(closed) }()
		close(start)
		producers.Wait()
		<-closed
		engine.Close()
		if engine.EmitUsage(UsageEvent{}) {
			t.Fatal("event accepted after close")
		}
		if count := <-drained; count != accepted.Load() {
			t.Fatalf("drained=%d accepted=%d", count, accepted.Load())
		}
	}
}
