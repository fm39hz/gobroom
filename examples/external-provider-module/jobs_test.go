package externalprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/pkg/daemon"
	"github.com/fm39hz/gobroom/pkg/provider"
)

func jobManifest() provider.ProviderDefinition {
	definition := provider.ProviderDefinition{ContractVersion: 1, ID: jobProvider.ID, Version: "1", DisplayName: "Example jobs", DefaultCredentialType: "api_key",
		AuthModes:  []provider.AuthModeDefinition{{CredentialType: "api_key", Auth: provider.PrimitiveRef{Kind: provider.PrimitiveAuth, ID: "static-secret", ContractVersion: 1}, Presentation: provider.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "}}},
		Operations: map[provider.Operation]provider.OperationBinding{},
	}
	for _, action := range []string{"create", "status", "cancel"} {
		ref := jobOperation(action)
		binding := provider.OperationBinding{Protocol: "example_jobs", TaskRef: &ref, ProviderFormat: "example.jobs.wire",
			Endpoint: provider.PrimitiveRef{Kind: provider.PrimitiveEndpoint, ID: "http-json", ContractVersion: 1}, Transport: provider.PrimitiveRef{Kind: provider.PrimitiveTransport, ID: "http", ContractVersion: 1},
			RequestCodec: provider.PrimitiveRef{Kind: provider.PrimitiveRequestCodec, ID: ref.ID, ContractVersion: 1}, ResponseDecoder: provider.PrimitiveRef{Kind: provider.PrimitiveResponseDecoder, ID: ref.ID, ContractVersion: 1},
		}
		if action == "create" {
			binding.Idempotency = provider.IdempotencyPolicy{Header: "Idempotency-Key", MinRetentionSeconds: 60}
		}
		definition.Operations[provider.Operation(ref.ID)] = binding
	}
	return definition
}

func TestExternalJobLifecycleKeepsEffectsAndHandlesScopedAcrossRestart(t *testing.T) {
	type upstreamJob struct {
		state   jobResult
		account string
		polls   int
	}
	var mu sync.Mutex
	jobs := map[string]*upstreamJob{}
	keys := map[string]string{}
	creates, createCalls, cancelCalls, statusCalls, wrongAccount := 0, 0, 0, 0, 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		account := r.Header.Get("Authorization")
		if r.Method == http.MethodPost && r.URL.Path == "/v1/jobs" {
			var input struct {
				Model  string `json:"model"`
				Prompt string `json:"prompt"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Model != "job-generator" || input.Prompt == "" {
				http.Error(w, "invalid job", 400)
				return
			}
			createCalls++
			key := r.Header.Get("Idempotency-Key")
			if key == "" || key == "client-create-key" {
				t.Error("missing issuer-derived idempotency key")
				w.WriteHeader(400)
				return
			}
			id := keys[key]
			if id == "" {
				creates++
				id = fmt.Sprintf("job-%d", creates)
				keys[key] = id
				jobs[id] = &upstreamJob{state: jobResult{ID: id, Status: "pending"}, account: account}
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Errorf("lose create response: %v", err)
					return
				}
				_ = conn.Close() // The upstream effect survives loss of its response.
				return
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(jobs[id].state)
			return
		}
		id := r.PathValue("id")
		cancel := r.Method == http.MethodPost
		job := jobs[id]
		if job == nil {
			w.WriteHeader(404)
			return
		}
		if job.account != account {
			wrongAccount++
			w.WriteHeader(403)
			return
		}
		if cancel && r.Method == http.MethodPost {
			cancelCalls++
			job.state.Status = "cancelled"
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("lose cancellation response: %v", err)
				return
			}
			_ = conn.Close()
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(405)
			return
		}
		statusCalls++
		if job.state.Status != "cancelled" {
			job.polls++
			job.state.Status = "running"
			if job.polls > 1 {
				job.state.Status = "completed"
			}
		}
		_ = json.NewEncoder(w).Encode(job.state)
	})
	mux := http.NewServeMux()
	mux.Handle("POST /v1/jobs", handler)
	mux.Handle("GET /v1/jobs/{id}", handler)
	mux.Handle("POST /v1/jobs/{id}/cancel", handler)
	upstream := httptest.NewServer(mux)
	defer upstream.Close()
	root := t.TempDir()
	manifestDir := filepath.Join(root, "providers")
	if err := os.Mkdir(manifestDir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest, err := provider.EncodeManifest(jobManifest())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "jobs.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	config := daemon.Config{DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "daemon.sock"), ProviderManifestDir: manifestDir, ArtifactStoreDir: filepath.Join(root, "artifacts"), HTTPEnabled: true, HTTPAddr: "127.0.0.1:0"}
	service := daemon.NewWithProviderModules(config, JobModule{})
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	installedCLI := os.Getenv("GOBROOM_TEST_CLI")
	cli := func(args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		output, err := exec.CommandContext(ctx, installedCLI, append([]string{"--ipc", config.IPCPath}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("installed CLI %v: %v: %s", args, err, output)
		}
		return output
	}
	defer func() { _ = service.Stop(context.Background()) }()
	rpc := func(method string, params map[string]any) string {
		t.Helper()
		response, err := daemon.CallIPC(context.Background(), config.IPCPath, daemon.IPCRequest{Method: method, Params: params})
		if err != nil || !response.OK {
			t.Fatalf("%s: response=%#v err=%v", method, response, err)
		}
		encoded, err := json.Marshal(response.Result)
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			ID string `json:"ID"`
		}
		if err := json.Unmarshal(encoded, &result); err != nil {
			t.Fatal(err)
		}
		return result.ID
	}
	nodeID := rpc("providers.create", map[string]any{"name": "Job account host", "prefix": "jobs", "baseUrl": upstream.URL + "/v1", "protocol": "example_jobs", "definitionID": jobProvider.ID})
	accountA := rpc("connections.create", map[string]any{"providerNodeID": nodeID, "name": "Account A", "credentialType": "api_key", "secret": "account-a"})
	accountB := rpc("connections.create", map[string]any{"providerNodeID": nodeID, "name": "Account B", "credentialType": "api_key", "secret": "account-b"})
	rpc("custom_models.upsert", map[string]any{"id": "job-route", "providerNodeID": nodeID, "kind": "custom", "externalID": "job-generator", "displayName": "Job generator", "connectionIds": []string{accountA, accountB}})
	rpc("physical_models.upsert", map[string]any{"name": "job-physical", "identity": map[string]any{"canonicalName": "job-generator"}, "sources": []map[string]any{{"routeId": "job-route", "fidelity": "exact"}}, "enabled": true})
	rpc("combo_models.upsert", map[string]any{"name": "job-role", "members": []map[string]any{{"kind": "physical", "id": "job-physical"}}, "discoverable": true, "enabled": true})
	rpc("custom_models.upsert", map[string]any{"id": "other-job-route", "providerNodeID": nodeID, "kind": "custom", "externalID": "other-job-generator", "displayName": "Other account generator", "connectionIds": []string{accountB}})
	rpc("physical_models.upsert", map[string]any{"name": "other-job-physical", "identity": map[string]any{"canonicalName": "other-job-generator"}, "sources": []map[string]any{{"routeId": "other-job-route", "fidelity": "exact"}}, "enabled": true})
	rpc("combo_models.upsert", map[string]any{"name": "other-job-role", "members": []map[string]any{{"kind": "physical", "id": "other-job-physical"}}, "discoverable": true, "enabled": true})
	client := &http.Client{Timeout: 5 * time.Second}
	selectedModel := "job-role"
	call := func(action, preferred, key string, payload json.RawMessage, handles []provider.ArtifactRef) (int, jobEnvelope, []byte) {
		t.Helper()
		body, err := json.Marshal(struct {
			Model     string                 `json:"model"`
			Payload   json.RawMessage        `json:"operationPayload"`
			Artifacts []provider.ArtifactRef `json:"artifacts,omitempty"`
		}{selectedModel, payload, handles})
		if err != nil {
			t.Fatal(err)
		}
		request, err := http.NewRequest(http.MethodPost, "http://"+service.Addresses().HTTP+"/v1/jobs/"+action, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		if preferred != "" {
			request.Header.Set("X-Connection-ID", preferred)
		}
		if key != "" {
			request.Header.Set("Idempotency-Key", key)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		encoded, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		var result jobEnvelope
		if response.StatusCode < 300 {
			if err := json.Unmarshal(encoded, &result); err != nil {
				t.Fatalf("job JSON=%s: %v", encoded, err)
			}
		}
		return response.StatusCode, result, encoded
	}
	status, created, body := call("create", accountA, "client-create-key", json.RawMessage(`{"prompt":"render a fixture"}`), nil)
	if status >= 300 || created.Result.Status != "pending" || len(created.Artifacts) != 1 {
		t.Fatalf("create status=%d body=%s", status, body)
	}
	handle := created.Artifacts[0]
	if handle.Owner.ConnectionID != accountA || handle.ReplayScope != provider.ArtifactReplayConnection || handle.Owner.IssuerRef == nil || *handle.Owner.IssuerRef != jobProvider {
		t.Fatalf("unscoped job handle: %#v", handle)
	}
	mu.Lock()
	createdEffects, sentCreates, unsolicitedPolls := creates, createCalls, statusCalls
	mu.Unlock()
	if createdEffects != 1 || sentCreates != 2 || unsolicitedPolls != 0 {
		t.Fatalf("create effects=%d calls=%d unsolicited polls=%d", createdEffects, sentCreates, unsolicitedPolls)
	}
	if err := service.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	service = daemon.NewWithProviderModules(config, JobModule{})
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if installedCLI != "" {
		var status struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(cli("status"), &status); err != nil || status.Status != "ok" {
			t.Fatalf("installed CLI restart status=%#v err=%v", status, err)
		}
		var catalog struct {
			Descriptors []provider.ExtensionDescriptor `json:"descriptors"`
		}
		if err := json.Unmarshal(cli("extensions", "catalog"), &catalog); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, descriptor := range catalog.Descriptors {
			if descriptor.Ref == jobArtifact && descriptor.ArtifactPolicy != nil && len(descriptor.ArtifactPolicy.ReplayScopes) == 1 && descriptor.ArtifactPolicy.ReplayScopes[0] == provider.ArtifactReplayConnection {
				found = true
			}
		}
		if !found {
			t.Fatal("installed CLI did not project the external scoped job artifact contract")
		}
	}
	status, running, body := call("status", "", "", json.RawMessage(`{}`), []provider.ArtifactRef{handle})
	if status >= 300 || running.Result.Status != "running" || running.Result.ID != created.Result.ID {
		t.Fatalf("status after restart=%d body=%s", status, body)
	}
	selectedModel = "other-job-role"
	if code, _, raw := call("status", "", "", json.RawMessage(`{}`), []provider.ArtifactRef{handle}); code < 400 {
		t.Fatalf("handle escaped into another connection's role: %d %s", code, raw)
	}
	selectedModel = "job-role"
	for _, invalid := range []struct {
		preferred string
		handle    provider.ArtifactRef
	}{
		{"", func() provider.ArtifactRef { h := handle.Clone(); h.ExpiresAt = time.Now().Add(-time.Second); return h }()},
		{"", func() provider.ArtifactRef { h := handle.Clone(); h.Owner.IssuerRef.ContractVersion++; return h }()},
		{"", func() provider.ArtifactRef { h := handle.Clone(); h.Owner.IssuerRef = nil; return h }()},
	} {
		if code, _, raw := call("status", invalid.preferred, "", json.RawMessage(`{}`), []provider.ArtifactRef{invalid.handle}); code < 400 {
			t.Fatalf("invalid job handle was dispatched: %d %s", code, raw)
		}
	}
	status, _, body = call("cancel", "", "", json.RawMessage(`{}`), []provider.ArtifactRef{handle})
	if status < 400 {
		t.Fatalf("lost unsafe cancellation response falsely succeeded: %d %s", status, body)
	}
	// Respect the actual passive health deadline; do not disable/reset the gate
	// or repeatedly probe the upstream to make the fixture pass.
	health, err := daemon.CallIPC(context.Background(), config.IPCPath, daemon.IPCRequest{Method: "health.list"})
	if err != nil || !health.OK {
		t.Fatalf("health evidence: %#v %v", health, err)
	}
	healthJSON, err := json.Marshal(health.Result)
	if err != nil {
		t.Fatal(err)
	}
	if installedCLI != "" {
		healthJSON = cli("health")
	}
	var states map[string]struct{ CooldownUntil time.Time }
	if err := json.Unmarshal(healthJSON, &states); err != nil {
		t.Fatal(err)
	}
	var deadline time.Time
	for _, state := range states {
		if state.CooldownUntil.After(deadline) {
			deadline = state.CooldownUntil
		}
	}
	wait := time.Until(deadline) + time.Millisecond
	if wait <= 0 || wait > 10*time.Second {
		t.Fatalf("expected bounded passive cooldown, got deadline=%s", deadline)
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	<-timer.C
	status, cancelled, body := call("status", "", "", json.RawMessage(`{}`), []provider.ArtifactRef{handle})
	if status >= 300 || cancelled.Result.Status != "cancelled" {
		t.Fatalf("could not reconcile cancellation: %d %s", status, body)
	}
	status, second, body := call("create", accountA, "completion-case", json.RawMessage(`{"prompt":"finish a fixture"}`), nil)
	if status >= 300 || len(second.Artifacts) != 1 || second.Result.ID == created.Result.ID {
		t.Fatalf("independent job create=%d %s", status, body)
	}
	for _, expected := range []string{"running", "completed"} {
		status, result, body := call("status", "", "", json.RawMessage(`{}`), second.Artifacts)
		if status >= 300 || result.Result.Status != expected || result.Result.ID != second.Result.ID {
			t.Fatalf("job transition expected=%s got=%d %s", expected, status, body)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if creates != 2 || createCalls != 4 || cancelCalls != 1 || statusCalls != 4 || wrongAccount != 0 {
		t.Fatalf("job effects escaped their contracts: created=%d cancellations=%d polls=%d wrongAccount=%d", creates, cancelCalls, statusCalls, wrongAccount)
	}
}
