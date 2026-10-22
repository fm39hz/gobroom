package kernel

import (
	"encoding/json"
	"testing"

	"github.com/fm39hz/gobroom/internal/extensions"
)

func TestKernelUsageAdmissionOwnsReportedValues(t *testing.T) {
	engine := &Kernel{Events: make(chan UsageEvent, 1)}
	t.Cleanup(engine.Close)
	event := UsageEvent{
		InputTokens: new(int64(11)),
		Extensions: []extensions.UsageValue{{
			TypeRef: extensions.Ref{Kind: extensions.UsageKind, ID: "fixture.owned", ContractVersion: 1},
			Value:   json.RawMessage(`{"units":7}`),
		}},
	}
	engine.UsageCatalog = fixtureUsageCatalog(t, `{"type":"object"}`, event.Extensions[0].TypeRef)
	if !engine.EmitUsage(event) {
		t.Fatal("usage was not admitted")
	}
	*event.InputTokens = 99
	event.Extensions[0].Value[9] = '8'
	stored := <-engine.Events
	if stored.InputTokens == nil || *stored.InputTokens != 11 || len(stored.Extensions) != 1 || string(stored.Extensions[0].Value) != `{"units":7}` {
		t.Fatalf("caller mutation rewrote admitted usage: %#v", stored)
	}
}
