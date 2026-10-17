package externalprovider

import (
	"context"
	"encoding/json"
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

// This is a public daemon E2E: every observation below comes from the real
// ingress, upstream HTTP exchange, or public IPC views.
func TestExternalPrincipalRenewalPreservesOperationalHealthAndQuota(t *testing.T) {
	root := t.TempDir()
	manifestDir := filepath.Join(root, "providers")
	if err := os.Mkdir(manifestDir, 0o700); err != nil {
		t.Fatal(err)
	}
	definition := opaqueStateManifest(t)
	for operation, binding := range definition.Operations {
		binding.ErrorClassifier = provider.PrimitiveRef{Kind: provider.PrimitiveErrorClassifier, ID: "http-json", ContractVersion: 1}
		binding.ErrorClassifierOptions = provider.ErrorClassifierOptions{HTTPJSON: &provider.HTTPJSONErrorClassifierOptions{
			QuotaHTTPStatuses: []int{http.StatusForbidden}, QuotaCodes: []string{"quota_exceeded"},
			QuotaWindowName: "account", WindowNamePath: "/error/window", WindowRemainingPath: "/error/remaining",
			AuthRecoverySignals: []provider.HTTPJSONAuthRecoverySignal{{HTTPStatuses: []int{http.StatusUnauthorized}, Codes: []string{"credential_expired"}}},
		}}
		definition.Operations[operation] = binding
	}
	manifest, err := provider.EncodeManifest(definition)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "opaque-state.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}

	const (
		seedTicket      = "seed-runtime-ticket"
		refreshedTicket = "refreshed-runtime-ticket"
		replacedTicket  = "replacement-ticket"
	)
	var limitedCalls, quotaCalls, authCalls, renewCalls, refreshedCalls, replacedCalls atomic.Int32
	renewEntered := make(chan struct{})
	releaseRenew := make(chan struct{})
	var renewOnce atomic.Bool
	var manualReplacement atomic.Bool
	var revokeAuth atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected upstream path %q", r.URL.Path)
		}
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		ticket := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if manualReplacement.Load() {
			if ticket != replacedTicket {
				t.Errorf("request after manual replacement used ticket %q", ticket)
			}
			replacedCalls.Add(1)
			writeRenewalChatOK(w)
			return
		}
		switch body.Model {
		case "limited-model":
			limitedCalls.Add(1)
			w.Header().Set("Retry-After", "3600")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"too_many_requests"}}`))
		case "quota-model":
			quotaCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":"quota_exceeded","window":"account","remaining":0}}`))
		case "auth-model":
			authCalls.Add(1)
			if revokeAuth.Load() {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"code":"credential_revoked"}}`))
				return
			}
			writeRenewalChatOK(w)
		case "renew-model":
			renewCalls.Add(1)
			if ticket == seedTicket && renewOnce.CompareAndSwap(false, true) {
				close(renewEntered)
				select {
				case <-releaseRenew:
				case <-r.Context().Done():
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"code":"credential_expired"}}`))
				return
			}
			if ticket == refreshedTicket {
				refreshedCalls.Add(1)
			}
			writeRenewalChatOK(w)
		default:
			t.Errorf("unexpected requested model %q", body.Model)
			http.Error(w, "unexpected model", http.StatusBadRequest)
		}
	}))
	defer upstream.Close()

	config := daemon.Config{DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "daemon.sock"), ProviderManifestDir: manifestDir, HTTPEnabled: true, HTTPAddr: "127.0.0.1:0"}
	service := daemon.NewWithProviderModules(config, opaqueStateModule{})
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = service.Stop(context.Background()) }()
	rpc := func(method string, params map[string]any) json.RawMessage {
		t.Helper()
		response, callErr := daemon.CallIPC(context.Background(), config.IPCPath, daemon.IPCRequest{Method: method, Params: params})
		if callErr != nil || !response.OK {
			t.Fatalf("%s: ok=%v error=%v", method, response.OK, callErr)
		}
		encoded, marshalErr := json.Marshal(response.Result)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return encoded
	}
	var node, connection struct {
		ID string `json:"ID"`
	}
	if err := json.Unmarshal(rpc("providers.create", map[string]any{"name": "Renewal fixture", "prefix": "renewal", "baseUrl": upstream.URL + "/v1", "protocol": "openai_chat", "definitionID": opaqueStateProviderID}), &node); err != nil {
		t.Fatal(err)
	}
	seed := `{"ticket":"seed-runtime-ticket","subject":"seed-subject","workspace":"workspace-7","metadata":{"issued_by":"device-fixture"}}`
	if err := json.Unmarshal(rpc("connections.create", map[string]any{"providerNodeID": node.ID, "name": "Renewal account", "credentialType": "opaque_ticket", "secret": seed}), &connection); err != nil {
		t.Fatal(err)
	}
	roles := []struct{ id, model string }{{"limited", "limited-model"}, {"quota", "quota-model"}, {"auth", "auth-model"}, {"renew", "renew-model"}}
	for _, role := range roles {
		routeID := "renewal-route-" + role.id
		physicalID := "renewal-physical-" + role.id
		_ = rpc("custom_models.upsert", map[string]any{"id": routeID, "providerNodeID": node.ID, "kind": "custom", "externalID": role.model, "displayName": role.model, "connectionIds": []string{connection.ID}})
		_ = rpc("physical_models.upsert", map[string]any{"name": physicalID, "identity": map[string]any{"canonicalName": role.model}, "sources": []map[string]any{{"routeId": routeID, "fidelity": "exact"}}, "enabled": true})
		_ = rpc("combo_models.upsert", map[string]any{"name": "renewal-role-" + role.id, "members": []map[string]any{{"kind": "physical", "id": physicalID}}, "discoverable": true, "enabled": true})
	}
	cli := os.Getenv("GOBROOM_TEST_CLI")
	readHealth := func() json.RawMessage {
		t.Helper()
		encoded := rpc("health.list", nil)
		if cli != "" {
			encoded = runDeviceRaceCLI(t, cli, config.IPCPath, "health")
		}
		return encoded
	}
	chat := func(role string) int {
		t.Helper()
		body, err := json.Marshal(struct {
			Model    string              `json:"model"`
			Messages []map[string]string `json:"messages"`
		}{Model: "renewal-role-" + role, Messages: []map[string]string{{"role": "user", "content": "hello"}}})
		if err != nil {
			t.Fatal(err)
		}
		request, err := http.NewRequest(http.MethodPost, "http://"+service.Addresses().HTTP+"/v1/chat/completions", strings.NewReader(string(body)))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		return response.StatusCode
	}
	if cli != "" {
		runDeviceRaceCLI(t, cli, config.IPCPath, "status")
	}
	if status := chat("limited"); status < 400 {
		t.Fatalf("limited request status=%d, want rejection", status)
	}
	if status := chat("quota"); status < 400 {
		t.Fatalf("quota request status=%d, want rejection", status)
	}
	assertAuthenticationState := func(wantInvalid bool) {
		t.Helper()
		root := readHealth()
		records := readRenewalHealthRecords(t, root)
		foundAuthentication := false
		foundInvalid := false
		for _, record := range records {
			if record.Lane == "authentication" {
				foundAuthentication = true
				foundInvalid = foundInvalid || record.Invalid
			}
		}
		if !foundAuthentication || foundInvalid != wantInvalid {
			t.Fatalf("authentication health invalid=%v want=%v; health=%s", foundInvalid, wantInvalid, root)
		}
	}
	// Start the renewal request while its seed credential is still accepted by
	// the daemon. The fake upstream holds that request before returning expiry.
	renewDone := make(chan int, 1)
	go func() { renewDone <- chat("renew") }()
	select {
	case <-renewEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("renewal request did not reach the upstream barrier")
	}
	revokeAuth.Store(true)
	if status := chat("auth"); status < 400 {
		t.Fatalf("revoked credential request status=%d, want rejection", status)
	}
	assertAuthenticationState(true)
	revokeAuth.Store(false)

	readQuota := func() map[string]provider.QuotaSnapshot {
		t.Helper()
		encoded := rpc("quota.list", nil)
		if cli != "" {
			encoded = runDeviceRaceCLI(t, cli, config.IPCPath, "quota")
		}
		var snapshots map[string]provider.QuotaSnapshot
		if err := json.Unmarshal(encoded, &snapshots); err != nil {
			t.Fatalf("quota snapshots %s: %v", encoded, err)
		}
		return snapshots
	}
	assertOperationalEvidence := func() {
		t.Helper()
		encoded := readHealth()
		records := readRenewalHealthRecords(t, encoded)
		cooling := false
		for _, record := range records {
			if record.Lane == "operational" && record.CooldownUntil.After(time.Now()) && (record.LastCause == "rate_limited" || record.LastCause == "quota_exhausted") {
				cooling = true
			}
		}
		if !cooling {
			t.Fatalf("operational health did not retain a rate limit/quota cooldown: %s", encoded)
		}
		foundQuota := false
		for _, snapshot := range readQuota() {
			if snapshot.ConnectionID == connection.ID && snapshot.Remaining != nil && *snapshot.Remaining == 0 && snapshot.ResetAt == nil {
				foundQuota = true
			}
		}
		if !foundQuota {
			t.Fatal("model quota snapshot with zero remaining and unknown reset was not retained")
		}
	}
	assertOperationalEvidence()
	close(releaseRenew)
	select {
	case status := <-renewDone:
		if status != http.StatusOK {
			t.Fatalf("held renewal request status=%d", status)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("held renewal request did not complete its refresh retry")
	}
	if refreshedCalls.Load() != 1 {
		t.Fatalf("refresh retry used refreshed credential %d times, want once", refreshedCalls.Load())
	}
	activationDeadline := time.Now().Add(5 * time.Second)
	for {
		var activation daemon.CredentialActivationView
		if err := json.Unmarshal(rpc("credential_activation.get", map[string]any{"connectionID": connection.ID}), &activation); err != nil {
			t.Fatal(err)
		}
		if activation.Status == "active" {
			if activation.Transition != daemon.CredentialRenewal {
				t.Fatalf("refresh was not declared renewal: %+v", activation)
			}
			break
		}
		if time.Now().After(activationDeadline) {
			t.Fatalf("renewal activation never completed: %+v", activation)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status := chat("auth"); status != http.StatusOK {
		t.Fatalf("auth role after renewal status=%d, want success", status)
	}
	assertAuthenticationState(false)
	assertOperationalEvidence()
	if limitedCalls.Load() != 1 || quotaCalls.Load() != 1 || authCalls.Load() != 2 || renewCalls.Load() != 2 {
		t.Fatalf("upstream counts limited=%d quota=%d auth=%d renew=%d", limitedCalls.Load(), quotaCalls.Load(), authCalls.Load(), renewCalls.Load())
	}

	if err := service.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	service = daemon.NewWithProviderModules(config, opaqueStateModule{})
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if cli != "" {
		runDeviceRaceCLI(t, cli, config.IPCPath, "status")
	}
	assertOperationalEvidence()
	if status := chat("limited"); status < 400 {
		t.Fatalf("persisted limited cooldown admitted request: %d", status)
	}
	if status := chat("quota"); status < 400 {
		t.Fatalf("persisted quota admitted request: %d", status)
	}
	if limitedCalls.Load() != 1 || quotaCalls.Load() != 1 {
		t.Fatalf("blocked routes dispatched after restart: limited=%d quota=%d", limitedCalls.Load(), quotaCalls.Load())
	}

	// A subsequent explicit credential replacement must advance the auth
	// principal generation and permit the formerly blocked auth role again.
	revokeAuth.Store(true)
	if status := chat("auth"); status < 400 {
		t.Fatalf("auth failure for the renewed principal status=%d, want rejection", status)
	}
	assertAuthenticationState(true)
	replacement := `{"ticket":"replacement-ticket","subject":"replacement-subject","workspace":"workspace-7","metadata":{"issued_by":"manual"}}`
	if cli != "" {
		runDeviceRaceCLI(t, cli, config.IPCPath, "connections", "update", "--id", connection.ID, "--secret", replacement)
	} else {
		_ = rpc("connections.update", map[string]any{"id": connection.ID, "secret": replacement})
	}
	manualReplacement.Store(true)
	revokeAuth.Store(false)
	if status := chat("auth"); status != http.StatusOK {
		t.Fatalf("auth role after manual replacement status=%d", status)
	}
	for _, role := range []string{"limited", "quota"} {
		if status := chat(role); status != http.StatusOK {
			t.Fatalf("former principal's %s suppression survived replacement: %d", role, status)
		}
	}
	assertAuthenticationState(false)
	if replacedCalls.Load() != 3 {
		t.Fatalf("manual replacement dispatched %d requests, want three", replacedCalls.Load())
	}
}

func writeRenewalChatOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"id":"chatcmpl-renewal","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
}

type renewalHealthRecord struct {
	Lane          string `json:"lane"`
	Invalid       bool
	LastCause     string
	CooldownUntil time.Time
}

func readRenewalHealthRecords(t *testing.T, raw json.RawMessage) []renewalHealthRecord {
	t.Helper()
	var views map[string]struct {
		renewalHealthRecord
		Operations     []renewalHealthRecord `json:"operations"`
		Authentication []renewalHealthRecord `json:"authentication"`
	}
	if err := json.Unmarshal(raw, &views); err != nil {
		t.Fatal(err)
	}
	var records []renewalHealthRecord
	for _, view := range views {
		records = append(records, view.renewalHealthRecord)
		records = append(records, view.Operations...)
		records = append(records, view.Authentication...)
	}
	return records
}
