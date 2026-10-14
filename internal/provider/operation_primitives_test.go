package provider

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
)

func TestAnthropicModelSourcePaginatesWithAPIKeyHeaders(t *testing.T) {
	page := 0
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("network listener unavailable: %v", err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "anthropic-secret" || r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("Anthropic auth headers missing: %#v", r.Header)
		}
		if r.URL.Query().Get("limit") != "1000" {
			t.Errorf("limit=%q", r.URL.Query().Get("limit"))
		}
		page++
		if page == 1 {
			if got := r.URL.Query().Get("after_id"); got != "" {
				t.Errorf("first cursor=%q", got)
			}
			_, _ = fmt.Fprint(w, `{"data":[{"id":"claude-a","display_name":"Claude A"}],"has_more":true,"last_id":"claude-a"}`)
			return
		}
		if got := r.URL.Query().Get("after_id"); got != "claude-a" {
			t.Errorf("next cursor=%q", got)
		}
		_, _ = fmt.Fprint(w, `{"data":[{"id":"claude-b","display_name":"Claude B"}],"has_more":false,"last_id":"claude-b"}`)
	}))
	server.Listener = listener
	server.Start()
	defer server.Close()
	snapshot, err := (AnthropicModelSource{}).ListSnapshot(t.Context(), ModelSourceInput{URL: server.URL, Credential: kernel.Credential{Secret: "anthropic-secret"}, Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Complete || len(snapshot.Models) != 2 || snapshot.Models[0].DisplayName != "Claude A" || snapshot.Models[1].ID != "claude-b" || page != 2 {
		t.Fatalf("snapshot=%#v pages=%d", snapshot, page)
	}
}

func TestHTTPJSONErrorClassifierExtractsQuotaResetFromStructuredBody(t *testing.T) {
	reset := time.Now().Add(4 * time.Minute).UTC().Truncate(time.Second)
	body := []byte(`{"error":{"type":"insufficient_quota","code":"insufficient_quota","message":"You exceeded your current quota","reset_at":"` + reset.Format(time.RFC3339) + `"}}`)
	outcome := (HTTPJSONErrorClassifier{}).ClassifyOutcome(http.StatusTooManyRequests, http.Header{}, body)
	if outcome.Class != kernel.ErrorCooldown || outcome.Cause != kernel.CauseQuotaExhausted || outcome.Retry != kernel.RetryAfter || outcome.Effect != kernel.EffectRejected {
		t.Fatalf("quota outcome=%#v", outcome)
	}
	if delta := outcome.RetryAt.Sub(reset); delta < -time.Second || delta > time.Second {
		t.Fatalf("reset deadline lost: got=%s want=%s", outcome.RetryAt, reset)
	}
	if len(outcome.Limits) != 1 {
		t.Fatalf("quota windows=%#v", outcome.Limits)
	}
	window := outcome.Limits[0]
	if window.Name != "quota" || window.Remaining == nil || *window.Remaining != 0 || window.ResetAt == nil || !window.ResetAt.Equal(reset) || window.Source != kernel.EvidenceErrorBody {
		t.Fatalf("quota window=%#v", window)
	}
	stringError := (HTTPJSONErrorClassifier{}).ClassifyOutcome(http.StatusTooManyRequests, nil, []byte(`{"error":"quota exceeded"}`))
	if stringError.Cause != kernel.CauseQuotaExhausted || len(stringError.Limits) != 1 || stringError.Limits[0].Remaining == nil || *stringError.Limits[0].Remaining != 0 {
		t.Fatalf("string error envelope lost quota evidence: %#v", stringError)
	}
}

func TestHTTPJSONErrorClassifierMergesRootAndNestedErrorEvidence(t *testing.T) {
	reset := time.Now().Add(3 * time.Minute).UTC().Truncate(time.Second)
	body := []byte(`{"type":"insufficient_quota","reset_at":"` + reset.Format(time.RFC3339) + `","error":{"message":"balance unavailable"}}`)
	outcome := (HTTPJSONErrorClassifier{}).ClassifyOutcome(http.StatusConflict, nil, body)
	if outcome.Class != kernel.ErrorCooldown || outcome.Cause != kernel.CauseQuotaExhausted || outcome.Retry != kernel.RetryAfter || !outcome.RetryAt.Equal(reset) {
		t.Fatalf("outer quota/reset fields were dropped by nested error object: %#v", outcome)
	}
	if len(outcome.Limits) != 1 || outcome.Limits[0].Remaining == nil || *outcome.Limits[0].Remaining != 0 || outcome.Limits[0].ResetAt == nil || !outcome.Limits[0].ResetAt.Equal(reset) {
		t.Fatalf("merged quota window=%#v", outcome.Limits)
	}
	arrayEnvelope := []byte(`{"type":"insufficient_quota","reset_at":"` + reset.Format(time.RFC3339) + `","error":[{"message":"balance unavailable"}]}`)
	arrayOutcome := (HTTPJSONErrorClassifier{}).ClassifyOutcome(http.StatusPaymentRequired, nil, arrayEnvelope)
	if arrayOutcome.Cause != kernel.CauseQuotaExhausted || !arrayOutcome.RetryAt.Equal(reset) {
		t.Fatalf("unrecognized nested error shape discarded root evidence: %#v", arrayOutcome)
	}
}

func TestDecodeModelCatalogSnapshotTracksWhetherAbsenceIsAuthoritative(t *testing.T) {
	complete, err := DecodeModelCatalogSnapshot([]byte(`{"data":[{"id":"a"},{"id":"b"}]}`))
	if err != nil || !complete.Complete || len(complete.Models) != 2 {
		t.Fatalf("complete catalog=%#v err=%v", complete, err)
	}
	for _, body := range []string{
		`{"data":[{"id":"a"}],"has_more":true}`,
		`{"data":[{"id":"a"}],"next_cursor":"cursor"}`,
	} {
		snapshot, err := DecodeModelCatalogSnapshot([]byte(body))
		if err != nil || snapshot.Complete {
			t.Fatalf("paginated catalog treated as complete: %#v err=%v", snapshot, err)
		}
	}
	if _, err := DecodeModelCatalogSnapshot([]byte(`{"ok":true}`)); err == nil {
		t.Fatal("unrecognized model-list shape must not be treated as an empty complete catalog")
	}
}

func TestHTTPJSONErrorClassifierExtractsGeminiRetryDelayWithoutCallingItQuota(t *testing.T) {
	body := []byte(`{"error":{"status":"RESOURCE_EXHAUSTED","message":"The service is temporarily overloaded","details":[{"retryDelay":"45s"}]}}`)
	outcome := (HTTPJSONErrorClassifier{}).ClassifyOutcome(http.StatusTooManyRequests, http.Header{}, body)
	if outcome.Cause != kernel.CauseRateLimited || outcome.Cause == kernel.CauseQuotaExhausted {
		t.Fatalf("non-quota exhaustion was misclassified: %#v", outcome)
	}
	if remaining := time.Until(outcome.RetryAt); remaining < 44*time.Second || remaining > 46*time.Second {
		t.Fatalf("structured retry delay not applied: %s", remaining)
	}
	if len(outcome.Limits) > 0 && outcome.Limits[0].Name == "quota" {
		t.Fatalf("ambiguous resource exhaustion was recorded as quota: %#v", outcome.Limits[0])
	}
}

func TestHTTPJSONErrorClassifierKeeps429RateLimitAndRetryAfterHeader(t *testing.T) {
	reset := time.Now().Add(3 * time.Minute).UTC().Truncate(time.Second)
	headers := make(http.Header)
	headers.Set("Retry-After", reset.Format(http.TimeFormat))
	headers.Set("X-RateLimit-Remaining", "0")
	headers.Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
	body := []byte(`{"error":{"type":"rate_limit_error","message":"Too many requests"}}`)
	outcome := (HTTPJSONErrorClassifier{}).ClassifyOutcome(http.StatusTooManyRequests, headers, body)
	if outcome.Cause != kernel.CauseRateLimited || outcome.Effect != kernel.EffectRejected || len(outcome.Limits) != 1 || outcome.Limits[0].Name != "rate_limit" {
		t.Fatalf("rate limit outcome=%#v", outcome)
	}
	if delta := outcome.RetryAt.Sub(reset); delta < -time.Second || delta > time.Second {
		t.Fatalf("HTTP-date Retry-After not parsed: got=%s want=%s", outcome.RetryAt, reset)
	}
}

func TestHTTPJSONErrorClassifierUsesRateLimitResetAndDeterministicDeadlinePrecedence(t *testing.T) {
	classifier := HTTPJSONErrorClassifier{}
	resetHeader := time.Now().Add(5 * time.Minute).UTC().Truncate(time.Second)
	resetBody := time.Now().Add(9 * time.Minute).UTC().Truncate(time.Second)
	retryHeader := time.Now().Add(2 * time.Minute).UTC().Truncate(time.Second)
	body := []byte(`{"error":{"type":"rate_limit_error","message":"Too many requests"}}`)

	resetOnly := make(http.Header)
	resetOnly.Set("X-RateLimit-Remaining", "0")
	resetOnly.Set("X-RateLimit-Reset", strconv.FormatInt(resetHeader.Unix(), 10))
	outcome := classifier.ClassifyOutcome(http.StatusTooManyRequests, resetOnly, body)
	if !outcome.RetryAt.Equal(resetHeader) {
		t.Fatalf("X-RateLimit-Reset did not drive route cooldown: got=%s want=%s", outcome.RetryAt, resetHeader)
	}
	if len(outcome.Limits) != 1 || outcome.Limits[0].Remaining == nil || *outcome.Limits[0].Remaining != 0 || outcome.Limits[0].ResetAt == nil || !outcome.Limits[0].ResetAt.Equal(resetHeader) {
		t.Fatalf("rate limit window lost reset/remaining evidence: %#v", outcome.Limits)
	}

	conflicting := make(http.Header)
	conflicting.Set("Retry-After", retryHeader.Format(http.TimeFormat))
	conflicting.Set("X-RateLimit-Reset", strconv.FormatInt(resetHeader.Unix(), 10))
	outcome = classifier.ClassifyOutcome(http.StatusTooManyRequests, conflicting, body)
	if delta := outcome.RetryAt.Sub(retryHeader); delta < -time.Second || delta > time.Second {
		t.Fatalf("Retry-After must outrank reset header: got=%s want=%s", outcome.RetryAt, retryHeader)
	}

	conflictingBody := []byte(`{"error":{"type":"rate_limit_error","message":"Too many requests","reset_at":"` + resetBody.Format(time.RFC3339) + `","retry_after":"75s"}}`)
	outcome = classifier.ClassifyOutcome(http.StatusTooManyRequests, conflicting, conflictingBody)
	if delta := outcome.RetryAt.Sub(resetBody); delta < -time.Second || delta > time.Second {
		t.Fatalf("provider error reset must outrank response retry signals: got=%s want=%s", outcome.RetryAt, resetBody)
	}

	bodyRetryOnly := []byte(`{"error":{"type":"rate_limit_error","message":"Too many requests","retry_after":"75s"}}`)
	outcome = classifier.ClassifyOutcome(http.StatusTooManyRequests, resetOnly, bodyRetryOnly)
	if delta := outcome.RetryAt.Sub(resetHeader); delta < -time.Second || delta > time.Second {
		t.Fatalf("reset header must outrank provider retry delay: got=%s want=%s", outcome.RetryAt, resetHeader)
	}
	invalidRequest := classifier.ClassifyOutcome(http.StatusBadRequest, resetOnly, []byte(`{"error":{"message":"invalid request"}}`))
	if invalidRequest.Retry != kernel.RetryNever || !invalidRequest.RetryAt.IsZero() {
		t.Fatalf("non-retryable request error inherited a retry deadline: %#v", invalidRequest)
	}
}

func TestHTTPJSONErrorClassifierKeepsCoarseClassAlignedWithRichCause(t *testing.T) {
	classifier := HTTPJSONErrorClassifier{}
	invalidQuotaMention := []byte(`{"error":{"message":"quota parameter is invalid for this request"}}`)
	class := classifier.Classify(http.StatusBadRequest, invalidQuotaMention)
	outcome := classifier.ClassifyOutcome(http.StatusBadRequest, nil, invalidQuotaMention)
	if class != kernel.ErrorTerminal || outcome.Class != class || outcome.Cause != kernel.CauseRequestInvalid {
		t.Fatalf("invalid request was misclassified as quota retry: class=%s outcome=%#v", class, outcome)
	}

	exhaustedQuota := []byte(`{"error":{"code":"insufficient_quota","message":"quota exceeded"}}`)
	class = classifier.Classify(http.StatusPaymentRequired, exhaustedQuota)
	outcome = classifier.ClassifyOutcome(http.StatusPaymentRequired, nil, exhaustedQuota)
	if class != kernel.ErrorCooldown || outcome.Class != class || outcome.Cause != kernel.CauseQuotaExhausted {
		t.Fatalf("explicit quota exhaustion was not retryable: class=%s outcome=%#v", class, outcome)
	}

	authOutcome := classifier.ClassifyOutcome(http.StatusUnauthorized, nil, []byte(`{"error":{"message":"invalid api key"}}`))
	if authOutcome.Cause != kernel.CauseAuth || authOutcome.Scope != kernel.ScopeConnection || authOutcome.Retry != kernel.RetryNever || authOutcome.Effect != kernel.EffectRejected {
		t.Fatalf("authentication failure did not carry permanent connection-scoped evidence: %#v", authOutcome)
	}
	for _, fixture := range []struct {
		status int
		body   string
		cause  kernel.OutcomeCause
		class  kernel.ErrorClass
	}{
		{status: http.StatusNotFound, body: `{"error":"invalid api key"}`, cause: kernel.CauseModelNotFound, class: kernel.ErrorTerminal},
		{status: http.StatusInternalServerError, body: `{"error":"unauthorized"}`, cause: kernel.CauseCapacity, class: kernel.ErrorCooldown},
		{status: http.StatusBadRequest, body: `{"error":"authentication parameter invalid"}`, cause: kernel.CauseRequestInvalid, class: kernel.ErrorTerminal},
	} {
		outcome := classifier.ClassifyOutcome(fixture.status, nil, []byte(fixture.body))
		if outcome.Class != fixture.class || outcome.Cause != fixture.cause {
			t.Fatalf("HTTP status/body precedence mismatch: status=%d class=%s/%s cause=%s/%s", fixture.status, outcome.Class, fixture.class, outcome.Cause, fixture.cause)
		}
	}
}

func TestHTTPJSONErrorClassifierHandlesTypedQuotaStatusesAndGenericConflict(t *testing.T) {
	classifier := HTTPJSONErrorClassifier{}
	reset := time.Now().Add(2 * time.Minute).UTC().Truncate(time.Second)
	quotaBody := []byte(`{"error":{"code":"PLAN_EXHAUSTED","message":"weekly quota exhausted","reset_at":"` + reset.Format(time.RFC3339) + `"}}`)
	for _, status := range []int{http.StatusConflict, http.StatusForbidden} {
		class := classifier.Classify(status, quotaBody)
		outcome := classifier.ClassifyOutcome(status, nil, quotaBody)
		if class != kernel.ErrorCooldown || outcome.Class != class || outcome.Cause != kernel.CauseQuotaExhausted || outcome.Retry != kernel.RetryAfter {
			t.Fatalf("typed quota evidence did not override HTTP %d default: class=%s outcome=%#v", status, class, outcome)
		}
	}

	custom := HTTPJSONErrorClassifier{Options: &HTTPJSONErrorClassifierOptions{QuotaHTTPStatuses: []int{418}, CodePath: "/error/code", QuotaCodes: []string{"ACCOUNT_DRY"}, ResetAtPath: "/error/reset_at"}}
	customBody := []byte(`{"error":{"code":"ACCOUNT_DRY","message":"balance exhausted","reset_at":"` + reset.Format(time.RFC3339) + `"}}`)
	class := custom.Classify(http.StatusTeapot, customBody)
	outcome := custom.ClassifyOutcome(http.StatusTeapot, nil, customBody)
	if class != kernel.ErrorCooldown || outcome.Class != class || outcome.Cause != kernel.CauseQuotaExhausted || !outcome.RetryAt.Equal(reset) {
		t.Fatalf("manifest quota HTTP status was not honored: class=%s outcome=%#v", class, outcome)
	}

	conflict := classifier.ClassifyOutcome(http.StatusConflict, nil, []byte(`{"error":{"message":"resource conflict"}}`))
	if conflict.Class != kernel.ErrorCooldown || conflict.Cause != kernel.CauseOverloaded || conflict.Retry != kernel.RetryAfter || conflict.Effect != kernel.EffectUnknown {
		t.Fatalf("generic 409 outcome disagrees with coarse cooldown: %#v", conflict)
	}
	scoped := HTTPJSONErrorClassifier{Options: &HTTPJSONErrorClassifierOptions{CapacityScope: kernel.ScopeProvider}}
	conflict = scoped.ClassifyOutcome(http.StatusConflict, nil, []byte(`{"error":{"message":"resource conflict"}}`))
	if conflict.Cause != kernel.CauseOverloaded || conflict.Scope != kernel.ScopeProvider {
		t.Fatalf("configured capacity scope was not applied to overloaded conflict: %#v", conflict)
	}
	if _, err := (HTTPJSONErrorClassifier{}).WithOptions(ErrorClassifierOptions{HTTPJSON: &HTTPJSONErrorClassifierOptions{QuotaHTTPStatuses: []int{399}}}); err == nil {
		t.Fatal("invalid quota HTTP status was accepted")
	}
}

func TestHTTPJSONErrorClassifierAppliesOrderedTypedScopeSignals(t *testing.T) {
	classifier := HTTPJSONErrorClassifier{Options: &HTTPJSONErrorClassifierOptions{
		DefaultScope: kernel.ScopeRoute, CapacityScope: kernel.ScopeConnection,
		ScopeSignals: []HTTPJSONScopeSignal{
			{Cause: kernel.CauseOverloaded, Codes: []string{"region_capacity"}, Scope: kernel.ScopeProvider},
			{Cause: kernel.CauseOverloaded, Types: []string{"resource_exhausted"}, Scope: kernel.ScopeConnection},
		},
	}}
	body := []byte(`{"error":{"code":"REGION_CAPACITY","status":"RESOURCE_EXHAUSTED","message":"regional capacity exhausted"}}`)
	outcome := classifier.ClassifyOutcome(http.StatusConflict, nil, body)
	if outcome.Cause != kernel.CauseOverloaded || outcome.Scope != kernel.ScopeProvider {
		t.Fatalf("first matching typed scope signal did not win: %#v", outcome)
	}
	other := classifier.ClassifyOutcome(http.StatusConflict, nil, []byte(`{"error":{"status":"RESOURCE_EXHAUSTED","message":"regional capacity exhausted"}}`))
	if other.Cause != kernel.CauseOverloaded || other.Scope != kernel.ScopeConnection {
		t.Fatalf("secondary typed scope signal did not match its envelope: %#v", other)
	}
	if _, err := (HTTPJSONErrorClassifier{}).WithOptions(ErrorClassifierOptions{HTTPJSON: &HTTPJSONErrorClassifierOptions{ScopeSignals: []HTTPJSONScopeSignal{{Scope: kernel.ScopeProvider}}}}); err == nil {
		t.Fatal("unconditional scope signal was accepted")
	}
}
