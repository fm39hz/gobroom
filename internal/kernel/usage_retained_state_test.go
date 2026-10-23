package kernel

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/extensions"
)

func TestAttemptUsageRetainedStateRemainsBoundedAcrossManyFrames(t *testing.T) {
	refs := make([]extensions.Ref, 48)
	for index := range refs {
		refs[index] = extensions.Ref{Kind: extensions.UsageKind, ID: fmt.Sprintf("fixture.retained-%02d", index), ContractVersion: 1}
	}
	catalog := fixtureUsageCatalog(t, `{"type":"string"}`, refs...)
	for _, limits := range []UsageLimits{
		{MaxValues: 4, MaxExtensionBytes: 65536},
		{MaxValues: 32, MaxExtensionBytes: 65536},
		{MaxValues: 32, MaxExtensionBytes: 512},
	} {
		t.Run(fmt.Sprintf("values-%d_bytes-%d", limits.MaxValues, limits.MaxExtensionBytes), func(t *testing.T) {
			k := responseFailureKernel(t, nil, "unused")
			k.UsageCatalog, k.UsageLimits = catalog, limits
			attempt := k.newUsageAttempt("bounded-frames", Route{}, NormalizedRequest{})
			for frame := range 1000 {
				payload, err := json.Marshal(strings.Repeat("x", 128))
				if err != nil {
					t.Fatal(err)
				}
				attempt.observe(UsageEvent{InputTokens: new(int64(0)), Extensions: []extensions.UsageValue{{TypeRef: refs[frame%len(refs)], Value: payload}}})
				// A module may immediately reuse its buffer after returning an
				// observation. Retained accounting must already own its data.
				payload[0] = '!'
				retained := attempt.usage
				encoded, err := json.Marshal(retained.Extensions)
				if err != nil {
					t.Fatalf("module buffer mutation changed retained JSON: %v", err)
				}
				retainedBytes := len(encoded)
				for _, value := range retained.Extensions {
					wire, err := json.Marshal(value.Value)
					if err != nil {
						t.Fatal(err)
					}
					retainedBytes += len(value.Value) - len(wire)
				}
				if len(retained.Extensions) > limits.MaxValues || int64(retainedBytes) > limits.MaxExtensionBytes || len(retained.AccountingIssues) > 33 {
					t.Fatalf("frame %d exceeded retained bounds: values=%d bytes=%d issues=%d", frame, len(retained.Extensions), retainedBytes, len(retained.AccountingIssues))
				}
				if retained.InputTokens == nil || *retained.InputTokens != 0 {
					t.Fatal("extension pressure erased known-zero common usage")
				}
			}
			if len(attempt.usage.Extensions) == 0 || len(attempt.usage.AccountingIssues) == 0 {
				t.Fatal("fixture did not exercise both retained values and rejected overflow")
			}
			if limits.MaxExtensionBytes == 65536 && len(attempt.usage.Extensions) != limits.MaxValues {
				t.Fatal("count-bound fixture never reached its configured capacity")
			}
		})
	}
}
