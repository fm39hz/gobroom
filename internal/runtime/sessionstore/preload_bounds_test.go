package sessionstore

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/store"
)

func TestCachePreloadBoundsEntriesByLastUsed(t *testing.T) {
	route := invalidationRoute()
	k, err := makeKey(route, "session-00000")
	if err != nil {
		t.Fatal(err)
	}
	namespace, err := kernel.EncodeSessionStoreNamespace(route)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	rows := make([]store.ProviderSession, 16385)
	for i := range rows {
		lastUsed := now.Add(time.Duration(i-len(rows)+1) * time.Second)
		rows[i] = store.ProviderSession{
			ConnectionID: k.connectionID,
			Namespace:    namespace,
			SessionKey:   fmt.Sprintf("session-%05d", i),
			LastUsedAt:   lastUsed.Format(time.RFC3339Nano),
			State: kernel.SessionState{
				ResponseID: fmt.Sprintf("response-%05d", i),
				ExpiresAt:  now.Add(48 * time.Hour),
			},
		}
	}
	cache, err := New(&cacheFixtureRepository{rows: rows})
	if err != nil {
		t.Fatal(err)
	}
	if len(cache.entries) != 16384 {
		t.Fatalf("preloaded %d entries, want maxEntries 16384", len(cache.entries))
	}
	if _, ok := cache.entries[key{connectionID: k.connectionID, namespace: namespace, sessionID: "session-00000"}]; ok {
		t.Fatal("oldest last-used row survived the preload bound")
	}
	if got := cache.entries[key{connectionID: k.connectionID, namespace: namespace, sessionID: "session-16384"}].state.ResponseID; got != "response-16384" {
		t.Fatalf("newest last-used row response ID=%q, want response-16384", got)
	}
}

func TestCachePreloadEnforcesTTLAndProviderExpiry(t *testing.T) {
	route := invalidationRoute()
	k, err := makeKey(route, "zero-expiry")
	if err != nil {
		t.Fatal(err)
	}
	namespace, err := kernel.EncodeSessionStoreNamespace(route)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	recent := now.Add(-time.Hour)
	old := now.Add(-25 * time.Hour)
	shortExpiry := now.Add(2 * time.Hour)
	rows := []store.ProviderSession{
		{ConnectionID: k.connectionID, Namespace: namespace, SessionKey: "zero-expiry", LastUsedAt: now.Format(time.RFC3339Nano), State: kernel.SessionState{ResponseID: "zero"}},
		{ConnectionID: k.connectionID, Namespace: namespace, SessionKey: "long-expiry", LastUsedAt: recent.Format(time.RFC3339Nano), State: kernel.SessionState{ResponseID: "long", ExpiresAt: now.Add(48 * time.Hour)}},
		{ConnectionID: k.connectionID, Namespace: namespace, SessionKey: "old-last-used", LastUsedAt: old.Format(time.RFC3339Nano), State: kernel.SessionState{ResponseID: "old", ExpiresAt: now.Add(48 * time.Hour)}},
		{ConnectionID: k.connectionID, Namespace: namespace, SessionKey: "short-expiry", LastUsedAt: now.Format(time.RFC3339Nano), State: kernel.SessionState{ResponseID: "short", ExpiresAt: shortExpiry}},
		{ConnectionID: k.connectionID, Namespace: namespace, SessionKey: "invalid-state", LastUsedAt: now.Format(time.RFC3339Nano), State: kernel.SessionState{ResponseID: strings.Repeat("x", 4097), ExpiresAt: now.Add(time.Hour)}},
	}
	cache, err := New(&cacheFixtureRepository{rows: rows})
	if err != nil {
		t.Fatal(err)
	}
	for _, sessionID := range []string{"zero-expiry", "old-last-used", "invalid-state"} {
		if _, ok := cache.entries[key{connectionID: k.connectionID, namespace: namespace, sessionID: sessionID}]; ok {
			t.Errorf("invalid or TTL-expired row %q was preloaded", sessionID)
		}
	}
	long, ok := cache.entries[key{connectionID: k.connectionID, namespace: namespace, sessionID: "long-expiry"}]
	if !ok {
		t.Fatal("recent row with future provider expiry was not preloaded")
	}
	if long.state.ExpiresAt.After(recent.Add(24*time.Hour)) || long.state.ExpiresAt.Before(recent.Add(24*time.Hour-time.Second)) {
		t.Fatalf("long provider expiry=%s, want cache TTL capped at persisted last-used + 24h (%s)", long.state.ExpiresAt, recent.Add(24*time.Hour))
	}
	short, ok := cache.entries[key{connectionID: k.connectionID, namespace: namespace, sessionID: "short-expiry"}]
	if !ok || !short.state.ExpiresAt.Equal(shortExpiry) {
		t.Fatalf("earlier provider expiry was not preserved: state=%#v present=%v", short.state, ok)
	}
}

func TestCachePreloadDoesNotRenewTTLOnRestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		route := invalidationRoute()
		namespace, err := kernel.EncodeSessionStoreNamespace(route)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		repository := &cacheFixtureRepository{rows: []store.ProviderSession{{
			ConnectionID: route.CredentialID, Namespace: namespace, SessionKey: "session",
			LastUsedAt: now.Add(-23 * time.Hour).Format(time.RFC3339Nano),
			State:      kernel.SessionState{ResponseID: "original", ProviderData: []byte(`{"private":"original"}`), ExpiresAt: now.Add(48 * time.Hour)},
		}}}
		first, err := New(repository)
		if err != nil {
			t.Fatal(err)
		}
		state, found, err := first.Load(context.Background(), route, "session")
		if err != nil || !found || !state.ExpiresAt.Equal(now.Add(time.Hour)) {
			t.Fatalf("first preload: %#v found=%v err=%v", state, found, err)
		}
		state.ProviderData[0] = 'X'
		if string(repository.rows[0].State.ProviderData) != `{"private":"original"}` || !repository.rows[0].State.ExpiresAt.Equal(now.Add(48*time.Hour)) {
			t.Fatal("preload mutated repository-owned state")
		}
		time.Sleep(2 * time.Hour) // Virtual time; no wall-clock wait or I/O worker.
		restarted, err := New(repository)
		if err != nil {
			t.Fatal(err)
		}
		if _, found, err := restarted.Load(context.Background(), route, "session"); err != nil || found {
			t.Fatalf("restart resurrected TTL-expired state: found=%v err=%v", found, err)
		}
	})
}

func TestCachePreloadSelectsNewestUniqueKeys(t *testing.T) {
	route := invalidationRoute()
	namespace, err := kernel.EncodeSessionStoreNamespace(route)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	row := func(id, response string, age time.Duration) store.ProviderSession {
		return store.ProviderSession{ConnectionID: route.CredentialID, Namespace: namespace, SessionKey: id, LastUsedAt: now.Add(-age).Format(time.RFC3339Nano), State: kernel.SessionState{ResponseID: response, ExpiresAt: now.Add(time.Hour)}}
	}
	c := &Cache{entries: map[key]entry{}, ttl: 24 * time.Hour, maxEntries: 2}
	c.preload([]store.ProviderSession{row("a", "old-a", 4*time.Hour), row("b", "b", 3*time.Hour), row("c", "c", 2*time.Hour), row("a", "new-a", time.Hour), row("a", "older-a", 5*time.Hour)}, now)
	if len(c.entries) != 2 {
		t.Fatalf("unique entries=%d want2", len(c.entries))
	}
	for id, want := range map[string]string{"a": "new-a", "c": "c"} {
		state, found, err := c.Load(context.Background(), route, id)
		if err != nil || !found || state.ResponseID != want {
			t.Fatalf("key %s: %#v found=%v err=%v", id, state, found, err)
		}
	}
}

type expiryCheckingRepository struct{ cacheFixtureRepository }

func (r *expiryCheckingRepository) SaveProviderSession(row store.ProviderSession) error {
	// Match SQLite's persisted-state admission: past expiry cannot replace an
	// existing row. Cache must persist a valid clear marker instead.
	if !row.State.ExpiresAt.After(time.Now()) {
		return fmt.Errorf("provider session expiry must be in the future")
	}
	return r.cacheFixtureRepository.SaveProviderSession(row)
}

func TestCachePreloadDoesNotResurrectProviderExpiredUpdate(t *testing.T) {
	route := invalidationRoute()
	namespace, err := kernel.EncodeSessionStoreNamespace(route)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	repository := &expiryCheckingRepository{cacheFixtureRepository{rows: []store.ProviderSession{{ConnectionID: route.CredentialID, Namespace: namespace, SessionKey: "session", LastUsedAt: now.Format(time.RFC3339Nano), State: kernel.SessionState{ResponseID: "old-parent", ExpiresAt: now.Add(time.Hour)}}}}}
	cache, err := New(repository)
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Save(context.Background(), route, "session", kernel.SessionState{ResponseID: "expired-parent", ExpiresAt: now.Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}
	if state, found, err := cache.Load(context.Background(), route, "session"); err != nil || found {
		t.Fatalf("expired update still usable: %#v found=%v err=%v", state, found, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cache.Run(ctx) // Deterministically drain the owned write queue before restart.
	restarted, err := New(repository)
	if err != nil {
		t.Fatal(err)
	}
	if state, found, err := restarted.Load(context.Background(), route, "session"); err != nil || found {
		t.Fatalf("restart resurrected old continuity: %#v found=%v err=%v", state, found, err)
	}
}
