package kernel

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/fm39hz/gobroom/internal/normalize"
)

type Kernel struct {
	Snapshots         *SnapshotStore
	Scheduler         *Scheduler
	Adapters          map[Protocol]ProviderAdapter
	Events            chan UsageEvent
	ResolveCredential CredentialResolver
	closed            atomic.Bool
}

type FeedbackGate interface {
	Gate
	MarkFailure(Route, ErrorClass, error)
	MarkFailureAfter(Route, ErrorClass, error, time.Duration)
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
	started := time.Now()
	model, err := k.Resolve(req.Model)
	if err != nil {
		return err
	}
	for _, candidate := range k.Scheduler.OrderWithPreferred(model, time.Now(), req.Transport.PreferredConnectionID) {
		if !supportsRequest(candidate, req) {
			continue
		}
		if !protocolMatchesRequest(req.SourceFormat, candidate.Protocol) {
			continue
		}
		adapter := k.Adapters[candidate.Protocol]
		if adapter == nil {
			continue
		}
		selectedCredential := credential
		if selectedCredential.Secret == "" && k.ResolveCredential != nil {
			selectedCredential, err = k.ResolveCredential(ctx, candidate)
			if err != nil {
				if feedback, ok := k.Scheduler.gate.(FeedbackGate); ok {
					feedback.MarkFailure(candidate, ErrorAuth, err)
				}
				continue
			}
		}
		upstream, err := adapter.Prepare(ctx, req, candidate, selectedCredential)
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
			body, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
			class := adapter.ClassifyError(response.Status, body)
			retryAfter := retryAfterDuration(response.Headers.Get("Retry-After"))
			if feedback, ok := k.Scheduler.gate.(FeedbackGate); ok {
				feedback.MarkFailureAfter(candidate, class, fmt.Errorf("upstream status %d: %s", response.Status, strings.TrimSpace(string(body))), retryAfter)
			}
			_ = response.Body.Close()
			if class == ErrorTerminal {
				return fmt.Errorf("upstream status %d: %s", response.Status, strings.TrimSpace(string(body)))
			}
			continue
		}
		defer response.Body.Close()
		return adapter.TranslateStream(ctx, response, writer, req.SourceFormat, StreamHooks{OnError: func(streamErr error) {
			if feedback, ok := k.Scheduler.gate.(FeedbackGate); ok {
				feedback.MarkFailure(candidate, ErrorRetryable, streamErr)
			}
		}, OnComplete: func(event UsageEvent) {
			if feedback, ok := k.Scheduler.gate.(FeedbackGate); ok {
				feedback.MarkSuccess(candidate)
			}
			if event.At.IsZero() {
				event.At = time.Now()
			}
			if event.Latency == 0 {
				event.Latency = time.Since(started)
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

func retryAfterDuration(value string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func supportsRequest(route Route, req NormalizedRequest) bool {
	if len(route.Capabilities) == 0 {
		return true
	}
	checks := map[string]bool{
		"vision": req.Modalities.Vision,
		"audio":  req.Modalities.AudioInput,
		"video":  req.Modalities.VideoInput,
		"pdf":    req.Modalities.PDF,
	}
	for capability, needed := range checks {
		if needed && !route.Capabilities[capability] {
			return false
		}
	}
	return true
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
