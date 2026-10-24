package sessionstore

import (
	"context"
	"testing"

	"github.com/fm39hz/gobroom/internal/kernel"
)

// A successful memory update is not an acknowledgement of queue admission.
// Removing the overflow error must fail this test, while live continuity must
// remain available when its asynchronous persistence could not be admitted.
func TestCacheSaveReportsOverflowWithoutDiscardingLiveState(t *testing.T) {
	c, err := New(&cacheFixtureRepository{})
	if err != nil {
		t.Fatal(err)
	}
	c.writes = make(chan write, 1)
	route := invalidationRoute()
	if err := c.Save(context.Background(), route, "session", kernel.SessionState{ResponseID: "first"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Save(context.Background(), route, "session", kernel.SessionState{ResponseID: "second"}); err == nil {
		t.Fatal("full persistence queue was acknowledged as a successful save")
	}
	state, found, err := c.Load(context.Background(), route, "session")
	if err != nil || !found || state.ResponseID != "second" {
		t.Fatalf("live state lost: %#v found=%v err=%v", state, found, err)
	}
	if got := c.PersistenceStatus(); got.QueueDropped != 1 {
		t.Fatalf("queueDropped=%d want1", got.QueueDropped)
	}
}
