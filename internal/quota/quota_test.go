package quota

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestExpiredQuotaResetIsUsable(t *testing.T) {
	remaining := float64(0)
	reset := time.Now().Add(-time.Minute)
	s := Snapshot{Remaining: &remaining, ResetAt: &reset}
	if !s.Usable(time.Now()) {
		t.Fatal("quota should be usable after reset")
	}
}

func TestSnapshotMarshalsEvidenceWithoutConfigFingerprint(t *testing.T) {
	snapshot := Snapshot{ProviderNodeID: "provider", ConnectionID: "connection", ModelRef: "model", Generation: "private-generation", Authority: AuthorityResponseHeader, ObservedAt: time.Now().UTC()}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"authority":"response_header"`) || !strings.Contains(string(encoded), `"observedAt"`) || strings.Contains(string(encoded), "private-generation") {
		t.Fatalf("quota evidence serialization=%s", encoded)
	}
	var decoded Snapshot
	if err := json.Unmarshal(encoded, &decoded); err != nil || decoded.Authority != AuthorityResponseHeader || decoded.ObservedAt.IsZero() {
		t.Fatalf("quota evidence did not round-trip: %#v err=%v", decoded, err)
	}
}

func TestActiveExhaustedQuotaIsBlocked(t *testing.T) {
	remaining := float64(0)
	reset := time.Now().Add(time.Minute)
	s := Snapshot{Remaining: &remaining, ResetAt: &reset}
	if s.Usable(time.Now()) {
		t.Fatal("active exhausted quota should be blocked")
	}
}

func TestMergeSnapshotPrefersNewerObservationBeforeAuthority(t *testing.T) {
	newer := time.Now()
	older := newer.Add(-time.Second)
	remainingZero := float64(0)
	remainingFull := float64(10)
	current := Snapshot{Remaining: &remainingFull, Authority: AuthorityQuotaAPI, ObservedAt: older, Source: "http-json-quota"}
	incoming := Snapshot{Remaining: &remainingZero, Authority: AuthorityResponseHeader, ObservedAt: newer, Source: "response_header"}
	merged := MergeSnapshot(current, incoming)
	if merged.Remaining == nil || *merged.Remaining != 0 || merged.Source != "response_header" {
		t.Fatalf("newer response evidence lost to stale quota API snapshot: %#v", merged)
	}

	stale := Snapshot{Remaining: &remainingFull, Authority: AuthorityQuotaAPI, ObservedAt: older, Source: "http-json-quota"}
	if got := MergeSnapshot(merged, stale); got.Remaining == nil || *got.Remaining != 0 || got.Source != "response_header" {
		t.Fatalf("older quota API evidence overwrote newer response: %#v", got)
	}
}

func TestMergeSnapshotUsesAuthorityForEqualObservationTime(t *testing.T) {
	at := time.Now()
	remainingZero := float64(0)
	remainingFull := float64(10)
	api := Snapshot{Remaining: &remainingFull, Authority: AuthorityQuotaAPI, ObservedAt: at, Source: "http-json-quota"}
	header := Snapshot{Remaining: &remainingZero, Authority: AuthorityResponseHeader, ObservedAt: at, Source: "response_header"}
	if got := MergeSnapshot(api, header); got.Remaining == nil || *got.Remaining != 0 {
		t.Fatalf("response-header authority did not win equal-time conflict: %#v", got)
	}
	if got := MergeSnapshot(header, api); got.Remaining == nil || *got.Remaining != 0 {
		t.Fatalf("low-authority quota API overwrote equal-time response: %#v", got)
	}
}

func TestMergeSnapshotPreservesKnownFieldsWhenNewerEvidenceOmitsThem(t *testing.T) {
	zero := float64(0)
	firstReset := time.Now().Add(time.Minute)
	secondReset := firstReset.Add(time.Minute)
	current := Snapshot{Remaining: &zero, ResetAt: &firstReset, Authority: AuthorityQuotaAPI, ObservedAt: firstReset.Add(-time.Minute)}
	incoming := Snapshot{ResetAt: &secondReset, Authority: AuthorityResponseHeader, ObservedAt: firstReset}
	merged := MergeSnapshot(current, incoming)
	if merged.Remaining == nil || *merged.Remaining != 0 || merged.ResetAt == nil || !merged.ResetAt.Equal(secondReset) {
		t.Fatalf("newer partial evidence erased a known blocking field: %#v", merged)
	}
}
