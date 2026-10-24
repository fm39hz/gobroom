package kernel

import (
	"errors"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/operations"
)

func TestOrdinaryClientFailureUsesSafeMessageWithoutChangingIdentity(t *testing.T) {
	private := &clientFailurePrivateError{message: "private-upstream-token-59bc"}
	for _, test := range []struct {
		err     error
		message string
	}{
		{err: private, message: "upstream request failed"},
		{err: &operations.InputError{Err: private}, message: "invalid request"},
	} {
		err := test.err
		failure := ClientFailureForError(err)
		if !failure.Valid() || failure.Message != test.message || strings.Contains(failure.Message, private.message) {
			t.Fatalf("ordinary internal error exposed: %#v", failure)
		}
	}
	if private.errorCalls != 0 {
		t.Fatalf("unknown error Error() called %d times", private.errorCalls)
	}
	explicit := ClientFailure{Code: "safe_custom", Type: "invalid_request_error", Message: "safe public message"}
	err := NewClientFailureError(explicit, private)
	if !errors.Is(err, private) || ClientFailureForError(err) != explicit || private.errorCalls != 0 {
		t.Fatal("safe presentation must preserve explicit metadata and private cause identity")
	}
}

func TestClientFailureForErrorWithFallbackUsesFallbackOnlyForUnknownErrors(t *testing.T) {
	fallback := ClientFailure{Code: "safe_decode_error", Type: "invalid_request_error", Message: "safe decode failure"}
	private := &clientFailurePrivateError{message: "private decode details"}
	if got := ClientFailureForErrorWithFallback(private, fallback); got != fallback {
		t.Fatalf("unknown error projection = %#v, want fallback %#v", got, fallback)
	}
	if private.errorCalls != 0 {
		t.Fatalf("fallback projection called unknown Error() %d times", private.errorCalls)
	}
	if got := ClientFailureForErrorWithFallback(nil, fallback); got != (ClientFailure{}) {
		t.Fatalf("nil error projection = %#v, want zero", got)
	}

	invalidFallback := ClientFailure{Code: "bad code", Type: "invalid_request_error", Message: "unsafe fallback"}
	wantSafeUpstream := ClientFailure{Code: ClientFailureUpstream, Type: "upstream_error", Message: "upstream request failed"}
	if got := ClientFailureForErrorWithFallback(private, invalidFallback); got != wantSafeUpstream {
		t.Fatalf("invalid fallback projection = %#v, want safe upstream %#v", got, wantSafeUpstream)
	}

	explicit := ClientFailure{Code: "safe_explicit", Type: "invalid_request_error", Message: "explicit public message"}
	explicitErr := NewClientFailureError(explicit, private)
	if got := ClientFailureForErrorWithFallback(explicitErr, fallback); got != explicit {
		t.Fatalf("explicit carrier did not take precedence: %#v", got)
	}
	if got := ClientFailureForErrorWithFallback(ErrNoRoute, fallback); got.Code != ClientFailureNoRoute || got.Message != ErrNoRoute.Error() {
		t.Fatalf("known sentinel did not take precedence over fallback: %#v", got)
	}
	inputErr := &operations.InputError{Err: private}
	if got := ClientFailureForErrorWithFallback(inputErr, fallback); got != (ClientFailure{Code: ClientFailureInvalidRequest, Type: "invalid_request_error", Message: "invalid request"}) {
		t.Fatalf("InputError did not use its safe classification: %#v", got)
	}
	if private.errorCalls != 0 {
		t.Fatalf("recognized-error projection called private Error() %d times", private.errorCalls)
	}
}

type clientFailurePrivateError struct {
	message    string
	errorCalls int
}

func (err *clientFailurePrivateError) Error() string {
	err.errorCalls++
	return err.message
}
