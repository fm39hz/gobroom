package kernel

import (
	"encoding/json"
	"strings"
	"testing"
)

type clientFailureDiagnosticsCarrier struct {
	diagnostics ClientFailureDiagnostics
}

func (carrier clientFailureDiagnosticsCarrier) Error() string { return "private carrier error" }

func (carrier clientFailureDiagnosticsCarrier) ClientFailureDiagnostics() ClientFailureDiagnostics {
	return carrier.diagnostics
}

func TestClientFailureDiagnosticsAcceptsExactLimits(t *testing.T) {
	maxPath := "/" + strings.Repeat("p", 255)
	maxReason := strings.Repeat("r", 128)
	diagnostics := ClientFailureDiagnostics{
		Paths:   make([]string, 32),
		Reasons: make([]string, 32),
	}
	for i := range diagnostics.Paths {
		diagnostics.Paths[i] = "/p"
		diagnostics.Reasons[i] = "reason"
	}
	diagnostics.Paths[0] = maxPath
	diagnostics.Reasons[0] = maxReason
	if !diagnostics.Valid() {
		t.Fatal("diagnostics at the exact path, reason, and item-count limits were rejected")
	}

	// The wire-size boundary is inclusive. Spread padding across paths so each
	// stays within its individual 256-byte limit.
	var encoded []byte
	var err error
	encoded, err = json.Marshal(diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	for i := range diagnostics.Paths {
		capacity := 256 - len(diagnostics.Paths[i])
		remaining := 8192 - len(encoded)
		if capacity > remaining {
			capacity = remaining
		}
		if capacity > 0 {
			diagnostics.Paths[i] += strings.Repeat("p", capacity)
			encoded, err = json.Marshal(diagnostics)
			if err != nil {
				t.Fatal(err)
			}
		}
		if len(encoded) == 8192 {
			break
		}
	}
	if len(encoded) != 8192 {
		t.Fatalf("could not construct exact encoded diagnostics boundary: %d bytes", len(encoded))
	}
	if !diagnostics.Valid() {
		t.Fatalf("diagnostics encoded to exactly 8192 bytes were rejected (%d)", len(encoded))
	}
	diagnostics.Paths[1] += "p"
	if diagnostics.Valid() {
		t.Fatal("diagnostics exceeding the 8192-byte encoded limit were accepted")
	}
}

func TestClientFailureDiagnosticsRejectsInvalidAndOverLimitValues(t *testing.T) {
	base := ClientFailureDiagnostics{Paths: []string{"/request/messages/0/content"}, Reasons: []string{"invalid_field"}}
	invalid := []ClientFailureDiagnostics{
		{Paths: []string{"request/messages"}, Reasons: []string{"invalid_field"}},
		{Paths: []string{"/request/\x00field"}, Reasons: []string{"invalid_field"}},
		{Paths: []string{"/request"}, Reasons: []string{"bad reason"}},
		{Paths: []string{"/" + strings.Repeat("p", 256)}, Reasons: []string{"invalid_field"}},
		{Paths: []string{"/request"}, Reasons: []string{strings.Repeat("r", 129)}},
		{Paths: make([]string, 33)},
		{Reasons: make([]string, 33)},
		{Paths: []string{string([]byte{'/', 0xff})}, Reasons: []string{"invalid_field"}},
	}
	for i := range invalid[5].Paths {
		invalid[5].Paths[i] = "/p"
	}
	for i := range invalid[6].Reasons {
		invalid[6].Reasons[i] = "reason"
	}
	if !base.Valid() {
		t.Fatal("ordinary valid diagnostics were rejected")
	}
	for i, diagnostics := range invalid {
		if diagnostics.Valid() {
			t.Errorf("invalid diagnostics case %d was accepted: %#v", i, diagnostics)
		}
	}
}

func TestClientFailureDiagnosticsForErrorDropsInvalidCarrierAndOwnsValidData(t *testing.T) {
	bad := ClientFailureDiagnostics{Paths: []string{"relative/private"}, Reasons: []string{"invalid_field"}}
	if bad.Valid() {
		t.Fatal("malformed carrier diagnostics unexpectedly validated")
	}
	if got := ClientFailureDiagnosticsForError(clientFailureDiagnosticsCarrier{diagnostics: bad}); !got.Valid() || len(got.Paths) != 0 || len(got.Reasons) != 0 {
		t.Fatalf("invalid optional diagnostics were not dropped: %#v", got)
	}

	paths := []string{"/request/messages/0/content"}
	reasons := []string{"invalid_field"}
	got := ClientFailureDiagnosticsForError(clientFailureDiagnosticsCarrier{
		diagnostics: ClientFailureDiagnostics{Paths: paths, Reasons: reasons},
	})
	paths[0] = "/mutated"
	reasons[0] = "mutated"
	if len(got.Paths) != 1 || got.Paths[0] != "/request/messages/0/content" || len(got.Reasons) != 1 || got.Reasons[0] != "invalid_field" {
		t.Fatalf("diagnostics retained module-owned buffers: %#v", got)
	}
	clone := got.Clone()
	clone.Paths[0] = "/clone-change"
	clone.Reasons[0] = "clone_change"
	if got.Paths[0] != "/request/messages/0/content" || got.Reasons[0] != "invalid_field" {
		t.Fatalf("Clone shares mutable slices with the original: original=%#v clone=%#v", got, clone)
	}
}
