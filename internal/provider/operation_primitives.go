package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/quota"
)

type ModelDescriptor struct {
	ID          string                   `json:"id"`
	DisplayName string                   `json:"displayName,omitempty"`
	Profile     kernel.CapabilityProfile `json:"profile,omitempty"`
	Metadata    map[string]any           `json:"metadata,omitempty"`
}

type StaticModelSourceOptions struct {
	Models []ModelDescriptor `json:"models"`
}

func (o StaticModelSourceOptions) Validate() error {
	if len(o.Models) == 0 || len(o.Models) > 2048 {
		return fmt.Errorf("static model source requires between 1 and 2048 models")
	}
	seen := make(map[string]struct{}, len(o.Models))
	for index, model := range o.Models {
		if model.ID == "" || strings.TrimSpace(model.ID) != model.ID {
			return fmt.Errorf("static model %d requires a non-empty trimmed ID", index)
		}
		if strings.TrimSpace(model.DisplayName) != model.DisplayName {
			return fmt.Errorf("static model %q display name must be trimmed", model.ID)
		}
		if _, duplicate := seen[model.ID]; duplicate {
			return fmt.Errorf("static model ID %q is duplicated", model.ID)
		}
		seen[model.ID] = struct{}{}
	}
	return nil
}

type StaticModelCatalogSource interface {
	ModelCatalogSnapshotSource
	StaticCatalog() bool
}

// ModelCatalogSnapshot states whether the returned list is authoritative for
// absence decisions. Incomplete responses may establish positive presence but
// must never mark omitted IDs as unavailable.
type ModelCatalogSnapshot struct {
	Models   []ModelDescriptor
	Complete bool
	Evidence ModelCatalogEvidence
}

type ModelCatalogEvidence string

const (
	ModelCatalogConnectionObserved ModelCatalogEvidence = "connection_observed"
	ModelCatalogProviderRegistry   ModelCatalogEvidence = "provider_registry"
)

type ModelSourceInput struct {
	URL          string
	Credential   kernel.Credential
	Presentation kernel.CredentialPresentation
	Client       *http.Client
	Headers      http.Header
}

type ModelSource interface {
	ID() string
	List(context.Context, ModelSourceInput) ([]ModelDescriptor, error)
}

// ConnectionTestModelSource marks model sources that actually contact the
// configured endpoint. Static catalogs intentionally do not satisfy this
// contract, so they cannot report a false-positive connection test.
type ConnectionTestModelSource interface {
	ModelSource
	Test(context.Context, ModelSourceInput) ([]ModelDescriptor, error)
}

type ModelCatalogSnapshotSource interface {
	ListSnapshot(context.Context, ModelSourceInput) (ModelCatalogSnapshot, error)
}

type ConnectionTestModelCatalogSnapshotSource interface {
	ModelCatalogSnapshotSource
	TestSnapshot(context.Context, ModelSourceInput) (ModelCatalogSnapshot, error)
}

type QuotaSource interface {
	ID() string
	Fetch(context.Context, QuotaRequest) ([]quota.Snapshot, error)
}

type QuotaScope string

const (
	QuotaScopeModel      QuotaScope = "model"
	QuotaScopeConnection QuotaScope = "connection"
)

// ScopedQuotaSource optionally declares the identity shared by its snapshots.
// Unscoped implementations retain the conservative model scope.
type ScopedQuotaSource interface {
	QuotaScope() QuotaScope
}

func QuotaScopeOf(source QuotaSource) QuotaScope {
	if scoped, ok := source.(ScopedQuotaSource); ok && scoped.QuotaScope() == QuotaScopeConnection {
		return QuotaScopeConnection
	}
	return QuotaScopeModel
}

type QuotaSourceFactory func(json.RawMessage) (QuotaSource, error)

type QuotaRequest struct {
	Credential      kernel.Credential
	Route           kernel.Route
	Endpoint        kernel.Endpoint
	Transport       kernel.Transport
	EndpointOptions kernel.EndpointOptions
	WindowName      string
	// ReportFailure surfaces a failed leg when the source returns partial data.
	// Callers should classify the error and must not retain or log its raw text.
	ReportFailure func(error)
}

type QuotaHTTPError struct{ Status int }

func (e *QuotaHTTPError) Error() string {
	return fmt.Sprintf("quota endpoint returned HTTP %d", e.Status)
}

func QuotaFailureCode(err error) string {
	var httpError *QuotaHTTPError
	if errors.As(err, &httpError) {
		switch {
		case httpError.Status == http.StatusUnauthorized:
			return "auth"
		case httpError.Status == http.StatusForbidden:
			return "permission"
		case httpError.Status == http.StatusTooManyRequests:
			return "rate_limited"
		case httpError.Status >= http.StatusInternalServerError:
			return "upstream_error"
		default:
			return "http_error"
		}
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "report_error"
}

// QuotaSnapshot is the normalized public result type for provider quota sources.
type QuotaSnapshot = quota.Snapshot
type QuotaRoutingEffect = quota.RoutingEffect

const (
	QuotaRoutingEnforce = quota.RoutingEffectEnforce
	QuotaRoutingObserve = quota.RoutingEffectObserve
)

type UsageSource interface {
	kernel.UsageEnricher
	ID() string
}

// HTTPHeaderUsageSource lets a provider manifest select response headers as
// authoritative usage values; body-derived values remain when a header is absent.
type HTTPHeaderUsageSource struct{}

func (HTTPHeaderUsageSource) ID() string { return "http-header-usage" }
func (HTTPHeaderUsageSource) EnrichUsage(_ context.Context, route kernel.Route, headers http.Header, event kernel.UsageEvent) (kernel.UsageEvent, error) {
	inputHeader := route.UsageOptions.InputTokensHeader
	if inputHeader == "" {
		inputHeader = "X-Usage-Input-Tokens"
	}
	outputHeader := route.UsageOptions.OutputTokensHeader
	if outputHeader == "" {
		outputHeader = "X-Usage-Output-Tokens"
	}
	costHeader := route.UsageOptions.EstimatedCostHeader
	if costHeader == "" {
		costHeader = "X-Usage-Cost-USD"
	}
	if value, err := strconv.ParseInt(strings.TrimSpace(headers.Get(inputHeader)), 10, 64); err == nil && value >= 0 {
		event.InputTokens = value
	}
	if value, err := strconv.ParseInt(strings.TrimSpace(headers.Get(outputHeader)), 10, 64); err == nil && value >= 0 {
		event.OutputTokens = value
	}
	if value, err := strconv.ParseFloat(strings.TrimSpace(headers.Get(costHeader)), 64); err == nil && value >= 0 && !math.IsInf(value, 0) && !math.IsNaN(value) {
		event.EstimatedCost = value
	}
	return event, nil
}

type ErrorClassifier interface {
	ID() string
	Classify(status int, body []byte) kernel.ErrorClass
}

type ConfigurableErrorClassifier interface {
	WithOptions(ErrorClassifierOptions) (ErrorClassifier, error)
}

type HTTPJSONErrorClassifier struct {
	Options *HTTPJSONErrorClassifierOptions
}

func (HTTPJSONErrorClassifier) ID() string { return "http-json" }
func (HTTPJSONErrorClassifier) WithOptions(options ErrorClassifierOptions) (ErrorClassifier, error) {
	if options.HTTPJSON == nil {
		return nil, fmt.Errorf("http-json classifier options are required")
	}
	if err := options.HTTPJSON.Validate(); err != nil {
		return nil, err
	}
	copy := cloneHTTPJSONErrorClassifierOptions(*options.HTTPJSON)
	return HTTPJSONErrorClassifier{Options: &copy}, nil
}

func cloneHTTPJSONErrorClassifierOptions(options HTTPJSONErrorClassifierOptions) HTTPJSONErrorClassifierOptions {
	cloneStrings := func(values []string) []string { return append([]string(nil), values...) }
	options.QuotaCodes = cloneStrings(options.QuotaCodes)
	options.QuotaTypes = cloneStrings(options.QuotaTypes)
	options.QuotaStatuses = cloneStrings(options.QuotaStatuses)
	options.QuotaHTTPStatuses = append([]int(nil), options.QuotaHTTPStatuses...)
	options.QuotaMessageTokens = cloneStrings(options.QuotaMessageTokens)
	cloneSignals := func(source []HTTPJSONErrorSignal) []HTTPJSONErrorSignal {
		result := append([]HTTPJSONErrorSignal(nil), source...)
		for index := range result {
			signal := &result[index]
			signal.HTTPStatuses = append([]int(nil), signal.HTTPStatuses...)
			signal.Codes = cloneStrings(signal.Codes)
			signal.Types = cloneStrings(signal.Types)
			signal.Statuses = cloneStrings(signal.Statuses)
			signal.Fields = append([]HTTPJSONFieldMatch(nil), signal.Fields...)
			for fieldIndex := range signal.Fields {
				signal.Fields[fieldIndex].Equals = cloneStrings(signal.Fields[fieldIndex].Equals)
				signal.Fields[fieldIndex].Contains = cloneStrings(signal.Fields[fieldIndex].Contains)
			}
		}
		return result
	}
	options.AuthRecoverySignals = cloneSignals(options.AuthRecoverySignals)
	options.ContinuityResetSignals = cloneSignals(options.ContinuityResetSignals)
	options.ResponseContinuityResetSignals = cloneSignals(options.ResponseContinuityResetSignals)
	options.LimitHeaders = append([]HTTPHeaderLimitSignal(nil), options.LimitHeaders...)
	for index := range options.LimitHeaders {
		options.LimitHeaders[index].ExhaustedStatusValues = cloneStrings(options.LimitHeaders[index].ExhaustedStatusValues)
	}
	options.ScopeSignals = append([]HTTPJSONScopeSignal(nil), options.ScopeSignals...)
	for index := range options.ScopeSignals {
		signal := &options.ScopeSignals[index]
		signal.Codes = cloneStrings(signal.Codes)
		signal.Types = cloneStrings(signal.Types)
		signal.Statuses = cloneStrings(signal.Statuses)
		signal.MessageTokens = cloneStrings(signal.MessageTokens)
		signal.Fields = append([]HTTPJSONFieldMatch(nil), signal.Fields...)
		for fieldIndex := range signal.Fields {
			signal.Fields[fieldIndex].Equals = cloneStrings(signal.Fields[fieldIndex].Equals)
			signal.Fields[fieldIndex].Contains = cloneStrings(signal.Fields[fieldIndex].Contains)
		}
	}
	return options
}
func (c HTTPJSONErrorClassifier) Classify(status int, body []byte) kernel.ErrorClass {
	evidence := decodeHTTPErrorEvidence(body, c.Options)
	return c.classifyWithEvidence(status, body, evidence)
}

// ClassifyResponseError recognizes provider-declared continuation failures in
// successful response streams. It deliberately has no HTTP status input.
func (c HTTPJSONErrorClassifier) ClassifyResponseError(body []byte) kernel.ClassifiedOutcome {
	outcome := kernel.ClassifiedOutcome{Class: kernel.ErrorCooldown, Cause: kernel.CauseStreamFailure, Effect: kernel.EffectAccepted, Scope: kernel.ScopeOperation, Retry: kernel.RetryAfter, Recovery: kernel.CredentialRecoveryNone, ContinuityRecovery: kernel.ContinuityRecoveryNone, StatusCode: 0, Evidence: []kernel.EvidenceSource{kernel.EvidenceErrorBody}, Message: "provider reported a terminal response error"}
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	var errorObject map[string]json.RawMessage
	canonicalError := json.Unmarshal(body, &envelope) == nil && len(envelope.Error) > 0 && json.Unmarshal(envelope.Error, &errorObject) == nil && errorObject != nil
	if c.Options != nil && canonicalError && matchHTTPJSONResponseSignal(c.Options.ResponseContinuityResetSignals, body, decodeHTTPErrorEvidence(body, c.Options)) {
		outcome.Class = kernel.ErrorTerminal
		outcome.Cause = kernel.CauseContinuityInvalid
		outcome.Effect = kernel.EffectAccepted
		outcome.Scope = kernel.ScopeRequest
		outcome.Retry = kernel.RetryNever
		outcome.Recovery = kernel.CredentialRecoveryNone
		outcome.ContinuityRecovery = kernel.ContinuityRecoveryInvalidate
	}
	return outcome
}

func matchHTTPJSONResponseSignal(signals []HTTPJSONErrorSignal, body []byte, evidence httpErrorEvidence) bool {
	for _, signal := range signals {
		if signal.HTTPStatuses != nil || len(signal.Codes)+len(signal.Types)+len(signal.Statuses)+len(signal.Fields) == 0 {
			continue
		}
		if matchHTTPJSONErrorBody(signal, body, evidence) {
			return true
		}
	}
	return false
}

func (c HTTPJSONErrorClassifier) classifyWithEvidence(status int, body []byte, evidence httpErrorEvidence) kernel.ErrorClass {
	switch {
	case c.matchContinuityResetSignal(status, body, evidence):
		return kernel.ErrorTerminal
	case evidence.QuotaExhausted && isQuotaHTTPStatus(status, c.Options):
		return kernel.ErrorCooldown
	case c.authRecoveryConfigured() && c.matchAuthRecoverySignal(status, body, evidence):
		return kernel.ErrorAuth
	case status == 401 || status == 403:
		return kernel.ErrorAuth
	case status == 408 || status == 409 || status == 429 || status >= 500:
		return kernel.ErrorCooldown
	case status >= 400:
		return kernel.ErrorTerminal
	default:
		return kernel.ErrorRetryable
	}
}

func (c HTTPJSONErrorClassifier) authRecoveryConfigured() bool {
	return c.Options != nil && len(c.Options.AuthRecoverySignals) > 0
}

func (c HTTPJSONErrorClassifier) matchAuthRecoverySignal(status int, body []byte, evidence httpErrorEvidence) bool {
	return c.Options != nil && matchHTTPJSONErrorSignal(c.Options.AuthRecoverySignals, status, body, evidence)
}

func matchHTTPJSONErrorSignal(signals []HTTPJSONErrorSignal, status int, body []byte, evidence httpErrorEvidence) bool {
	if status < 400 {
		return false
	}
	for _, signal := range signals {
		if len(signal.HTTPStatuses) > 0 && !slices.Contains(signal.HTTPStatuses, status) {
			continue
		}
		if matchHTTPJSONErrorBody(signal, body, evidence) {
			return true
		}
	}
	return false
}

func matchHTTPJSONErrorBody(signal HTTPJSONErrorSignal, body []byte, evidence httpErrorEvidence) bool {
	if len(signal.Codes) > 0 && !matchesAny(evidence.Code, signal.Codes) {
		return false
	}
	if len(signal.Types) > 0 && !matchesAny(evidence.Type, signal.Types) {
		return false
	}
	if len(signal.Statuses) > 0 && !matchesAny(evidence.Status, signal.Statuses) {
		return false
	}
	for _, field := range signal.Fields {
		value := pointerText(body, field.Path)
		if !matchesAny(value, field.Equals) && !matchesAnyContains(value, field.Contains) {
			return false
		}
	}
	return true
}

func (c HTTPJSONErrorClassifier) matchContinuityResetSignal(status int, body []byte, evidence httpErrorEvidence) bool {
	return status >= 400 && status < 500 && status != 401 && status != 403 && status != 408 && status != 429 &&
		c.Options != nil && matchHTTPJSONErrorSignal(c.Options.ContinuityResetSignals, status, body, evidence)
}

func isQuotaHTTPStatus(status int, options *HTTPJSONErrorClassifierOptions) bool {
	switch status {
	case http.StatusPaymentRequired, http.StatusForbidden, http.StatusConflict, http.StatusTooManyRequests:
		return true
	}
	if options != nil {
		for _, configured := range options.QuotaHTTPStatuses {
			if status == configured {
				return true
			}
		}
	}
	return false
}

func extractHeaderLimitWindows(headers http.Header, options *HTTPJSONErrorClassifierOptions, now time.Time) ([]kernel.LimitWindow, time.Time) {
	if options == nil || len(options.LimitHeaders) == 0 {
		return nil, time.Time{}
	}
	windows := make([]kernel.LimitWindow, 0, len(options.LimitHeaders))
	var latestReset time.Time
	for _, signal := range options.LimitHeaders {
		window := kernel.LimitWindow{Name: signal.Name, Kind: signal.Kind, Source: kernel.EvidenceResponseHeader}
		observed := false
		remainingProvided := false
		for _, item := range []struct {
			header    string
			target    **float64
			remaining bool
		}{{signal.LimitHeader, &window.Limit, false}, {signal.UsedHeader, &window.Used, false}, {signal.RemainingHeader, &window.Remaining, true}} {
			if item.header == "" {
				continue
			}
			if value := headers.Get(item.header); value != "" {
				if item.remaining {
					remainingProvided = true
				}
				if parsed := parseHeaderLimitValue(value); parsed != nil {
					*item.target = parsed
					observed = true
				}
			}
		}
		exhausted := window.Remaining != nil && *window.Remaining <= 0
		if signal.ExhaustedStatusHeader != "" {
			if status := headers.Get(signal.ExhaustedStatusHeader); status != "" {
				observed = true
				if matchesAny(status, signal.ExhaustedStatusValues) {
					zero := float64(0)
					window.Remaining = &zero
					exhausted = true
				}
			}
		}
		if signal.ResetHeader != "" {
			if raw := headers.Get(signal.ResetHeader); raw != "" {
				reset := parseHeaderReset(raw, now)
				if reset.After(now) {
					window.ResetAt = &reset
					observed = true
					resetIsApplicable := exhausted || (!remainingProvided && signal.ExhaustedStatusHeader == "")
					if resetIsApplicable && reset.After(latestReset) {
						latestReset = reset
					}
				}
			}
		}
		if observed {
			windows = append(windows, window)
		}
	}
	return windows, latestReset
}

func parseHeaderLimitValue(value string) *float64 {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return nil
	}
	return &parsed
}

func parseHeaderReset(value string, now time.Time) time.Time {
	value = strings.TrimSpace(value)
	if at, err := time.Parse(time.RFC3339Nano, value); err == nil && at.After(now) {
		return at
	}
	if at, err := http.ParseTime(value); err == nil && at.After(now) {
		return at
	}
	if numeric, err := strconv.ParseFloat(value, 64); err == nil && numeric > 0 && !math.IsNaN(numeric) && !math.IsInf(numeric, 0) {
		whole, fractional := math.Modf(numeric)
		var at time.Time
		switch {
		case numeric > 10000000000 && numeric <= 100000000000000:
			at = time.UnixMilli(int64(whole)).Add(time.Duration(fractional * float64(time.Millisecond)))
		case numeric > 1000000000 && numeric <= 10000000000:
			at = time.Unix(int64(whole), int64(fractional*float64(time.Second)))
		case numeric <= 1000000000:
			at = now.Add(time.Duration(numeric * float64(time.Second)))
		default:
			return time.Time{}
		}
		if at.After(now) {
			return at
		}
		return time.Time{}
	}
	return parseRetryAfter(value, now)
}

// ClassifyOutcome adds provider-neutral evidence that the coarse class
// classifier cannot carry. Providers may replace this primitive in a manifest
// without touching the kernel.
func (c HTTPJSONErrorClassifier) ClassifyOutcome(status int, headers http.Header, body []byte) kernel.ClassifiedOutcome {
	message := strings.TrimSpace(string(body))
	errorEvidence := decodeHTTPErrorEvidence(body, c.Options)
	continuityResetMatched := c.matchContinuityResetSignal(status, body, errorEvidence)
	scope := kernel.ScopeRoute
	if c.Options != nil && c.Options.DefaultScope != "" {
		scope = c.Options.DefaultScope
	}
	outcome := kernel.ClassifiedOutcome{Class: c.classifyWithEvidence(status, body, errorEvidence), Scope: scope, Confidence: 0.7, Evidence: []kernel.EvidenceSource{kernel.EvidenceErrorBody}, StatusCode: status, Message: message, Recovery: kernel.CredentialRecoveryNone}
	authRecoveryMatched := c.authRecoveryConfigured() && c.matchAuthRecoverySignal(status, body, errorEvidence)
	switch {
	case errorEvidence.QuotaExhausted && isQuotaHTTPStatus(status, c.Options):
		outcome.Cause, outcome.Retry = kernel.CauseQuotaExhausted, kernel.RetryAfter
	case authRecoveryMatched:
		outcome.Cause, outcome.Retry, outcome.Recovery = kernel.CauseAuth, kernel.RetryNever, kernel.CredentialRecoveryRefresh
	case status == 401:
		outcome.Cause, outcome.Retry = kernel.CauseAuth, kernel.RetryNever
		if !c.authRecoveryConfigured() {
			outcome.Recovery = kernel.CredentialRecoveryRefresh
		}
	case status == 403:
		outcome.Cause, outcome.Retry = kernel.CausePermission, kernel.RetryNever
	case status == 404:
		outcome.Cause, outcome.Retry = kernel.CauseModelNotFound, kernel.RetryNever
	case status == http.StatusRequestTimeout:
		outcome.Cause, outcome.Retry = kernel.CauseTimeout, kernel.RetryAfter
	case status == http.StatusTooManyRequests:
		outcome.Cause, outcome.Retry = kernel.CauseRateLimited, kernel.RetryAfter
	case status == http.StatusConflict:
		outcome.Cause, outcome.Retry = kernel.CauseOverloaded, kernel.RetryAfter
	case status >= 500:
		outcome.Cause, outcome.Retry = kernel.CauseCapacity, kernel.RetryAfter
	case status >= 400:
		outcome.Cause, outcome.Retry = kernel.CauseRequestInvalid, kernel.RetryNever
	default:
		outcome.Cause, outcome.Retry, outcome.Confidence = kernel.CauseSuccess, kernel.RetryNow, 1
	}
	switch {
	case status >= http.StatusOK && status < http.StatusMultipleChoices:
		outcome.Effect = kernel.EffectAccepted
	case status >= 400 && status < 500 && status != http.StatusRequestTimeout && status != http.StatusConflict:
		outcome.Effect = kernel.EffectRejected
	default:
		outcome.Effect = kernel.EffectUnknown
	}
	if c.Options != nil {
		switch outcome.Cause {
		case kernel.CauseAuth, kernel.CausePermission:
			if c.Options.AuthScope != "" {
				outcome.Scope = c.Options.AuthScope
			}
		case kernel.CauseQuotaExhausted:
			if c.Options.QuotaScope != "" {
				outcome.Scope = c.Options.QuotaScope
			}
		case kernel.CauseRateLimited:
			if c.Options.RateLimitScope != "" {
				outcome.Scope = c.Options.RateLimitScope
			}
		case kernel.CauseCapacity, kernel.CauseOverloaded:
			if c.Options.CapacityScope != "" {
				outcome.Scope = c.Options.CapacityScope
			}
		}
	}
	if outcome.Cause == kernel.CauseAuth && (c.Options == nil || c.Options.DefaultScope == "") && outcome.Scope == kernel.ScopeRoute {
		outcome.Scope = kernel.ScopeConnection
	}
	if scope, matched := matchHTTPJSONScopeSignal(c.Options, errorEvidence, outcome.Cause, body); matched {
		outcome.Scope = scope
	}
	if authRecoveryMatched && outcome.Cause == kernel.CauseAuth {
		outcome.Scope = kernel.ScopeConnection
		if c.Options != nil && c.Options.AuthScope != "" {
			outcome.Scope = c.Options.AuthScope
		}
	}
	now := time.Now()
	headerRetryAt := parseRetryAfter(headers.Get("Retry-After"), now)
	rateLimitResetAt := parseHeaderReset(headers.Get("X-RateLimit-Reset"), now)
	headerResetAt := rateLimitResetAt
	configuredLimitWindows, configuredHeaderResetAt := extractHeaderLimitWindows(headers, c.Options, now)
	if configuredHeaderResetAt.After(headerResetAt) {
		headerResetAt = configuredHeaderResetAt
	}
	if outcome.Retry != kernel.RetryNever {
		switch {
		case errorEvidence.ResetAt.After(now):
			outcome.RetryAt = errorEvidence.ResetAt
		case headerRetryAt.After(now):
			outcome.RetryAt = headerRetryAt
		case headerResetAt.After(now):
			outcome.RetryAt = headerResetAt
		case errorEvidence.RetryAt.After(now):
			outcome.RetryAt = errorEvidence.RetryAt
		}
	}
	if headerRetryAt.After(now) || headerResetAt.After(now) {
		outcome.Evidence = append(outcome.Evidence, kernel.EvidenceResponseHeader)
	}
	remaining := parseHeaderLimitValue(headers.Get("X-RateLimit-Remaining"))
	if remaining != nil || !rateLimitResetAt.IsZero() {
		window := kernel.LimitWindow{Name: "rate_limit", Kind: "requests", Source: kernel.EvidenceResponseHeader}
		window.Remaining = remaining
		if !rateLimitResetAt.IsZero() {
			window.ResetAt = &rateLimitResetAt
		}
		outcome.Limits = append(outcome.Limits, window)
		if len(outcome.Evidence) == 0 || outcome.Evidence[len(outcome.Evidence)-1] != kernel.EvidenceResponseHeader {
			outcome.Evidence = append(outcome.Evidence, kernel.EvidenceResponseHeader)
		}
	}
	if len(configuredLimitWindows) > 0 {
		outcome.Limits = append(outcome.Limits, configuredLimitWindows...)
		if len(outcome.Evidence) == 0 || outcome.Evidence[len(outcome.Evidence)-1] != kernel.EvidenceResponseHeader {
			outcome.Evidence = append(outcome.Evidence, kernel.EvidenceResponseHeader)
		}
	}
	if outcome.Cause == kernel.CauseQuotaExhausted || errorEvidence.HasWindow {
		remaining := errorEvidence.Remaining
		if outcome.Cause == kernel.CauseQuotaExhausted && remaining == nil {
			zero := float64(0)
			remaining = &zero
		}
		reset := errorEvidence.ResetAt
		if reset.IsZero() {
			reset = errorEvidence.RetryAt
		}
		name := errorEvidence.WindowName
		kind := errorEvidence.WindowKind
		if c.Options != nil && name == "" && c.Options.QuotaWindowName != "" {
			name = c.Options.QuotaWindowName
		}
		if name == "" && outcome.Cause == kernel.CauseQuotaExhausted {
			name = "quota"
		}
		if name == "" && errorEvidence.HasWindow {
			name = "provider_limit"
		}
		if kind == "" && outcome.Cause == kernel.CauseQuotaExhausted {
			kind = "requests"
			if c.Options != nil && c.Options.QuotaWindowKind != "" {
				kind = c.Options.QuotaWindowKind
			}
		}
		outcome.Limits = append(outcome.Limits, kernel.LimitWindow{Name: name, Kind: kind, Limit: errorEvidence.Limit, Used: errorEvidence.Used, Remaining: remaining, ResetAt: optionalTime(reset), Source: kernel.EvidenceErrorBody})
	}
	if continuityResetMatched {
		outcome.Class = kernel.ErrorTerminal
		outcome.Cause = kernel.CauseContinuityInvalid
		outcome.Scope = kernel.ScopeRequest
		outcome.Retry = kernel.RetryNever
		outcome.RetryAt = time.Time{}
		outcome.Recovery = kernel.CredentialRecoveryNone
		outcome.Effect = kernel.EffectRejected
		outcome.ContinuityRecovery = kernel.ContinuityRecoveryInvalidate
		outcome.Limits = nil
	}
	return outcome
}

type httpErrorEnvelope struct {
	Error   json.RawMessage `json:"error,omitempty"`
	Type    string          `json:"type,omitempty"`
	Code    json.RawMessage `json:"code,omitempty"`
	Status  string          `json:"status,omitempty"`
	Message string          `json:"message,omitempty"`
}

type httpErrorDetails struct {
	Type            string            `json:"type,omitempty"`
	Code            json.RawMessage   `json:"code,omitempty"`
	Status          string            `json:"status,omitempty"`
	Message         string            `json:"message,omitempty"`
	ResetAt         json.RawMessage   `json:"reset_at,omitempty"`
	ResetAtCamel    json.RawMessage   `json:"resetAt,omitempty"`
	ResetsAt        json.RawMessage   `json:"resets_at,omitempty"`
	ResetsAtCamel   json.RawMessage   `json:"resetsAt,omitempty"`
	RetryAfter      json.RawMessage   `json:"retry_after,omitempty"`
	RetryAfterCamel json.RawMessage   `json:"retryAfter,omitempty"`
	RetryDelay      json.RawMessage   `json:"retry_delay,omitempty"`
	RetryDelayCamel json.RawMessage   `json:"retryDelay,omitempty"`
	Details         []httpErrorDetail `json:"details,omitempty"`
}

type httpErrorDetail struct {
	RetryDelay string `json:"retryDelay,omitempty"`
	RetryAfter string `json:"retryAfter,omitempty"`
}

type httpErrorEvidence struct {
	QuotaExhausted bool
	Code           string
	Type           string
	Status         string
	Message        string
	ResetAt        time.Time
	RetryAt        time.Time
	HasWindow      bool
	WindowName     string
	WindowKind     string
	Limit          *float64
	Used           *float64
	Remaining      *float64
}

func decodeHTTPErrorEvidence(body []byte, options *HTTPJSONErrorClassifierOptions) httpErrorEvidence {
	var envelope httpErrorEnvelope
	if json.Unmarshal(body, &envelope) != nil {
		return httpErrorEvidence{}
	}
	var details httpErrorDetails
	if json.Unmarshal(body, &details) != nil {
		return httpErrorEvidence{}
	}
	if len(envelope.Error) > 0 && string(envelope.Error) != "null" {
		var object httpErrorDetails
		if err := json.Unmarshal(envelope.Error, &object); err == nil {
			details = mergeHTTPErrorDetails(details, object)
		} else {
			var message string
			if json.Unmarshal(envelope.Error, &message) == nil && details.Message == "" {
				details.Message = message
			}
		}
	}
	code := strings.ToLower(rawJSONText(details.Code))
	typeName := strings.ToLower(details.Type)
	statusName := strings.ToLower(details.Status)
	errorMessage := strings.ToLower(details.Message)
	quotaExhausted := containsAny(code+" "+typeName+" "+statusName+" "+errorMessage,
		"insufficient_quota", "quota_exceeded", "quota exceeded", "quota exhausted", "out of quota", "usage limit reached")
	if !quotaExhausted && (strings.Contains(code, "quota") || strings.Contains(typeName, "quota") || strings.Contains(statusName, "quota")) {
		quotaExhausted = true
	}
	if !quotaExhausted && strings.Contains(statusName, "resource_exhausted") && strings.Contains(errorMessage, "quota") {
		quotaExhausted = true
	}
	evidence := httpErrorEvidence{QuotaExhausted: quotaExhausted, Code: code, Type: typeName, Status: statusName, Message: errorMessage}
	if options != nil {
		customCode := pointerText(body, options.CodePath)
		customType := pointerText(body, options.TypePath)
		customStatus := pointerText(body, options.StatusPath)
		customMessage := pointerText(body, options.MessagePath)
		if customCode != "" {
			evidence.Code = strings.ToLower(customCode)
		}
		if customType != "" {
			evidence.Type = strings.ToLower(customType)
		}
		if customStatus != "" {
			evidence.Status = strings.ToLower(customStatus)
		}
		if customMessage != "" {
			evidence.Message = strings.ToLower(customMessage)
		}
		quotaExhausted = quotaExhausted || matchesAny(customCode, options.QuotaCodes) || matchesAny(customType, options.QuotaTypes) || matchesAny(customStatus, options.QuotaStatuses) || containsAny(strings.ToLower(customMessage), options.QuotaMessageTokens...)
		if at := parseResetTimestamp(pointerValue(body, options.ResetAtPath)); at.After(evidence.ResetAt) {
			evidence.ResetAt = at
		}
		for _, path := range []string{options.RetryAfterPath, options.RetryDelayPath} {
			if at := parseRetryAfter(rawJSONText(pointerValue(body, path)), time.Now()); at.After(evidence.RetryAt) {
				evidence.RetryAt = at
			}
		}
		evidence.WindowName = pointerText(body, options.WindowNamePath)
		evidence.WindowKind = options.QuotaWindowKind
		evidence.Limit = parseJSONFloat(pointerValue(body, options.WindowLimitPath))
		evidence.Used = parseJSONFloat(pointerValue(body, options.WindowUsedPath))
		evidence.Remaining = parseJSONFloat(pointerValue(body, options.WindowRemainingPath))
		evidence.HasWindow = evidence.WindowName != "" || evidence.Limit != nil || evidence.Used != nil || evidence.Remaining != nil
	}
	evidence.QuotaExhausted = quotaExhausted
	for _, raw := range []json.RawMessage{details.ResetAt, details.ResetAtCamel, details.ResetsAt, details.ResetsAtCamel} {
		if at := parseResetTimestamp(raw); at.After(evidence.ResetAt) {
			evidence.ResetAt = at
		}
	}
	for _, raw := range []json.RawMessage{details.RetryAfter, details.RetryAfterCamel, details.RetryDelay, details.RetryDelayCamel} {
		if at := parseRetryAfter(rawJSONText(raw), time.Now()); at.After(evidence.RetryAt) {
			evidence.RetryAt = at
		}
	}
	for _, detail := range details.Details {
		for _, value := range []string{detail.RetryDelay, detail.RetryAfter} {
			if at := parseRetryAfter(value, time.Now()); at.After(evidence.RetryAt) {
				evidence.RetryAt = at
			}
		}
	}
	return evidence
}

func mergeHTTPErrorDetails(outer, inner httpErrorDetails) httpErrorDetails {
	if inner.Type == "" {
		inner.Type = outer.Type
	}
	if len(inner.Code) == 0 || string(inner.Code) == "null" {
		inner.Code = outer.Code
	}
	if inner.Status == "" {
		inner.Status = outer.Status
	}
	if inner.Message == "" {
		inner.Message = outer.Message
	}
	if len(inner.ResetAt) == 0 || string(inner.ResetAt) == "null" {
		inner.ResetAt = outer.ResetAt
	}
	if len(inner.ResetAtCamel) == 0 || string(inner.ResetAtCamel) == "null" {
		inner.ResetAtCamel = outer.ResetAtCamel
	}
	if len(inner.ResetsAt) == 0 || string(inner.ResetsAt) == "null" {
		inner.ResetsAt = outer.ResetsAt
	}
	if len(inner.ResetsAtCamel) == 0 || string(inner.ResetsAtCamel) == "null" {
		inner.ResetsAtCamel = outer.ResetsAtCamel
	}
	if len(inner.RetryAfter) == 0 || string(inner.RetryAfter) == "null" {
		inner.RetryAfter = outer.RetryAfter
	}
	if len(inner.RetryAfterCamel) == 0 || string(inner.RetryAfterCamel) == "null" {
		inner.RetryAfterCamel = outer.RetryAfterCamel
	}
	if len(inner.RetryDelay) == 0 || string(inner.RetryDelay) == "null" {
		inner.RetryDelay = outer.RetryDelay
	}
	if len(inner.RetryDelayCamel) == 0 || string(inner.RetryDelayCamel) == "null" {
		inner.RetryDelayCamel = outer.RetryDelayCamel
	}
	// `details` is a repeated evidence field (for example Google's
	// RetryInfo entries), not a scalar override. Keep both levels so a nested
	// provider error cannot erase a usable retry hint from its outer envelope.
	inner.Details = append(inner.Details, outer.Details...)
	return inner
}

func matchHTTPJSONScopeSignal(options *HTTPJSONErrorClassifierOptions, evidence httpErrorEvidence, cause kernel.OutcomeCause, body []byte) (kernel.OutcomeScope, bool) {
	if options == nil {
		return "", false
	}
	for _, signal := range options.ScopeSignals {
		if signal.Cause != "" && signal.Cause != cause {
			continue
		}
		if len(signal.Codes) > 0 && !matchesAny(evidence.Code, signal.Codes) {
			continue
		}
		if len(signal.Types) > 0 && !matchesAny(evidence.Type, signal.Types) {
			continue
		}
		if len(signal.Statuses) > 0 && !matchesAny(evidence.Status, signal.Statuses) {
			continue
		}
		if len(signal.MessageTokens) > 0 && !containsAny(evidence.Message, signal.MessageTokens...) {
			continue
		}
		fieldsMatch := true
		for _, field := range signal.Fields {
			value := pointerText(body, field.Path)
			if !matchesAny(value, field.Equals) && !matchesAnyContains(value, field.Contains) {
				fieldsMatch = false
				break
			}
		}
		if !fieldsMatch {
			continue
		}
		return signal.Scope, true
	}
	return "", false
}

func matchesAnyContains(value string, tokens []string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, token := range tokens {
		if strings.Contains(value, strings.ToLower(strings.TrimSpace(token))) {
			return true
		}
	}
	return false
}

func rawJSONText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	return strings.TrimSpace(string(raw))
}

func pointerValue(body []byte, pointer string) json.RawMessage {
	if pointer == "" || !strings.HasPrefix(pointer, "/") {
		return nil
	}
	current := json.RawMessage(body)
	for _, escaped := range strings.Split(pointer[1:], "/") {
		segment := strings.ReplaceAll(strings.ReplaceAll(escaped, "~1", "/"), "~0", "~")
		var object map[string]json.RawMessage
		if json.Unmarshal(current, &object) == nil {
			next, ok := object[segment]
			if !ok {
				return nil
			}
			current = next
			continue
		}
		var array []json.RawMessage
		if json.Unmarshal(current, &array) != nil {
			return nil
		}
		index, err := strconv.Atoi(segment)
		if err != nil || index < 0 || index >= len(array) {
			return nil
		}
		current = array[index]
	}
	return current
}

func pointerText(body []byte, pointer string) string {
	return rawJSONText(pointerValue(body, pointer))
}

func parseJSONFloat(raw json.RawMessage) *float64 {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	value, err := strconv.ParseFloat(rawJSONText(raw), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return nil
	}
	return &value
}

func matchesAny(value string, options []string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, option := range options {
		if value == strings.ToLower(strings.TrimSpace(option)) {
			return true
		}
	}
	return false
}

func parseResetTimestamp(raw json.RawMessage) time.Time {
	value := rawJSONText(raw)
	if value == "" {
		return time.Time{}
	}
	if at, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return at
	}
	if at, err := http.ParseTime(value); err == nil {
		return at
	}
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || seconds <= 0 {
		return time.Time{}
	}
	if seconds > 100000000000 {
		return time.UnixMilli(seconds)
	}
	return time.Unix(seconds, 0)
}

func containsAny(value string, tokens ...string) bool {
	for _, token := range tokens {
		if strings.Contains(value, token) {
			return true
		}
	}
	return false
}

func optionalTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func parseRetryAfter(value string, now time.Time) time.Time {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds > 0 {
			return now.Add(time.Duration(seconds) * time.Second)
		}
		return time.Time{}
	}
	if delay, err := time.ParseDuration(value); err == nil && delay > 0 {
		return now.Add(delay)
	}
	if at, err := http.ParseTime(value); err == nil && at.After(now) {
		return at
	}
	return time.Time{}
}

type StaticModelSource struct{ Models []ModelDescriptor }

func (StaticModelSource) ID() string            { return "static-models" }
func (s StaticModelSource) StaticCatalog() bool { return true }
func (s StaticModelSource) List(_ context.Context, _ ModelSourceInput) ([]ModelDescriptor, error) {
	return cloneModelDescriptors(s.Models)
}
func (s StaticModelSource) ListSnapshot(_ context.Context, _ ModelSourceInput) (ModelCatalogSnapshot, error) {
	models, err := cloneModelDescriptors(s.Models)
	return ModelCatalogSnapshot{Models: models, Complete: err == nil, Evidence: ModelCatalogProviderRegistry}, err
}

func cloneModelDescriptors(models []ModelDescriptor) ([]ModelDescriptor, error) {
	encoded, err := json.Marshal(models)
	if err != nil {
		return nil, fmt.Errorf("clone static model catalog: %w", err)
	}
	var cloned []ModelDescriptor
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		return nil, fmt.Errorf("decode cloned static model catalog: %w", err)
	}
	return cloned, nil
}

type OpenAIModelSource struct{}

// AnthropicModelSource implements the Anthropic cursor-based model catalog.
// Pagination is bounded so a malformed upstream cannot keep discovery alive.
type AnthropicModelSource struct{}

func (AnthropicModelSource) ID() string { return "anthropic-models" }
func (source AnthropicModelSource) Test(ctx context.Context, input ModelSourceInput) ([]ModelDescriptor, error) {
	snapshot, err := source.ListSnapshot(ctx, input)
	return snapshot.Models, err
}
func (source AnthropicModelSource) TestSnapshot(ctx context.Context, input ModelSourceInput) (ModelCatalogSnapshot, error) {
	return source.ListSnapshot(ctx, input)
}
func (source AnthropicModelSource) List(ctx context.Context, input ModelSourceInput) ([]ModelDescriptor, error) {
	snapshot, err := source.ListSnapshot(ctx, input)
	return snapshot.Models, err
}
func (AnthropicModelSource) ListSnapshot(ctx context.Context, input ModelSourceInput) (ModelCatalogSnapshot, error) {
	if input.URL == "" {
		return ModelCatalogSnapshot{}, fmt.Errorf("resolved model endpoint URL is required")
	}
	if err := input.Credential.ValidateRuntimeContext(); err != nil {
		return ModelCatalogSnapshot{}, err
	}
	client := input.Client
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	endpoint, err := url.Parse(input.URL)
	if err != nil {
		return ModelCatalogSnapshot{}, err
	}
	models := make([]ModelDescriptor, 0)
	seenModels, seenCursors := map[string]bool{}, map[string]bool{}
	cursor := ""
	for page := 0; page < 100; page++ {
		query := endpoint.Query()
		query.Set("limit", "1000")
		if cursor == "" {
			query.Del("after_id")
		} else {
			query.Set("after_id", cursor)
		}
		pageURL := *endpoint
		pageURL.RawQuery = query.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL.String(), nil)
		if err != nil {
			return ModelCatalogSnapshot{}, err
		}
		for key, values := range input.Headers {
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}
		req.Header.Set("Accept", "application/json")
		if err := kernel.ApplyCredentialPresentation(req.Header, input.Credential, input.Presentation); err != nil {
			return ModelCatalogSnapshot{}, err
		}
		req.Header.Set("anthropic-version", "2023-06-01")
		response, err := client.Do(req)
		if err != nil {
			return ModelCatalogSnapshot{}, err
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 8<<20))
		response.Body.Close()
		if readErr != nil {
			return ModelCatalogSnapshot{}, readErr
		}
		if response.StatusCode >= 400 {
			return ModelCatalogSnapshot{}, fmt.Errorf("models endpoint returned %s: %s", response.Status, strings.TrimSpace(string(body)))
		}
		snapshot, err := DecodeModelCatalogSnapshot(body)
		if err != nil {
			return ModelCatalogSnapshot{}, err
		}
		for _, model := range snapshot.Models {
			if !seenModels[model.ID] {
				seenModels[model.ID] = true
				models = append(models, model)
			}
		}
		var pageInfo struct {
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		if err := json.Unmarshal(body, &pageInfo); err != nil {
			return ModelCatalogSnapshot{}, err
		}
		if !pageInfo.HasMore {
			return ModelCatalogSnapshot{Models: models, Complete: true, Evidence: ModelCatalogConnectionObserved}, nil
		}
		if pageInfo.LastID == "" || seenCursors[pageInfo.LastID] {
			return ModelCatalogSnapshot{}, fmt.Errorf("anthropic model catalog has_more without a progressing last_id")
		}
		seenCursors[pageInfo.LastID] = true
		cursor = pageInfo.LastID
	}
	return ModelCatalogSnapshot{Models: models, Complete: false, Evidence: ModelCatalogConnectionObserved}, nil
}

func (OpenAIModelSource) ID() string { return "openai-models" }
func (source OpenAIModelSource) Test(ctx context.Context, input ModelSourceInput) ([]ModelDescriptor, error) {
	return source.List(ctx, input)
}
func (source OpenAIModelSource) TestSnapshot(ctx context.Context, input ModelSourceInput) (ModelCatalogSnapshot, error) {
	return source.ListSnapshot(ctx, input)
}
func (OpenAIModelSource) List(ctx context.Context, input ModelSourceInput) ([]ModelDescriptor, error) {
	snapshot, err := (OpenAIModelSource{}).ListSnapshot(ctx, input)
	return snapshot.Models, err
}
func (OpenAIModelSource) ListSnapshot(ctx context.Context, input ModelSourceInput) (ModelCatalogSnapshot, error) {
	if input.URL == "" {
		return ModelCatalogSnapshot{}, fmt.Errorf("resolved model endpoint URL is required")
	}
	if err := input.Credential.ValidateRuntimeContext(); err != nil {
		return ModelCatalogSnapshot{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, input.URL, nil)
	if err != nil {
		return ModelCatalogSnapshot{}, err
	}
	for key, values := range input.Headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	req.Header.Set("Accept", "application/json")
	if err := kernel.ApplyCredentialPresentation(req.Header, input.Credential, input.Presentation); err != nil {
		return ModelCatalogSnapshot{}, err
	}
	client := input.Client
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	response, err := client.Do(req)
	if err != nil {
		return ModelCatalogSnapshot{}, err
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
		return ModelCatalogSnapshot{}, fmt.Errorf("models endpoint returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return ModelCatalogSnapshot{}, err
	}
	return DecodeModelCatalogSnapshot(body)
}

func DecodeModelCatalog(data []byte) ([]ModelDescriptor, error) {
	snapshot, err := DecodeModelCatalogSnapshot(data)
	return snapshot.Models, err
}

func DecodeModelCatalogSnapshot(data []byte) (ModelCatalogSnapshot, error) {
	var payload any
	if err := json.Unmarshal(data, &payload); err != nil {
		return ModelCatalogSnapshot{}, err
	}
	var raw []any
	complete := true
	recognized := false
	switch value := payload.(type) {
	case []any:
		raw = value
		recognized = true
	case map[string]any:
		for _, key := range []string{"data", "models", "results"} {
			if candidate, ok := value[key].([]any); ok {
				raw = candidate
				recognized = true
				break
			}
		}
		if more, ok := value["has_more"].(bool); ok && more {
			complete = false
		}
		if more, ok := value["hasMore"].(bool); ok && more {
			complete = false
		}
		for _, key := range []string{"next_cursor", "nextCursor", "next_page_token", "nextPageToken"} {
			if next, ok := value[key].(string); ok && strings.TrimSpace(next) != "" {
				complete = false
			}
		}
	default:
		return ModelCatalogSnapshot{}, fmt.Errorf("model catalog payload must be a list or object containing data/models/results")
	}
	if !recognized {
		return ModelCatalogSnapshot{}, fmt.Errorf("model catalog object has no data, models or results array")
	}
	seen := map[string]bool{}
	result := make([]ModelDescriptor, 0, len(raw))
	for _, item := range raw {
		id := ""
		metadata := map[string]any{}
		if object, ok := item.(map[string]any); ok {
			metadata = object
			for _, key := range []string{"id", "name", "model"} {
				if value, ok := object[key].(string); ok && value != "" {
					id = value
					break
				}
			}
		} else if value, ok := item.(string); ok {
			id = value
		}
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		displayName := id
		if name, ok := metadata["display_name"].(string); ok && strings.TrimSpace(name) != "" {
			displayName = name
		}
		result = append(result, ModelDescriptor{ID: id, DisplayName: displayName, Metadata: metadata})
	}
	return ModelCatalogSnapshot{Models: result, Complete: complete, Evidence: ModelCatalogConnectionObserved}, nil
}

type NoopQuotaSource struct{}

func (NoopQuotaSource) ID() string { return "none" }
func (NoopQuotaSource) Fetch(_ context.Context, _ QuotaRequest) ([]quota.Snapshot, error) {
	return nil, nil
}

type HTTPJSONQuotaSource struct {
	Path       string
	WindowName string
	Options    HTTPJSONQuotaOptions
}

func (s HTTPJSONQuotaSource) ID() string { return "http-json-quota" }
func (s HTTPJSONQuotaSource) QuotaScope() QuotaScope {
	if s.Options.Scope == "" {
		return QuotaScopeModel
	}
	return s.Options.Scope
}

func quotaRouteForScope(route kernel.Route, scope QuotaScope) kernel.Route {
	if scope == QuotaScopeConnection {
		route.ExternalModel = ""
	}
	return route
}

func (s HTTPJSONQuotaSource) Fetch(ctx context.Context, input QuotaRequest) ([]quota.Snapshot, error) {
	if input.Endpoint == nil || input.Transport == nil {
		return nil, fmt.Errorf("quota operation requires endpoint and transport primitives")
	}
	if err := input.Credential.ValidateRuntimeContext(); err != nil {
		return nil, err
	}
	if err := s.Options.Validate(); err != nil {
		return nil, err
	}
	quotaRoute := quotaRouteForScope(input.Route, s.QuotaScope())
	if len(s.Options.Reports) == 0 {
		body, headers, err := s.fetchQuotaReport(ctx, input, input.EndpointOptions)
		if err != nil {
			return nil, err
		}
		var snapshots []quota.Snapshot
		if len(s.Options.Windows) > 0 {
			snapshots, err = DecodeQuotaJSONWithOptions(body, quotaRoute, input.WindowName, s.Options)
		} else if len(s.Options.HeaderWindows) == 0 {
			windowName := input.WindowName
			if windowName == "" {
				windowName = s.WindowName
			}
			snapshots, err = DecodeQuotaJSON(body, quotaRoute, windowName)
		}
		if err != nil {
			return nil, err
		}
		headerSnapshots, err := DecodeQuotaHeaders(headers, quotaRoute, s.Options.HeaderWindows, time.Now())
		if err != nil {
			return nil, err
		}
		if len(snapshots)+len(headerSnapshots) > 128 {
			return nil, fmt.Errorf("quota report exceeds 128 normalized windows")
		}
		seenNames := make(map[string]struct{}, len(snapshots)+len(headerSnapshots))
		for _, snapshot := range append(snapshots, headerSnapshots...) {
			key := strings.ToLower(snapshot.WindowName)
			if _, duplicate := seenNames[key]; duplicate {
				return nil, fmt.Errorf("quota report produced duplicate window name %q", snapshot.WindowName)
			}
			seenNames[key] = struct{}{}
		}
		snapshots = append(snapshots, headerSnapshots...)
		return snapshots, nil
	}

	var snapshots []quota.Snapshot
	var failures []error
	completedReports := 0
	for index, report := range s.Options.Reports {
		endpointOptions := mergeQuotaEndpointOptions(input.EndpointOptions, report.EndpointOptions)
		body, _, err := s.fetchQuotaReport(ctx, input, endpointOptions)
		if err == nil {
			reportOptions := s.Options
			reportOptions.Reports = nil
			reportOptions.FailureMode = ""
			reportOptions.Windows = report.Windows
			snapshotsForReport, decodeErr := DecodeQuotaJSONWithOptions(body, quotaRoute, input.WindowName, reportOptions)
			if decodeErr != nil {
				err = decodeErr
			} else {
				if len(snapshots)+len(snapshotsForReport) > 128 {
					return nil, fmt.Errorf("quota reports exceed 128 normalized windows")
				}
				completedReports++
				snapshots = append(snapshots, snapshotsForReport...)
			}
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("quota report %d: %w", index, err))
			if s.Options.FailureMode != QuotaReportsKeepAvailable {
				return nil, errors.Join(failures...)
			}
		}
	}
	if len(failures) > 0 && completedReports == 0 {
		return nil, errors.Join(failures...)
	}
	if input.ReportFailure != nil {
		for _, failure := range failures {
			input.ReportFailure(failure)
		}
	}
	seenNames := make(map[string]struct{}, len(snapshots))
	for _, snapshot := range snapshots {
		key := strings.ToLower(snapshot.WindowName)
		if _, duplicate := seenNames[key]; duplicate {
			return nil, fmt.Errorf("quota reports produced duplicate window name %q", snapshot.WindowName)
		}
		seenNames[key] = struct{}{}
	}
	return snapshots, nil
}

const maxQuotaReportBytes = 1 << 20

func (s HTTPJSONQuotaSource) fetchQuotaReport(ctx context.Context, input QuotaRequest, options kernel.EndpointOptions) ([]byte, http.Header, error) {
	path := s.Path
	if path == "" {
		path = "/usage"
	}
	endpointURL, err := input.Endpoint.Resolve(input.Route.BaseURL, path, options)
	if err != nil {
		return nil, nil, err
	}
	headers := make(http.Header)
	headers.Set("Accept", "application/json")
	for name, value := range input.Route.CredentialPresentation.Headers {
		headers.Set(name, value)
	}
	for name, value := range s.Options.RequestHeaders {
		headers.Set(name, value)
	}
	presentation := input.Route.CredentialPresentation
	if s.Options.CredentialHeader != "" {
		presentation.Header = s.Options.CredentialHeader
		presentation.Prefix = s.Options.CredentialPrefix
		if presentation.Prefix == "" && strings.EqualFold(presentation.Header, "Authorization") {
			presentation.Prefix = "Bearer "
		}
	}
	if err := kernel.ApplyCredentialPresentation(headers, input.Credential, presentation); err != nil {
		return nil, nil, err
	}
	response, err := input.Transport.Execute(ctx, kernel.UpstreamRequest{Method: http.MethodGet, URL: endpointURL, Headers: headers})
	if err != nil {
		return nil, nil, err
	}
	if response.Body == nil {
		return nil, nil, fmt.Errorf("quota endpoint returned no response body")
	}
	defer response.Body.Close()
	if response.Status >= 400 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 32*1024))
		return nil, nil, &QuotaHTTPError{Status: response.Status}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxQuotaReportBytes+1))
	if err != nil {
		return nil, nil, err
	}
	if len(body) > maxQuotaReportBytes {
		return nil, nil, fmt.Errorf("quota response exceeds %d-byte limit", maxQuotaReportBytes)
	}
	return body, response.Headers, nil
}

func DecodeQuotaHeaders(headers http.Header, route kernel.Route, mappings []HTTPHeaderQuotaWindow, now time.Time) ([]quota.Snapshot, error) {
	if len(mappings) > 64 {
		return nil, fmt.Errorf("quota source supports at most 64 header windows")
	}
	snapshots := make([]quota.Snapshot, 0, len(mappings))
	for _, mapping := range mappings {
		var limit, used, remaining *float64
		if value := headers.Get(mapping.LimitHeader); mapping.LimitHeader != "" && value != "" {
			limit = parseHeaderLimitValue(value)
		}
		if value := headers.Get(mapping.UsedHeader); mapping.UsedHeader != "" && value != "" {
			used = parseHeaderLimitValue(value)
		}
		if value := headers.Get(mapping.RemainingHeader); mapping.RemainingHeader != "" && value != "" {
			remaining = parseHeaderLimitValue(value)
		}
		var resetAt *time.Time
		if value := headers.Get(mapping.ResetHeader); mapping.ResetHeader != "" && value != "" {
			parsed := parseHeaderReset(value, now)
			if !parsed.IsZero() && parsed.After(now) {
				resetAt = &parsed
			}
		}
		if limit == nil && used == nil && remaining == nil && resetAt == nil {
			continue
		}
		snapshots = append(snapshots, quota.Snapshot{ProviderNodeID: route.NodeID, ConnectionID: route.CredentialID, ModelRef: route.ExternalModel, WindowName: mapping.Name, Used: valueOrZero(used), Limit: limit, Remaining: remaining, ResetAt: resetAt, Source: "http-header-quota", RoutingEffect: mapping.RoutingEffect})
	}
	return snapshots, nil
}

func mergeQuotaEndpointOptions(base, override kernel.EndpointOptions) kernel.EndpointOptions {
	result := kernel.EndpointOptions{Path: base.Path, Query: make(map[string]string, len(base.Query)+len(override.Query))}
	for key, value := range base.Query {
		result.Query[key] = value
	}
	if override.Path != "" {
		result.Path = override.Path
	}
	for key, value := range override.Query {
		result.Query[key] = value
	}
	return result
}

func DecodeQuotaJSON(data []byte, route kernel.Route, windowName string) ([]quota.Snapshot, error) {
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	if windowName == "" {
		windowName = stringValue(payload["window"])
		if windowName == "" {
			windowName = "default"
		}
	}
	used := numberValue(payload["used"])
	limit := numberValue(payload["limit"])
	remaining := numberValue(payload["remaining"])
	if value, ok := payload["usage"].(map[string]any); ok {
		used = firstNumber(value, used, "used", "consumed")
		limit = firstNumber(value, limit, "limit", "total")
		remaining = firstNumber(value, remaining, "remaining", "left")
	}
	var limitPtr, remainingPtr *float64
	if limit != nil {
		limitPtr = limit
	}
	if remaining != nil {
		remainingPtr = remaining
	}
	return []quota.Snapshot{{ProviderNodeID: route.NodeID, ConnectionID: route.CredentialID, ModelRef: route.ExternalModel, WindowName: windowName, Used: valueOrZero(used), Limit: limitPtr, Remaining: remainingPtr, Source: "http-json-quota"}}, nil
}

func DecodeQuotaJSONWithOptions(data []byte, route kernel.Route, defaultWindow string, options HTTPJSONQuotaOptions) ([]quota.Snapshot, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	var snapshots []quota.Snapshot
	seenWindows := make(map[string]struct{})
	for _, mapping := range options.Windows {
		rows, err := quotaWindowRows(data, mapping)
		if err != nil {
			return nil, fmt.Errorf("decode quota window %q rows: %w", mapping.Name, err)
		}
		if mapping.Aggregation == "any_positive" {
			if len(rows) == 0 {
				continue
			}
			available := false
			for _, row := range rows {
				value := parseJSONFloat(pointerValue(row.raw, mapping.AggregationValuePath))
				if value != nil && *value > 0 {
					available = true
					break
				}
			}
			if mapping.AggregationStatusPath != "" && !matchesAny(pointerText(data, mapping.AggregationStatusPath), mapping.AggregationStatusValues) {
				available = false
			}
			used, remaining := 1.0, 0.0
			if available {
				used, remaining = 0, 1
			}
			if _, duplicate := seenWindows[strings.ToLower(mapping.Name)]; duplicate {
				return nil, fmt.Errorf("quota report contains duplicate window name %q", mapping.Name)
			}
			seenWindows[strings.ToLower(mapping.Name)] = struct{}{}
			if len(snapshots) >= 128 {
				return nil, fmt.Errorf("quota report exceeds 128 normalized windows")
			}
			snapshots = append(snapshots, quota.Snapshot{
				ProviderNodeID: route.NodeID, ConnectionID: route.CredentialID,
				ModelRef: route.ExternalModel, WindowName: mapping.Name, Used: used,
				Limit: floatPointer(1), Remaining: floatPointer(remaining), Source: "http-json-quota", RoutingEffect: mapping.RoutingEffect,
			})
			continue
		}
		for _, row := range rows {
			if len(mapping.IgnoreWhenValues) > 0 && matchesAny(pointerText(row.raw, mapping.IgnoreWhenPath), mapping.IgnoreWhenValues) {
				continue
			}
			name := mapping.Name
			if mapping.NamePath != "" {
				name = pointerText(row.raw, mapping.NamePath)
			}
			if name == "" {
				name = row.key
			}
			if name == "" && mapping.ItemsPath != "" {
				return nil, fmt.Errorf("quota itemsPath row has no name; configure namePath for array results")
			}
			if name == "" {
				name = defaultWindow
			}
			if name == "" {
				return nil, fmt.Errorf("quota window mapping produced an empty name")
			}
			used := parseJSONFloat(pointerValue(row.raw, mapping.UsedPath))
			limit := parseJSONFloat(pointerValue(row.raw, mapping.LimitPath))
			remaining := parseJSONFloat(pointerValue(row.raw, mapping.RemainingPath))
			if mapping.RemainingFromLimitUsed && used != nil && limit != nil {
				remaining = floatPointer(*limit - *used)
			}
			if mapping.UsedPercentPath != "" {
				if percent := parseJSONFloat(pointerValue(row.raw, mapping.UsedPercentPath)); percent != nil {
					bounded := math.Max(0, math.Min(100, *percent))
					limit = floatPointer(100)
					used = floatPointer(bounded)
					remaining = floatPointer(100 - bounded)
				}
			}
			if mapping.RemainingPercentPath != "" {
				if percent := parseJSONFloat(pointerValue(row.raw, mapping.RemainingPercentPath)); percent != nil {
					bounded := math.Max(0, math.Min(100, *percent))
					limit = floatPointer(100)
					remaining = floatPointer(bounded)
					used = floatPointer(100 - bounded)
				}
			}
			if len(mapping.ExhaustedStatusValues) > 0 && matchesAny(pointerText(row.raw, mapping.ExhaustedStatusPath), mapping.ExhaustedStatusValues) {
				remaining = floatPointer(0)
			}
			var resetAt *time.Time
			if mapping.ResetAtPath != "" {
				if value := pointerValue(row.raw, mapping.ResetAtPath); len(value) > 0 {
					reset := parseHeaderReset(rawJSONText(value), time.Now())
					if !reset.IsZero() {
						resetAt = &reset
					}
				}
			}
			if used == nil && limit == nil && remaining == nil && resetAt == nil {
				continue
			}
			nameKey := strings.ToLower(name)
			if _, duplicate := seenWindows[nameKey]; duplicate {
				return nil, fmt.Errorf("quota report contains duplicate window name %q", name)
			}
			seenWindows[nameKey] = struct{}{}
			if len(snapshots) >= 128 {
				return nil, fmt.Errorf("quota report exceeds 128 normalized windows")
			}
			snapshots = append(snapshots, quota.Snapshot{
				ProviderNodeID: route.NodeID, ConnectionID: route.CredentialID,
				ModelRef: route.ExternalModel, WindowName: name, Used: valueOrZero(used),
				Limit: limit, Remaining: remaining, ResetAt: resetAt, Source: "http-json-quota", RoutingEffect: mapping.RoutingEffect,
			})
		}
	}
	return snapshots, nil
}

type quotaWindowRow struct {
	key string
	raw json.RawMessage
}

func quotaWindowRows(data []byte, mapping HTTPJSONQuotaWindow) ([]quotaWindowRow, error) {
	if mapping.ItemsPath == "" {
		return []quotaWindowRow{{key: mapping.Name, raw: append(json.RawMessage(nil), data...)}}, nil
	}
	items := pointerValue(data, mapping.ItemsPath)
	if len(items) == 0 || string(items) == "null" {
		return nil, nil
	}
	var array []json.RawMessage
	if json.Unmarshal(items, &array) == nil {
		rows := make([]quotaWindowRow, 0, len(array))
		for _, item := range array {
			rows = append(rows, quotaWindowRow{raw: item})
		}
		return rows, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(items, &object); err != nil {
		return nil, fmt.Errorf("itemsPath must select an object or array")
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	rows := make([]quotaWindowRow, 0, len(keys))
	for _, key := range keys {
		rows = append(rows, quotaWindowRow{key: key, raw: object[key]})
	}
	return rows, nil
}

func floatPointer(value float64) *float64 { return &value }

func firstNumber(values map[string]any, fallback *float64, keys ...string) *float64 {
	for _, key := range keys {
		if value := numberValue(values[key]); value != nil {
			return value
		}
	}
	return fallback
}

func numberValue(value any) *float64 {
	var number float64
	switch v := value.(type) {
	case float64:
		number = v
	case int:
		number = float64(v)
	case int64:
		number = float64(v)
	default:
		return nil
	}
	return &number
}

func valueOrZero(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

func stringValue(value any) string {
	result, _ := value.(string)
	return result
}
