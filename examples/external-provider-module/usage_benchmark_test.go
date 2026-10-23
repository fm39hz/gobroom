package externalprovider

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/pkg/provider"
)

const (
	benchmarkMaxExtensionBytes int64 = 64 << 10
	benchmarkMaxRawBytes       int64 = 256 << 10
)

var (
	benchmarkUsageSink   provider.UsageEvent
	benchmarkRawSink     *provider.RawUsageFragment
	benchmarkValueSink   provider.UsageValue
	benchmarkEncodedSink int64
)

func BenchmarkUsageCommon(b *testing.B) {
	input, output, cached := int64(128), int64(32), int64(16)
	event := provider.UsageEvent{InputTokens: &input, OutputTokens: &output, CachedReadTokens: &cached}
	b.ReportAllocs()
	for b.Loop() {
		benchmarkUsageSink = event.Clone()
	}
}

func BenchmarkUsageExtensions(b *testing.B) {
	snapshot := benchmarkUsageSnapshot(b)
	small := provider.UsageValue{TypeRef: benchmarkUsageRef, Value: json.RawMessage(`{"units":9007199254740993}`)}
	ceiling := benchmarkSizedUsageValue(b, snapshot)
	oversizePayload := int(benchmarkMaxExtensionBytes) - len(`{"payload":""}`) + 1
	oversize := provider.UsageValue{TypeRef: benchmarkUsageRef, Value: json.RawMessage(`{"payload":"` + strings.Repeat("x", oversizePayload) + `"}`)}
	for _, scenario := range []struct {
		name  string
		value provider.UsageValue
		limit int64
		valid bool
	}{{"small", small, benchmarkMaxExtensionBytes, true}, {"ceiling", ceiling, benchmarkMaxExtensionBytes, true}, {"rejected_oversize", oversize, benchmarkMaxExtensionBytes, false}} {
		b.Run(scenario.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				err := snapshot.ValidateUsageValueWithin(scenario.value, scenario.limit)
				if (err == nil) != scenario.valid {
					b.Fatal("unexpected extension validation result")
				}
				owned, size, err := scenario.value.OwnedEncoding(scenario.limit)
				if scenario.valid {
					if err != nil {
						b.Fatal(err)
					}
					benchmarkValueSink, benchmarkEncodedSink = owned, size
					benchmarkUsageSink = (provider.UsageEvent{Extensions: []provider.UsageValue{owned}}).Clone()
				} else {
					if err == nil {
						b.Fatal("oversized extension passed owned encoding")
					}
					benchmarkValueSink, benchmarkEncodedSink = owned, size
				}
			}
		})
	}
}

func BenchmarkUsageRawCapture(b *testing.B) {
	providerRef := provider.Ref{Kind: "provider-definition", ID: "benchmark-provider", ContractVersion: 1}
	small := json.RawMessage(`{"input_tokens":9007199254740993}`)
	ceiling := benchmarkRawObject(int(benchmarkMaxRawBytes))
	oversize := benchmarkRawObject(int(benchmarkMaxRawBytes) + 1)
	for _, scenario := range []struct {
		name   string
		policy provider.UsageCapturePolicy
		value  json.RawMessage
		retain bool
	}{{"disabled", provider.UsageCapturePolicy{Enabled: false, MaxBytes: benchmarkMaxRawBytes}, small, false}, {"small", provider.UsageCapturePolicy{Enabled: true, MaxBytes: benchmarkMaxRawBytes}, small, true}, {"ceiling", provider.UsageCapturePolicy{Enabled: true, MaxBytes: benchmarkMaxRawBytes}, ceiling, true}, {"rejected_oversize", provider.UsageCapturePolicy{Enabled: true, MaxBytes: benchmarkMaxRawBytes}, oversize, false}} {
		b.Run(scenario.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				fragment, issues := provider.SelectRawUsage(scenario.policy, providerRef, "chat", scenario.value)
				if (fragment != nil) != scenario.retain || (scenario.retain && len(issues) != 0) || (!scenario.retain && len(issues) != 0 && scenario.name == "disabled") {
					b.Fatal("unexpected raw usage selection result")
				}
				benchmarkRawSink = fragment
			}
		})
	}
}

var benchmarkUsageRef = provider.Ref{Kind: provider.UsageKind, ID: "example.benchmark-usage", ContractVersion: 1}

type usageBenchmarkModule struct{}

func (usageBenchmarkModule) ModuleDescriptor() provider.ModuleRef {
	return provider.ModuleRef{ID: "example.usage-benchmark", Version: "1", HostContractVersion: provider.RuntimeModuleHostContractVersion}
}

func (usageBenchmarkModule) Register(runtime *provider.RuntimeRegistry) error {
	schemaRef := provider.Ref{Kind: provider.SchemaKind, ID: "example.benchmark-usage.schema", ContractVersion: 1}
	if err := runtime.RegisterSchemaDocument(schemaRef, json.RawMessage(`{"type":"object","properties":{"units":{"type":"integer","minimum":0},"payload":{"type":"string","maxLength":65536}},"additionalProperties":false}`)); err != nil {
		return err
	}
	return runtime.RegisterExtension(provider.ExtensionDescriptor{
		Ref: benchmarkUsageRef, ImplementationVersion: "1", DisplayName: "Benchmark usage",
		Description: "Bounded public API accounting benchmark fixture.", InputSchemaRef: &schemaRef,
		UsagePolicy: &provider.UsagePolicy{Unit: "fixture", MaxValueBytes: benchmarkMaxExtensionBytes},
	}, nil)
}

func benchmarkUsageSnapshot(tb testing.TB) interface {
	ValidateUsageValueWithin(provider.UsageValue, int64) error
} {
	tb.Helper()
	runtime, err := provider.NewRuntimeRegistryWithModules(usageBenchmarkModule{})
	if err != nil {
		tb.Fatal(err)
	}
	snapshot, err := runtime.FreezeCatalog()
	if err != nil {
		tb.Fatal(err)
	}
	return snapshot
}

func benchmarkSizedUsageValue(tb testing.TB, snapshot interface {
	ValidateUsageValueWithin(provider.UsageValue, int64) error
}) provider.UsageValue {
	tb.Helper()
	empty := provider.UsageValue{TypeRef: benchmarkUsageRef, Value: json.RawMessage(`{"payload":""}`)}
	_, emptySize, err := empty.OwnedEncoding(benchmarkMaxExtensionBytes)
	if err != nil {
		tb.Fatal(err)
	}
	payloadSize := int(benchmarkMaxExtensionBytes - emptySize)
	value := provider.UsageValue{TypeRef: benchmarkUsageRef, Value: json.RawMessage(`{"payload":"` + strings.Repeat("x", payloadSize) + `"}`)}
	_, size, err := value.OwnedEncoding(benchmarkMaxExtensionBytes)
	if err != nil || size != benchmarkMaxExtensionBytes || snapshot.ValidateUsageValueWithin(value, benchmarkMaxExtensionBytes) != nil {
		tb.Fatalf("could not construct exact extension ceiling fixture: encoded=%d err=%v", size, err)
	}
	return value
}

func benchmarkRawObject(size int) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"padding":"%s"}`, strings.Repeat("x", size-len(`{"padding":""}`))))
}

func TestUsageAccountingBenchmarkBounds(t *testing.T) {
	snapshot := benchmarkUsageSnapshot(t)
	exact := benchmarkSizedUsageValue(t, snapshot)
	owned, encodedSize, err := exact.OwnedEncoding(benchmarkMaxExtensionBytes)
	if err != nil || encodedSize != benchmarkMaxExtensionBytes || snapshot.ValidateUsageValueWithin(owned, benchmarkMaxExtensionBytes) != nil {
		t.Fatalf("extension at configured ceiling was not retained exactly: size=%d err=%v", encodedSize, err)
	}
	retained := (provider.UsageEvent{Extensions: []provider.UsageValue{owned}}).Clone()
	if len(retained.Extensions) != 1 || len(retained.Extensions[0].Value) > int(benchmarkMaxExtensionBytes) {
		t.Fatal("cloned extension retention exceeded its configured byte bound")
	}
	tooLargeValue := int(benchmarkMaxExtensionBytes) - len(`{"payload":""}`) + 1
	tooLarge := provider.UsageValue{TypeRef: benchmarkUsageRef, Value: json.RawMessage(`{"payload":"` + strings.Repeat("x", tooLargeValue) + `"}`)}
	if _, _, err := tooLarge.OwnedEncoding(benchmarkMaxExtensionBytes); err == nil || snapshot.ValidateUsageValueWithin(tooLarge, benchmarkMaxExtensionBytes) == nil {
		t.Fatal("oversized extension passed bounded admission helpers")
	}

	providerRef := provider.Ref{Kind: "provider-definition", ID: "benchmark-provider", ContractVersion: 1}
	policy := provider.UsageCapturePolicy{Enabled: true, MaxBytes: benchmarkMaxRawBytes}
	for name, value := range map[string]json.RawMessage{"exact": benchmarkRawObject(int(benchmarkMaxRawBytes)), "oversize": benchmarkRawObject(int(benchmarkMaxRawBytes) + 1)} {
		fragment, issues := provider.SelectRawUsage(policy, providerRef, "chat", value)
		if name == "exact" {
			if fragment == nil || len(fragment.Value) != int(benchmarkMaxRawBytes) || len(issues) != 0 {
				t.Fatalf("raw usage at configured ceiling was not retained exactly: fragment=%#v issues=%#v", fragment, issues)
			}
		} else if fragment != nil || len(issues) == 0 {
			t.Fatal("oversized raw usage was retained instead of rejected")
		}
	}
	disabled, issues := provider.SelectRawUsage(provider.UsageCapturePolicy{Enabled: false, MaxBytes: benchmarkMaxRawBytes}, providerRef, "chat", benchmarkRawObject(int(benchmarkMaxRawBytes)))
	if disabled != nil || len(issues) != 0 {
		t.Fatal("disabled raw capture retained bytes or emitted an issue")
	}
}
