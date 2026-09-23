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
		routes := k.Scheduler.RankRoutesForSession(resolveRouteMember(snapshot, member), time.Now(), requestClass(req), req.Session.ID)
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
			if eligible, _ := Eligible(candidate, CompileRequirements(req)); !eligible {
				continue
			}
			if !protocolMatchesRequest(req.SourceFormat, candidate.Protocol) {
				continue
			}
			adapter := k.Adapters[candidate.AdapterID]
			if adapter == nil {
				continue
			}
			if !k.Scheduler.Acquire(candidate, time.Now()) {
				failureClass = ErrorCooldown
				continue
			}
			selectedCredential := credential
			var err error
			if selectedCredential.Secret == "" && k.ResolveCredential != nil {
				selectedCredential, err = k.ResolveCredential(ctx, candidate)
				if err != nil {
					k.Scheduler.Release(candidate)
					failureClass, memberErr = ErrorAuth, err
					if feedback, ok := k.Scheduler.gate.(FeedbackGate); ok {
						feedback.MarkFailure(candidate, ErrorAuth, err)
					}
					continue
				}
			}
			upstream, err := adapter.Prepare(ctx, req, candidate, selectedCredential)
			if err != nil {
				k.Scheduler.Release(candidate)
				failureClass, memberErr = ErrorRetryable, err
				continue
			}
			response, err := adapter.Execute(ctx, upstream)
			if err != nil {
				k.Scheduler.Release(candidate)
				failureClass, memberErr = kernelErrorClass(err), err
				if observer, ok := k.Scheduler.gate.(OutcomeObserver); ok {
					observer.ObserveOutcome(candidate, ClassifiedOutcome{Class: failureClass, Cause: CauseNetwork, Scope: ScopeRoute, Retry: RetryAfter, Confidence: 0.8, Evidence: []EvidenceSource{EvidenceInferred}, Message: err.Error()})
				} else if feedback, ok := k.Scheduler.gate.(FeedbackGate); ok {
					feedback.MarkFailure(candidate, kernelErrorClass(err), err)
				}
				continue
			}
			if response.Status >= 400 {
				k.Scheduler.Release(candidate)
				body, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
				class := adapter.ClassifyError(response.Status, body)
				outcome := classifyOutcome(adapter, response.Status, response.Headers, body, class)
				if classifier, ok := k.ErrorClassifiers[candidate.ErrorClassifierID]; ok {
					class = classifier.ClassifyError(response.Status, body)
					if rich, ok := classifier.(OutcomeClassifier); ok {
						outcome = rich.ClassifyOutcome(response.Status, response.Headers, body)
					}
				}
				outcome.Class = class
				if observer, ok := k.Scheduler.gate.(OutcomeObserver); ok {
					observer.ObserveOutcome(candidate, outcome)
				}
				failureClass = class
				memberErr = fmt.Errorf("upstream status %d: %s", response.Status, strings.TrimSpace(string(body)))
				retryAfter := retryAfterDuration(response.Headers.Get("Retry-After"))
				if _, rich := k.Scheduler.gate.(OutcomeObserver); !rich {
					if feedback, ok := k.Scheduler.gate.(FeedbackGate); ok {
						feedback.MarkFailureAfter(candidate, class, fmt.Errorf("upstream status %d: %s", response.Status, strings.TrimSpace(string(body))), retryAfter)
					}
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
			var firstByteAt time.Time
			emitEvent := responseEventSink(writer)
			return adapter.TranslateStream(ctx, response, writer, req.SourceFormat, StreamHooks{OnError: func(streamErr error) {
				k.Scheduler.Release(candidate)
				if emitEvent != nil {
					emitEvent(ResponseEvent{At: time.Now(), Kind: EventResponseError, Error: streamErr.Error()})
				}
				if observer, ok := k.Scheduler.gate.(OutcomeObserver); ok {
					observer.ObserveOutcome(candidate, ClassifiedOutcome{Class: ErrorRetryable, Cause: CauseStreamFailure, Scope: ScopeRoute, Retry: RetryAfter, Confidence: 0.9, Evidence: []EvidenceSource{EvidenceInferred}, Message: streamErr.Error()})
				} else if feedback, ok := k.Scheduler.gate.(FeedbackGate); ok {
					feedback.MarkFailure(candidate, ErrorRetryable, streamErr)
				}
			}, OnEvent: func(event ResponseEvent) {
				if emitEvent != nil {
					emitEvent(event)
				}
			}, OnFirstByte: func(at time.Time) {
				firstByteAt = at
				if emitEvent != nil {
					emitEvent(ResponseEvent{At: at, Kind: EventResponseStarted})
				}
			}, OnComplete: func(event UsageEvent) {
				k.Scheduler.Release(candidate)
				if observer, ok := k.Scheduler.gate.(OutcomeObserver); ok {
					observer.ObserveOutcome(candidate, ClassifiedOutcome{Class: ErrorTerminal, Cause: CauseSuccess, Scope: ScopeRoute, Retry: RetryNow, Confidence: 1, Evidence: []EvidenceSource{EvidenceSuccessBody}})
				}
				if _, rich := k.Scheduler.gate.(OutcomeObserver); !rich {
					if feedback, ok := k.Scheduler.gate.(FeedbackGate); ok {
						feedback.MarkSuccess(candidate)
					}
				}
				if event.At.IsZero() {
					event.At = time.Now()
				}
				if event.Latency == 0 {
					event.Latency = time.Since(started)
				}
				if event.FirstByteAt.IsZero() {
					event.FirstByteAt = firstByteAt
				}
				if !event.FirstByteAt.IsZero() {
					event.TTFT = event.FirstByteAt.Sub(started)
				}
				if event.OutputTokens > 0 && event.Latency > 0 {
					event.OutputTokensPerSecond = float64(event.OutputTokens) / event.Latency.Seconds()
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
				if event.RequestClass == "" {
					event.RequestClass = requestClass(req)
				}
				if event.SessionID == "" {
					event.SessionID = req.Session.ID
				}
				if observer, ok := k.Scheduler.gate.(UsageObserver); ok {
					observer.ObserveUsage(candidate, event)
				}
				if emitEvent != nil {
					emitEvent(ResponseEvent{At: event.At, Kind: EventResponseComplete, Usage: &event})
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

type responseEventWriter interface{ ResponseEvent(ResponseEvent) }

func responseEventSink(writer http.ResponseWriter) func(ResponseEvent) {
	if sink, ok := writer.(responseEventWriter); ok {
		return sink.ResponseEvent
	}
	return nil
}

func requestClass(req NormalizedRequest) string {
	contextBucket := "empty"
	if len(req.Messages) > 32 {
		contextBucket = "large"
	} else if len(req.Messages) > 8 {
		contextBucket = "medium"
	} else if len(req.Messages) > 0 {
		contextBucket = "small"
	}
	return fmt.Sprintf("format=%s;stream=%t;vision=%t;audio=%t;video=%t;pdf=%t;tools=%t;thinking=%s;context=%s", req.SourceFormat, req.Stream, req.Modalities.Vision, req.Modalities.AudioInput, req.Modalities.VideoInput, req.Modalities.PDF, len(req.Tools) > 0, req.Thinking.Effort, contextBucket)
}

func retryAfterDuration(value string) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if parsed, err := http.ParseTime(value); err == nil {
		if delay := time.Until(parsed); delay > 0 {
			return delay
		}
	}
	return 0
}

func classifyOutcome(adapter ProviderAdapter, status int, headers http.Header, body []byte, class ErrorClass) ClassifiedOutcome {
	if rich, ok := adapter.(OutcomeClassifier); ok {
		outcome := rich.ClassifyOutcome(status, headers, body)
		outcome.Class = class
		return outcome
	}
	outcome := ClassifiedOutcome{Class: class, Scope: ScopeRoute, Confidence: 0.5, Evidence: []EvidenceSource{EvidenceErrorBody}, StatusCode: status, Message: strings.TrimSpace(string(body))}
	switch {
	case status == 401 || status == 403:
		outcome.Cause, outcome.Retry = CauseAuth, RetryNever
	case status == 429:
		outcome.Cause, outcome.Retry = CauseRateLimited, RetryAfter
	case status >= 500:
		outcome.Cause, outcome.Retry = CauseCapacity, RetryAfter
	case status >= 400:
		outcome.Cause, outcome.Retry = CauseRequestInvalid, RetryNever
	default:
		outcome.Cause, outcome.Retry = CauseUnknown, RetryUnknown
	}
	return outcome
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
