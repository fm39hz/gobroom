package usage

import "github.com/fm39hz/gobroom/internal/kernel"

type Repository interface {
	SaveUsageEvent(kernel.UsageEvent) error
}

type Worker struct {
	Store  Repository
	Events <-chan kernel.UsageEvent
}

// Run drains every accepted event until its producer closes Events. Request or
// daemon cancellation is not a persistence drain; the owner must close the
// producer after execution stops, join Run, and only then close the repository.
func (w Worker) Run() {
	for event := range w.Events {
		if w.Store != nil {
			_ = w.Store.SaveUsageEvent(event)
		}
	}
}
