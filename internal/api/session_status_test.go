package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/fm39hz/gobroom/internal/sessionpersistence"
	"github.com/fm39hz/gobroom/internal/store"
)

func TestSessionPersistenceStatusProjectionAndFaultHistory(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input sessionpersistence.Status
		want  string
	}{
		{"healthy", sessionpersistence.Status{Received: 3, Persisted: 3}, "ok"},
		{"expected discard", sessionpersistence.Status{Received: 3, Persisted: 2, Discarded: 1}, "ok"},
		{"write failure", sessionpersistence.Status{Received: 3, Persisted: 2, Failed: 1}, "degraded"},
		{"queue rejection", sessionpersistence.Status{QueueDropped: 1}, "degraded"},
		{"save error", sessionpersistence.Status{SaveErrors: 1}, "degraded"},
		{"later success retains evidence", sessionpersistence.Status{Received: 3, Persisted: 2, Failed: 1, SaveErrors: 1}, "degraded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, err := store.Open(t.TempDir() + "/state.db")
			if err != nil {
				t.Fatal(err)
			}
			defer state.Close()
			server := NewServer(state)
			server.SetSessionPersistenceObserver(func() sessionpersistence.Status { return tc.input })
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/status", nil))
			if response.Code != http.StatusOK {
				t.Fatalf("HTTP status=%d", response.Code)
			}
			var body struct {
				Status  string                    `json:"status"`
				Session sessionpersistence.Status `json:"sessionPersistence"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Status != tc.want || body.Session != tc.input {
				t.Fatalf("status projection=%s", response.Body.String())
			}
		})
	}
}

func TestSessionPersistenceProjectionContainsOnlyCountersAndPreservesDegradation(t *testing.T) {
	server := &Server{}
	server.SetSessionPersistenceObserver(func() sessionpersistence.Status { return sessionpersistence.Status{SaveErrors: 9007199254740993} })
	result := server.Status()
	if result["status"] != "degraded" {
		t.Fatal("session observer erased control-plane degradation")
	}
	encoded, err := json.Marshal(result["sessionPersistence"])
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"received": "0", "persisted": "0", "failed": "0", "discarded": "0", "queueDropped": "0", "saveErrors": "9007199254740993"}
	got := make(map[string]string, len(fields))
	for key, value := range fields {
		got[key] = string(value)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unsafe or lossy status fields: %s", encoded)
	}
	server.SetSessionPersistenceObserver(func() sessionpersistence.Status { return sessionpersistence.Status{} })
	if server.Status()["status"] != "degraded" {
		t.Fatal("healthy session counters cleared missing control plane")
	}
}
