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
		if !engine.EmitUsage(UsageEvent{InputTokens: new(int64(1))}) {
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
					if engine.EmitUsage(UsageEvent{InputTokens: new(int64(1))}) {
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

func TestKernelUsageOverflowIsCountedSeparatelyFromClose(t *testing.T) {
	engine := &Kernel{Events: make(chan UsageEvent, 1)}
	if !engine.EmitUsage(UsageEvent{}) {
		t.Fatal("first event rejected")
	}
	for range 2 {
		if engine.EmitUsage(UsageEvent{}) {
			t.Fatal("full queue accepted another event")
		}
	}
	if engine.DroppedUsage() != 2 {
		t.Fatalf("overflow count=%d", engine.DroppedUsage())
	}
	engine.Close()
	if engine.EmitUsage(UsageEvent{}) || engine.DroppedUsage() != 2 {
		t.Fatal("normal post-close rejection changed overflow evidence")
	}
}
