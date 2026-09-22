package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/quota"
)

type ModelDescriptor struct {
	ID           string
	DisplayName  string
	Capabilities map[string]bool
	Metadata     map[string]any
}

type ModelSourceInput struct {
	BaseURL    string
	ModelsPath string
	Credential kernel.Credential
	Client     *http.Client
	Headers    http.Header
}

type ModelSource interface {
	ID() string
	List(context.Context, ModelSourceInput) ([]ModelDescriptor, error)
}

type QuotaSource interface {
	ID() string
	Fetch(context.Context, kernel.Credential, kernel.Route) ([]quota.Snapshot, error)
}

type ErrorClassifier interface {
	ID() string
	Classify(status int, body []byte) kernel.ErrorClass
}

type HTTPJSONErrorClassifier struct{}

func (HTTPJSONErrorClassifier) ID() string { return "http-json" }
func (HTTPJSONErrorClassifier) Classify(status int, body []byte) kernel.ErrorClass {
	message := strings.ToLower(string(body))
	switch {
	case status == 401 || status == 403:
		return kernel.ErrorAuth
	case strings.Contains(message, "invalid api key"), strings.Contains(message, "unauthorized"), strings.Contains(message, "authentication"):
		return kernel.ErrorAuth
	case status == 408 || status == 409 || status == 429 || status >= 500, strings.Contains(message, "rate limit"), strings.Contains(message, "quota"):
		return kernel.ErrorCooldown
	case status >= 400:
		return kernel.ErrorTerminal
	default:
		return kernel.ErrorRetryable
	}
}

// ClassifyOutcome adds the provider-neutral evidence that the legacy class
// classifier cannot carry. Providers may replace this primitive in a manifest
// without touching the kernel.
func (HTTPJSONErrorClassifier) ClassifyOutcome(status int, headers http.Header, body []byte) kernel.ClassifiedOutcome {
	message := strings.TrimSpace(string(body))
	outcome := kernel.ClassifiedOutcome{Class: HTTPJSONErrorClassifier{}.Classify(status, body), Scope: kernel.ScopeRoute, Confidence: 0.7, Evidence: []kernel.EvidenceSource{kernel.EvidenceErrorBody}, StatusCode: status, Message: message}
	switch {
	case status == 401:
		outcome.Cause, outcome.Retry = kernel.CauseAuth, kernel.RetryNever
	case status == 403:
		outcome.Cause, outcome.Retry = kernel.CausePermission, kernel.RetryNever
	case status == 404:
		outcome.Cause, outcome.Retry = kernel.CauseModelNotFound, kernel.RetryNever
	case status == 429:
		outcome.Cause, outcome.Retry = kernel.CauseRateLimited, kernel.RetryAfter
	case status >= 500:
		outcome.Cause, outcome.Retry = kernel.CauseCapacity, kernel.RetryAfter
	case status >= 400:
		outcome.Cause, outcome.Retry = kernel.CauseRequestInvalid, kernel.RetryNever
	default:
		outcome.Cause, outcome.Retry, outcome.Confidence = kernel.CauseSuccess, kernel.RetryNow, 1
	}
	if value := headers.Get("Retry-After"); value != "" {
		if seconds, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && seconds > 0 {
			outcome.RetryAt = time.Now().Add(time.Duration(seconds) * time.Second)
		}
		outcome.Evidence = append(outcome.Evidence, kernel.EvidenceResponseHeader)
	}
	if remaining, err := strconv.ParseFloat(headers.Get("X-RateLimit-Remaining"), 64); err == nil {
		window := kernel.LimitWindow{Name: "rate_limit", Kind: "requests", Remaining: &remaining, Source: kernel.EvidenceResponseHeader}
		if reset, err := strconv.ParseInt(headers.Get("X-RateLimit-Reset"), 10, 64); err == nil && reset > 0 {
			at := time.Unix(reset, 0)
			window.ResetAt = &at
		}
		outcome.Limits = append(outcome.Limits, window)
		outcome.Evidence = append(outcome.Evidence, kernel.EvidenceResponseHeader)
	}
	return outcome
}

type StaticModelSource struct{}

func (StaticModelSource) ID() string { return "static-models" }
func (StaticModelSource) List(_ context.Context, _ ModelSourceInput) ([]ModelDescriptor, error) {
	return nil, nil
}

type OpenAIModelSource struct{}

func (OpenAIModelSource) ID() string { return "openai-models" }
func (OpenAIModelSource) List(ctx context.Context, input ModelSourceInput) ([]ModelDescriptor, error) {
	if input.BaseURL == "" {
		return nil, fmt.Errorf("model source base URL is required")
	}
	path := input.ModelsPath
	if path == "" {
		path = "/models"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	url := strings.TrimRight(input.BaseURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
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
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
		return nil, fmt.Errorf("models endpoint returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	return DecodeModelCatalog(body)
}

func DecodeModelCatalog(data []byte) ([]ModelDescriptor, error) {
	var payload any
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	var raw []any
	switch value := payload.(type) {
	case []any:
		raw = value
	case map[string]any:
		for _, key := range []string{"data", "models", "results"} {
			if candidate, ok := value[key].([]any); ok {
				raw = candidate
				break
			}
		}
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
		result = append(result, ModelDescriptor{ID: id, DisplayName: id, Metadata: metadata})
	}
	return result, nil
}

type NoopQuotaSource struct{}

func (NoopQuotaSource) ID() string { return "none" }
func (NoopQuotaSource) Fetch(_ context.Context, _ kernel.Credential, _ kernel.Route) ([]quota.Snapshot, error) {
	return nil, nil
}

type HTTPJSONQuotaSource struct {
	Path       string
	WindowName string
}

func (s HTTPJSONQuotaSource) ID() string { return "http-json-quota" }
func (s HTTPJSONQuotaSource) Fetch(ctx context.Context, credential kernel.Credential, route kernel.Route) ([]quota.Snapshot, error) {
	path := s.Path
	if path == "" {
		path = "/usage"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	url := strings.TrimRight(route.BaseURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if credential.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+credential.Secret)
	}
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 32*1024))
		return nil, fmt.Errorf("quota endpoint returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	return DecodeQuotaJSON(body, route, s.WindowName)
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
