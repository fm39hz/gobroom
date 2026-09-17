package kernel

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/fm39hz/gobroom/internal/normalize"
)

type Kernel struct {
	Snapshots *SnapshotStore
	Scheduler *Scheduler
	Adapters  map[Protocol]ProviderAdapter
	Events    chan UsageEvent
	closed    atomic.Bool
}

type FeedbackGate interface {
	Gate
	MarkFailure(Route, ErrorClass, error)
	MarkSuccess(Route)
}

func New(initial Snapshot, gate Gate, buffer int) (*Kernel, error) {
	store, err := NewSnapshotStore(initial)
	if err != nil {
		return nil, err
	}
	if buffer < 1 {
		buffer = 256
	}
	return &Kernel{Snapshots: store, Scheduler: NewScheduler(gate), Adapters: map[Protocol]ProviderAdapter{}, Events: make(chan UsageEvent, buffer)}, nil
}

func (k *Kernel) Resolve(name string) (ResolvedModel, error) {
	return ResolvePublic(k.Snapshots.Load(), name)
}

func (k *Kernel) Select(name string, now time.Time) (Route, error) {
	model, err := k.Resolve(name)
	if err != nil {
		return Route{}, err
	}
	return k.Scheduler.Select(model, now)
}

func (k *Kernel) PublishSnapshot(snapshot Snapshot) error { return k.Snapshots.Publish(snapshot) }

func (k *Kernel) EmitUsage(event UsageEvent) bool {
	if k.closed.Load() {
		return false
	}
	select {
	case k.Events <- event:
		return true
	default:
		return false
	}
}

func (k *Kernel) Close() {
	if k.closed.CompareAndSwap(false, true) {
		close(k.Events)
	}
}

// Execute is the single data-plane orchestration boundary. Provider adapters
// own protocol details; the kernel owns public-model resolution and selection.
func (k *Kernel) Execute(ctx context.Context, req NormalizedRequest, credential Credential, writer http.ResponseWriter) error {
	model, err := k.Resolve(req.Model)
	if err != nil {
		return err
	}
	for _, candidate := range model.Candidates {
		if !candidate.Enabled || !k.Scheduler.gate.Usable(candidate, time.Now()) {
			continue
		}
		if !protocolMatchesRequest(req.SourceFormat, candidate.Protocol) {
			continue
		}
		adapter := k.Adapters[candidate.Protocol]
		if adapter == nil {
			continue
		}
		upstream, err := adapter.Prepare(ctx, req, candidate, credential)
		if err != nil {
			continue
		}
		response, err := adapter.Execute(ctx, upstream)
		if err != nil {
			if feedback, ok := k.Scheduler.gate.(FeedbackGate); ok {
				feedback.MarkFailure(candidate, kernelErrorClass(err), err)
			}
			continue
		}
		if response.Status >= 400 {
			if feedback, ok := k.Scheduler.gate.(FeedbackGate); ok {
				feedback.MarkFailure(candidate, adapter.ClassifyError(response.Status, nil), fmt.Errorf("upstream status %d", response.Status))
			}
			_ = response.Body.Close()
			continue
		}
		if feedback, ok := k.Scheduler.gate.(FeedbackGate); ok {
			feedback.MarkSuccess(candidate)
		}
		return adapter.TranslateStream(ctx, response, writer, req.SourceFormat, StreamHooks{OnComplete: func(event UsageEvent) {
			if event.At.IsZero() {
				event.At = time.Now()
			}
			if event.LogicalModel == "" {
				event.LogicalModel = req.Model
			}
			if event.ProviderNodeID == "" {
				event.ProviderNodeID = candidate.NodeID
			}
			if event.ExternalModel == "" {
				event.ExternalModel = candidate.ExternalModel
			}
			if event.ConnectionID == "" {
				event.ConnectionID = candidate.CredentialID
			}
			k.EmitUsage(event)
		}})
	}
	return ErrNoRoute
}

func kernelErrorClass(err error) ErrorClass {
	if err == nil {
		return ErrorRetryable
	}
	return ErrorCooldown
}

func protocolMatchesRequest(format normalize.Format, protocol Protocol) bool {
	switch format {
	case normalize.FormatAnthropic:
		return protocol == ProtocolAnthropic
	case normalize.FormatOpenAIResponses:
		return protocol == ProtocolOpenAIResponses
	case normalize.FormatOpenAIChat:
		return protocol == ProtocolOpenAIChat || protocol == ProtocolAnthropic
	default:
		return protocol == ProtocolOpenAIChat
	}
}
