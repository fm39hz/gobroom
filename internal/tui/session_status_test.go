package tui

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStatusDisplaysSessionPersistenceCountersWithoutRounding(t *testing.T) {
	entries, err := makeEntries("status", json.RawMessage(`{"status":"degraded","sessionPersistence":{"received":2,"persisted":1,"failed":1,"queueDropped":0,"saveErrors":9007199254740993}}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].summary != "degraded" ||
		!strings.Contains(entries[0].detail, `"sessionPersistence"`) ||
		!strings.Contains(entries[0].detail, `9007199254740993`) {
		t.Fatalf("missing or rounded session failure evidence: %#v", entries)
	}
}
