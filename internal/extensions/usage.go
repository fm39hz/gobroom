package extensions

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const UsageKind = "usage-value"

const usageValueDefaultMaxBytes int64 = 64 << 10
const usageDescriptorMaxBytes = 64 << 10

// UsageDisclosure controls whether a registered usage value may be disclosed
// in management interfaces. Private is the default when omitted.
type UsageDisclosure string

const (
	UsagePrivate           UsageDisclosure = "private"
	UsageManagementVisible UsageDisclosure = "management-visible"
)

// UsagePolicy bounds and classifies values recorded under a usage contract.
// It describes disclosure only; it does not authorize billing or execution.
type UsagePolicy struct {
	Unit          string          `json:"unit"`
	Disclosure    UsageDisclosure `json:"disclosure,omitempty"`
	MaxValueBytes int64           `json:"maxValueBytes"`
}

// UsageValue carries a value under one exact registered usage contract.
type UsageValue struct {
	TypeRef Ref             `json:"typeRef"`
	Value   json.RawMessage `json:"value"`
}

// OwnedEncoding returns a canonical, independently owned value and its actual
// JSON envelope size. Compact RawMessage encoding preserves numeric lexemes;
// retaining the same encoded representation prevents whitespace or escaping
// from creating a gap between the byte budget and retained buffers.
func (value UsageValue) OwnedEncoding(maxBytes int64) (UsageValue, int64, error) {
	if maxBytes <= 0 || int64(len(value.Value)) > maxBytes || int64(len(value.TypeRef.Kind)) > maxBytes || int64(len(value.TypeRef.ID)) > maxBytes {
		return UsageValue{}, 0, fmt.Errorf("usage value exceeds its encoded byte limit")
	}
	encoded, err := json.Marshal(value)
	if err != nil || int64(len(encoded)) > maxBytes {
		return UsageValue{}, 0, fmt.Errorf("usage value exceeds its encoded byte limit or is invalid JSON")
	}
	var owned UsageValue
	if err := json.Unmarshal(encoded, &owned); err != nil {
		return UsageValue{}, 0, fmt.Errorf("usage value cannot be bound to its contract")
	}
	return owned, int64(len(encoded)), nil
}

// UsageDescriptorSnapshot is the immutable, non-executable metadata needed to
// interpret a usage value without exposing a catalog factory or runtime value.
type UsageDescriptorSnapshot struct {
	Ref            Ref             `json:"ref"`
	SchemaRef      Ref             `json:"schemaRef"`
	SchemaDigest   string          `json:"schemaDigest"`
	Label          string          `json:"label"`
	Description    string          `json:"description"`
	Unit           string          `json:"unit"`
	Classification UsageDisclosure `json:"classification"`
}

// ValidateUsageValue validates a value against the exact registered usage
// contract, its byte bound, and its frozen JSON Schema. Errors intentionally
// omit payload bytes and schema validator details.
func (s *Snapshot) ValidateUsageValue(value UsageValue) error {
	return s.ValidateUsageValueWithin(value, usageValueDefaultMaxBytes)
}

// ValidateUsageValueWithin validates a usage value under the smaller of its
// descriptor limit and the host's per-value limit. Hosts may explicitly allow
// larger values without changing the registered contract.
func (s *Snapshot) ValidateUsageValueWithin(value UsageValue, maxBytes int64) error {
	if s == nil {
		return fmt.Errorf("usage catalog snapshot is unavailable")
	}
	if maxBytes <= 0 {
		return fmt.Errorf("host usage value byte limit must be positive")
	}
	if err := value.TypeRef.Validate(); err != nil || value.TypeRef.Kind != UsageKind {
		return fmt.Errorf("usage value requires an exact usage contract reference")
	}
	item, ok := s.registrations[value.TypeRef]
	if !ok || item.descriptor.Ref.Kind != UsageKind || item.descriptor.UsagePolicy == nil || item.descriptor.InputSchemaRef == nil {
		return fmt.Errorf("usage contract is not registered")
	}
	effectiveMaxBytes := maxBytes
	if item.descriptor.UsagePolicy.MaxValueBytes < effectiveMaxBytes {
		effectiveMaxBytes = item.descriptor.UsagePolicy.MaxValueBytes
	}
	if int64(len(value.Value)) > effectiveMaxBytes {
		return fmt.Errorf("usage value exceeds its registered byte limit")
	}
	if !json.Valid(value.Value) {
		return fmt.Errorf("usage value is not valid JSON")
	}
	schema, ok := s.compiled[*item.descriptor.InputSchemaRef]
	if !ok {
		return fmt.Errorf("usage contract schema is unavailable")
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(value.Value))
	if err != nil || schema.Validate(instance) != nil {
		return fmt.Errorf("usage value does not satisfy its registered contract")
	}
	return nil
}

func validateUsageDescriptorSize(descriptor Descriptor) error {
	if descriptor.Ref.Kind != UsageKind {
		return nil
	}
	if len(descriptor.SetupView) > usageDescriptorMaxBytes {
		return fmt.Errorf("usage setup view exceeds the descriptor byte limit")
	}
	serialized, err := json.Marshal(descriptor)
	if err != nil {
		return fmt.Errorf("usage descriptor cannot be serialized")
	}
	if len(serialized) > usageDescriptorMaxBytes {
		return fmt.Errorf("usage descriptor exceeds the total byte limit")
	}
	return nil
}

// UsageDescriptor returns a metadata-only view bound to the registered schema
// identity and digest. It never returns a factory or a usage payload.
func (s *Snapshot) UsageDescriptor(ref Ref) (UsageDescriptorSnapshot, bool) {
	if s == nil || ref.Validate() != nil || ref.Kind != UsageKind {
		return UsageDescriptorSnapshot{}, false
	}
	item, ok := s.registrations[ref]
	if !ok || item.descriptor.UsagePolicy == nil || item.descriptor.InputSchemaRef == nil {
		return UsageDescriptorSnapshot{}, false
	}
	schema, ok := s.schemas[*item.descriptor.InputSchemaRef]
	if !ok {
		return UsageDescriptorSnapshot{}, false
	}
	return UsageDescriptorSnapshot{
		Ref: ref, SchemaRef: schema.Ref, SchemaDigest: schema.Digest,
		Label: item.descriptor.DisplayName, Description: item.descriptor.Description,
		Unit: item.descriptor.UsagePolicy.Unit, Classification: item.descriptor.UsagePolicy.Disclosure,
	}, true
}

func prepareUsagePolicy(descriptor *Descriptor) error {
	if descriptor.Ref.Kind != UsageKind {
		if descriptor.UsagePolicy != nil {
			return fmt.Errorf("only usage-value descriptors may declare a usage policy")
		}
		return nil
	}
	if descriptor.UsagePolicy == nil || descriptor.InputSchemaRef == nil {
		return fmt.Errorf("usage descriptor requires a policy and exact value schema")
	}
	policy := *descriptor.UsagePolicy
	if policy.Disclosure == "" {
		policy.Disclosure = UsagePrivate
	}
	if policy.Disclosure != UsagePrivate && policy.Disclosure != UsageManagementVisible {
		return fmt.Errorf("usage disclosure mode is unsupported")
	}
	if policy.Unit != "" && (policy.Unit != strings.TrimSpace(policy.Unit) || len(policy.Unit) > 256 || strings.IndexFunc(policy.Unit, unicode.IsControl) >= 0) {
		return fmt.Errorf("usage unit must be trimmed, control-free, and at most 256 bytes")
	}
	if policy.MaxValueBytes <= 0 {
		return fmt.Errorf("usage value byte limit must be positive")
	}
	if len(descriptor.DisplayName) > 256 || len(descriptor.Description) > 4096 {
		return fmt.Errorf("usage label or description exceeds its metadata limit")
	}
	if descriptor.InputSchemaRef.Kind != SchemaKind || descriptor.InputSchemaRef.Validate() != nil {
		return fmt.Errorf("usage descriptor requires an exact schema reference")
	}
	descriptor.UsagePolicy = &policy
	return nil
}
