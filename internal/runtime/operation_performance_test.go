package runtime

import (
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

func TestOperationRankingIgnoresOtherWorkloadClass(t *testing.T) {
	gate := NewPolicyGate()
	create := operationTestRef(string(normalize.OperationChatGenerate), 1)
	first := operationTestBound(kernel.Route{ID: "first"}, create)
	second := operationTestBound(kernel.Route{ID: "second"}, create)
	now := time.Now()
	for range 2 {
		gate.Performance.Observe(second, kernel.UsageEvent{At: now, Status: "ok", RequestClass: "status-class", Latency: time.Second})
	}
	ordered := gate.RankRoutesFor([]kernel.Route{first, second}, now, "create-class")
	if len(ordered) != 2 || ordered[0].ID != first.ID {
		t.Fatalf("status workload performance promoted a cold create route: %#v", ordered)
	}
	for range 2 {
		gate.Performance.Observe(second, kernel.UsageEvent{At: now, Status: "ok", RequestClass: "create-class", Latency: time.Second})
	}
	ordered = gate.RankRoutesFor([]kernel.Route{first, second}, now, "create-class")
	if len(ordered) != 2 || ordered[0].ID != second.ID {
		t.Fatalf("matching create workload performance did not promote its route: %#v", ordered)
	}
}

func TestOperationAffinityIsPartitionedByExactOperationRef(t *testing.T) {
	gate := NewPolicyGate()
	createV1 := operationTestRef(string(normalize.OperationChatGenerate), 1)
	createV2 := operationTestRef(string(normalize.OperationChatGenerate), 2)
	status := operationTestRef("chat.status", 1)
	firstStatus := operationTestBound(kernel.Route{ID: "first"}, status)
	secondStatus := operationTestBound(kernel.Route{ID: "second"}, status)
	firstCreate := operationTestBound(kernel.Route{ID: "first"}, createV1)
	secondCreate := operationTestBound(kernel.Route{ID: "second"}, createV1)
	firstCreateV2 := operationTestBound(kernel.Route{ID: "first"}, createV2)
	secondCreateV2 := operationTestBound(kernel.Route{ID: "second"}, createV2)
	now := time.Now()
	const sessionID = "fixed-session"
	gate.ObserveUsage(secondStatus, kernel.UsageEvent{At: now, Status: "ok", RequestClass: "status-class", SessionID: sessionID})

	ordered := gate.RankRoutesForSession([]kernel.Route{firstCreate, secondCreate}, now, "create-class", sessionID)
	if len(ordered) != 2 || ordered[0].ID != firstCreate.ID {
		t.Fatalf("status affinity promoted a create operation route: %#v", ordered)
	}
	ordered = gate.RankRoutesForSession([]kernel.Route{firstStatus, secondStatus}, now, "status-class", sessionID)
	if len(ordered) != 2 || ordered[0].ID != secondStatus.ID {
		t.Fatalf("matching status operation did not receive its affinity: %#v", ordered)
	}
	gate.ObserveUsage(secondCreate, kernel.UsageEvent{At: now, Status: "ok", RequestClass: "create-class", SessionID: sessionID})
	ordered = gate.RankRoutesForSession([]kernel.Route{firstCreate, secondCreate}, now, "create-class", sessionID)
	if len(ordered) != 2 || ordered[0].ID != secondCreate.ID {
		t.Fatalf("matching create v1 operation did not receive its affinity: %#v", ordered)
	}
	ordered = gate.RankRoutesForSession([]kernel.Route{firstCreateV2, secondCreateV2}, now, "create-class", sessionID)
	if len(ordered) != 2 || ordered[0].ID != firstCreateV2.ID {
		t.Fatalf("affinity leaked across operation contract versions: %#v", ordered)
	}
}
