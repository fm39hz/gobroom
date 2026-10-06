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
	if outcome.Class != kernel.ErrorCooldown || outcome.Cause != kernel.CauseQuotaExhausted || outcome.Retry != kernel.RetryAfter {
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
	if outcome.Cause != kernel.CauseRateLimited || len(outcome.Limits) != 1 || outcome.Limits[0].Name != "rate_limit" {
		t.Fatalf("rate limit outcome=%#v", outcome)
	}
	if delta := outcome.RetryAt.Sub(reset); delta < -time.Second || delta > time.Second {
		t.Fatalf("HTTP-date Retry-After not parsed: got=%s want=%s", outcome.RetryAt, reset)
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
}
