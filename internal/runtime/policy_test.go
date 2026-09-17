package runtime

import (
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/quota"
)

func TestPolicyGateBlocksExhaustedQuota(t *testing.T) {
	gate := NewPolicyGate()
	route := kernel.Route{ID: "r", NodeID: "node", ExternalModel: "model", Enabled: true}
	remaining := float64(0)
	reset := time.Now().Add(time.Hour)
	gate.SetQuota(quota.Snapshot{ProviderNodeID: "node", ModelRef: "model", WindowName: "daily", Remaining: &remaining, ResetAt: &reset})
	if gate.Usable(route, time.Now()) {
		t.Fatal("exhausted quota should block route")
	}
}
