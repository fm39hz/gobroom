package kernel

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode"

	"github.com/fm39hz/gobroom/internal/extensions"
)

const (
	defaultUsageCaptureMaxBytes int64 = 262144
	invalidRawUsageCode               = "invalid_raw_usage"
)

// DefaultUsageCapturePolicy returns the bounded, disabled-by-default policy.
func DefaultUsageCapturePolicy() UsageCapturePolicy {
	return UsageCapturePolicy{Enabled: false, MaxBytes: defaultUsageCaptureMaxBytes}
}

// SelectRawUsage retains an owned copy of a decoder-selected usage object when
// capture is enabled and its metadata and complete JSON value are valid.
// Shape safety is the decoder's responsibility; this function does not redact.
func SelectRawUsage(policy UsageCapturePolicy, providerDef extensions.Ref, api string, value json.RawMessage) (*RawUsageFragment, []UsageIssue) {
	if !policy.Enabled {
		return nil, nil
	}
	if policy.MaxBytes <= 0 || int64(len(value)) > policy.MaxBytes ||
		providerDef.Kind != "provider-definition" || providerDef.Validate() != nil ||
		!validRawUsageAPI(api) || !validRawUsageObject(value) {
		return nil, []UsageIssue{{Code: invalidRawUsageCode}}
	}
	return &RawUsageFragment{
		ProviderDefinition: providerDef,
		API:                api,
		Value:              bytes.Clone(value),
	}, nil
}

func validRawUsageAPI(api string) bool {
	if strings.TrimSpace(api) == "" {
		return false
	}
	for _, character := range api {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validRawUsageObject(value json.RawMessage) bool {
	trimmed := bytes.TrimSpace(value)
	return len(trimmed) != 0 && trimmed[0] == '{' && json.Valid(trimmed)
}
