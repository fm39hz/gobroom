package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"

	"github.com/fm39hz/gobroom/internal/store"
	"github.com/fm39hz/gobroom/internal/usage"
)

func TestStatusIncludesHealthyUsagePersistenceSnapshot(t *testing.T) {
	state, err := store.Open(t.TempDir() + "/usage-status.db")
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()

	want := usage.Status{Received: 12, Persisted: 12}
	server := NewServer(state)
	server.SetUsagePersistenceObserver(func() usage.Status { return want })

	status := server.Status()
	if status["status"] != "ok" {
		t.Fatalf("status=%#v, want healthy status", status)
	}
	if got := status["usagePersistence"]; got != want {
		t.Fatalf("usagePersistence=%#v, want %#v", got, want)
	}

	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("HTTP status=%d body=%s", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" || !reflect.DeepEqual(body["usagePersistence"], map[string]any{
		"received": float64(12), "persisted": float64(12), "failed": float64(0),
		"abandoned": float64(0), "queueDropped": float64(0),
	}) {
		t.Fatalf("HTTP status view=%#v", body)
	}
}

func TestUsagePersistenceFailuresDegradeStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status usage.Status
	}{
		{name: "failed", status: usage.Status{Failed: 1}},
		{name: "queue dropped", status: usage.Status{QueueDropped: 1}},
		{name: "abandoned", status: usage.Status{Abandoned: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, err := store.Open(t.TempDir() + "/usage-status.db")
			if err != nil {
				t.Fatal(err)
			}
			defer state.Close()
			server := NewServer(state)
			server.SetUsagePersistenceObserver(func() usage.Status { return tc.status })
			status := server.Status()
			if status["status"] != "degraded" {
				t.Fatalf("status=%#v, want degraded", status)
			}
			if got := status["usagePersistence"]; got != tc.status {
				t.Fatalf("usagePersistence=%#v, want %#v", got, tc.status)
			}
		})
	}
}

func TestUsagePersistenceProjectionContainsOnlyPublicCountersAndPreservesControlDegradation(t *testing.T) {
	server := &Server{} // Missing control plane already degrades the API status.
	server.SetUsagePersistenceObserver(func() usage.Status { return usage.Status{} })

	status := server.Status()
	if status["status"] != "degraded" {
		t.Fatalf("usage observer cleared control degradation: %#v", status)
	}
	encoded, err := json.Marshal(status["usagePersistence"])
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	wantFields := []string{"received", "persisted", "failed", "abandoned", "queueDropped"}
	gotFields := make([]string, 0, len(fields))
	for field := range fields {
		gotFields = append(gotFields, field)
	}
	if !reflect.DeepEqual(sortedStrings(gotFields), sortedStrings(wantFields)) {
		t.Fatalf("usagePersistence fields=%v, want only %v", gotFields, wantFields)
	}
	for _, privateField := range []string{"error", "provider", "cause", "lastError"} {
		if _, ok := fields[privateField]; ok {
			t.Fatalf("usagePersistence exposes private field %q: %s", privateField, encoded)
		}
	}
}

func sortedStrings(values []string) []string {
	sort.Strings(values)
	return values
}
