package kernel

import (
	"context"
	"errors"
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
	Adapters          map[string]ProviderAdapter
	ErrorClassifiers  map[string]ErrorClassifier
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
	return &Kernel{Snapshots: store, Scheduler: NewScheduler(gate), Adapters: map[string]ProviderAdapter{}, ErrorClassifiers: map[string]ErrorClassifier{}, Events: make(chan UsageEvent, buffer)}, nil
}

func (k *Kernel) Resolve(name string) (ResolvedModel, error) {
	return ResolvePublic(k.Snapshots.Load(), name)
}

func (k *Kernel) ResolveNode(name string) (ModelNode, error) {
	return ResolvePublicNode(k.Snapshots.Load(), name)
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
	snapshot := k.Snapshots.Load()
	root, err := ResolvePublicNode(snapshot, req.Model)
	if err != nil {
		return err
	}
	return k.executeNode(ctx, snapshot, root, req, credential, writer, started, map[string]bool{})
}

func (k *Kernel) executeNode(ctx context.Context, snapshot Snapshot, node ModelNode, req NormalizedRequest, credential Credential, writer http.ResponseWriter, started time.Time, stack map[string]bool) error {
	if stack[node.ID] {
		return fmt.Errorf("model cycle at %q", node.ID)
	}
	stack[node.ID] = true
	defer delete(stack, node.ID)
	for _, member := range k.Scheduler.Plan(node) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if member.Kind == MemberModel {
			child, ok := snapshot.Nodes[member.ID]
			if !ok {
				return fmt.Errorf("unknown model node %q", member.ID)
			}
			err := k.executeNode(ctx, snapshot, child, req, credential, writer, started, stack)
			if err == nil {
				return nil
			} else if !errors.Is(err, ErrNoRoute) {
				return err
			}
			failure := StrategyFailure{NodeID: node.ID, Member: member, Class: ErrorRetryable, Err: err}
			if k.Scheduler.OnFailure(node, failure) == FailureStop {
				return err
			}
			continue
		}
		routes := resolveRouteMember(snapshot, member)
		failureClass := ErrorRetryable
		var memberErr error = ErrNoRoute
		if preferred := req.Transport.PreferredConnectionID; preferred != "" {
			for i, candidate := range routes {
				if candidate.CredentialID == preferred && i > 0 {
					routes = append([]Route{candidate}, append(routes[:i:i], routes[i+1:]...)...)
					break
				}
			}
		}
		for _, candidate := range routes {
			if !candidate.Enabled || !k.Scheduler.gate.Usable(candidate, time.Now()) {
				failureClass = ErrorCooldown
				continue
			}
			if !supportsRequest(candidate, req) {
				continue
			}
			if !protocolMatchesRequest(req.SourceFormat, candidate.Protocol) {
				continue
			}
			adapter := k.Adapters[candidate.AdapterID]
			if adapter == nil {
				continue
			}
			selectedCredential := credential
			var err error
			if selectedCredential.Secret == "" && k.ResolveCredential != nil {
				selectedCredential, err = k.ResolveCredential(ctx, candidate)
				if err != nil {
					failureClass, memberErr = ErrorAuth, err
					if feedback, ok := k.Scheduler.gate.(FeedbackGate); ok {
						feedback.MarkFailure(candidate, ErrorAuth, err)
					}
					continue
				}
			}
			upstream, err := adapter.Prepare(ctx, req, candidate, selectedCredential)
			if err != nil {
				failureClass, memberErr = ErrorRetryable, err
				continue
			}
			response, err := adapter.Execute(ctx, upstream)
			if err != nil {
				failureClass, memberErr = kernelErrorClass(err), err
				if feedback, ok := k.Scheduler.gate.(FeedbackGate); ok {
					feedback.MarkFailure(candidate, kernelErrorClass(err), err)
				}
				continue
			}
			if response.Status >= 400 {
				body, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
				class := adapter.ClassifyError(response.Status, body)
				if classifier, ok := k.ErrorClassifiers[candidate.ErrorClassifierID]; ok {
					class = classifier.ClassifyError(response.Status, body)
				}
				failureClass = class
				memberErr = fmt.Errorf("upstream status %d: %s", response.Status, strings.TrimSpace(string(body)))
				retryAfter := retryAfterDuration(response.Headers.Get("Retry-After"))
				if feedback, ok := k.Scheduler.gate.(FeedbackGate); ok {
					feedback.MarkFailureAfter(candidate, class, fmt.Errorf("upstream status %d: %s", response.Status, strings.TrimSpace(string(body))), retryAfter)
				}
				_ = response.Body.Close()
				if class == ErrorTerminal {
					terminalErr := fmt.Errorf("upstream status %d: %s", response.Status, strings.TrimSpace(string(body)))
					failure := StrategyFailure{NodeID: node.ID, Member: member, Class: class, Err: terminalErr}
					if k.Scheduler.OnFailure(node, failure) == FailureStop {
						return terminalErr
					}
					memberErr = terminalErr
					continue
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
		failure := StrategyFailure{NodeID: node.ID, Member: member, Class: failureClass, Err: memberErr}
		if k.Scheduler.OnFailure(node, failure) == FailureStop {
			return ErrNoRoute
		}
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
