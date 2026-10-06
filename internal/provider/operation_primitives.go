package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/quota"
)

type ModelDescriptor struct {
	ID          string
	DisplayName string
	Profile     kernel.CapabilityProfile
	Metadata    map[string]any
}

// ModelCatalogSnapshot states whether the returned list is authoritative for
// absence decisions. Incomplete responses may establish positive presence but
// must never mark omitted IDs as unavailable.
type ModelCatalogSnapshot struct {
	Models   []ModelDescriptor
	Complete bool
}

type ModelSourceInput struct {
	URL        string
	Credential kernel.Credential
	Client     *http.Client
	Headers    http.Header
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

type QuotaRequest struct {
	Credential      kernel.Credential
	Route           kernel.Route
	Endpoint        kernel.Endpoint
	Transport       kernel.Transport
	EndpointOptions kernel.EndpointOptions
	WindowName      string
}

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
	copy := *options.HTTPJSON
	return HTTPJSONErrorClassifier{Options: &copy}, nil
}
func (c HTTPJSONErrorClassifier) Classify(status int, body []byte) kernel.ErrorClass {
	message := strings.ToLower(string(body))
	quotaEvidence := decodeHTTPErrorEvidence(body, c.Options).QuotaExhausted
	switch {
	case status == 401 || status == 403:
		return kernel.ErrorAuth
	case strings.Contains(message, "invalid api key"), strings.Contains(message, "unauthorized"), strings.Contains(message, "authentication"):
		return kernel.ErrorAuth
	case status == 408 || status == 409 || status == 429 || status >= 500, strings.Contains(message, "rate limit"), status == http.StatusPaymentRequired && quotaEvidence:
		return kernel.ErrorCooldown
	case status >= 400:
		return kernel.ErrorTerminal
	default:
		return kernel.ErrorRetryable
	}
}

// ClassifyOutcome adds provider-neutral evidence that the coarse class
// classifier cannot carry. Providers may replace this primitive in a manifest
// without touching the kernel.
func (c HTTPJSONErrorClassifier) ClassifyOutcome(status int, headers http.Header, body []byte) kernel.ClassifiedOutcome {
	message := strings.TrimSpace(string(body))
	scope := kernel.ScopeRoute
	if c.Options != nil && c.Options.DefaultScope != "" {
		scope = c.Options.DefaultScope
	}
	outcome := kernel.ClassifiedOutcome{Class: c.Classify(status, body), Scope: scope, Confidence: 0.7, Evidence: []kernel.EvidenceSource{kernel.EvidenceErrorBody}, StatusCode: status, Message: message}
	errorEvidence := decodeHTTPErrorEvidence(body, c.Options)
	switch {
	case status == 401:
		outcome.Cause, outcome.Retry = kernel.CauseAuth, kernel.RetryNever
	case status == 403:
		outcome.Cause, outcome.Retry = kernel.CausePermission, kernel.RetryNever
	case status == 404:
		outcome.Cause, outcome.Retry = kernel.CauseModelNotFound, kernel.RetryNever
	case (status == http.StatusTooManyRequests || status == http.StatusPaymentRequired) && errorEvidence.QuotaExhausted:
		outcome.Cause, outcome.Retry = kernel.CauseQuotaExhausted, kernel.RetryAfter
	case status == http.StatusTooManyRequests:
		outcome.Cause, outcome.Retry = kernel.CauseRateLimited, kernel.RetryAfter
	case status >= 500:
		outcome.Cause, outcome.Retry = kernel.CauseCapacity, kernel.RetryAfter
	case status >= 400:
		outcome.Cause, outcome.Retry = kernel.CauseRequestInvalid, kernel.RetryNever
	default:
		outcome.Cause, outcome.Retry, outcome.Confidence = kernel.CauseSuccess, kernel.RetryNow, 1
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
		case kernel.CauseCapacity:
			if c.Options.CapacityScope != "" {
				outcome.Scope = c.Options.CapacityScope
			}
		}
	}
	if !errorEvidence.ResetAt.IsZero() {
		outcome.RetryAt = errorEvidence.ResetAt
	} else if value := headers.Get("Retry-After"); value != "" {
		outcome.RetryAt = parseRetryAfter(value, time.Now())
		outcome.Evidence = append(outcome.Evidence, kernel.EvidenceResponseHeader)
	}
	if remaining, err := strconv.ParseFloat(headers.Get("X-RateLimit-Remaining"), 64); err == nil {
		window := kernel.LimitWindow{Name: "rate_limit", Kind: "requests", Remaining: &remaining, Source: kernel.EvidenceResponseHeader}
		if at := parseResetTimestamp(json.RawMessage(headers.Get("X-RateLimit-Reset"))); !at.IsZero() {
			window.ResetAt = &at
		}
		outcome.Limits = append(outcome.Limits, window)
		outcome.Evidence = append(outcome.Evidence, kernel.EvidenceResponseHeader)
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
	if outcome.RetryAt.IsZero() && !errorEvidence.RetryAt.IsZero() {
		outcome.RetryAt = errorEvidence.RetryAt
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
	details := httpErrorDetails{Type: envelope.Type, Code: envelope.Code, Status: envelope.Status, Message: envelope.Message}
	if len(envelope.Error) > 0 && string(envelope.Error) != "null" {
		var object httpErrorDetails
		if err := json.Unmarshal(envelope.Error, &object); err == nil {
			details = object
		} else {
			var message string
			if json.Unmarshal(envelope.Error, &message) != nil {
				return httpErrorEvidence{}
			}
			details.Message = message
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
	evidence := httpErrorEvidence{QuotaExhausted: quotaExhausted}
	if options != nil {
		customCode := pointerText(body, options.CodePath)
		customType := pointerText(body, options.TypePath)
		customStatus := pointerText(body, options.StatusPath)
		customMessage := pointerText(body, options.MessagePath)
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

type StaticModelSource struct{}

func (StaticModelSource) ID() string { return "static-models" }
func (StaticModelSource) List(_ context.Context, _ ModelSourceInput) ([]ModelDescriptor, error) {
	return nil, nil
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
		req.Header.Set("x-api-key", input.Credential.Secret)
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
			return ModelCatalogSnapshot{Models: models, Complete: true}, nil
		}
		if pageInfo.LastID == "" || seenCursors[pageInfo.LastID] {
			return ModelCatalogSnapshot{}, fmt.Errorf("anthropic model catalog has_more without a progressing last_id")
		}
		seenCursors[pageInfo.LastID] = true
		cursor = pageInfo.LastID
	}
	return ModelCatalogSnapshot{Models: models, Complete: false}, nil
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
	if input.Credential.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+input.Credential.Secret)
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
	return ModelCatalogSnapshot{Models: result, Complete: complete}, nil
}

type NoopQuotaSource struct{}

func (NoopQuotaSource) ID() string { return "none" }
func (NoopQuotaSource) Fetch(_ context.Context, _ QuotaRequest) ([]quota.Snapshot, error) {
	return nil, nil
}

type HTTPJSONQuotaSource struct {
	Path       string
	WindowName string
}

func (s HTTPJSONQuotaSource) ID() string { return "http-json-quota" }
func (s HTTPJSONQuotaSource) Fetch(ctx context.Context, input QuotaRequest) ([]quota.Snapshot, error) {
	if input.Endpoint == nil || input.Transport == nil {
		return nil, fmt.Errorf("quota operation requires endpoint and transport primitives")
	}
	path := s.Path
	if path == "" {
		path = "/usage"
	}
	url, err := input.Endpoint.Resolve(input.Route.BaseURL, path, input.EndpointOptions)
	if err != nil {
		return nil, err
	}
	headers := make(http.Header)
	headers.Set("Accept", "application/json")
	if input.Credential.Secret != "" {
		headers.Set("Authorization", "Bearer "+input.Credential.Secret)
	}
	response, err := input.Transport.Execute(ctx, kernel.UpstreamRequest{Method: http.MethodGet, URL: url, Headers: headers})
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.Status >= 400 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 32*1024))
		return nil, fmt.Errorf("quota endpoint returned %s: %s", http.StatusText(response.Status), strings.TrimSpace(string(body)))
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	windowName := input.WindowName
	if windowName == "" {
		windowName = s.WindowName
	}
	return DecodeQuotaJSON(body, input.Route, windowName)
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
