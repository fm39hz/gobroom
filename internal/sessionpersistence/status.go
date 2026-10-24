// Package sessionpersistence defines safe operational facts, not a claim that
// the latest durable session state is or is not consistent.
package sessionpersistence

// Status counters are daemon-run-lifetime and independently sampled. Received,
// Persisted and Failed describe the built-in writer, including tombstones.
// SaveErrors describes kernel Save calls, including custom session stores.
// QueueDropped can overlap SaveErrors and must not be added as unique losses.
type Status struct {
	Received     uint64 `json:"received"`
	Persisted    uint64 `json:"persisted"`
	Failed       uint64 `json:"failed"`
	Discarded    uint64 `json:"discarded"`
	QueueDropped uint64 `json:"queueDropped"`
	SaveErrors   uint64 `json:"saveErrors"`
}

func (s Status) Faulted() bool {
	return s.Failed != 0 || s.QueueDropped != 0 || s.SaveErrors != 0
}
