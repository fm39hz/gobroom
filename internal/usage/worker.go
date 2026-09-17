package usage

import (
	"context"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/store"
)

type Worker struct {
	Store  *store.Store
	Events <-chan kernel.UsageEvent
}

func (w Worker) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-w.Events:
			if !ok {
				return
			}
			if w.Store != nil {
				_ = w.Store.SaveUsageEvent(event)
			}
		}
	}
}
