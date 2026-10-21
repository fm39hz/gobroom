package provider

import (
	"net/http"
	"testing"

	"github.com/fm39hz/gobroom/internal/kernel"
)

func TestHTTPJSONAuthRecoveryDefaultsAndOverrides(t *testing.T) {
	classifier := HTTPJSONErrorClassifier{}
	unauthorized := classifier.ClassifyOutcome(http.StatusUnauthorized, nil, nil)
	if unauthorized.Class != kernel.ErrorAuth || unauthorized.Cause != kernel.CauseAuth || unauthorized.Recovery != kernel.CredentialRecoveryRefresh {
		t.Fatalf("401 outcome=%#v", unauthorized)
	}
	forbidden := classifier.ClassifyOutcome(http.StatusForbidden, nil, nil)
	if forbidden.Cause != kernel.CausePermission || forbidden.Recovery != kernel.CredentialRecoveryNone {
		t.Fatalf("403 outcome=%#v", forbidden)
	}

	custom := HTTPJSONErrorClassifier{Options: &HTTPJSONErrorClassifierOptions{AuthRecoverySignals: []HTTPJSONErrorSignal{{
		HTTPStatuses: []int{498}, Codes: []string{"token_expired"}, Fields: []HTTPJSONFieldMatch{{Path: "/error/token_state", Equals: []string{"expired"}}},
	}}}}
	body := []byte(`{"error":{"code":"TOKEN_EXPIRED","token_state":"expired"}}`)
	outcome := custom.ClassifyOutcome(498, nil, body)
	if outcome.Class != kernel.ErrorAuth || outcome.Cause != kernel.CauseAuth || outcome.Recovery != kernel.CredentialRecoveryRefresh || outcome.Scope != kernel.ScopeConnection || outcome.Effect != kernel.EffectRejected {
		t.Fatalf("custom recovery outcome=%#v", outcome)
	}
	if custom.Classify(498, body) != outcome.Class {
		t.Fatalf("Classify disagrees with ClassifyOutcome: class=%v outcome=%#v", custom.Classify(498, body), outcome)
	}
	if got := custom.ClassifyOutcome(http.StatusUnauthorized, nil, []byte(`{"error":{"code":"other"}}`)); got.Recovery != kernel.CredentialRecoveryNone || got.Cause != kernel.CauseAuth {
		t.Fatalf("declared signals must disable fallback 401 refresh: %#v", got)
	}
}

func TestHTTPJSONAuthRecoveryIgnoresSuccessfulResponses(t *testing.T) {
	classifier := HTTPJSONErrorClassifier{Options: &HTTPJSONErrorClassifierOptions{AuthRecoverySignals: []HTTPJSONErrorSignal{{Codes: []string{"token_expired"}}}}}
	outcome := classifier.ClassifyOutcome(http.StatusOK, nil, []byte(`{"error":{"code":"token_expired"}}`))
	if outcome.Cause != kernel.CauseSuccess || outcome.Recovery != kernel.CredentialRecoveryNone || outcome.Effect != kernel.EffectAccepted {
		t.Fatalf("success misclassified for auth recovery: %+v", outcome)
	}
}

func TestHTTPJSONAuthRecoveryQuotaAndPermissionPrecedence(t *testing.T) {
	classifier := HTTPJSONErrorClassifier{Options: &HTTPJSONErrorClassifierOptions{
		QuotaHTTPStatuses: []int{498}, QuotaCodes: []string{"quota_exceeded"},
		AuthRecoverySignals: []HTTPJSONErrorSignal{{HTTPStatuses: []int{498}, Codes: []string{"quota_exceeded"}}},
	}}
	outcome := classifier.ClassifyOutcome(498, nil, []byte(`{"error":{"code":"quota_exceeded"}}`))
	if outcome.Cause != kernel.CauseQuotaExhausted || outcome.Recovery != kernel.CredentialRecoveryNone || outcome.Class != kernel.ErrorCooldown {
		t.Fatalf("quota should take precedence over conflicting recovery signal: %#v", outcome)
	}
	forbidden := (HTTPJSONErrorClassifier{}).ClassifyOutcome(http.StatusForbidden, nil, []byte(`{"error":{"code":"token_expired"}}`))
	if forbidden.Cause != kernel.CausePermission || forbidden.Recovery != kernel.CredentialRecoveryNone {
		t.Fatalf("permission denial suggested refresh: %#v", forbidden)
	}
	serverError := classifier.ClassifyOutcome(http.StatusInternalServerError, nil, []byte(`{"error":{"code":"quota_exceeded"}}`))
	if serverError.Effect != kernel.EffectUnknown || serverError.Recovery != kernel.CredentialRecoveryNone {
		t.Fatalf("5xx must remain ambiguous and non-refreshable: %#v", serverError)
	}
}

func TestHTTPJSONAuthRecoveryMatchingAndCloneIsolation(t *testing.T) {
	classifier := HTTPJSONErrorClassifier{Options: &HTTPJSONErrorClassifierOptions{AuthRecoverySignals: []HTTPJSONErrorSignal{{
		HTTPStatuses: []int{498}, Codes: []string{"token_expired", "expired"}, Types: []string{"auth_error"},
		Statuses: []string{"invalid_token"}, Fields: []HTTPJSONFieldMatch{
			{Path: "/error/state", Equals: []string{"expired", "stale"}},
			{Path: "/error/owner", Contains: []string{"account"}},
		},
	}}}}
	matching := []byte(`{"error":{"code":"TOKEN_EXPIRED","type":"AUTH_ERROR","status":"INVALID_TOKEN","state":"stale","owner":"organization/account-7"}}`)
	if got := classifier.ClassifyOutcome(498, nil, matching); got.Recovery != kernel.CredentialRecoveryRefresh {
		t.Fatalf("all selector groups should match: %#v", got)
	}
	nonmatching := []byte(`{"error":{"code":"token_expired","type":"auth_error","status":"invalid_token","state":"expired","owner":"organization/team-7"}}`)
	if got := classifier.ClassifyOutcome(498, nil, nonmatching); got.Recovery != kernel.CredentialRecoveryNone {
		t.Fatalf("field group mismatch should reject signal: %#v", got)
	}

	options := ErrorClassifierOptions{HTTPJSON: &HTTPJSONErrorClassifierOptions{AuthRecoverySignals: []HTTPJSONErrorSignal{{
		HTTPStatuses: []int{498}, Codes: []string{"token_expired"}, Fields: []HTTPJSONFieldMatch{{Path: "/error/state", Equals: []string{"expired"}}},
	}}}}
	boundValue, err := (HTTPJSONErrorClassifier{}).WithOptions(options)
	if err != nil {
		t.Fatal(err)
	}
	options.HTTPJSON.AuthRecoverySignals[0].HTTPStatuses[0] = 401
	options.HTTPJSON.AuthRecoverySignals[0].Codes[0] = "changed"
	options.HTTPJSON.AuthRecoverySignals[0].Fields[0].Equals[0] = "changed"
	bound := boundValue.(HTTPJSONErrorClassifier)
	if got := bound.ClassifyOutcome(498, nil, []byte(`{"error":{"code":"token_expired","state":"expired"}}`)); got.Recovery != kernel.CredentialRecoveryRefresh {
		t.Fatalf("bound classifier retained caller-owned auth match data: %#v", got)
	}
}

func TestHTTPJSONAuthRecoveryRejectsInvalidOptions(t *testing.T) {
	emptySignals := []HTTPJSONErrorSignal{}
	if err := (HTTPJSONErrorClassifierOptions{AuthRecoverySignals: emptySignals}).Validate(); err == nil {
		t.Fatal("explicitly empty auth recovery declaration was accepted")
	}
	invalid := []HTTPJSONErrorSignal{
		{},
		{HTTPStatuses: []int{}, Codes: []string{"token_expired"}},
		{HTTPStatuses: []int{399}},
		{HTTPStatuses: []int{400, 401, 402, 403, 404, 405, 406, 407, 408, 409, 410, 411, 412, 413, 414, 415, 416, 417, 418, 419, 420, 421, 422, 423, 424, 425, 426, 427, 428, 429, 430, 431, 432}},
		{Codes: []string{" "}},
		{Fields: []HTTPJSONFieldMatch{{Path: "error/code", Equals: []string{"expired"}}}},
		{Fields: []HTTPJSONFieldMatch{{Path: "/error/code", Equals: []string{}, Contains: []string{"expired"}}}},
	}
	for index, signal := range invalid {
		if err := (HTTPJSONErrorClassifierOptions{AuthRecoverySignals: []HTTPJSONErrorSignal{signal}}).Validate(); err == nil {
			t.Errorf("invalid auth recovery signal %d was accepted: %#v", index, signal)
		}
	}
	tooManyCodes := make([]string, 65)
	for index := range tooManyCodes {
		tooManyCodes[index] = "code"
	}
	if err := (HTTPJSONErrorClassifierOptions{AuthRecoverySignals: []HTTPJSONErrorSignal{{Codes: tooManyCodes}}}).Validate(); err == nil {
		t.Fatal("auth recovery selector accepted more than 64 values")
	}
}
