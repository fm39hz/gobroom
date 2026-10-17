package controlplane

import (
	"testing"

	"github.com/fm39hz/gobroom/internal/store"
)

func TestReloadReceiptReportsPublishedSnapshotWhenVersionPersistenceFails(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/reload-receipt.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	manager, err := NewManager(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`CREATE TRIGGER reject_snapshot_version BEFORE UPDATE ON settings WHEN OLD.key='snapshot_version' BEGIN SELECT RAISE(FAIL, 'version write rejected'); END;`); err != nil {
		t.Fatal(err)
	}
	result, err := manager.Reload()
	if err == nil {
		t.Fatal("expected version persistence error")
	}
	if result.Version != manager.Snapshot().Version || result.Version != manager.Version() || !result.Published || result.VersionPersisted || result.FailureStage != "version_persist" {
		t.Fatalf("reload receipt=%#v snapshot=%d managerVersion=%d", result, manager.Snapshot().Version, manager.Version())
	}
}

func TestReloadReceiptReportsLoadFailureWithoutPublication(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/reload-load-failure.db")
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(db)
	if err != nil {
		t.Fatal(err)
	}
	previous := manager.Snapshot().Version
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := manager.Reload()
	if err == nil || result.Published || result.FailureStage != "load" || result.Version != previous || manager.Snapshot().Version != previous {
		t.Fatalf("result=%#v snapshot=%d err=%v", result, manager.Snapshot().Version, err)
	}
}
