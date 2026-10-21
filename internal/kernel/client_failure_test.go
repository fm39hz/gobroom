package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/operations"
)

func TestClientFailureStableContinuityDescriptorAndPrivatePrefix(t *testing.T) {
	wrapped := fmt.Errorf("private adapter prefix: %w", ErrContinuityRestartRequired)
	got := ClientFailureForError(wrapped)
	want := ClientFailure{Code: ClientFailureContinuityRestart, Type: "invalid_request_error", Message: ErrContinuityRestartRequired.Error()}
	if got != want {
		t.Fatalf("continuity descriptor = %#v, want %#v", got, want)
	}
	if strings.Contains(got.Message, "private adapter prefix") {
		t.Fatalf("private wrapper prefix leaked: %q", got.Message)
	}
}

func TestClientFailureKnownBoundaryErrorsKeepClassification(t *testing.T) {
	for _, test := range []struct {
		err  error
		code ClientFailureCode
		typ  string
	}{
		{ErrModelNotPublished, ClientFailureModelUnavailable, "invalid_request_error"},
		{ErrNoRoute, ClientFailureNoRoute, "upstream_error"},
		{ErrOperationRequired, ClientFailureInvalidRequest, "invalid_request_error"},
		{&operations.InputError{Err: errors.New("invalid field")}, ClientFailureInvalidRequest, "invalid_request_error"},
	} {
		got := ClientFailureForError(fmt.Errorf("private outer context: %w", test.err))
		if !got.Valid() || got.Code != test.code || got.Type != test.typ || strings.Contains(got.Message, "private outer context") {
			t.Fatalf("boundary projection=%#v", got)
		}
	}
}

func TestClientFailureExplicitMetadataPreservesCauseAndResponseFailureWrapping(t *testing.T) {
	cause := errors.New("private provider detail")
	want := ClientFailure{Code: ClientFailureUpstream, Type: "rate_limit_error", Message: "Try again later"}
	err := NewClientFailureError(want, cause)
	if !errors.Is(err, cause) {
		t.Fatal("explicit client failure lost cause identity")
	}
	wrapped := WrapResponseFailure(ResponseFailureProvider, err)
	if !errors.Is(wrapped, cause) {
		t.Fatal("response failure wrapping lost cause identity")
	}
	if got := ClientFailureForError(wrapped); got != want {
		t.Fatalf("wrapped explicit descriptor = %#v, want %#v", got, want)
	}
	outcomeErr := withOutcomeClientFailure(ClassifiedOutcome{ClientFailure: want}, cause)
	if !errors.Is(outcomeErr, cause) || ClientFailureForError(WrapResponseFailure(ResponseFailureProvider, outcomeErr)) != want {
		t.Fatal("classified outcome metadata did not survive cause and response failure wrapping")
	}
}

func TestClientFailureInvalidDescriptorsFailClosed(t *testing.T) {
	valid := ClientFailure{Code: ClientFailureUpstream, Type: "upstream_error", Message: "safe"}
	invalid := []ClientFailure{
		{Code: "bad code", Type: valid.Type, Message: valid.Message},
		{Code: ClientFailureUpstream, Type: "bad/type", Message: valid.Message},
		{Code: ClientFailureUpstream, Type: valid.Type, Message: ""},
		{Code: ClientFailureUpstream, Type: valid.Type, Message: strings.Repeat("x", MaxClientFailureMessageBytes+1)},
		{Code: ClientFailureUpstream, Type: valid.Type, Message: string([]byte{0xff})},
	}
	for _, descriptor := range invalid {
		cause := errors.New("cause")
		if err := NewClientFailureError(descriptor, cause); err == nil || errors.Is(err, cause) || ClientFailureForError(err) == descriptor {
			t.Errorf("NewClientFailureError accepted %#v", descriptor)
		}
	}
	if got := ClientFailureForError(nil); got != (ClientFailure{}) {
		t.Fatalf("nil error descriptor = %#v", got)
	}
	if got := ClientFailureForError(errors.New(strings.Repeat("x", MaxClientFailureMessageBytes+1))); got.Message != "upstream request failed" || !got.Valid() {
		t.Fatalf("oversized fallback was not bounded: %#v", got)
	}
	if got := ClientFailureForError(errors.New(string([]byte{0xff}))); !got.Valid() || !utf8.ValidString(got.Message) {
		t.Fatalf("invalid UTF-8 fallback was not safe: %#v", got)
	}
}

func TestResponseEventBudgetCountsAndValidatesClientFailure(t *testing.T) {
	detail := ClientFailure{Code: ClientFailureUpstream, Type: "upstream_error", Message: "bounded failure"}
	event := ResponseEvent{Kind: EventResponseError, Error: detail}
	budget := responseEventBudget{maxEvent: int64(len(detail.Code) + len(detail.Type) + len(detail.Message) - 1)}
	if err := budget.consume(event); err == nil {
		t.Fatal("budget omitted client failure descriptor bytes")
	}
	for _, invalid := range []ResponseEvent{
		{Kind: EventResponseError},
		{Kind: EventResponseError, Error: ClientFailure{Code: detail.Code, Type: "bad/type", Message: detail.Message}},
		{Kind: EventTextDelta, Error: detail},
	} {
		if err := (&responseEventBudget{}).consume(invalid); err == nil {
			t.Errorf("budget accepted invalid event %#v", invalid)
		}
	}
	if err := (&responseEventBudget{}).consume(event); err != nil {
		t.Fatalf("valid response error rejected: %v", err)
	}
}

func TestOperationEventPayloadEncodesClientFailureAsObject(t *testing.T) {
	detail := ClientFailure{Code: ClientFailureUpstream, Type: "upstream_error", Message: "safe message"}
	payload, err := operationEventPayload(ResponseEvent{Kind: EventResponseError, Error: detail}, 1)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Content map[string]json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatal(err)
	}
	var got ClientFailure
	if raw := envelope.Content["error"]; len(raw) == 0 || raw[0] != '{' {
		t.Fatalf("content.error is not a JSON object: %s", raw)
	} else if err := json.Unmarshal(raw, &got); err != nil || got != detail {
		t.Fatalf("content.error = %#v, decode err %v", got, err)
	}
}

type clientFailureMutationTransform struct {
	field string
}

func (transform clientFailureMutationTransform) Definition() ResponseTransformDefinition {
	return ResponseTransformDefinition{Ref: extensions.Ref{Kind: ResponseTransformKind, ID: "fixture.client-failure-mutation." + transform.field, ContractVersion: 1}, ImplementationVersion: "1", Label: "Client failure mutation", Description: "Mutation fixture for immutable client failure metadata.", Effects: []ResponseTransformEffect{ResponseEffectText}}
}

func (transform clientFailureMutationTransform) ApplyResponse(_ context.Context, event ResponseEvent, _ json.RawMessage) (ResponseEvent, error) {
	switch transform.field {
	case "code":
		event.Error.Code = ClientFailureCode("rewritten")
	case "type":
		event.Error.Type = "rewritten"
	case "message":
		event.Error.Message = "rewritten"
	}
	return event, nil
}

func TestResponseTransformCannotRewriteClientFailureMetadata(t *testing.T) {
	detail := ClientFailure{Code: ClientFailureUpstream, Type: "upstream_error", Message: "safe message"}
	for _, field := range []string{"code", "type", "message"} {
		t.Run(field, func(t *testing.T) {
			transform := clientFailureMutationTransform{field: field}
			registry := NewResponseTransformRegistry()
			if err := registry.Register(transform); err != nil {
				t.Fatal(err)
			}
			updated, err := registry.ApplyScopes(context.Background(), ResponseEvent{Kind: EventResponseError, Error: detail}, []TransformBinding{{
				ID: "client-failure", TransformRef: transform.Definition().Ref, Enabled: true, Scope: daemonTransformScope(),
			}}, daemonTransformScope())
			if !errors.Is(err, ErrTransformSafetyViolation) || updated.Error != (ClientFailure{}) {
				t.Fatalf("transform rewrote client failure metadata: updated=%#v err=%v", updated, err)
			}
		})
	}
}
