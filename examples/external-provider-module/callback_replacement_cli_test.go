package externalprovider

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/pkg/daemon"
)

func TestInstalledCLICallbackReplacementRace(t *testing.T) {
	cli := os.Getenv("GOBROOM_TEST_CLI")
	if cli == "" {
		t.Skip("dedicated installed CLI gate requires GOBROOM_TEST_CLI")
	}

	f := newCallbackReplacementFixture(t)
	f.replace(t, cli)
	f.serve(t)
	f.finish(t)

	terminal := f.terminal(t)
	if terminal.SessionID != f.sessionID || terminal.ConnectionID != f.connectionID || terminal.Status != "failed" || len(terminal.Actions) != 0 || len(terminal.CallbackModes) != 0 || terminal.Activation != nil {
		t.Fatalf("setup did not reach a terminal state: status=%q", terminal.Status)
	}

	setup := portabilityCLI(t, cli, f.host.config.IPCPath, nil, "connections", "setup", "get", "--session-id", f.sessionID)
	status := portabilityCLI(t, cli, f.host.config.IPCPath, nil, "status")
	logs := portabilityCLI(t, cli, f.host.config.IPCPath, nil, "logs", "--limit", "100")
	assertCallbackReplacementPrivacy(t, f, setup, status, logs)

	f.restart(t)
	f.serve(t)
	status = portabilityCLI(t, cli, f.host.config.IPCPath, nil, "status")
	var health struct {
		Status  string `json:"status"`
		Control uint64 `json:"controlSnapshotVersion"`
		Serving uint64 `json:"servingSnapshotVersion"`
	}
	if err := json.Unmarshal(status, &health); err != nil || health.Status != "ok" || health.Control == 0 || health.Control != health.Serving {
		t.Fatalf("installed CLI status after restart=%s err=%v", status, err)
	}
	assertCallbackReplacementPrivacy(t, f, nil, status, nil)
}

func assertCallbackReplacementPrivacy(t *testing.T, f *callbackReplacementFixture, setup, status, logs []byte) {
	t.Helper()
	private := []string{"late-callback-proof", "manual-callback-key", "manual-callback-subject", "original-callback-key", "original-callback-subject"}
	if f.callbackURL != nil {
		private = append(private, f.callbackURL.Query().Get("ticket"))
	}
	for label, output := range map[string][]byte{"setup": setup, "status": status, "logs": logs} {
		for _, value := range private {
			if value != "" && strings.Contains(string(output), value) {
				t.Errorf("%s output exposed private callback or credential data", label)
			}
		}
	}
	if setup != nil {
		var view daemon.AuthorizationSetupView
		if err := json.Unmarshal(setup, &view); err != nil {
			t.Errorf("installed setup view was not valid JSON: %v", err)
		} else if view.SessionID != f.sessionID || view.ConnectionID != f.connectionID || view.Status != "failed" || len(view.Actions) != 0 || len(view.CallbackModes) != 0 || view.Activation != nil {
			t.Errorf("installed setup view is not terminal for this session: status=%q", view.Status)
		}
	}
}
