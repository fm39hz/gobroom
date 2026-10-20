package externalprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/pkg/daemon"
	"github.com/fm39hz/gobroom/pkg/provider"
)

func TestExternalAuthorizationSetupResumeAcrossFrontends(t *testing.T) {
	root := t.TempDir()
	manifestDir := filepath.Join(root, "providers")
	if err := os.Mkdir(manifestDir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest, err := provider.EncodeManifest(setupActionManifest(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "authorization-setup.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	var authorization atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization.Store(r.Header.Get("Authorization"))
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"fixture","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()
	config := daemon.Config{DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "daemon.sock"), ProviderManifestDir: manifestDir, HTTPEnabled: true, HTTPAddr: "127.0.0.1:0"}
	pollCompleted := make(chan struct{}, 1)
	module := setupActionModule{pollCompleted: pollCompleted}
	service := daemon.NewWithProviderModules(config, module)
	stop := func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := service.Stop(ctx); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = service.Stop(ctx)
	})
	startDaemon := func() {
		t.Helper()
		if err := service.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	startDaemon()
	rpc := func(method string, params map[string]any) (json.RawMessage, error) {
		t.Helper()
		response, callErr := daemon.CallIPC(context.Background(), config.IPCPath, daemon.IPCRequest{Method: method, Params: params})
		if callErr != nil {
			return nil, callErr
		}
		if !response.OK {
			return nil, fmt.Errorf("%s: %s", method, response.Error)
		}
		return json.Marshal(response.Result)
	}
	require := func(method string, params map[string]any) json.RawMessage {
		t.Helper()
		raw, callErr := rpc(method, params)
		if callErr != nil {
			t.Fatal(callErr)
		}
		return raw
	}
	var node, connection struct{ ID string }
	if err := json.Unmarshal(require("providers.create", map[string]any{"name": "Setup fixture", "prefix": "setup", "baseUrl": upstream.URL + "/v1", "protocol": "openai_chat", "definitionID": setupActionProviderID}), &node); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(require("connections.create", map[string]any{"providerNodeID": node.ID, "name": "Resume account", "credentialType": "opaque_ticket", "secret": ""}), &connection); err != nil {
		t.Fatal(err)
	}
	cli := os.Getenv("GOBROOM_TEST_CLI")
	frontendCall := func(method string, params map[string]any, args ...string) json.RawMessage {
		t.Helper()
		if cli == "" {
			return require(method, params)
		}
		return runDeviceRaceCLI(t, cli, config.IPCPath, args...)
	}
	decode := func(raw json.RawMessage) daemon.AuthorizationSetupView {
		t.Helper()
		var view daemon.AuthorizationSetupView
		if err := json.Unmarshal(raw, &view); err != nil {
			t.Fatalf("decode setup view %s: %v", raw, err)
		}
		return view
	}
	assertPrivateDataAbsent := func(raw json.RawMessage) {
		t.Helper()
		for _, secret := range []string{"cookie-from-browser", "proof-from-browser", "manualCookie", "Stage", "ExpiresAt", "authorization-setup-ticket", "setup-subject", `"private"`} {
			if strings.Contains(string(raw), secret) {
				t.Fatalf("setup response exposed private value %q: %s", secret, raw)
			}
		}
	}
	startArgs := []string{"connections", "setup", "start", "--connection-id", connection.ID}
	firstRaw := frontendCall("auth.setup.start", map[string]any{"connectionID": connection.ID}, startArgs...)
	assertPrivateDataAbsent(firstRaw)
	first := decode(firstRaw)
	if first.SessionID == "" || first.Revision != 1 || first.Status != "awaiting_user" || len(first.Actions) != 3 {
		t.Fatalf("initial setup view=%+v", first)
	}
	formInput := map[string]any{"manualCookie": "cookie-from-browser", "proof": "proof-from-browser"}
	formJSON, err := json.Marshal(formInput)
	if err != nil {
		t.Fatal(err)
	}
	var formRaw json.RawMessage
	if cli != "" {
		path := filepath.Join(t.TempDir(), "form.json")
		if err := os.WriteFile(path, formJSON, 0o600); err != nil {
			t.Fatal(err)
		}
		formRaw = frontendCall("auth.setup.advance", nil, "connections", "setup", "submit", "--session-id", first.SessionID, "--action-id", "form", "--revision", "1", "--input-file", path)
	} else {
		formRaw = frontendCall("auth.setup.advance", map[string]any{"sessionID": first.SessionID, "actionID": "form", "revision": first.Revision, "value": formInput})
	}
	assertPrivateDataAbsent(formRaw)
	formView := decode(formRaw)
	if formView.SessionID != first.SessionID || formView.Revision != 2 || len(formView.Actions) != 1 || formView.Actions[0].ID != "otp" {
		t.Fatalf("form submission view=%+v", formView)
	}
	resumeArgs := append(append([]string{}, startArgs...), "--resume")
	resumeParams := map[string]any{"connectionID": connection.ID, "mode": daemon.AuthorizationSetupResume}
	resumedRaw := frontendCall("auth.setup.start", resumeParams, resumeArgs...)
	assertPrivateDataAbsent(resumedRaw)
	resumed := decode(resumedRaw)
	if resumed.SessionID != first.SessionID || resumed.Revision != 2 || len(resumed.Actions) != 1 || resumed.Actions[0].ID != "otp" {
		t.Fatalf("resume rewound or replaced the active flow: %+v", resumed)
	}
	// Use IPC directly for expected errors: the shared CLI helper intentionally Fatalf's on failure.
	if _, err := rpc("auth.setup.start", map[string]any{"connectionID": connection.ID, "mode": daemon.AuthorizationSetupNew}); err == nil {
		t.Fatal("fresh start succeeded while the connection had an active setup")
	}
	currentRaw := require("auth.setup.get", map[string]any{"sessionID": first.SessionID})
	assertPrivateDataAbsent(currentRaw)
	current := decode(currentRaw)
	if current.SessionID != first.SessionID || current.Revision != 2 || len(current.Actions) != 1 || current.Actions[0].ID != "otp" {
		t.Fatalf("rejected fresh start changed active setup: %+v", current)
	}
	otpInput := map[string]any{"otp": "123456"}
	otpJSON, err := json.Marshal(otpInput)
	if err != nil {
		t.Fatal(err)
	}
	var otpRaw json.RawMessage
	if cli != "" {
		path := filepath.Join(t.TempDir(), "otp.json")
		if err := os.WriteFile(path, otpJSON, 0o600); err != nil {
			t.Fatal(err)
		}
		otpRaw = frontendCall("auth.setup.advance", nil, "connections", "setup", "submit", "--session-id", first.SessionID, "--action-id", "otp", "--revision", "2", "--input-file", path)
	} else {
		otpRaw = frontendCall("auth.setup.advance", map[string]any{"sessionID": first.SessionID, "actionID": "otp", "revision": resumed.Revision, "value": otpInput})
	}
	assertPrivateDataAbsent(otpRaw)
	view := decode(otpRaw)
	if view.SessionID != first.SessionID || view.Revision != 3 || view.Status != "awaiting_user" || len(view.Actions) != 0 {
		t.Fatalf("OTP transition=%+v", view)
	}
	select {
	case <-pollCompleted:
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not independently poll authorization")
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		viewRaw := require("auth.setup.get", map[string]any{"sessionID": first.SessionID})
		assertPrivateDataAbsent(viewRaw)
		view = decode(viewRaw)
		if view.Status == "completed" && view.Activation != nil && view.Activation.Status == "active" {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("activation did not become ready: %+v", view)
		}
	}
	for _, item := range []struct {
		method string
		params map[string]any
	}{
		{"custom_models.upsert", map[string]any{"id": "route_setup_resume", "providerNodeID": node.ID, "kind": "custom", "externalID": "gpt-test", "displayName": "Resume chat", "profile": map[string]any{}, "connectionIds": []string{connection.ID}}},
		{"physical_models.upsert", map[string]any{"name": "setup-resume-physical", "identity": map[string]any{"canonicalName": "gpt-test"}, "sources": []map[string]any{{"routeId": "route_setup_resume", "fidelity": "exact"}}, "enabled": true}},
		{"combo_models.upsert", map[string]any{"name": "setup-resume-chat", "members": []map[string]any{{"kind": "physical", "id": "setup-resume-physical"}}, "discoverable": true, "enabled": true}},
	} {
		require(item.method, item.params)
	}
	response, err := http.Post("http://"+service.Addresses().HTTP+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"setup-resume-chat","messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || authorization.Load() != "Bearer authorization-setup-ticket" {
		t.Fatalf("inference did not use activated setup credential: status=%d authorization=%v", response.StatusCode, authorization.Load())
	}
	stop()
}

func TestExternalAuthorizationResumeAfterDaemonRestartStartsFreshSession(t *testing.T) {
	root := t.TempDir()
	manifestDir := filepath.Join(root, "providers")
	if err := os.Mkdir(manifestDir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest, err := provider.EncodeManifest(setupActionManifest(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "authorization-setup.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	config := daemon.Config{DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "daemon.sock"), ProviderManifestDir: manifestDir}
	module := setupActionModule{pollCompleted: make(chan struct{}, 1)}
	service := daemon.NewWithProviderModules(config, module)
	stop := func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := service.Stop(ctx); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = service.Stop(ctx)
	})
	start := func() {
		t.Helper()
		if err := service.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	call := func(method string, params map[string]any) (json.RawMessage, error) {
		response, callErr := daemon.CallIPC(context.Background(), config.IPCPath, daemon.IPCRequest{Method: method, Params: params})
		if callErr != nil {
			return nil, callErr
		}
		if !response.OK {
			return nil, fmt.Errorf("%s: %s", method, response.Error)
		}
		return json.Marshal(response.Result)
	}
	require := func(method string, params map[string]any) json.RawMessage {
		t.Helper()
		raw, e := call(method, params)
		if e != nil {
			t.Fatal(e)
		}
		return raw
	}
	start()
	var node, connection struct{ ID string }
	if err := json.Unmarshal(require("providers.create", map[string]any{"name": "Restart fixture", "prefix": "restart", "baseUrl": "http://127.0.0.1:1/v1", "protocol": "openai_chat", "definitionID": setupActionProviderID}), &node); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(require("connections.create", map[string]any{"providerNodeID": node.ID, "name": "Restart account", "credentialType": "opaque_ticket", "secret": ""}), &connection); err != nil {
		t.Fatal(err)
	}
	var old daemon.AuthorizationSetupView
	if err := json.Unmarshal(require("auth.setup.start", map[string]any{"connectionID": connection.ID}), &old); err != nil {
		t.Fatal(err)
	}
	if old.SessionID == "" || old.Revision != 1 {
		t.Fatalf("initial view=%+v", old)
	}
	stop()
	service = daemon.NewWithProviderModules(config, module)
	start()
	resumed := require("auth.setup.start", map[string]any{"connectionID": connection.ID, "mode": daemon.AuthorizationSetupResume})
	var fresh daemon.AuthorizationSetupView
	if err := json.Unmarshal(resumed, &fresh); err != nil {
		t.Fatal(err)
	}
	if fresh.SessionID == "" || fresh.SessionID == old.SessionID || fresh.Revision != 1 || fresh.Status != "awaiting_user" {
		t.Fatalf("restart resume did not create a fresh interactive session: old=%+v new=%+v", old, fresh)
	}
	if _, err := call("auth.setup.get", map[string]any{"sessionID": old.SessionID}); err == nil {
		t.Fatal("old interactive handle was accepted after daemon restart")
	}
	stop()
}
