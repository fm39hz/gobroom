package sessionstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/sessionpersistence"
	"github.com/fm39hz/gobroom/internal/store"
)

type persistenceRepository struct {
	entered chan struct{}
	results chan error
}

func newPersistenceRepository() *persistenceRepository {
	return &persistenceRepository{make(chan struct{}, 8), make(chan error, 8)}
}
func (*persistenceRepository) ProviderSessions() ([]store.ProviderSession, error)    { return nil, nil }
func (*persistenceRepository) PruneExpiredProviderSessions(time.Time) (int64, error) { return 0, nil }
func (r *persistenceRepository) SaveProviderSession(store.ProviderSession) error {
	r.entered <- struct{}{}
	return <-r.results
}

func startPersistenceWorker(c *Cache) (context.CancelFunc, <-chan struct{}) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.Run(ctx) }()
	return cancel, done
}

func receiveWithin[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case value := <-ch:
		return value
	case <-timer.C:
		var zero T
		t.Fatal("timed out waiting for cache persistence event")
		return zero
	}
}

func TestCachePersistenceFailureRemainsVisibleAfterLaterSuccess(t *testing.T) {
	repository := newPersistenceRepository()
	c, err := New(repository)
	if err != nil {
		t.Fatal(err)
	}
	failures := make(chan struct{}, 1)
	c.OnFailure = func() { failures <- struct{}{} }
	cancel, done := startPersistenceWorker(c)
	route := invalidationRoute()
	if err := c.Save(context.Background(), route, "s", kernel.SessionState{ResponseID: "first"}); err != nil {
		t.Fatal(err)
	}
	receiveWithin(t, repository.entered)
	repository.results <- errors.New("private error")
	receiveWithin(t, failures)
	if err := c.Save(context.Background(), route, "s", kernel.SessionState{ResponseID: "later"}); err != nil {
		t.Fatal(err)
	}
	receiveWithin(t, repository.entered)
	repository.results <- nil
	cancel()
	receiveWithin(t, done)
	want := sessionpersistence.Status{Received: 2, Persisted: 1, Failed: 1, QueueDropped: 0, SaveErrors: 0}
	if got := c.PersistenceStatus(); got != want {
		t.Fatalf("status=%+v want %+v", got, want)
	}
}

func TestCachePersistenceOverflowPreservesLiveStateAndNotifiesOutsideLock(t *testing.T) {
	c, err := New(newPersistenceRepository())
	if err != nil {
		t.Fatal(err)
	}
	c.writes = make(chan write, 1)
	route := invalidationRoute()
	observed := make(chan string, 1)
	c.OnFailure = func() {
		state, found, err := c.Load(context.Background(), route, "s")
		if err == nil && found {
			observed <- state.ResponseID
		}
	}
	if err := c.Save(context.Background(), route, "s", kernel.SessionState{ResponseID: "queued"}); err != nil {
		t.Fatal(err)
	}
	saveResult := make(chan error, 1)
	go func() {
		saveResult <- c.Save(context.Background(), route, "s", kernel.SessionState{ResponseID: "live"})
	}()
	if err := receiveWithin(t, saveResult); err == nil {
		t.Fatal("queue overflow was acknowledged")
	}
	if got := receiveWithin(t, observed); got != "live" {
		t.Fatalf("callback observed %q, want live", got)
	}
	state, found, err := c.Load(context.Background(), route, "s")
	if err != nil || !found || state.ResponseID != "live" {
		t.Fatalf("live state=%#v found=%v err=%v", state, found, err)
	}
	want := sessionpersistence.Status{Received: 0, Persisted: 0, Failed: 0, QueueDropped: 1, SaveErrors: 0}
	if got := c.PersistenceStatus(); got != want {
		t.Fatalf("status=%+v want %+v", got, want)
	}
}

func TestCachePersistenceInvalidationAcknowledgementAndDrainCounters(t *testing.T) {
	repository := newPersistenceRepository()
	c, err := New(repository)
	if err != nil {
		t.Fatal(err)
	}
	cancel, done := startPersistenceWorker(c)
	route, ctx := invalidationRoute(), context.Background()
	if err := c.Save(ctx, route, "s", kernel.SessionState{ResponseID: "old"}); err != nil {
		t.Fatal(err)
	}
	receiveWithin(t, repository.entered)
	state, found, err := c.Load(ctx, route, "s")
	if err != nil || !found || state.ResponseID != "old" {
		t.Fatalf("initial state=%#v found=%v err=%v", state, found, err)
	}
	type result struct {
		removed bool
		err     error
	}
	failed := make(chan result, 1)
	go func() { removed, err := c.Invalidate(ctx, route, "s", state); failed <- result{removed, err} }()
	repository.results <- nil            // initial save
	receiveWithin(t, repository.entered) // invalidation tombstone
	repository.results <- errors.New("private repository detail")
	first := receiveWithin(t, failed)
	if first.removed || first.err == nil {
		t.Fatalf("failed invalidation=%+v", first)
	}
	state, found, err = c.Load(ctx, route, "s")
	if err != nil || !found || state.ResponseID != "old" {
		t.Fatalf("failed invalidation did not restore state: %#v found=%v err=%v", state, found, err)
	}
	ack := make(chan result, 1)
	go func() { removed, err := c.Invalidate(ctx, route, "s", state); ack <- result{removed, err} }()
	receiveWithin(t, repository.entered)
	if err := c.Save(ctx, route, "s", kernel.SessionState{ResponseID: "new"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Save(ctx, route, "drain", kernel.SessionState{ResponseID: "queued"}); err != nil {
		t.Fatal(err)
	}
	repository.results <- nil
	second := receiveWithin(t, ack)
	if !second.removed || second.err != nil {
		t.Fatalf("successful invalidation acknowledgement=%+v", second)
	}
	receiveWithin(t, repository.entered) // newer state active; other queued record drains on shutdown
	cancel()
	repository.results <- nil
	receiveWithin(t, repository.entered)
	repository.results <- nil
	receiveWithin(t, done)
	state, found, err = c.Load(ctx, route, "s")
	if err != nil || !found || state.ResponseID != "new" {
		t.Fatalf("newer state=%#v found=%v err=%v", state, found, err)
	}
	want := sessionpersistence.Status{Received: 5, Persisted: 4, Failed: 1, QueueDropped: 0, SaveErrors: 0}
	if got := c.PersistenceStatus(); got != want {
		t.Fatalf("status=%+v want %+v", got, want)
	}
}

func TestCacheSupersededWriteIsDiscardedWithoutFaultAndPreservesInvalidationAck(t *testing.T) {
	privateErr := errors.New("private repository detail")
	supersededErr := errors.Join(kernel.ErrSessionStateSuperseded, privateErr)

	repository := newPersistenceRepository()
	c, err := New(repository)
	if err != nil {
		t.Fatal(err)
	}
	notified := make(chan struct{}, 1)
	c.OnFailure = func() { notified <- struct{}{} }
	cancel, done := startPersistenceWorker(c)
	if err := c.Save(context.Background(), invalidationRoute(), "stale", kernel.SessionState{ResponseID: "response"}); err != nil {
		t.Fatal(err)
	}
	receiveWithin(t, repository.entered)
	repository.results <- supersededErr
	cancel()
	receiveWithin(t, done)
	want := sessionpersistence.Status{Received: 1, Persisted: 0, Failed: 0, Discarded: 1, QueueDropped: 0, SaveErrors: 0}
	if got := c.PersistenceStatus(); got != want {
		t.Fatalf("superseded write status=%+v want %+v", got, want)
	}
	if got := c.PersistenceStatus().Faulted(); got {
		t.Fatal("superseded write faulted persistence status")
	}
	select {
	case <-notified:
		t.Fatal("superseded write invoked OnFailure")
	default:
	}

	ackRepository := newPersistenceRepository()
	ackCache, err := New(ackRepository)
	if err != nil {
		t.Fatal(err)
	}
	ackCancel, ackDone := startPersistenceWorker(ackCache)
	route := invalidationRoute()
	if err := ackCache.Save(context.Background(), route, "ack", kernel.SessionState{ResponseID: "current"}); err != nil {
		t.Fatal(err)
	}
	receiveWithin(t, ackRepository.entered)
	ackRepository.results <- nil
	state, found, err := ackCache.Load(context.Background(), route, "ack")
	if err != nil || !found || state.ResponseID != "current" {
		t.Fatalf("invalidation setup state=%#v found=%v err=%v", state, found, err)
	}
	type invalidationResult struct {
		removed bool
		err     error
	}
	resultCh := make(chan invalidationResult, 1)
	go func() {
		removed, err := ackCache.Invalidate(context.Background(), route, "ack", state)
		resultCh <- invalidationResult{removed: removed, err: err}
	}()
	receiveWithin(t, ackRepository.entered)
	ackRepository.results <- supersededErr
	result := receiveWithin(t, resultCh)
	if result.removed || !errors.Is(result.err, kernel.ErrSessionStateSuperseded) || !errors.Is(result.err, privateErr) {
		t.Fatalf("invalidation did not return original joined acknowledgement: %+v", result)
	}
	ackCancel()
	receiveWithin(t, ackDone)
}
