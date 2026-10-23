package externalprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	"github.com/fm39hz/gobroom/pkg/provider"
)

var (
	searchUsageContractRef = provider.Ref{Kind: provider.UsageKind, ID: "example.search-units", ContractVersion: 1}
	searchUsageSchemaRef   = provider.Ref{Kind: provider.SchemaKind, ID: "example.search-units.value", ContractVersion: 1}
	searchTransformOptRef  = provider.Ref{Kind: provider.SchemaKind, ID: "example.search-usage-transform.options", ContractVersion: 1}
)

const (
	searchUsageSchema            = `{"type":"object","properties":{"units":{"type":"number","minimum":0},"marker":{"type":"string","maxLength":128}},"required":["units","marker"],"additionalProperties":false}`
	searchTransformOptionsSchema = `{"type":"object","properties":{"outputAdjustment":{"type":"integer","minimum":0,"maximum":1000}},"required":["outputAdjustment"],"additionalProperties":false}`
	searchUnitsHeader            = "X-Search-Units"
	searchMarkerHeader           = "X-Search-Marker"
)

// SearchUsageModule demonstrates a typed, private provider usage value and a
// client-only usage transform using the public module contracts.
type SearchUsageModule struct{}

func (SearchUsageModule) ModuleDescriptor() provider.ModuleRef {
	return provider.ModuleRef{
		ID: "example.search-usage", Version: "1.0.0",
		HostContractVersion: provider.RuntimeModuleHostContractVersion,
	}
}

func (SearchUsageModule) Register(runtime *provider.RuntimeRegistry) error {
	if err := runtime.RegisterSchemaDocument(searchUsageSchemaRef, json.RawMessage(searchUsageSchema)); err != nil {
		return err
	}
	if err := runtime.RegisterExtension(provider.ExtensionDescriptor{
		Ref: searchUsageContractRef, ImplementationVersion: "1",
		DisplayName:    "Search units",
		Description:    "Provider-reported search units; no automatic aggregation or pricing.",
		InputSchemaRef: &searchUsageSchemaRef,
		UsagePolicy:    &provider.UsagePolicy{Unit: "search", Disclosure: provider.UsagePrivate, MaxValueBytes: 256},
	}, nil); err != nil {
		return err
	}
	if err := runtime.RegisterResponseTransform(searchUsageResponseTransform{}, json.RawMessage(searchTransformOptionsSchema)); err != nil {
		return err
	}
	return runtime.RegisterUsageSource(searchUsageSource{})
}

type searchUsageSource struct{}

func (searchUsageSource) ID() string { return "example.search-usage-source" }

func (searchUsageSource) EnrichUsage(_ context.Context, _ provider.Route, evidence provider.UsageEvidence, event provider.UsageEvent) (provider.UsageEvent, error) {
	units := evidence.Headers.Values(searchUnitsHeader)
	markers := evidence.Headers.Values(searchMarkerHeader)
	if len(units) == 0 && len(markers) == 0 {
		return event, nil
	}
	invalid := func() (provider.UsageEvent, error) {
		event.AccountingIssues = append(event.AccountingIssues, provider.UsageIssue{Code: "invalid_search_usage", TypeRef: searchUsageContractRef})
		return event, nil
	}
	if len(units) != 1 || len(markers) != 1 {
		return invalid()
	}
	value := struct {
		Units  json.Number `json:"units"`
		Marker string      `json:"marker"`
	}{Units: json.Number(units[0]), Marker: markers[0]}
	encoded, err := json.Marshal(value)
	if err != nil {
		return invalid()
	}
	event.Extensions = append(event.Extensions, provider.UsageValue{TypeRef: searchUsageContractRef, Value: encoded})
	return event, nil
}

type searchUsageResponseTransform struct{}

func (searchUsageResponseTransform) Definition() provider.ResponseTransformDefinition {
	return provider.ResponseTransformDefinition{
		Ref:                   provider.Ref{Kind: provider.ResponseTransformKind, ID: "example.search-usage-transform", ContractVersion: 1},
		ImplementationVersion: "1",
		Label:                 "Adjust client usage projection",
		Description:           "Adds a configured adjustment to client-facing output usage only.",
		OptionsSchemaRef:      &searchTransformOptRef,
		Effects:               []provider.ResponseTransformEffect{provider.ResponseEffectUsage},
	}
}

func (searchUsageResponseTransform) ApplyResponse(_ context.Context, event provider.ResponseEvent, raw json.RawMessage) (provider.ResponseEvent, error) {
	var options struct {
		OutputAdjustment int64 `json:"outputAdjustment"`
	}
	if err := json.Unmarshal(raw, &options); err != nil {
		return event, err
	}
	if event.Kind != provider.EventUsage || event.Usage == nil || event.Usage.OutputTokens == nil {
		return event, nil
	}
	if options.OutputAdjustment < 0 || *event.Usage.OutputTokens > math.MaxInt64-options.OutputAdjustment {
		return event, fmt.Errorf("usage adjustment is outside the supported range")
	}
	event.Usage.OutputTokens = new(*event.Usage.OutputTokens + options.OutputAdjustment)
	return event, nil
}
