package usage

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
)

const workerTestTimeout = 5 * time.Second

type recordingRepository struct {
	saved   chan kernel.UsageEvent
	enter   chan kernel.UsageEvent
	release <-chan struct{}
}

func (r *recordingRepository) SaveUsageEvent(_ context.Context, event kernel.UsageEvent) error {
	if r.enter != nil {
		r.enter <- event
		<-r.release
	}
	if r.saved != nil {
		r.saved <- event
	}
	return nil
}

func TestWorkerDrainsAcceptedEventsInOrderAfterClose(t *testing.T) {
	events := make(chan kernel.UsageEvent, 3)
	want := []kernel.UsageEvent{
		{LogicalModel: "first", InputTokens: new(int64(1))},
		{LogicalModel: "second", InputTokens: new(int64(2))},
		{LogicalModel: "third", InputTokens: new(int64(3))},
	}
	for _, event := range want {
		events <- event
	}
	close(events)

	saved := make(chan kernel.UsageEvent, len(want))
	done := make(chan struct{})
	go func() {
		worker := &Worker{Store: &recordingRepository{saved: saved}, Events: events}
		_ = worker.Run(context.Background())
		close(done)
	}()

	for i, expected := range want {
		select {
		case got := <-saved:
			if !reflect.DeepEqual(got, expected) {
				t.Fatalf("saved event %d = %+v, want %+v", i, got, expected)
			}
		case <-time.After(workerTestTimeout):
			t.Fatalf("timed out waiting for saved event %d", i)
		}
	}

	select {
	case <-done:
	case <-time.After(workerTestTimeout):
		t.Fatal("worker did not return after draining closed Events")
	}
}

func TestWorkerWaitsForInFlightSaveBeforeReturning(t *testing.T) {
	events := make(chan kernel.UsageEvent, 1)
	event := kernel.UsageEvent{LogicalModel: "held", OutputTokens: new(int64(7))}
	events <- event
	close(events)

	entered := make(chan kernel.UsageEvent, 1)
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		worker := &Worker{Store: &recordingRepository{enter: entered, release: release}, Events: events}
		_ = worker.Run(context.Background())
		close(done)
	}()

	select {
	case got := <-entered:
		if !reflect.DeepEqual(got, event) {
			t.Fatalf("saving event = %+v, want %+v", got, event)
		}
	case <-time.After(workerTestTimeout):
		t.Fatal("timed out waiting for SaveUsageEvent to start")
	}

	select {
	case <-done:
		t.Fatal("worker returned while SaveUsageEvent was blocked")
	default:
	}

	close(release)
	select {
	case <-done:
	case <-time.After(workerTestTimeout):
		t.Fatal("worker did not return after SaveUsageEvent was released")
	}
}
