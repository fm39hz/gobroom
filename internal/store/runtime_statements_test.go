package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
)

func TestPreparedRuntimeStatementsKeepLiveCredentialAuthority(t *testing.T) {
	s, connection := newCredentialRevisionFixture(t)
	defer s.Close()
	ctx := context.Background()
	before, err := s.ConnectionCredentialBinding(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateConnection(UpdateConnectionInput{ID: connection.ID, Secret: "replacement"}); err != nil {
		t.Fatal(err)
	}
	after, err := s.ConnectionCredentialBinding(ctx, connection.ID)
	if err != nil || before.Revision == after.Revision || before.PrincipalGeneration == after.PrincipalGeneration {
		t.Fatalf("prepared read cached authority: before=%#v after=%#v err=%v", before, after, err)
	}
	credential, ok := s.ConnectionCredentialByID(connection.ID)
	if !ok || credential.Secret != "replacement" || credential.Revision != after.Revision {
		t.Fatal("prepared credential read returned stale material")
	}
	transaction, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := readCredentialBinding(ctx, transaction, connection.ID)
	_ = transaction.Rollback()
	if err != nil || bound != after {
		t.Fatalf("transaction binding differs: %#v err=%v", bound, err)
	}
	disabled := false
	if _, err := s.UpdateConnection(UpdateConnectionInput{ID: connection.ID, Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConnectionCredentialBinding(ctx, connection.ID); err == nil {
		t.Fatal("disabled credential passed prepared authority read")
	}
	if _, ok := s.ConnectionCredentialByID(connection.ID); ok {
		t.Fatal("disabled credential remained readable")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.ConnectionCredentialBinding(cancelled, connection.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled prepared read=%v", err)
	}
}

func TestPreparedUsageStatementsPreserveAtomicRollbackAndLifecycle(t *testing.T) {
	s, err := Open(t.TempDir() + "/statements.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.DB.SetMaxOpenConns(1) // Tx.Stmt must reuse the transaction connection.
	if _, err := s.DB.Exec(`CREATE TRIGGER reject_daily BEFORE INSERT ON usage_daily BEGIN SELECT RAISE(FAIL,'fixture rejection'); END`); err != nil {
		t.Fatal(err)
	}
	event := kernel.UsageEvent{At: time.Now(), Status: "ok", InputTokens: 7, OutputTokens: 2}
	if err := s.SaveUsageEvent(event); err == nil {
		t.Fatal("failed daily write was accepted")
	}
	var count int
	if err := s.DB.QueryRow(`SELECT count(*) FROM usage_events`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("transaction rollback lost: count=%d err=%v", count, err)
	}
	if _, err := s.DB.Exec(`DROP TRIGGER reject_daily`); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveUsageEvent(event); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT count(*) FROM usage_events`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("prepared usage write failed: count=%d err=%v", count, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveUsageEvent(event); err == nil {
		t.Fatal("store accepted a write after Close")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("repeated Close=%v", err)
	}
}
