package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/fm39hz/gobroom/internal/adapter/anthropic"
	"github.com/fm39hz/gobroom/internal/adapter/gemini"
	openai "github.com/fm39hz/gobroom/internal/adapter/openai"
	egress "github.com/fm39hz/gobroom/internal/adapter/renderers"
	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/operations"
	"github.com/fm39hz/gobroom/internal/quota"
)

type Operation string

func ProviderDefinitionRef(id string, contractVersion uint64) extensions.Ref {
	return extensions.Ref{Kind: "provider-definition", ID: id, ContractVersion: contractVersion}
}

func OperationRef(task normalize.Operation, contractVersion uint64) *extensions.Ref {
	return &extensions.Ref{Kind: "operation", ID: string(task), ContractVersion: contractVersion}
}

const (
	OperationChat       Operation = "chat"
	OperationResponses  Operation = "responses"
	OperationMessages   Operation = "messages"
	OperationModels     Operation = "models"
	OperationEmbeddings Operation = "embeddings"
	OperationImage      Operation = "image"
	OperationAudio      Operation = "audio"
	OperationVideo      Operation = "video"
	OperationQuota      Operation = "quota"
)

type PrimitiveKind string

const (
	PrimitiveEndpoint        PrimitiveKind = "endpoint"
	PrimitiveTransport       PrimitiveKind = "transport"
	PrimitiveAuth            PrimitiveKind = "auth"
	PrimitiveRequestCodec    PrimitiveKind = "request_codec"
	PrimitiveResponseDecoder PrimitiveKind = "response_decoder"
	PrimitiveModelSource     PrimitiveKind = "model_source"
	PrimitiveUsageSource     PrimitiveKind = "usage_source"
	PrimitiveQuotaSource     PrimitiveKind = "quota_source"
	PrimitiveSessionStore    PrimitiveKind = "session_store"
	PrimitiveErrorClassifier PrimitiveKind = "error_classifier"
	PrimitiveExtension       PrimitiveKind = "extension"
)

type PrimitiveRef struct {
	Kind            PrimitiveKind `json:"kind"`
	ID              string        `json:"id"`
	ContractVersion uint64        `json:"contractVersion"`
}

// ErrorClassifierOptions configure a shared classifier for a provider's
// upstream wire shape. Options remain declarative data; the kernel only sees
// ClassifiedOutcome values.
type ErrorClassifierOptions struct {
	HTTPJSON *HTTPJSONErrorClassifierOptions `json:"httpJson,omitempty"`
}

type HTTPJSONErrorClassifierOptions struct {
	DefaultScope        kernel.OutcomeScope     `json:"defaultScope,omitempty"`
	AuthScope           kernel.OutcomeScope     `json:"authScope,omitempty"`
	QuotaScope          kernel.OutcomeScope     `json:"quotaScope,omitempty"`
	RateLimitScope      kernel.OutcomeScope     `json:"rateLimitScope,omitempty"`
	CapacityScope       kernel.OutcomeScope     `json:"capacityScope,omitempty"`
	CodePath            string                  `json:"codePath,omitempty"`
	TypePath            string                  `json:"typePath,omitempty"`
	StatusPath          string                  `json:"statusPath,omitempty"`
	MessagePath         string                  `json:"messagePath,omitempty"`
	ResetAtPath         string                  `json:"resetAtPath,omitempty"`
	RetryAfterPath      string                  `json:"retryAfterPath,omitempty"`
	RetryDelayPath      string                  `json:"retryDelayPath,omitempty"`
	WindowNamePath      string                  `json:"windowNamePath,omitempty"`
	WindowLimitPath     string                  `json:"windowLimitPath,omitempty"`
	WindowUsedPath      string                  `json:"windowUsedPath,omitempty"`
	WindowRemainingPath string                  `json:"windowRemainingPath,omitempty"`
	QuotaWindowName     string                  `json:"quotaWindowName,omitempty"`
	QuotaWindowKind     string                  `json:"quotaWindowKind,omitempty"`
	QuotaCodes          []string                `json:"quotaCodes,omitempty"`
	QuotaTypes          []string                `json:"quotaTypes,omitempty"`
	QuotaStatuses       []string                `json:"quotaStatuses,omitempty"`
	QuotaHTTPStatuses   []int                   `json:"quotaHTTPStatuses,omitempty"`
	QuotaMessageTokens  []string                `json:"quotaMessageTokens,omitempty"`
	ScopeSignals        []HTTPJSONScopeSignal   `json:"scopeSignals,omitempty"`
	LimitHeaders        []HTTPHeaderLimitSignal `json:"limitHeaders,omitempty"`
}

// HTTPHeaderLimitSignal extracts one independent limit window from response
// headers. ResetHeader accepts an absolute timestamp or a relative duration.
type HTTPHeaderLimitSignal struct {
	Name                  string   `json:"name"`
	Kind                  string   `json:"kind,omitempty"`
	LimitHeader           string   `json:"limitHeader,omitempty"`
	UsedHeader            string   `json:"usedHeader,omitempty"`
	RemainingHeader       string   `json:"remainingHeader,omitempty"`
	ExhaustedStatusHeader string   `json:"exhaustedStatusHeader,omitempty"`
	ExhaustedStatusValues []string `json:"exhaustedStatusValues,omitempty"`
	ResetHeader           string   `json:"resetHeader,omitempty"`
}

// HTTPJSONScopeSignal maps an exact structured error pattern to a routing
// scope. Signals are checked in declaration order; the first match wins.
type HTTPJSONScopeSignal struct {
	Cause         kernel.OutcomeCause  `json:"cause,omitempty"`
	Codes         []string             `json:"codes,omitempty"`
	Types         []string             `json:"types,omitempty"`
	Statuses      []string             `json:"statuses,omitempty"`
	MessageTokens []string             `json:"messageTokens,omitempty"`
	Fields        []HTTPJSONFieldMatch `json:"fields,omitempty"`
	Scope         kernel.OutcomeScope  `json:"scope"`
}

// HTTPJSONFieldMatch matches a provider-specific JSON Pointer field. Values
// within a matcher are alternatives; separate matchers on one scope signal
// are conjunctive. Matching is case-insensitive after trimming whitespace.
type HTTPJSONFieldMatch struct {
	Path     string   `json:"path"`
	Equals   []string `json:"equals,omitempty"`
	Contains []string `json:"contains,omitempty"`
}

func (o ErrorClassifierOptions) Empty() bool {
	return o.HTTPJSON == nil || o.HTTPJSON.Empty()
}

func (o HTTPJSONErrorClassifierOptions) Empty() bool {
	return o.DefaultScope == "" && o.AuthScope == "" && o.QuotaScope == "" && o.RateLimitScope == "" && o.CapacityScope == "" && o.CodePath == "" && o.TypePath == "" && o.StatusPath == "" && o.MessagePath == "" && o.ResetAtPath == "" && o.RetryAfterPath == "" && o.RetryDelayPath == "" && o.WindowNamePath == "" && o.WindowLimitPath == "" && o.WindowUsedPath == "" && o.WindowRemainingPath == "" && o.QuotaWindowName == "" && o.QuotaWindowKind == "" && len(o.QuotaCodes) == 0 && len(o.QuotaTypes) == 0 && len(o.QuotaStatuses) == 0 && len(o.QuotaHTTPStatuses) == 0 && len(o.QuotaMessageTokens) == 0 && len(o.ScopeSignals) == 0 && len(o.LimitHeaders) == 0
}

func (o HTTPJSONErrorClassifierOptions) Validate() error {
	for name, scope := range map[string]kernel.OutcomeScope{
		"defaultScope": o.DefaultScope, "authScope": o.AuthScope, "quotaScope": o.QuotaScope,
		"rateLimitScope": o.RateLimitScope, "capacityScope": o.CapacityScope,
	} {
		if scope != "" && !validOutcomeScope(scope) {
			return fmt.Errorf("error classifier %s has unsupported scope %q", name, scope)
		}
	}
	for _, item := range []struct{ name, path string }{
		{"codePath", o.CodePath}, {"typePath", o.TypePath}, {"statusPath", o.StatusPath},
		{"messagePath", o.MessagePath}, {"resetAtPath", o.ResetAtPath},
		{"retryAfterPath", o.RetryAfterPath}, {"retryDelayPath", o.RetryDelayPath},
		{"windowNamePath", o.WindowNamePath}, {"windowLimitPath", o.WindowLimitPath},
		{"windowUsedPath", o.WindowUsedPath}, {"windowRemainingPath", o.WindowRemainingPath},
	} {
		if item.path != "" && !validJSONPointer(item.path) {
			return fmt.Errorf("error classifier %s must be a valid absolute JSON pointer", item.name)
		}
	}
	for _, values := range [][]string{o.QuotaCodes, o.QuotaTypes, o.QuotaStatuses, o.QuotaMessageTokens} {
		for _, value := range values {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("error classifier match values must not be empty")
			}
		}
	}
	seenLimitNames := make(map[string]struct{}, len(o.LimitHeaders))
	for index, signal := range o.LimitHeaders {
		if strings.TrimSpace(signal.Name) == "" {
			return fmt.Errorf("error classifier limit header signal %d requires a window name", index)
		}
		if signal.Name != strings.TrimSpace(signal.Name) {
			return fmt.Errorf("error classifier limit header signal %d window name must be trimmed", index)
		}
		windowKey := strings.ToLower(signal.Name)
		if _, duplicate := seenLimitNames[windowKey]; duplicate {
			return fmt.Errorf("error classifier limit header window %q is duplicated", signal.Name)
		}
		seenLimitNames[windowKey] = struct{}{}
		configured := 0
		if (signal.ExhaustedStatusHeader == "") != (len(signal.ExhaustedStatusValues) == 0) {
			return fmt.Errorf("error classifier limit header window %q must configure both exhausted status header and values", signal.Name)
		}
		for _, item := range []struct{ field, name string }{
			{"limitHeader", signal.LimitHeader}, {"usedHeader", signal.UsedHeader},
			{"remainingHeader", signal.RemainingHeader}, {"exhaustedStatusHeader", signal.ExhaustedStatusHeader}, {"resetHeader", signal.ResetHeader},
		} {
			if item.name == "" {
				continue
			}
			if !validHTTPHeaderName(item.name) {
				return fmt.Errorf("error classifier limit header signal %q has invalid %s", signal.Name, item.field)
			}
			configured++
		}
		for _, value := range signal.ExhaustedStatusValues {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("error classifier limit header window %q has an empty exhausted status value", signal.Name)
			}
		}
		if configured == 0 {
			return fmt.Errorf("error classifier limit header signal %q has no headers", signal.Name)
		}
	}
	for index, signal := range o.ScopeSignals {
		if !validOutcomeScope(signal.Scope) {
			return fmt.Errorf("error classifier scope signal %d has unsupported scope %q", index, signal.Scope)
		}
		if signal.Cause != "" && !validOutcomeCause(signal.Cause) {
			return fmt.Errorf("error classifier scope signal %d has unsupported cause %q", index, signal.Cause)
		}
		if len(signal.Codes)+len(signal.Types)+len(signal.Statuses)+len(signal.MessageTokens)+len(signal.Fields) == 0 {
			return fmt.Errorf("error classifier scope signal %d has no matching evidence", index)
		}
		for _, values := range [][]string{signal.Codes, signal.Types, signal.Statuses, signal.MessageTokens} {
			for _, value := range values {
				if strings.TrimSpace(value) == "" {
					return fmt.Errorf("error classifier scope signal %d has an empty matcher", index)
				}
			}
		}
		for fieldIndex, field := range signal.Fields {
			if !validJSONPointer(field.Path) {
				return fmt.Errorf("error classifier scope signal %d field matcher %d must have a valid absolute JSON pointer", index, fieldIndex)
			}
			if len(field.Equals)+len(field.Contains) == 0 {
				return fmt.Errorf("error classifier scope signal %d field matcher %d has no values", index, fieldIndex)
			}
			for _, values := range [][]string{field.Equals, field.Contains} {
				for _, value := range values {
					if strings.TrimSpace(value) == "" {
						return fmt.Errorf("error classifier scope signal %d field matcher %d has an empty value", index, fieldIndex)
					}
				}
			}
		}
	}
	seenQuotaHTTPStatuses := make(map[int]struct{}, len(o.QuotaHTTPStatuses))
	for _, status := range o.QuotaHTTPStatuses {
		if status < 400 || status > 599 {
			return fmt.Errorf("error classifier quota HTTP statuses must be between 400 and 599, got %d", status)
		}
		if _, duplicate := seenQuotaHTTPStatuses[status]; duplicate {
			return fmt.Errorf("error classifier quota HTTP status %d is duplicated", status)
		}
		seenQuotaHTTPStatuses[status] = struct{}{}
	}
	return nil
}

func validHTTPHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for index := 0; index < len(name); index++ {
		character := name[index]
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' {
			continue
		}
		switch character {
		case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		default:
			return false
		}
	}
	return true
}

func validOutcomeCause(cause kernel.OutcomeCause) bool {
	switch cause {
	case kernel.CauseRequestInvalid, kernel.CauseCapability, kernel.CauseModelNotFound, kernel.CauseAuth, kernel.CausePermission, kernel.CauseQuotaExhausted, kernel.CauseRateLimited, kernel.CauseCapacity, kernel.CauseOverloaded, kernel.CauseTimeout, kernel.CauseNetwork, kernel.CauseProtocol, kernel.CauseStreamFailure, kernel.CauseUnknown:
		return true
	default:
		return false
	}
}

func validOutcomeScope(scope kernel.OutcomeScope) bool {
	switch scope {
	case kernel.ScopeRequest, kernel.ScopeRoute, kernel.ScopeRouteConnection, kernel.ScopeConnection, kernel.ScopeProvider:
		return true
	default:
		return false
	}
}

func validJSONPointer(path string) bool {
	if !strings.HasPrefix(path, "/") {
		return false
	}
	for index := 0; index < len(path); index++ {
		if path[index] != '~' {
			continue
		}
		if index+1 >= len(path) || (path[index+1] != '0' && path[index+1] != '1') {
			return false
		}
		index++
	}
	return true
}

type OperationBinding struct {
	Protocol               kernel.Protocol           `json:"protocol,omitempty"`
	TaskRef                *extensions.Ref           `json:"task,omitempty"`
	IdempotencyHeader      string                    `json:"idempotencyHeader,omitempty"`
	ProviderFormat         normalize.Format          `json:"providerFormat,omitempty"`
	Endpoint               PrimitiveRef              `json:"endpoint"`
	EndpointOptions        kernel.EndpointOptions    `json:"endpointOptions,omitempty"`
	Transport              PrimitiveRef              `json:"transport"`
	RequestCodec           PrimitiveRef              `json:"requestCodec,omitempty"`
	RequestCodecOptions    json.RawMessage           `json:"requestCodecOptions,omitempty"`
	ResponseDecoder        PrimitiveRef              `json:"responseDecoder,omitempty"`
	ModelSource            PrimitiveRef              `json:"modelSource,omitempty"`
	ModelSourceOptions     json.RawMessage           `json:"modelSourceOptions,omitempty"`
	UsageSource            PrimitiveRef              `json:"usageSource,omitempty"`
	UsageOptions           kernel.UsageSourceOptions `json:"usageOptions,omitempty"`
	QuotaSource            PrimitiveRef              `json:"quotaSource,omitempty"`
	QuotaSourceOptions     json.RawMessage           `json:"quotaSourceOptions,omitempty"`
	QuotaWindowName        string                    `json:"quotaWindowName,omitempty"`
	ErrorClassifier        PrimitiveRef              `json:"errorClassifier,omitempty"`
	ErrorClassifierOptions ErrorClassifierOptions    `json:"errorClassifierOptions,omitempty"`
}

// HTTPJSONQuotaOptions describes provider-neutral extraction from one or more
// JSON quota reports. Providers with non-composable semantics can register a
// dedicated QuotaSource primitive instead.
type HTTPJSONQuotaOptions struct {
	Scope            QuotaScope              `json:"scope,omitempty"`
	CredentialHeader string                  `json:"credentialHeader,omitempty"`
	CredentialPrefix string                  `json:"credentialPrefix,omitempty"`
	RequestHeaders   map[string]string       `json:"requestHeaders,omitempty"`
	Windows          []HTTPJSONQuotaWindow   `json:"windows,omitempty"`
	HeaderWindows    []HTTPHeaderQuotaWindow `json:"headerWindows,omitempty"`
	Reports          []HTTPJSONQuotaReport   `json:"reports,omitempty"`
	FailureMode      QuotaReportFailureMode  `json:"failureMode,omitempty"`
}

// HTTPHeaderQuotaWindow maps one response-header set into a normalized quota window.
type HTTPHeaderQuotaWindow struct {
	Name            string              `json:"name"`
	LimitHeader     string              `json:"limitHeader,omitempty"`
	UsedHeader      string              `json:"usedHeader,omitempty"`
	RemainingHeader string              `json:"remainingHeader,omitempty"`
	ResetHeader     string              `json:"resetHeader,omitempty"`
	RoutingEffect   quota.RoutingEffect `json:"routingEffect,omitempty"`
}

type QuotaReportFailureMode string

const (
	QuotaReportsFailAll       QuotaReportFailureMode = "fail_all"
	QuotaReportsKeepAvailable QuotaReportFailureMode = "keep_available"
)

// HTTPJSONQuotaReport selects one report endpoint. Its windows
// override the source-level Windows; absent endpoint fields inherit the
// operation quota endpoint settings.
type HTTPJSONQuotaReport struct {
	EndpointOptions kernel.EndpointOptions `json:"endpointOptions,omitempty"`
	Windows         []HTTPJSONQuotaWindow  `json:"windows,omitempty"`
}

// HTTPJSONQuotaWindow maps either one named object or each item under
// ItemsPath into normalized quota snapshots. For object maps, the object key
// is used as the window name unless NamePath is configured. The any_positive
// aggregation emits one binary availability window when at least one item
// has a positive AggregationValuePath; an optional status gate can override
// that result using the root report object.
type HTTPJSONQuotaWindow struct {
	Name                    string              `json:"name,omitempty"`
	ItemsPath               string              `json:"itemsPath,omitempty"`
	Aggregation             string              `json:"aggregation,omitempty"`
	AggregationValuePath    string              `json:"aggregationValuePath,omitempty"`
	AggregationStatusPath   string              `json:"aggregationStatusPath,omitempty"`
	AggregationStatusValues []string            `json:"aggregationStatusValues,omitempty"`
	NamePath                string              `json:"namePath,omitempty"`
	UsedPath                string              `json:"usedPath,omitempty"`
	LimitPath               string              `json:"limitPath,omitempty"`
	RemainingPath           string              `json:"remainingPath,omitempty"`
	RemainingFromLimitUsed  bool                `json:"remainingFromLimitUsed,omitempty"`
	UsedPercentPath         string              `json:"usedPercentPath,omitempty"`
	RemainingPercentPath    string              `json:"remainingPercentPath,omitempty"`
	ResetAtPath             string              `json:"resetAtPath,omitempty"`
	ExhaustedStatusPath     string              `json:"exhaustedStatusPath,omitempty"`
	ExhaustedStatusValues   []string            `json:"exhaustedStatusValues,omitempty"`
	IgnoreWhenPath          string              `json:"ignoreWhenPath,omitempty"`
	IgnoreWhenValues        []string            `json:"ignoreWhenValues,omitempty"`
	RoutingEffect           quota.RoutingEffect `json:"routingEffect,omitempty"`
}

func (o HTTPJSONQuotaOptions) Validate() error {
	if o.Scope != "" && o.Scope != QuotaScopeModel && o.Scope != QuotaScopeConnection {
		return fmt.Errorf("quota source scope %q is unsupported", o.Scope)
	}
	if o.CredentialHeader != "" && !validHTTPHeaderName(o.CredentialHeader) {
		return fmt.Errorf("quota credential header name is invalid")
	}
	if strings.ContainsAny(o.CredentialPrefix, "\r\n") {
		return fmt.Errorf("quota credential prefix must not contain line breaks")
	}
	if len(o.RequestHeaders) > 64 {
		return fmt.Errorf("quota source supports at most 64 configured request headers")
	}
	credentialHeader := o.CredentialHeader
	if credentialHeader == "" {
		credentialHeader = "Authorization"
	}
	seenRequestHeaders := make(map[string]string, len(o.RequestHeaders))
	for name, value := range o.RequestHeaders {
		if !validHTTPHeaderName(name) {
			return fmt.Errorf("quota request header name %q is invalid", name)
		}
		switch strings.ToLower(name) {
		case "authorization", "proxy-authorization", "x-api-key", "api-key", "cookie", "set-cookie":
			return fmt.Errorf("quota request header %q must come from the connection credential, not manifest options", name)
		}
		canonical := strings.ToLower(name)
		if previous, exists := seenRequestHeaders[canonical]; exists {
			return fmt.Errorf("quota request headers %q and %q have the same case-insensitive name", previous, name)
		}
		seenRequestHeaders[canonical] = name
		if strings.EqualFold(name, credentialHeader) {
			return fmt.Errorf("quota request header %q conflicts with credential presentation", name)
		}
		if strings.ContainsAny(value, "\r\n") || len(value) > 8192 {
			return fmt.Errorf("quota request header %q value is invalid or exceeds 8192 bytes", name)
		}
	}
	if o.FailureMode != "" && o.FailureMode != QuotaReportsFailAll && o.FailureMode != QuotaReportsKeepAvailable {
		return fmt.Errorf("quota report failureMode %q is unsupported", o.FailureMode)
	}
	if o.FailureMode != "" && len(o.Reports) == 0 {
		return fmt.Errorf("quota report failureMode requires report endpoints")
	}
	if (len(o.Windows) > 0 || len(o.HeaderWindows) > 0) && len(o.Reports) > 0 {
		return fmt.Errorf("quota source cannot combine source-level windows with report endpoints")
	}
	if len(o.HeaderWindows) > 64 {
		return fmt.Errorf("quota source supports at most 64 header windows")
	}
	seenHeaderWindows := make(map[string]struct{}, len(o.HeaderWindows))
	for i, window := range o.HeaderWindows {
		if strings.TrimSpace(window.Name) == "" {
			return fmt.Errorf("quota header window %d requires a name", i)
		}
		if window.RoutingEffect != "" && window.RoutingEffect != quota.RoutingEffectEnforce && window.RoutingEffect != quota.RoutingEffectObserve {
			return fmt.Errorf("quota header window %d routingEffect %q is unsupported", i, window.RoutingEffect)
		}
		key := strings.ToLower(strings.TrimSpace(window.Name))
		if _, exists := seenHeaderWindows[key]; exists {
			return fmt.Errorf("quota header window name %q is duplicated", window.Name)
		}
		seenHeaderWindows[key] = struct{}{}
		if window.LimitHeader == "" && window.UsedHeader == "" && window.RemainingHeader == "" && window.ResetHeader == "" {
			return fmt.Errorf("quota header window %q has no measurable headers", window.Name)
		}
		for _, header := range []string{window.LimitHeader, window.UsedHeader, window.RemainingHeader, window.ResetHeader} {
			if header != "" && !validHTTPHeaderName(header) {
				return fmt.Errorf("quota header window %q has invalid header name %q", window.Name, header)
			}
		}
	}
	if err := validateHTTPJSONQuotaWindows(o.Windows); err != nil {
		return err
	}
	if len(o.Reports) > 8 {
		return fmt.Errorf("quota source supports at most 8 report endpoints")
	}
	for index, report := range o.Reports {
		if report.EndpointOptions.Path == "" && len(report.EndpointOptions.Query) == 0 {
			return fmt.Errorf("quota report %d requires endpoint path or query options", index)
		}
		if len(report.Windows) == 0 {
			return fmt.Errorf("quota report %d requires at least one window mapping", index)
		}
		if len(report.Windows) > 64 {
			return fmt.Errorf("quota report %d supports at most 64 window mappings", index)
		}
		if err := validateHTTPJSONQuotaWindows(report.Windows); err != nil {
			return fmt.Errorf("quota report %d: %w", index, err)
		}
	}
	return nil
}

func validateHTTPJSONQuotaWindows(windows []HTTPJSONQuotaWindow) error {
	if len(windows) > 64 {
		return fmt.Errorf("quota source supports at most 64 window mappings")
	}
	seenNames := make(map[string]struct{}, len(windows))
	for index, window := range windows {
		if window.ItemsPath == "" && strings.TrimSpace(window.Name) == "" && window.NamePath == "" {
			return fmt.Errorf("quota window %d requires a name, namePath or itemsPath", index)
		}
		if window.ItemsPath != "" && window.Name != "" && window.Aggregation != "any_positive" {
			return fmt.Errorf("quota window %d cannot combine a static name with itemsPath", index)
		}
		if window.RoutingEffect != "" && window.RoutingEffect != quota.RoutingEffectEnforce && window.RoutingEffect != quota.RoutingEffectObserve {
			return fmt.Errorf("quota window %d routingEffect %q is unsupported", index, window.RoutingEffect)
		}
		for _, item := range []struct{ field, path string }{
			{"itemsPath", window.ItemsPath}, {"aggregationValuePath", window.AggregationValuePath},
			{"aggregationStatusPath", window.AggregationStatusPath}, {"namePath", window.NamePath}, {"usedPath", window.UsedPath},
			{"limitPath", window.LimitPath}, {"remainingPath", window.RemainingPath},
			{"usedPercentPath", window.UsedPercentPath}, {"remainingPercentPath", window.RemainingPercentPath}, {"resetAtPath", window.ResetAtPath},
			{"exhaustedStatusPath", window.ExhaustedStatusPath}, {"ignoreWhenPath", window.IgnoreWhenPath},
		} {
			if item.path != "" && !validJSONPointer(item.path) {
				return fmt.Errorf("quota window %d %s must be an absolute JSON Pointer", index, item.field)
			}
		}
		if (window.ExhaustedStatusPath == "") != (len(window.ExhaustedStatusValues) == 0) {
			return fmt.Errorf("quota window %d must configure exhausted status path and values together", index)
		}
		if (window.IgnoreWhenPath == "") != (len(window.IgnoreWhenValues) == 0) {
			return fmt.Errorf("quota window %d must configure ignore path and values together", index)
		}
		if window.Aggregation != "" && window.Aggregation != "any_positive" {
			return fmt.Errorf("quota window %d aggregation %q is unsupported", index, window.Aggregation)
		}
		if window.Aggregation == "any_positive" {
			if window.ItemsPath == "" || window.Name == "" || window.NamePath != "" || window.AggregationValuePath == "" {
				return fmt.Errorf("quota window %d any_positive aggregation requires name, itemsPath and aggregationValuePath", index)
			}
			if (window.AggregationStatusPath == "") != (len(window.AggregationStatusValues) == 0) {
				return fmt.Errorf("quota window %d aggregation status path and values must be configured together", index)
			}
			if window.UsedPath != "" || window.LimitPath != "" || window.RemainingPath != "" || window.UsedPercentPath != "" || window.RemainingPercentPath != "" || window.ExhaustedStatusPath != "" {
				return fmt.Errorf("quota window %d any_positive aggregation cannot combine scalar quota fields", index)
			}
		} else if window.AggregationValuePath != "" || window.AggregationStatusPath != "" || len(window.AggregationStatusValues) > 0 {
			return fmt.Errorf("quota window %d aggregation fields require aggregation=any_positive", index)
		}
		if (window.RemainingPercentPath != "" || window.UsedPercentPath != "") && (window.UsedPath != "" || window.LimitPath != "" || window.RemainingPath != "") {
			return fmt.Errorf("quota window %d cannot combine percentage paths with absolute amount paths", index)
		}
		if window.RemainingPercentPath != "" && window.UsedPercentPath != "" {
			return fmt.Errorf("quota window %d cannot configure both usedPercentPath and remainingPercentPath", index)
		}
		if window.RemainingFromLimitUsed && (window.UsedPath == "" || window.LimitPath == "" || window.RemainingPath != "" || window.RemainingPercentPath != "" || window.UsedPercentPath != "") {
			return fmt.Errorf("quota window %d remainingFromLimitUsed requires limitPath and usedPath only", index)
		}
		if window.Aggregation == "" && window.UsedPath == "" && window.LimitPath == "" && window.RemainingPath == "" && window.UsedPercentPath == "" && window.RemainingPercentPath == "" && window.ResetAtPath == "" && window.ExhaustedStatusPath == "" {
			return fmt.Errorf("quota window %d has no measurable fields", index)
		}
		for _, values := range [][]string{window.ExhaustedStatusValues, window.IgnoreWhenValues, window.AggregationStatusValues} {
			for _, value := range values {
				if strings.TrimSpace(value) == "" {
					return fmt.Errorf("quota window %d has an empty status matcher", index)
				}
			}
		}
		if window.Name != "" {
			key := strings.ToLower(strings.TrimSpace(window.Name))
			if window.Name != strings.TrimSpace(window.Name) {
				return fmt.Errorf("quota window %d name must be trimmed", index)
			}
			if _, duplicate := seenNames[key]; duplicate {
				return fmt.Errorf("quota window name %q is duplicated", window.Name)
			}
			seenNames[key] = struct{}{}
		}
	}
	return nil
}

type CapabilitySet struct {
	Chat        bool `json:"chat,omitempty"`
	Responses   bool `json:"responses,omitempty"`
	Messages    bool `json:"messages,omitempty"`
	Embeddings  bool `json:"embeddings,omitempty"`
	Image       bool `json:"image,omitempty"`
	AudioInput  bool `json:"audioInput,omitempty"`
	AudioOutput bool `json:"audioOutput,omitempty"`
	Video       bool `json:"video,omitempty"`
	Tools       bool `json:"tools,omitempty"`
	Thinking    bool `json:"thinking,omitempty"`
	Streaming   bool `json:"streaming,omitempty"`
	Usage       bool `json:"usage,omitempty"`
	Quota       bool `json:"quota,omitempty"`
}

type OAuthFlowOptions struct {
	ClientID      string   `json:"clientId"`
	AuthURL       string   `json:"authUrl"`
	TokenURL      string   `json:"tokenUrl"`
	DeviceAuthURL string   `json:"deviceAuthUrl,omitempty"`
	Scopes        []string `json:"scopes,omitempty"`
	RedirectURL   string   `json:"redirectUrl,omitempty"`
}

type AuthOptions struct {
	OAuth   *OAuthFlowOptions `json:"oauth,omitempty"`
	Options json.RawMessage   `json:"options,omitempty"`
}

type ProviderDefinition struct {
	ContractVersion uint64                         `json:"contractVersion"`
	ID              string                         `json:"id"`
	Version         string                         `json:"version"`
	DisplayName     string                         `json:"displayName"`
	Aliases         []string                       `json:"aliases,omitempty"`
	Operations      map[Operation]OperationBinding `json:"operations"`
	Auth            PrimitiveRef                   `json:"auth"`
	AuthOptions     AuthOptions                    `json:"authOptions,omitempty"`
	Session         PrimitiveRef                   `json:"session,omitempty"`
	Extensions      []PrimitiveRef                 `json:"extensions,omitempty"`
	Capabilities    CapabilitySet                  `json:"capabilities"`
	Defaults        map[string]any                 `json:"defaults,omitempty"`
}

func DecodeProviderDefinitionJSON(data []byte) (ProviderDefinition, error) {
	var definition ProviderDefinition
	if err := decodeStrictJSON(data, &definition); err != nil {
		return ProviderDefinition{}, fmt.Errorf("decode provider definition: %w", err)
	}
	return definition, nil
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func EncodeProviderDefinitionJSON(definition ProviderDefinition) ([]byte, error) {
	return json.MarshalIndent(definition, "", "  ")
}

type PrimitivePlugin interface {
	Definition() ProviderDefinition
}

type PrimitiveRegistry struct {
	definitions map[string]ProviderDefinition
	primitives  map[PrimitiveKind]map[extensions.Ref]struct{}
	catalog     *extensions.Catalog
	Operations  *operations.Registry
}

type RuntimeRegistry struct {
	Primitives        *PrimitiveRegistry
	Auth              *AuthRegistry
	extensionSnapshot *extensions.Snapshot
	operationSnapshot *operations.Snapshot
	endpoints         map[extensions.Ref]kernel.Endpoint
	transports        map[extensions.Ref]kernel.Transport
	requestCodecs     map[extensions.Ref]kernel.RequestCodec
	responseDecoders  map[extensions.Ref]kernel.ResponseDecoder
	renderers         map[normalize.Format]kernel.ResponseRenderer
	modelSources      map[extensions.Ref]ModelSource
	usageSources      map[extensions.Ref]UsageSource
	sessionStores     map[extensions.Ref]kernel.SessionStore
	errorClassifiers  map[extensions.Ref]ErrorClassifier
	quotaSources      map[extensions.Ref]QuotaSource
}

// RuntimeModule adds reusable provider operations and primitive
// implementations to a runtime registry. Modules are linked by the
// composition root; manifests bind their exact contracts without adding
// provider-specific branches to the kernel or daemon request path.
type RuntimeModule interface {
	ID() string
	Register(*RuntimeRegistry) error
}

type RuntimeModuleFunc struct {
	Name string
	Init func(*RuntimeRegistry) error
}

func (m RuntimeModuleFunc) ID() string { return m.Name }
func (m RuntimeModuleFunc) Register(registry *RuntimeRegistry) error {
	if m.Init == nil {
		return fmt.Errorf("runtime module %q has no registration function", m.Name)
	}
	return m.Init(registry)
}

// RegisterFeatureEvaluator contributes a versioned feature evaluator and its
// request-constraint schema to the shared extension catalog. Model profiles
// retain the semantic feature ID; each invocation pins one evaluator version.
func (r *RuntimeRegistry) RegisterFeatureEvaluator(ref extensions.Ref, implementationVersion, displayName, description string, constraintsSchemaRef extensions.Ref, constraintsSchema json.RawMessage, evaluator kernel.FeatureEvaluator) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if err := ref.Validate(); err != nil || ref.Kind != kernel.FeatureEvaluatorKind || evaluator == nil {
		return fmt.Errorf("feature evaluator requires an exact %q reference and implementation", kernel.FeatureEvaluatorKind)
	}
	if constraintsSchemaRef.Kind != extensions.SchemaKind || constraintsSchemaRef.ContractVersion == 0 {
		return fmt.Errorf("feature evaluator %q requires an exact constraint schema reference", ref.Key())
	}
	document, err := extensions.BindSchemaDocument(constraintsSchemaRef, constraintsSchema)
	if err != nil {
		return fmt.Errorf("feature evaluator %q constraints schema: %w", ref.Key(), err)
	}
	if err := r.Primitives.catalog.RegisterSchema(constraintsSchemaRef, document); err != nil {
		return err
	}
	return r.Primitives.catalog.Register(extensions.Descriptor{
		Ref: ref, ImplementationVersion: implementationVersion, DisplayName: displayName,
		Description: description, InputSchemaRef: &constraintsSchemaRef,
	}, func(json.RawMessage) (any, error) { return evaluator, nil })
}

// RegisterArtifactInterpreter adds a versioned bounded stream interpreter to
// the shared extension catalog. Operations bind this exact ref to named input
// roles and compile its schema-validated facts into request requirements.
func (r *RuntimeRegistry) RegisterArtifactInterpreter(ref extensions.Ref, implementationVersion, displayName, description string, factsSchemaRef extensions.Ref, factsSchema json.RawMessage, optionsSchemaRef *extensions.Ref, optionsSchema json.RawMessage, bounds extensions.ResourceBounds, factory func(json.RawMessage) (any, error)) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if err := ref.Validate(); err != nil || ref.Kind != extensions.ArtifactInterpreterKind {
		return fmt.Errorf("artifact interpreter requires an exact %q reference", extensions.ArtifactInterpreterKind)
	}
	if factsSchemaRef.Kind != extensions.SchemaKind || factsSchemaRef.ContractVersion == 0 {
		return fmt.Errorf("artifact interpreter %q requires an exact facts schema reference", ref.Key())
	}
	factsDocument, err := extensions.BindSchemaDocument(factsSchemaRef, factsSchema)
	if err != nil {
		return fmt.Errorf("artifact interpreter %q facts schema: %w", ref.Key(), err)
	}
	if err := r.Primitives.catalog.RegisterSchema(factsSchemaRef, factsDocument); err != nil {
		return err
	}
	var optionsRef *extensions.Ref
	if optionsSchemaRef != nil {
		if optionsSchemaRef.Kind != extensions.SchemaKind || optionsSchemaRef.ContractVersion == 0 {
			return fmt.Errorf("artifact interpreter %q has an invalid options schema ref", ref.Key())
		}
		optionsDocument, err := extensions.BindSchemaDocument(*optionsSchemaRef, optionsSchema)
		if err != nil {
			return fmt.Errorf("artifact interpreter %q options schema: %w", ref.Key(), err)
		}
		if err := r.Primitives.catalog.RegisterSchema(*optionsSchemaRef, optionsDocument); err != nil {
			return err
		}
		copy := *optionsSchemaRef
		optionsRef = &copy
	} else if len(optionsSchema) > 0 {
		return fmt.Errorf("artifact interpreter %q options schema body requires an exact schema ref", ref.Key())
	}
	return r.Primitives.catalog.Register(extensions.Descriptor{
		Ref: ref, ImplementationVersion: implementationVersion, DisplayName: displayName, Description: description,
		OptionsSchemaRef: optionsRef, ResultSchemaRef: &factsSchemaRef, ResourceBounds: bounds,
	}, extensions.Factory(factory))
}

// RegisterSchemaDocument adds a local JSON Schema to the shared extension
// catalog. Its ref and all references remain version-pinned at catalog freeze.
func (r *RuntimeRegistry) RegisterSchemaDocument(ref extensions.Ref, schema json.RawMessage) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	document, err := extensions.BindSchemaDocument(ref, schema)
	if err != nil {
		return err
	}
	return r.Primitives.catalog.RegisterSchema(ref, document)
}

// RegisterExtension adds a statically linked versioned implementation to the
// shared catalog. Provider modules should declare dependencies and schemas on
// the descriptor; Freeze rejects unresolved refs before the daemon serves.
func (r *RuntimeRegistry) RegisterExtension(descriptor extensions.Descriptor, factory extensions.Factory) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	return r.Primitives.catalog.Register(descriptor, factory)
}

func NewRuntimeRegistry() (*RuntimeRegistry, error) {
	return NewRuntimeRegistryWithModules()
}

// NewRuntimeRegistryWithModules composes statically linked extension modules
// after the shared primitive catalog is initialized and before any provider
// manifest is loaded or the catalog is frozen.
func NewRuntimeRegistryWithModules(modules ...RuntimeModule) (*RuntimeRegistry, error) {
	primitives, err := NewBuiltinPrimitiveRegistry()
	if err != nil {
		return nil, err
	}
	runtime := &RuntimeRegistry{Primitives: primitives, Auth: NewAuthRegistry(), endpoints: map[extensions.Ref]kernel.Endpoint{}, transports: map[extensions.Ref]kernel.Transport{}, requestCodecs: map[extensions.Ref]kernel.RequestCodec{}, responseDecoders: map[extensions.Ref]kernel.ResponseDecoder{}, renderers: map[normalize.Format]kernel.ResponseRenderer{}, modelSources: map[extensions.Ref]ModelSource{}, usageSources: map[extensions.Ref]UsageSource{}, sessionStores: map[extensions.Ref]kernel.SessionStore{}, errorClassifiers: map[extensions.Ref]ErrorClassifier{}, quotaSources: map[extensions.Ref]QuotaSource{}}
	if err := registerMultipartRequestCodec(runtime); err != nil {
		return nil, fmt.Errorf("register generic multipart request codec: %w", err)
	}
	oauthSchema, err := oauthOptionsSchema()
	if err != nil {
		return nil, err
	}
	staticModelsSchema, err := staticModelSourceOptionsSchema()
	if err != nil {
		return nil, err
	}
	for _, authID := range []string{"static-secret", "none"} {
		if err := runtime.registerAuthImplementation(authID, 1); err != nil {
			return nil, err
		}
	}
	requestCodec, responseDecoder := openai.NewChatCodecs()
	registrations := []error{
		runtime.RegisterEndpoint(kernel.HTTPJSONEndpoint{}), runtime.RegisterTransport(kernel.HTTPTransport{}),
		runtime.RegisterRequestCodec(requestCodec), runtime.RegisterResponseDecoder(responseDecoder),
	}
	requestCodec, responseDecoder = openai.NewResponsesCodecs()
	registrations = append(registrations, runtime.RegisterRequestCodec(requestCodec), runtime.RegisterResponseDecoder(responseDecoder))
	requestCodec, responseDecoder = anthropic.NewMessageCodecs()
	registrations = append(registrations, runtime.RegisterRequestCodec(requestCodec), runtime.RegisterResponseDecoder(responseDecoder))
	requestCodec, responseDecoder = gemini.NewCodecs()
	registrations = append(registrations, runtime.RegisterRequestCodec(requestCodec), runtime.RegisterResponseDecoder(responseDecoder))
	registrations = append(registrations,
		runtime.RegisterSessionStore(kernel.NewMemorySessionStore()),
		runtime.RegisterModelSource(OpenAIModelSource{}), runtime.RegisterModelSource(AnthropicModelSource{}),
		runtime.RegisterModelSourceFactory("static-models", 1, staticModelSourceSchemaRef, staticModelsSchema, func(raw json.RawMessage) (ModelSource, error) {
			var options StaticModelSourceOptions
			if err := json.Unmarshal(raw, &options); err != nil {
				return nil, fmt.Errorf("decode static model source options: %w", err)
			}
			if err := options.Validate(); err != nil {
				return nil, err
			}
			models, err := cloneModelDescriptors(options.Models)
			if err != nil {
				return nil, err
			}
			return StaticModelSource{Models: models}, nil
		}),
		runtime.RegisterUsageSource(HTTPHeaderUsageSource{}),
		runtime.RegisterErrorClassifier(HTTPJSONErrorClassifier{}),
		runtime.RegisterQuotaSource(NoopQuotaSource{}),
		runtime.RegisterQuotaSourceFactory(HTTPJSONQuotaSource{}, 1, func(raw json.RawMessage) (QuotaSource, error) {
			var options HTTPJSONQuotaOptions
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &options); err != nil {
					return nil, fmt.Errorf("decode HTTP/JSON quota options: %w", err)
				}
			}
			if err := options.Validate(); err != nil {
				return nil, err
			}
			return HTTPJSONQuotaSource{Options: options}, nil
		}),
		runtime.RegisterAuthFactoryWithOptionsSchema("oauth2", 1, oauthOptionsSchemaRef, oauthSchema, OAuthAuthFactory),
	)
	for _, renderer := range egress.Builtins() {
		registrations = append(registrations, runtime.RegisterResponseRenderer(renderer))
	}
	for _, registrationErr := range registrations {
		if registrationErr != nil {
			return nil, registrationErr
		}
	}
	seenModules := make(map[string]bool, len(modules))
	for _, module := range modules {
		if module == nil {
			return nil, fmt.Errorf("runtime module must not be nil")
		}
		id := strings.TrimSpace(module.ID())
		if id == "" || id != module.ID() {
			return nil, fmt.Errorf("runtime module ID must be non-empty and trimmed")
		}
		if seenModules[id] {
			return nil, fmt.Errorf("runtime module %q is registered more than once", id)
		}
		seenModules[id] = true
		if err := module.Register(runtime); err != nil {
			return nil, fmt.Errorf("register runtime module %q: %w", id, err)
		}
	}
	if err := runtime.loadEmbeddedBuiltinDefinitions(); err != nil {
		return nil, err
	}
	return runtime, nil
}

func (r *RuntimeRegistry) RegisterResponseRenderer(renderer kernel.ResponseRenderer) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if renderer == nil || renderer.ID() == "" {
		return fmt.Errorf("response renderer and format ID are required")
	}
	if _, exists := r.renderers[renderer.ID()]; exists {
		return fmt.Errorf("response renderer for %q is already registered", renderer.ID())
	}
	ref := extensions.Ref{Kind: "client-renderer", ID: string(renderer.ID()), ContractVersion: 1}
	if err := r.Primitives.catalog.Register(extensions.Descriptor{
		Ref: ref, ImplementationVersion: "1", DisplayName: string(renderer.ID()),
		Description: "Client response wire renderer.",
	}, func(json.RawMessage) (any, error) { return renderer, nil }); err != nil {
		return err
	}
	r.renderers[renderer.ID()] = renderer
	return nil
}

func (r *RuntimeRegistry) ResponseRenderers() map[normalize.Format]kernel.ResponseRenderer {
	result := make(map[normalize.Format]kernel.ResponseRenderer, len(r.renderers))
	for format, renderer := range r.renderers {
		result[format] = renderer
	}
	return result
}

func (r *RuntimeRegistry) RegisterRequestTransform(transform kernel.RequestTransform, optionsSchema json.RawMessage) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if transform == nil {
		return fmt.Errorf("request transform implementation is required")
	}
	definition := transform.Definition()
	if err := kernel.NewRequestTransformRegistry().Register(transform); err != nil {
		return err
	}
	if err := validateRuntimeTransformRegistration(definition.Ref, definition.ImplementationVersion, definition.OptionsSchemaRef, optionsSchema); err != nil {
		return err
	}
	if err := registerTransformDescriptor(r.Primitives.catalog, definition.Ref, definition.ImplementationVersion, definition.Label, definition.Description, definition.OptionsSchemaRef, definition.ResourceBounds, string(definition.Stage), transformEffects(definition.Effects), transformFailureModes(definition.FailureModes), optionsSchema, transform); err != nil {
		return err
	}
	return nil
}

func (r *RuntimeRegistry) RegisterResponseTransform(transform kernel.ResponseTransform, optionsSchema json.RawMessage) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if transform == nil {
		return fmt.Errorf("response transform implementation is required")
	}
	definition := transform.Definition()
	if err := kernel.NewResponseTransformRegistry().Register(transform); err != nil {
		return err
	}
	if err := validateRuntimeTransformRegistration(definition.Ref, definition.ImplementationVersion, definition.OptionsSchemaRef, optionsSchema); err != nil {
		return err
	}
	capabilities := make([]string, len(definition.Effects))
	for i, effect := range definition.Effects {
		capabilities[i] = string(effect)
	}
	if err := registerTransformDescriptor(r.Primitives.catalog, definition.Ref, definition.ImplementationVersion, definition.Label, definition.Description, definition.OptionsSchemaRef, definition.ResourceBounds, "response.semantic", capabilities, transformFailureModes(definition.FailureModes), optionsSchema, transform); err != nil {
		return err
	}
	return nil
}

// RegisterStrategyDefinition contributes a versioned scheduling policy to the
// same frozen catalog used for provider, operation, feature and transform
// modules. Exact strategy refs can coexist at different contract versions.
func (r *RuntimeRegistry) RegisterStrategyDefinition(definition kernel.StrategyDefinition) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	return kernel.RegisterStrategyExtension(r.Primitives.catalog, definition)
}

func validateRuntimeTransformRegistration(ref extensions.Ref, implementationVersion string, optionsSchemaRef *extensions.Ref, optionsSchema json.RawMessage) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(implementationVersion) == "" {
		return fmt.Errorf("transform %q requires an implementation version", ref.Key())
	}
	if optionsSchemaRef == nil && len(optionsSchema) > 0 {
		return fmt.Errorf("transform %q provided an options schema without optionsSchemaRef", ref.Key())
	}
	if optionsSchemaRef == nil {
		return nil
	}
	if optionsSchemaRef.Kind != extensions.SchemaKind {
		return fmt.Errorf("transform %q optionsSchemaRef must use kind %q", ref.Key(), extensions.SchemaKind)
	}
	if len(optionsSchema) == 0 {
		return fmt.Errorf("transform %q has an optionsSchemaRef but no options schema document", ref.Key())
	}
	if _, err := extensions.BindSchemaDocument(*optionsSchemaRef, optionsSchema); err != nil {
		return err
	}
	return nil
}

func registerTransformDescriptor(catalog *extensions.Catalog, ref extensions.Ref, implementationVersion, label, description string, schemaRef *extensions.Ref, bounds extensions.ResourceBounds, stage string, effects, failureModes []string, schemaShape json.RawMessage, implementation any) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	if implementationVersion == "" || label == "" || description == "" {
		return fmt.Errorf("transform %q requires implementation version and display metadata", ref.Key())
	}
	if schemaRef == nil && len(schemaShape) > 0 {
		return fmt.Errorf("transform %q supplied an options schema without an optionsSchemaRef", ref.Key())
	}
	if schemaRef != nil {
		if schemaRef.Kind != extensions.SchemaKind {
			return fmt.Errorf("transform %q options schema reference must use kind %q", ref.Key(), extensions.SchemaKind)
		}
		document, err := extensions.BindSchemaDocument(*schemaRef, schemaShape)
		if err != nil {
			return err
		}
		if err := catalog.RegisterSchema(*schemaRef, document); err != nil {
			return err
		}
	}
	return catalog.Register(extensions.Descriptor{
		Ref: ref, ImplementationVersion: implementationVersion, DisplayName: label, Description: description,
		OptionsSchemaRef: schemaRef, LifecycleCapabilities: append([]string{stage}, effects...), FailureModes: append([]string(nil), failureModes...), ResourceBounds: bounds,
	}, func(json.RawMessage) (any, error) { return implementation, nil })
}

func transformFailureModes(modes []kernel.TransformFailureMode) []string {
	result := make([]string, len(modes))
	for index, mode := range modes {
		result[index] = string(mode)
	}
	return result
}

func transformEffects[T ~string](effects []T) []string {
	result := make([]string, len(effects))
	for i, effect := range effects {
		result[i] = string(effect)
	}
	return result
}

func (r *RuntimeRegistry) ValidateTransformBindings(bindings []kernel.TransformBinding) error {
	snapshot, err := r.FreezeCatalog()
	if err != nil {
		return err
	}
	for _, binding := range bindings {
		_, ok := snapshot.Descriptor(binding.TransformRef)
		if !ok {
			return fmt.Errorf("transform binding %q references unavailable extension %q", binding.ID, binding.TransformRef.Key())
		}
		if _, _, err := snapshot.Bind(binding.TransformRef, binding.Options); err != nil {
			return fmt.Errorf("transform binding %q: %w", binding.ID, err)
		}
	}
	return nil
}

func (r *RuntimeRegistry) registerImplementation(kind PrimitiveKind, id string, contractVersion uint64, factory extensions.Factory) error {
	if r == nil || r.Primitives == nil {
		return fmt.Errorf("runtime registry is not initialized")
	}
	if id == "" || contractVersion == 0 || factory == nil {
		return fmt.Errorf("%s primitive ID, positive contract version and factory are required", kind)
	}
	ref := PrimitiveRef{Kind: kind, ID: id, ContractVersion: contractVersion}
	if !r.Primitives.hasPrimitive(ref) {
		if err := r.Primitives.RegisterPrimitiveVersion(kind, id, contractVersion); err != nil {
			return err
		}
	}
	return r.Primitives.catalog.RegisterFactory(extensionRef(ref), factory)
}

// RegisterPrimitiveOptionsSchema binds JSON Schema metadata to a primitive
// contract before its implementation is registered. Provider modules use it
// for their operation-specific option payloads.
func (r *RuntimeRegistry) RegisterPrimitiveOptionsSchema(kind PrimitiveKind, id string, contractVersion uint64, schemaRef extensions.Ref, schema json.RawMessage) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	return r.Primitives.RegisterPrimitiveWithOptionsSchema(kind, id, contractVersion, schemaRef, schema)
}

// RegisterPrimitiveImplementation installs an exact versioned primitive
// factory in the shared catalog. Identical IDs at different contract versions
// remain independent; no latest-version fallback is performed at bind time.
func (r *RuntimeRegistry) RegisterPrimitiveImplementation(kind PrimitiveKind, id string, contractVersion uint64, factory extensions.Factory) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if id == "" || contractVersion == 0 || factory == nil {
		return fmt.Errorf("primitive requires ID, positive contract version and factory")
	}
	ref := PrimitiveRef{Kind: kind, ID: id, ContractVersion: contractVersion}
	if !r.Primitives.hasPrimitive(ref) {
		if err := r.Primitives.RegisterPrimitiveVersion(kind, id, contractVersion); err != nil {
			return err
		}
	}
	return r.Primitives.catalog.RegisterFactory(extensionRef(ref), factory)
}

func (r *RuntimeRegistry) ensureReady() error {
	if r == nil || r.Primitives == nil {
		return fmt.Errorf("runtime registry is not initialized")
	}
	return nil
}

// FreezeCatalog resolves exact extension references and compiles the complete
// schema set once during daemon startup. Repeated callers receive the same
// immutable snapshot.
func (r *RuntimeRegistry) FreezeCatalog() (*extensions.Snapshot, error) {
	if err := r.ensureReady(); err != nil {
		return nil, err
	}
	if r.extensionSnapshot != nil {
		return r.extensionSnapshot, nil
	}
	snapshot, err := r.Primitives.catalog.Freeze()
	if err != nil {
		return nil, err
	}
	if r.Primitives.Operations != nil {
		r.operationSnapshot, err = r.Primitives.Operations.Seal(snapshot)
		if err != nil {
			return nil, err
		}
	}
	r.extensionSnapshot = snapshot
	return snapshot, nil
}

func (r *RuntimeRegistry) OperationSnapshot() (*operations.Snapshot, error) {
	if _, err := r.FreezeCatalog(); err != nil {
		return nil, err
	}
	if r.operationSnapshot == nil {
		return nil, fmt.Errorf("operation catalog is unavailable")
	}
	return r.operationSnapshot, nil
}

func (r *RuntimeRegistry) ExtensionCatalog() (extensions.CatalogView, error) {
	snapshot, err := r.FreezeCatalog()
	if err != nil {
		return extensions.CatalogView{}, err
	}
	return snapshot.View(), nil
}

func (r *RuntimeRegistry) registerAuthImplementation(id string, contractVersion uint64) error {
	ref := extensions.Ref{Kind: string(PrimitiveAuth), ID: id, ContractVersion: contractVersion}
	return r.registerImplementation(PrimitiveAuth, id, contractVersion, func(raw json.RawMessage) (any, error) {
		var options AuthOptions
		if len(raw) > 0 {
			if err := decodeStrictJSON(raw, &options); err != nil {
				return nil, err
			}
		}
		return r.Auth.Build(ref, options)
	})
}

func (r *RuntimeRegistry) RegisterEndpoint(endpoint kernel.Endpoint) error {
	return r.RegisterEndpointVersion(endpoint, 1)
}

func (r *RuntimeRegistry) RegisterEndpointVersion(endpoint kernel.Endpoint, contractVersion uint64) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if endpoint == nil || contractVersion == 0 {
		return fmt.Errorf("endpoint implementation and positive contract version are required")
	}
	ref := extensions.Ref{Kind: string(PrimitiveEndpoint), ID: endpoint.ID(), ContractVersion: contractVersion}
	if _, exists := r.endpoints[ref]; exists {
		return fmt.Errorf("endpoint %q is already registered", ref.Key())
	}
	if err := r.registerImplementation(PrimitiveEndpoint, endpoint.ID(), contractVersion, func(json.RawMessage) (any, error) { return endpoint, nil }); err != nil {
		return err
	}
	r.endpoints[ref] = endpoint
	return nil
}

func (r *RuntimeRegistry) RegisterTransport(transport kernel.Transport) error {
	return r.RegisterTransportVersion(transport, 1)
}

func (r *RuntimeRegistry) RegisterTransportVersion(transport kernel.Transport, contractVersion uint64) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if transport == nil || contractVersion == 0 {
		return fmt.Errorf("transport implementation and positive contract version are required")
	}
	ref := extensions.Ref{Kind: string(PrimitiveTransport), ID: transport.ID(), ContractVersion: contractVersion}
	if _, exists := r.transports[ref]; exists {
		return fmt.Errorf("transport %q is already registered", ref.Key())
	}
	if err := r.registerImplementation(PrimitiveTransport, transport.ID(), contractVersion, func(json.RawMessage) (any, error) { return transport, nil }); err != nil {
		return err
	}
	r.transports[ref] = transport
	return nil
}

func (r *RuntimeRegistry) RegisterRequestCodec(codec kernel.RequestCodec) error {
	return r.RegisterRequestCodecVersion(codec, 1)
}

func (r *RuntimeRegistry) RegisterRequestCodecVersion(codec kernel.RequestCodec, contractVersion uint64) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if codec == nil || contractVersion == 0 {
		return fmt.Errorf("request codec implementation and positive contract version are required")
	}
	ref := extensions.Ref{Kind: string(PrimitiveRequestCodec), ID: codec.ID(), ContractVersion: contractVersion}
	if _, exists := r.requestCodecs[ref]; exists {
		return fmt.Errorf("request codec %q is already registered", ref.Key())
	}
	if err := r.registerImplementation(PrimitiveRequestCodec, codec.ID(), contractVersion, func(json.RawMessage) (any, error) { return codec, nil }); err != nil {
		return err
	}
	r.requestCodecs[ref] = codec
	return nil
}

// RegisterConfiguredRequestCodec registers a request codec factory with a
// versioned options schema. Provider manifests can select the exact codec
// contract and configure its wire fields without adding provider branches.
func (r *RuntimeRegistry) RegisterConfiguredRequestCodec(id string, contractVersion uint64, optionsSchemaRef extensions.Ref, optionsSchema json.RawMessage, factory extensions.Factory) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if strings.TrimSpace(id) == "" || contractVersion == 0 || factory == nil {
		return fmt.Errorf("configured request codec requires ID, positive contract version and factory")
	}
	ref := extensions.Ref{Kind: string(PrimitiveRequestCodec), ID: id, ContractVersion: contractVersion}
	if _, exists := r.requestCodecs[ref]; exists {
		return fmt.Errorf("request codec %q is already registered", ref.Key())
	}
	boundSchema, err := extensions.BindSchemaDocument(optionsSchemaRef, optionsSchema)
	if err != nil {
		return fmt.Errorf("bind request codec %q options schema: %w", ref.Key(), err)
	}
	if err := r.Primitives.RegisterPrimitiveWithOptionsSchema(PrimitiveRequestCodec, id, contractVersion, optionsSchemaRef, boundSchema); err != nil {
		return err
	}
	if err := r.Primitives.catalog.RegisterFactory(ref, factory); err != nil {
		return fmt.Errorf("register request codec %q factory: %w", ref.Key(), err)
	}
	r.requestCodecs[ref] = nil
	return nil
}

func (r *RuntimeRegistry) RegisterResponseDecoder(codec kernel.ResponseDecoder) error {
	return r.RegisterResponseDecoderVersion(codec, 1)
}

func (r *RuntimeRegistry) RegisterResponseDecoderVersion(codec kernel.ResponseDecoder, contractVersion uint64) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if codec == nil || contractVersion == 0 {
		return fmt.Errorf("response codec implementation and positive contract version are required")
	}
	ref := extensions.Ref{Kind: string(PrimitiveResponseDecoder), ID: codec.ID(), ContractVersion: contractVersion}
	if _, exists := r.responseDecoders[ref]; exists {
		return fmt.Errorf("response codec %q is already registered", ref.Key())
	}
	if err := r.registerImplementation(PrimitiveResponseDecoder, codec.ID(), contractVersion, func(json.RawMessage) (any, error) { return codec, nil }); err != nil {
		return err
	}
	r.responseDecoders[ref] = codec
	return nil
}

func (r *RuntimeRegistry) RegisterModelSource(source ModelSource) error {
	return r.RegisterModelSourceVersion(source, 1)
}

func (r *RuntimeRegistry) RegisterModelSourceVersion(source ModelSource, contractVersion uint64) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if source == nil || contractVersion == 0 {
		return fmt.Errorf("model source implementation and positive contract version are required")
	}
	ref := extensions.Ref{Kind: string(PrimitiveModelSource), ID: source.ID(), ContractVersion: contractVersion}
	if _, exists := r.modelSources[ref]; exists {
		return fmt.Errorf("model source %q is already registered", ref.Key())
	}
	if err := r.registerImplementation(PrimitiveModelSource, source.ID(), contractVersion, func(json.RawMessage) (any, error) { return source, nil }); err != nil {
		return err
	}
	r.modelSources[ref] = source
	return nil
}

func (r *RuntimeRegistry) RegisterModelSourceFactory(id string, contractVersion uint64, schemaRef extensions.Ref, schema json.RawMessage, factory func(json.RawMessage) (ModelSource, error)) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if id == "" || contractVersion == 0 || factory == nil {
		return fmt.Errorf("model source factory requires an ID, positive contract version and factory")
	}
	if !r.Primitives.hasPrimitive(PrimitiveRef{Kind: PrimitiveModelSource, ID: id, ContractVersion: contractVersion}) {
		if err := r.RegisterPrimitiveOptionsSchema(PrimitiveModelSource, id, contractVersion, schemaRef, schema); err != nil {
			return err
		}
	}
	ref := extensions.Ref{Kind: string(PrimitiveModelSource), ID: id, ContractVersion: contractVersion}
	if _, exists := r.modelSources[ref]; exists {
		return fmt.Errorf("model source %q is already registered", ref.Key())
	}
	if err := r.RegisterPrimitiveImplementation(PrimitiveModelSource, id, contractVersion, func(raw json.RawMessage) (any, error) {
		source, err := factory(raw)
		if err != nil {
			return nil, err
		}
		if source == nil || source.ID() != id {
			return nil, fmt.Errorf("model source factory for %q returned a mismatched implementation", id)
		}
		return source, nil
	}); err != nil {
		return err
	}
	r.modelSources[ref] = nil
	return nil
}

func (r *RuntimeRegistry) RegisterUsageSource(source UsageSource) error {
	return r.RegisterUsageSourceVersion(source, 1)
}

func (r *RuntimeRegistry) RegisterUsageSourceVersion(source UsageSource, contractVersion uint64) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if source == nil || contractVersion == 0 {
		return fmt.Errorf("usage source implementation and positive contract version are required")
	}
	ref := extensions.Ref{Kind: string(PrimitiveUsageSource), ID: source.ID(), ContractVersion: contractVersion}
	if _, exists := r.usageSources[ref]; exists {
		return fmt.Errorf("usage source %q is already registered", ref.Key())
	}
	if err := r.registerImplementation(PrimitiveUsageSource, source.ID(), contractVersion, func(json.RawMessage) (any, error) { return source, nil }); err != nil {
		return err
	}
	r.usageSources[ref] = source
	return nil
}

func (r *RuntimeRegistry) RegisterSessionStore(sessionStore kernel.SessionStore) error {
	return r.RegisterSessionStoreVersion(sessionStore, 1)
}

func (r *RuntimeRegistry) RegisterSessionStoreVersion(sessionStore kernel.SessionStore, contractVersion uint64) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if sessionStore == nil || contractVersion == 0 {
		return fmt.Errorf("session store implementation and positive contract version are required")
	}
	ref := extensions.Ref{Kind: string(PrimitiveSessionStore), ID: sessionStore.ID(), ContractVersion: contractVersion}
	if _, exists := r.sessionStores[ref]; exists {
		return fmt.Errorf("session store %q is already registered", ref.Key())
	}
	if err := r.registerImplementation(PrimitiveSessionStore, sessionStore.ID(), contractVersion, func(json.RawMessage) (any, error) { return sessionStore, nil }); err != nil {
		return err
	}
	r.sessionStores[ref] = sessionStore
	return nil
}

// ReplaceSessionStore lets the daemon install its durable, async-backed
// implementation under the same manifest primitive before bindings are built.
func (r *RuntimeRegistry) ReplaceSessionStore(sessionStore kernel.SessionStore) error {
	return r.ReplaceSessionStoreVersion(sessionStore, 1)
}

func (r *RuntimeRegistry) ReplaceSessionStoreVersion(sessionStore kernel.SessionStore, contractVersion uint64) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if sessionStore == nil || contractVersion == 0 {
		return fmt.Errorf("session store implementation and positive contract version are required")
	}
	ref := extensions.Ref{Kind: string(PrimitiveSessionStore), ID: sessionStore.ID(), ContractVersion: contractVersion}
	if _, exists := r.sessionStores[ref]; !exists {
		return fmt.Errorf("session store %q is not registered", ref.Key())
	}
	if err := r.Primitives.catalog.ReplaceFactory(ref, func(json.RawMessage) (any, error) { return sessionStore, nil }); err != nil {
		return err
	}
	r.sessionStores[ref] = sessionStore
	return nil
}

func (r *RuntimeRegistry) SessionStores() map[extensions.Ref]kernel.SessionStore {
	result := make(map[extensions.Ref]kernel.SessionStore, len(r.sessionStores))
	for ref, sessionStore := range r.sessionStores {
		result[ref] = sessionStore
	}
	return result
}

func (r *RuntimeRegistry) Endpoints() map[extensions.Ref]kernel.Endpoint {
	result := make(map[extensions.Ref]kernel.Endpoint, len(r.endpoints))
	for ref, endpoint := range r.endpoints {
		result[ref] = endpoint
	}
	return result
}

func (r *RuntimeRegistry) Transports() map[extensions.Ref]kernel.Transport {
	result := make(map[extensions.Ref]kernel.Transport, len(r.transports))
	for ref, transport := range r.transports {
		result[ref] = transport
	}
	return result
}

func (r *RuntimeRegistry) RegisterErrorClassifier(classifier ErrorClassifier) error {
	return r.RegisterErrorClassifierVersion(classifier, 1)
}

func (r *RuntimeRegistry) RegisterErrorClassifierVersion(classifier ErrorClassifier, contractVersion uint64) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if classifier == nil || contractVersion == 0 {
		return fmt.Errorf("error classifier implementation and positive contract version are required")
	}
	ref := extensions.Ref{Kind: string(PrimitiveErrorClassifier), ID: classifier.ID(), ContractVersion: contractVersion}
	if _, exists := r.errorClassifiers[ref]; exists {
		return fmt.Errorf("error classifier %q is already registered", ref.Key())
	}
	if err := r.registerImplementation(PrimitiveErrorClassifier, classifier.ID(), contractVersion, func(json.RawMessage) (any, error) { return classifier, nil }); err != nil {
		return err
	}
	r.errorClassifiers[ref] = classifier
	return nil
}

func (r *RuntimeRegistry) RegisterQuotaSource(source QuotaSource) error {
	return r.RegisterQuotaSourceVersion(source, 1)
}

func (r *RuntimeRegistry) RegisterQuotaSourceVersion(source QuotaSource, contractVersion uint64) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if source == nil || contractVersion == 0 {
		return fmt.Errorf("quota source implementation and positive contract version are required")
	}
	ref := extensions.Ref{Kind: string(PrimitiveQuotaSource), ID: source.ID(), ContractVersion: contractVersion}
	if _, exists := r.quotaSources[ref]; exists {
		return fmt.Errorf("quota source %q is already registered", ref.Key())
	}
	if err := r.registerImplementation(PrimitiveQuotaSource, source.ID(), contractVersion, func(json.RawMessage) (any, error) { return source, nil }); err != nil {
		return err
	}
	r.quotaSources[ref] = source
	return nil
}

// RegisterQuotaSourceFactory binds exact manifest options into a per-provider
// QuotaSource instance. The primitive descriptor must already carry the
// matching versioned options schema.
func (r *RuntimeRegistry) RegisterQuotaSourceFactory(source QuotaSource, contractVersion uint64, factory QuotaSourceFactory) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if source == nil || factory == nil || contractVersion == 0 {
		return fmt.Errorf("quota source, options factory and positive contract version are required")
	}
	ref := PrimitiveRef{Kind: PrimitiveQuotaSource, ID: source.ID(), ContractVersion: contractVersion}
	if _, exists := r.quotaSources[extensionRef(ref)]; exists {
		return fmt.Errorf("quota source %q is already registered", extensionRef(ref).Key())
	}
	if err := r.registerImplementation(PrimitiveQuotaSource, source.ID(), contractVersion, func(raw json.RawMessage) (any, error) {
		configured, err := factory(append(json.RawMessage(nil), raw...))
		if err != nil {
			return nil, err
		}
		if configured == nil {
			return nil, fmt.Errorf("quota source options factory returned nil")
		}
		return configured, nil
	}); err != nil {
		return err
	}
	r.quotaSources[extensionRef(ref)] = source
	return nil
}

func (r *RuntimeRegistry) RegisterAuthFlow(flow AuthFlow) error {
	return r.RegisterAuthFlowVersion(flow, 1)
}

func (r *RuntimeRegistry) RegisterAuthFlowVersion(flow AuthFlow, contractVersion uint64) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if flow == nil || contractVersion == 0 {
		return fmt.Errorf("auth flow implementation and positive contract version are required")
	}
	if err := r.registerImplementation(PrimitiveAuth, flow.ID(), contractVersion, func(json.RawMessage) (any, error) { return flow, nil }); err != nil {
		return err
	}
	return r.Auth.RegisterVersion(flow, contractVersion)
}

func (r *RuntimeRegistry) RegisterAuthFactory(id string, factory AuthFlowFactory) error {
	return r.registerAuthFactory(id, 1, nil, nil, factory)
}

func (r *RuntimeRegistry) RegisterAuthFactoryWithOptionsSchema(id string, contractVersion uint64, schemaRef extensions.Ref, schema json.RawMessage, factory AuthFlowFactory) error {
	return r.registerAuthFactory(id, contractVersion, &schemaRef, schema, factory)
}

func (r *RuntimeRegistry) registerAuthFactory(id string, contractVersion uint64, schemaRef *extensions.Ref, schema json.RawMessage, factory AuthFlowFactory) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if factory == nil || id == "" {
		return fmt.Errorf("auth factory and ID are required")
	}
	if schemaRef != nil {
		if err := r.Primitives.RegisterPrimitiveWithOptionsSchema(PrimitiveAuth, id, contractVersion, *schemaRef, schema); err != nil {
			return err
		}
	} else if !r.Primitives.hasPrimitive(PrimitiveRef{Kind: PrimitiveAuth, ID: id, ContractVersion: contractVersion}) {
		if err := r.Primitives.RegisterPrimitiveVersion(PrimitiveAuth, id, contractVersion); err != nil {
			return err
		}
	}
	ref := extensions.Ref{Kind: string(PrimitiveAuth), ID: id, ContractVersion: contractVersion}
	if err := r.Auth.RegisterFactoryVersion(ref, factory); err != nil {
		return err
	}
	if err := r.registerAuthImplementation(id, contractVersion); err != nil {
		return err
	}
	return nil
}

func (r *RuntimeRegistry) AdaptersForBindings(bindings map[string]RuntimeBinding) map[string]kernel.ProviderAdapter {
	result := make(map[string]kernel.ProviderAdapter, len(bindings))
	for _, binding := range bindings {
		if binding.Adapter != nil {
			result[binding.AdapterID] = binding.Adapter
		}
	}
	return result
}

func (r *RuntimeRegistry) AuthFlowsForBindings(bindings map[string]RuntimeBinding) map[string]AuthFlow {
	result := make(map[string]AuthFlow, len(bindings))
	for _, binding := range bindings {
		if binding.Auth != nil {
			result[binding.AuthFlowID] = binding.Auth
		}
	}
	return result
}

func (r *RuntimeRegistry) DefaultCredentialTypes() map[string]string {
	result := make(map[string]string)
	if r == nil || r.Primitives == nil {
		return result
	}
	for _, definition := range r.Primitives.Definitions() {
		if definition.Auth.ID == "" {
			continue
		}
		credentialType := definition.Auth.ID
		if credentialType == "static-secret" {
			credentialType = "api_key"
		}
		result[definition.ID] = credentialType
	}
	return result
}

func (r *RuntimeRegistry) AuthFlowIDsByDefinition() map[string]string {
	result := make(map[string]string)
	if r == nil || r.Primitives == nil {
		return result
	}
	for _, definition := range r.Primitives.Definitions() {
		if definition.Auth.ID != "" {
			result[definition.ID] = AuthBindingKey(definition.ID, definition.Auth)
		}
	}
	return result
}

func (r *RuntimeRegistry) ErrorClassifiers() map[extensions.Ref]kernel.ErrorClassifier {
	result := make(map[extensions.Ref]kernel.ErrorClassifier, len(r.errorClassifiers))
	for ref, classifier := range r.errorClassifiers {
		result[ref] = adaptErrorClassifier(classifier)
	}
	return result
}

func (r *RuntimeRegistry) ErrorClassifiersForBindings(bindings map[string]RuntimeBinding) map[extensions.Ref]kernel.ErrorClassifier {
	result := r.ErrorClassifiers()
	for _, binding := range bindings {
		if binding.ErrorClassifier == nil || binding.ErrorClassifierRef.ID == "" {
			continue
		}
		result[binding.ErrorClassifierRef] = adaptErrorClassifier(binding.ErrorClassifier)
	}
	return result
}

func adaptErrorClassifier(classifier ErrorClassifier) kernel.ErrorClassifier {
	adapter := classifierAdapter{classifier}
	if rich, ok := classifier.(kernel.OutcomeClassifier); ok {
		return outcomeClassifierAdapter{classifierAdapter: adapter, outcomeClassifier: rich}
	}
	return adapter
}

func (r *RuntimeRegistry) QuotaSources() map[extensions.Ref]QuotaSource {
	result := make(map[extensions.Ref]QuotaSource, len(r.quotaSources))
	for ref, source := range r.quotaSources {
		result[ref] = source
	}
	return result
}

func (r *RuntimeRegistry) QuotaSourcesForBindings(bindings map[string]RuntimeBinding) map[extensions.Ref]QuotaSource {
	result := r.QuotaSources()
	for _, binding := range bindings {
		if binding.QuotaSource != nil && binding.QuotaSourceRef.ID != "" {
			result[binding.QuotaSourceRef] = binding.QuotaSource
		}
	}
	return result
}

func (r *RuntimeRegistry) UsageSources() map[extensions.Ref]kernel.UsageEnricher {
	result := make(map[extensions.Ref]kernel.UsageEnricher, len(r.usageSources))
	for ref, source := range r.usageSources {
		result[ref] = source
	}
	return result
}

type classifierAdapter struct{ classifier ErrorClassifier }

func (c classifierAdapter) ClassifyError(status int, body []byte) kernel.ErrorClass {
	return c.classifier.Classify(status, body)
}

type outcomeClassifierAdapter struct {
	classifierAdapter
	outcomeClassifier kernel.OutcomeClassifier
}

func (c outcomeClassifierAdapter) ClassifyOutcome(status int, headers http.Header, body []byte) kernel.ClassifiedOutcome {
	return c.outcomeClassifier.ClassifyOutcome(status, headers, body)
}

func NewPrimitiveRegistry() *PrimitiveRegistry {
	catalog := extensions.NewCatalog()
	operationRegistry, _ := operations.NewRegistry(catalog)
	return &PrimitiveRegistry{
		definitions: map[string]ProviderDefinition{},
		primitives: map[PrimitiveKind]map[extensions.Ref]struct{}{
			PrimitiveEndpoint: {}, PrimitiveTransport: {}, PrimitiveAuth: {}, PrimitiveRequestCodec: {},
			PrimitiveResponseDecoder: {}, PrimitiveModelSource: {}, PrimitiveUsageSource: {},
			PrimitiveQuotaSource: {}, PrimitiveSessionStore: {},
			PrimitiveErrorClassifier: {}, PrimitiveExtension: {},
		},
		catalog:    catalog,
		Operations: operationRegistry,
	}
}

func (r *PrimitiveRegistry) RegisterPrimitive(kind PrimitiveKind, id string) error {
	return r.RegisterPrimitiveVersion(kind, id, 1)
}

// RegisterPrimitiveVersion declares one exact primitive contract. Multiple
// versions of one kind/ID coexist; every binding must select its exact ref.
func (r *PrimitiveRegistry) RegisterPrimitiveVersion(kind PrimitiveKind, id string, contractVersion uint64) error {
	return r.registerPrimitiveContract(kind, id, contractVersion, nil)
}

func (r *PrimitiveRegistry) RegisterPrimitiveWithOptionsSchema(kind PrimitiveKind, id string, contractVersion uint64, schemaRef extensions.Ref, schema json.RawMessage) error {
	if r == nil || r.catalog == nil {
		return fmt.Errorf("primitive registry catalog is not initialized")
	}
	if err := r.catalog.RegisterSchema(schemaRef, schema); err != nil {
		return err
	}
	return r.registerPrimitiveContract(kind, id, contractVersion, &schemaRef)
}

func (r *PrimitiveRegistry) registerPrimitiveContract(kind PrimitiveKind, id string, contractVersion uint64, optionsSchemaRef *extensions.Ref) error {
	if r == nil || id == "" || contractVersion == 0 {
		return fmt.Errorf("primitive registry, ID and positive contractVersion are required")
	}
	set, ok := r.primitives[kind]
	if !ok {
		return fmt.Errorf("unknown primitive kind %q", kind)
	}
	ref := extensions.Ref{Kind: string(kind), ID: id, ContractVersion: contractVersion}
	if _, exists := set[ref]; exists {
		return fmt.Errorf("primitive %s already registered", ref.Key())
	}
	if err := r.catalog.RegisterDescriptor(extensions.Descriptor{
		Ref: ref, ImplementationVersion: fmt.Sprintf("contract-%d", contractVersion),
		DisplayName: string(kind) + ": " + id, Description: "Runtime provider primitive contract.",
		OptionsSchemaRef: optionsSchemaRef,
	}); err != nil {
		return err
	}
	set[ref] = struct{}{}
	return nil
}

func (r *PrimitiveRegistry) ContractCatalog() *extensions.Catalog {
	if r == nil {
		return nil
	}
	return r.catalog
}

func (r *PrimitiveRegistry) RegisterDefinition(def ProviderDefinition) error {
	if r == nil {
		return fmt.Errorf("primitive registry is nil")
	}
	if def.ContractVersion != 1 || def.ID == "" || def.Version == "" || def.DisplayName == "" {
		return fmt.Errorf("provider definition requires supported contractVersion 1, ID, version and display name")
	}
	if _, exists := r.definitions[def.ID]; exists {
		return fmt.Errorf("provider definition %q already registered", def.ID)
	}
	if len(def.Operations) == 0 {
		return fmt.Errorf("provider %q has no operations", def.ID)
	}
	operationBindings := make(map[Operation]OperationBinding, len(def.Operations))
	for operation, binding := range def.Operations {
		binding.RequestCodecOptions = append(json.RawMessage(nil), binding.RequestCodecOptions...)
		binding.ModelSourceOptions = append(json.RawMessage(nil), binding.ModelSourceOptions...)
		binding.QuotaSourceOptions = append(json.RawMessage(nil), binding.QuotaSourceOptions...)
		operationBindings[operation] = binding
	}
	def.Operations = operationBindings
	if def.Auth.ID == "" {
		return fmt.Errorf("provider %q must bind an auth primitive explicitly (use %q when no credential is needed)", def.ID, "none")
	}
	if err := validatePrimitiveRef(def.Auth); err != nil {
		return fmt.Errorf("provider %q auth reference: %w", def.ID, err)
	}
	if !r.hasPrimitive(def.Auth) {
		return fmt.Errorf("provider %q references unknown auth primitive %q", def.ID, def.Auth.ID)
	}
	if def.Session.ID != "" && !r.hasPrimitive(def.Session) {
		return fmt.Errorf("provider %q references unknown session store %q", def.ID, def.Session.ID)
	}
	for operation, binding := range def.Operations {
		if operation == "" {
			return fmt.Errorf("provider %q has incomplete %q operation binding", def.ID, operation)
		}
		if operation == OperationModels {
			if binding.ModelSource.ID == "" || (binding.Endpoint.ID == "") != (binding.Transport.ID == "") {
				return fmt.Errorf("provider %q has incomplete %q model-source binding", def.ID, operation)
			}
		} else if binding.Endpoint.ID == "" || binding.Transport.ID == "" {
			return fmt.Errorf("provider %q has incomplete %q operation binding", def.ID, operation)
		}
		if binding.IdempotencyHeader != "" && !validIdempotencyHeaderName(binding.IdempotencyHeader) {
			return fmt.Errorf("provider %q %q operation has an invalid idempotency header name", def.ID, operation)
		}
		if binding.IdempotencyHeader != "" {
			if binding.TaskRef == nil || r.Operations == nil {
				return fmt.Errorf("provider %q %q idempotency header requires an exact replayable task ref", def.ID, operation)
			}
			replaySafety, registered := r.Operations.ReplayContract(*binding.TaskRef)
			if !registered || replaySafety != operations.ReplayWithKey {
				return fmt.Errorf("provider %q %q idempotency header requires operation %q to declare scoped_idempotency_key", def.ID, operation, binding.TaskRef.Key())
			}
		}
		requestResponseOperation := binding.RequestCodec.ID != "" || binding.ResponseDecoder.ID != ""
		if requestResponseOperation && (binding.RequestCodec.ID == "" || binding.ResponseDecoder.ID == "") {
			return fmt.Errorf("provider %q has incomplete %q codec binding", def.ID, operation)
		}
		if binding.RequestCodec.ID == "" && len(binding.RequestCodecOptions) > 0 {
			return fmt.Errorf("provider %q %q requestCodecOptions requires a requestCodec", def.ID, operation)
		}
		if binding.ModelSource.ID == "" && len(binding.ModelSourceOptions) > 0 {
			return fmt.Errorf("provider %q %q modelSourceOptions requires a modelSource", def.ID, operation)
		}
		if operation != OperationModels && binding.ModelSource.ID != "" {
			return fmt.Errorf("provider %q modelSource must be bound to the models operation", def.ID)
		}
		if binding.TaskRef == nil {
			if requestResponseOperation {
				return fmt.Errorf("provider %q %q inference binding requires a semantic task ref", def.ID, operation)
			}
		} else if err := binding.TaskRef.Validate(); err != nil || binding.TaskRef.Kind != "operation" || r.Operations == nil || !r.Operations.Contains(*binding.TaskRef) {
			return fmt.Errorf("provider %q %q references an unknown or invalid semantic task ref", def.ID, operation)
		}
		if operation == OperationQuota && binding.QuotaSource.ID == "" {
			return fmt.Errorf("provider %q quota operation requires a quotaSource", def.ID)
		}
		if len(binding.QuotaSourceOptions) > 0 && binding.QuotaSource.ID == "" {
			return fmt.Errorf("provider %q %q quotaSourceOptions requires a quotaSource", def.ID, operation)
		}
		if operation != OperationQuota && binding.QuotaSource.ID != "" {
			return fmt.Errorf("provider %q quotaSource must be bound to the quota operation", def.ID)
		}
		if !binding.ErrorClassifierOptions.Empty() {
			if binding.ErrorClassifier.ID == "" {
				return fmt.Errorf("provider %q %q classifier options require an errorClassifier", def.ID, operation)
			}
			if binding.ErrorClassifierOptions.HTTPJSON == nil || binding.ErrorClassifier.ID != "http-json" {
				return fmt.Errorf("provider %q %q has options unsupported by classifier %q", def.ID, operation, binding.ErrorClassifier.ID)
			}
			if err := binding.ErrorClassifierOptions.HTTPJSON.Validate(); err != nil {
				return fmt.Errorf("provider %q %q: %w", def.ID, operation, err)
			}
		}
		for _, ref := range []PrimitiveRef{binding.Endpoint, binding.Transport, binding.RequestCodec, binding.ResponseDecoder, binding.ModelSource, binding.UsageSource, binding.QuotaSource, binding.ErrorClassifier} {
			if ref.ID == "" {
				continue
			}
			if err := validatePrimitiveRef(ref); err != nil {
				return fmt.Errorf("provider %q operation %q: %w", def.ID, operation, err)
			}
			if !r.hasPrimitive(ref) {
				return fmt.Errorf("provider %q references unknown %s primitive %q contract %d", def.ID, ref.Kind, ref.ID, ref.ContractVersion)
			}
		}
	}
	for _, extension := range def.Extensions {
		if extension.ID == "" || !r.hasPrimitive(extension) {
			return fmt.Errorf("provider %q references unknown extension %q", def.ID, extension.ID)
		}
	}
	dependencies := map[extensions.Ref]struct{}{extensionRef(def.Auth): {}}
	if def.Session.ID != "" {
		dependencies[extensionRef(def.Session)] = struct{}{}
	}
	for _, binding := range def.Operations {
		if binding.TaskRef != nil {
			dependencies[*binding.TaskRef] = struct{}{}
		}
		for _, primitive := range []PrimitiveRef{binding.Endpoint, binding.Transport, binding.RequestCodec, binding.ResponseDecoder, binding.ModelSource, binding.UsageSource, binding.QuotaSource, binding.ErrorClassifier} {
			if primitive.ID != "" {
				dependencies[extensionRef(primitive)] = struct{}{}
			}
		}
	}
	for _, extension := range def.Extensions {
		dependencies[extensionRef(extension)] = struct{}{}
	}
	dependencyRefs := make([]extensions.Ref, 0, len(dependencies))
	for dependency := range dependencies {
		dependencyRefs = append(dependencyRefs, dependency)
	}
	sort.Slice(dependencyRefs, func(i, j int) bool { return dependencyRefs[i].Key() < dependencyRefs[j].Key() })
	providerRef := ProviderDefinitionRef(def.ID, def.ContractVersion)
	if err := r.catalog.Register(extensions.Descriptor{
		Ref: providerRef, ImplementationVersion: def.Version, DisplayName: def.DisplayName,
		Description:  "Provider behavior definition with exact primitive and operation dependencies.",
		Dependencies: dependencyRefs, LifecycleCapabilities: []string{"provider.definition"},
	}, func(json.RawMessage) (any, error) { return def, nil }); err != nil {
		return fmt.Errorf("register provider definition extension %q: %w", providerRef.Key(), err)
	}
	r.definitions[def.ID] = def
	return nil
}

func validIdempotencyHeaderName(name string) bool {
	if name == "" {
		return false
	}
	switch strings.ToLower(name) {
	case "authorization", "proxy-authorization", "cookie", "set-cookie", "host", "content-length", "content-type", "transfer-encoding":
		return false
	}
	for _, char := range []byte(name) {
		if (char >= '0' && char <= '9') || (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') {
			continue
		}
		switch char {
		case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
			continue
		default:
			return false
		}
	}
	return true
}

// RegisterDefinitionIfAbsent merges a persisted portable definition with an
// already installed built-in/manifest definition only when they are identical.
// A differing definition under the same ID is rejected rather than shadowed.
func (r *PrimitiveRegistry) RegisterDefinitionIfAbsent(def ProviderDefinition) error {
	if existing, ok := r.definitions[def.ID]; ok {
		if SameProviderDefinition(existing, def) {
			return nil
		}
		return fmt.Errorf("provider definition %q conflicts with the installed definition", def.ID)
	}
	return r.RegisterDefinition(def)
}

func (r *PrimitiveRegistry) RegisterPlugin(plugin PrimitivePlugin) error {
	if plugin == nil {
		return fmt.Errorf("provider plugin is nil")
	}
	return r.RegisterDefinition(plugin.Definition())
}

func (r *PrimitiveRegistry) Definition(id string) (ProviderDefinition, bool) {
	definition, ok := r.definitions[id]
	return definition, ok
}

func (r *PrimitiveRegistry) Definitions() []ProviderDefinition {
	result := make([]ProviderDefinition, 0, len(r.definitions))
	for _, definition := range r.definitions {
		result = append(result, definition)
	}
	return result
}

func (r *PrimitiveRegistry) hasPrimitive(ref PrimitiveRef) bool {
	if ref.ID == "" || ref.ContractVersion == 0 {
		return false
	}
	set, ok := r.primitives[ref.Kind]
	if !ok {
		return false
	}
	_, exists := set[extensions.Ref{Kind: string(ref.Kind), ID: ref.ID, ContractVersion: ref.ContractVersion}]
	return exists
}

func validatePrimitiveRef(ref PrimitiveRef) error {
	if ref.Kind == "" || ref.ID == "" || ref.ContractVersion == 0 {
		return fmt.Errorf("primitive ref requires kind, ID and positive contractVersion")
	}
	return nil
}
