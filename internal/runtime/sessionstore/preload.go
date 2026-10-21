package sessionstore

import (
	"cmp"
	"container/heap"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/store"
)

type preloadItem struct {
	key       key
	state     kernel.SessionState
	updatedAt time.Time
	index     int
}

// The standard heap retains only the newest bounded set, even when a repository
// does not provide SQLite's ordering and row limit. Payloads are cloned only
// after selection; repository-owned rows and their buffers remain untouched.
func (c *Cache) preload(rows []store.ProviderSession, now time.Time) {
	selected := make(map[key]*preloadItem)
	newest := &preloadHeap{}
	for _, row := range rows {
		if row.ConnectionID == "" || row.Namespace == "" || row.SessionKey == "" {
			continue
		}
		if _, err := kernel.DecodeSessionStoreNamespace(row.Namespace); err != nil {
			continue
		}
		if row.State.ResponseID == "" && len(row.State.ProviderData) == 0 && len(row.State.Signatures) == 0 {
			continue
		}
		if err := kernel.ValidateSessionState(row.State); err != nil {
			continue
		}
		lastUsed, err := time.Parse(time.RFC3339Nano, row.LastUsedAt)
		if err != nil || lastUsed.After(now) || row.State.ExpiresAt.IsZero() {
			continue
		}
		state := row.State
		if ceiling := lastUsed.Add(c.ttl); state.ExpiresAt.After(ceiling) {
			state.ExpiresAt = ceiling
		}
		if !state.ExpiresAt.After(now) {
			continue
		}
		candidate := preloadItem{key: key{connectionID: row.ConnectionID, namespace: row.Namespace, sessionID: row.SessionKey}, state: state, updatedAt: lastUsed}
		if current := selected[candidate.key]; current != nil {
			if comparePreload(candidate, *current) > 0 {
				candidate.index = current.index
				*current = candidate
				heap.Fix(newest, current.index)
			}
			continue
		}
		if newest.Len() == c.maxEntries {
			if comparePreload(candidate, *(*newest)[0]) <= 0 {
				continue
			}
			removed := heap.Pop(newest).(*preloadItem)
			delete(selected, removed.key)
		}
		item := new(preloadItem)
		*item = candidate
		heap.Push(newest, item)
		selected[item.key] = item
	}
	for k, item := range selected {
		c.version++
		state := clone(item.state)
		state.StoreVersion = c.version
		c.entries[k] = entry{state: state, updatedAt: item.updatedAt}
	}
}

func comparePreload(a, b preloadItem) int {
	return cmp.Or(a.updatedAt.Compare(b.updatedAt), cmp.Compare(a.key.connectionID, b.key.connectionID), cmp.Compare(a.key.namespace, b.key.namespace), cmp.Compare(a.key.sessionID, b.key.sessionID))
}

type preloadHeap []*preloadItem

func (h preloadHeap) Len() int           { return len(h) }
func (h preloadHeap) Less(i, j int) bool { return comparePreload(*h[i], *h[j]) < 0 }
func (h preloadHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i]; h[i].index, h[j].index = i, j }
func (h *preloadHeap) Push(value any) {
	item := value.(*preloadItem)
	item.index = len(*h)
	*h = append(*h, item)
}
func (h *preloadHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	old[len(old)-1] = nil
	*h = old[:len(old)-1]
	return last
}
