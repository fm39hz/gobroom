package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/store"
)

func reloadReceiptServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/reload-api.db")
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(db)
	if err := server.InitializationError(); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return server, db
}

func TestModelsProjectionFollowsActualServingPublication(t *testing.T) {
	server, db := reloadReceiptServer(t)
	defer db.Close()
	previous := kernel.Snapshot{Version: server.Control().Version(), PublicModels: map[string]kernel.PublicModel{"still-serving": {Name: "still-serving", OwnedBy: "fixture"}}}
	server.servingSnapshot.Store(&previous)
	server.SetReloadHook(func(kernel.Snapshot) (kernel.Snapshot, error) { return previous, errors.New("publication rejected") })
	if _, err := server.Reload(); err == nil {
		t.Fatal("fixture did not fail publication")
	}
	getModels := func() string {
		response := httptest.NewRecorder()
		server.models(response, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
		return response.Body.String()
	}
	if body := getModels(); !strings.Contains(body, "still-serving") {
		t.Fatalf("discovery lost actual serving models: %s", body)
	}
	server.SetReloadHook(func(next kernel.Snapshot) (kernel.Snapshot, error) { return next, nil })
	if _, err := server.Reload(); err != nil {
		t.Fatal(err)
	}
	if body := getModels(); strings.Contains(body, "still-serving") {
		t.Fatalf("discovery retained superseded serving models: %s", body)
	}
}

func TestReloadCallsHookAfterPublishedVersionPersistenceFailure(t *testing.T) {
	server, db := reloadReceiptServer(t)
	defer db.Close()
	if _, err := db.DB.Exec(`CREATE TRIGGER reject_snapshot_version BEFORE UPDATE ON settings WHEN OLD.key='snapshot_version' BEGIN SELECT RAISE(FAIL, 'version write rejected'); END;`); err != nil {
		t.Fatal(err)
	}
	called := false
	server.SetReloadHook(func(snapshot kernel.Snapshot) (kernel.Snapshot, error) { called = true; return snapshot, nil })
	result, err := server.Reload()
	if err == nil || !called || !result.Control.Published || result.Control.VersionPersisted || !result.ServingPublished || result.ServingVersion != result.Control.Version {
		t.Fatalf("result=%#v called=%v err=%v", result, called, err)
	}
	status := server.Status()
	if status["controlSnapshotVersion"] != result.Control.Version || status["servingSnapshotVersion"] != result.Control.Version || status["status"] != "degraded" {
		t.Fatalf("status after persistence failure=%#v", status)
	}
}

func TestReloadHookFailureReportsActualServingVersionAndDegradedStatus(t *testing.T) {
	server, db := reloadReceiptServer(t)
	defer db.Close()
	failure := errors.New("serve publish failed")
	server.SetReloadHook(func(snapshot kernel.Snapshot) (kernel.Snapshot, error) {
		return kernel.Snapshot{Version: snapshot.Version - 1}, failure
	})
	result, err := server.Reload()
	if !errors.Is(err, failure) || result.ServingVersion != result.Control.Version-1 || result.ServingPublished || result.FailureStage != "serve_publish" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	status := server.Status()
	if status["status"] != "degraded" || status["controlSnapshotVersion"] != result.Control.Version || status["servingSnapshotVersion"] != result.ServingVersion {
		t.Fatalf("status after hook failure=%#v", status)
	}
}

func TestReloadHookErrorCanReportThatSnapshotIsAlreadyServing(t *testing.T) {
	server, db := reloadReceiptServer(t)
	defer db.Close()
	failure := errors.New("hook reported post publish failure")
	server.SetReloadHook(func(snapshot kernel.Snapshot) (kernel.Snapshot, error) { return snapshot, failure })
	result, err := server.Reload()
	if !errors.Is(err, failure) || result.ServingVersion != result.Control.Version || !result.ServingPublished {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	status := server.Status()
	if status["status"] != "degraded" || status["servingSnapshotVersion"] != result.ServingVersion {
		t.Fatalf("status=%#v", status)
	}
}

func TestReloadWithoutHookTreatsControlSnapshotAsServing(t *testing.T) {
	server, db := reloadReceiptServer(t)
	defer db.Close()
	result, err := server.Reload()
	if err != nil || !result.ServingPublished || result.ServingVersion != result.Control.Version {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestSuccessfulHookMustReportControlVersion(t *testing.T) {
	server, db := reloadReceiptServer(t)
	defer db.Close()
	server.SetReloadHook(func(snapshot kernel.Snapshot) (kernel.Snapshot, error) {
		return kernel.Snapshot{Version: snapshot.Version - 1}, nil
	})
	result, err := server.Reload()
	if err == nil || result.FailureStage != "serve_publish" || result.ServingPublished || result.ServingVersion == result.Control.Version {
		t.Fatalf("mismatched successful hook result=%#v err=%v", result, err)
	}
	status := server.Status()
	if status["status"] != "degraded" || status["servingSnapshotVersion"] != result.ServingVersion {
		t.Fatalf("status after mismatched hook result=%#v", status)
	}
}

func TestNoHookStatusTracksDirectManagerReload(t *testing.T) {
	server, db := reloadReceiptServer(t)
	defer db.Close()
	result, err := server.Control().Reload()
	if err != nil {
		t.Fatal(err)
	}
	status := server.Status()
	if status["controlSnapshotVersion"] != result.Version || status["servingSnapshotVersion"] != result.Version || status["status"] != "ok" {
		t.Fatalf("status after direct manager reload=%#v receipt=%#v", status, result)
	}
}

func TestStatusDegradesWhenLastReloadFailedEvenAtMatchingVersions(t *testing.T) {
	server, db := reloadReceiptServer(t)
	defer db.Close()
	if _, err := db.DB.Exec(`CREATE TRIGGER reject_snapshot_version BEFORE UPDATE ON settings WHEN OLD.key='snapshot_version' BEGIN SELECT RAISE(FAIL, 'version write rejected'); END;`); err != nil {
		t.Fatal(err)
	}
	result, err := server.Reload()
	if err == nil {
		t.Fatal("expected reload failure")
	}
	status := server.Status()
	if result.Control.Version != result.ServingVersion || status["status"] != "degraded" {
		t.Fatalf("status after failed reload=%#v result=%#v err=%v", status, result, err)
	}
}
