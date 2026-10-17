package store

import (
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/quota"
)

func TestOperationRuntimeAndQuotaPersistenceAreExactAndResettable(t *testing.T) {
	dbPath := t.TempDir() + "/operations.db"
	s, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err := s.DB.Exec(`INSERT INTO provider_nodes(id,name,base_url,protocol,prefix) VALUES('node','Provider','http://provider','openai_chat','p'); INSERT INTO connections(id,provider_node_id,name,credential_type) VALUES('conn','node','Account','api_key'); INSERT INTO connections(id,provider_node_id,name,credential_type) VALUES('other-conn','node','Other','api_key')`); err != nil {
		t.Fatal(err)
	}

	op1 := extensions.Ref{Kind: "operation", ID: "messages", ContractVersion: 1}
	op2 := extensions.Ref{Kind: "operation", ID: "messages", ContractVersion: 2}
	newer := time.Date(2026, 5, 2, 3, 4, 5, 0, time.UTC)
	for _, item := range []OperationRuntime{
		{ConnectionID: "conn", ModelRef: "model", OperationRef: op1, Status: "blocked", Generation: "g1", Failures: 3, LastError: "latest", ObservedAt: newer},
		{ConnectionID: "conn", ModelRef: "model", OperationRef: op2, Status: "invalid", Generation: "g1", Failures: 7, ObservedAt: newer},
		{ConnectionID: "other-conn", ModelRef: "model", OperationRef: op1, Status: "blocked", Generation: "g1", Failures: 5, ObservedAt: newer},
	} {
		if err := s.SaveOperationRuntime(item); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SaveOperationRuntime(OperationRuntime{ConnectionID: "conn", ModelRef: "model", OperationRef: op1, Status: "available", Generation: "g1", Failures: 0, LastError: "stale", ObservedAt: newer.Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}
	runtimes, err := s.OperationRuntimes()
	if err != nil {
		t.Fatal(err)
	}
	if len(runtimes) != 3 {
		t.Fatalf("operation identities collapsed: %#v", runtimes)
	}
	for _, item := range runtimes {
		if item.ConnectionID == "conn" && item.OperationRef == op1 && (item.Status != "blocked" || item.LastError != "latest" || !item.ObservedAt.Equal(newer)) {
			t.Fatalf("older operation observation replaced newer row: %#v", item)
		}
	}

	remaining := 8.0
	used := 4.0
	base := quota.Snapshot{ProviderNodeID: "node", ConnectionID: "conn", ModelRef: "model", OperationRef: op1, WindowName: "daily", Generation: "q1", Authority: quota.AuthorityResponseHeader, ObservedAt: newer, Used: used, Remaining: &remaining, Source: "header"}
	if err := s.SaveQuotaSnapshot(base); err != nil {
		t.Fatal(err)
	}
	base.OperationRef = op2
	base.Used = 20
	if err := s.SaveQuotaSnapshot(base); err != nil {
		t.Fatal(err)
	}
	base.OperationRef = op1
	base.WindowName = "weekly"
	base.Used = 30
	if err := s.SaveQuotaSnapshot(base); err != nil {
		t.Fatal(err)
	}
	base.OperationRef = extensions.Ref{}
	base.WindowName = "daily"
	base.Used = 40
	if err := s.SaveQuotaSnapshot(base); err != nil {
		t.Fatal(err)
	}

	newerPartial := base
	newerPartial.OperationRef = op1
	newerPartial.WindowName = "daily"
	newerPartial.ObservedAt = newer.Add(time.Second)
	newerPartial.Authority = quota.AuthorityQuotaAPI
	newerPartial.Source = "quota-api"
	newerPartial.Remaining = nil
	newerPartial.Used = 5
	if err := s.SaveQuotaSnapshot(newerPartial); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	older := base
	older.OperationRef = op1
	older.ObservedAt = newer.Add(-time.Minute)
	older.Used = 99
	if err := s.SaveQuotaSnapshot(older); err != nil {
		t.Fatal(err)
	}
	snapshots, err := s.QuotaSnapshots()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 4 {
		t.Fatalf("expected operation versions/windows plus a broad snapshot, got %#v", snapshots)
	}
	for _, item := range snapshots {
		if item.OperationRef == op1 && item.WindowName == "daily" && (item.Used != 5 || item.Remaining == nil || *item.Remaining != remaining || item.Authority != quota.AuthorityQuotaAPI || item.ObservedAt != newer.Add(time.Second)) {
			t.Fatalf("stale or partial operation quota replaced winner: %#v", item)
		}
		if item.OperationRef == (extensions.Ref{}) && item.Used != 40 {
			t.Fatalf("operation quota overwrote broad quota scope: %#v", item)
		}
	}

	if err := s.ResetConnectionRoutingState("conn"); err != nil {
		t.Fatal(err)
	}
	runtimes, err = s.OperationRuntimes()
	if err != nil || len(runtimes) != 1 || runtimes[0].ConnectionID != "other-conn" {
		t.Fatalf("connection reset did not remove its operation health: %#v err=%v", runtimes, err)
	}
	snapshots, err = s.QuotaSnapshots()
	if err != nil || len(snapshots) != 0 {
		t.Fatalf("connection reset did not remove scoped and broad quota: %#v err=%v", snapshots, err)
	}

	if err := s.SaveOperationRuntime(OperationRuntime{ConnectionID: "conn", ModelRef: "model", OperationRef: op1, Status: "blocked", Generation: "g1", ObservedAt: newer}); err != nil {
		t.Fatal(err)
	}
	base.OperationRef = op1
	if err := s.SaveQuotaSnapshot(base); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetProviderRoutingState("node"); err != nil {
		t.Fatal(err)
	}
	runtimes, err = s.OperationRuntimes()
	if err != nil || len(runtimes) != 0 {
		t.Fatalf("provider reset did not remove operation health: %#v err=%v", runtimes, err)
	}
	snapshots, err = s.QuotaSnapshots()
	if err != nil || len(snapshots) != 0 {
		t.Fatalf("provider reset did not remove operation quota: %#v err=%v", snapshots, err)
	}
}
