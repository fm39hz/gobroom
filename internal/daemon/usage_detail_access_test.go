package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/store"
)

func TestUsageDetailControlAuthAndIndependentPayloadClasses(t *testing.T) {
	for _, allowPrivate := range []bool{false, true} {
		for _, allowRaw := range []bool{false, true} {
			root := t.TempDir()
			policy := DefaultUsagePolicy()
			policy.CaptureRaw = true
			policy.AllowManagementDetails = true
			policy.AllowPrivateDetails = allowPrivate
			policy.AllowRawDetails = allowRaw
			privateRef := extensions.Ref{Kind: extensions.UsageKind, ID: "fixture.private-detail", ContractVersion: 1}
			publicRef := extensions.Ref{Kind: extensions.UsageKind, ID: "fixture.public-detail", ContractVersion: 1}
			module := provider.RuntimeModuleFunc{Name: "fixture.usage-access", Version: "1", HostContractVersion: provider.RuntimeModuleHostContractVersion, Init: func(runtime *provider.RuntimeRegistry) error {
				schema := extensions.Ref{Kind: extensions.SchemaKind, ID: "fixture.detail.string", ContractVersion: 1}
				if err := runtime.RegisterSchemaDocument(schema, json.RawMessage(`{"type":"string"}`)); err != nil {
					return err
				}
				for _, item := range []struct {
					ref   extensions.Ref
					class extensions.UsageDisclosure
				}{{privateRef, extensions.UsagePrivate}, {publicRef, extensions.UsageManagementVisible}} {
					if err := runtime.RegisterExtension(extensions.Descriptor{Ref: item.ref, ImplementationVersion: "1", DisplayName: "Detail", Description: "Typed detail test.", InputSchemaRef: &schema, UsagePolicy: &extensions.UsagePolicy{MaxValueBytes: 1024, Disclosure: item.class}}, nil); err != nil {
						return err
					}
				}
				return nil
			}}
			d := NewWithProviderModules(Config{DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "run.sock"), HTTPControl: true, HTTPControlAddr: "127.0.0.1:0", HTTPToken: "inference-key", HTTPControlToken: "control-key", UsagePolicy: &policy}, module)
			if err := d.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = d.Stop(context.Background()) })
			event := kernel.UsageEvent{At: time.Now(), InputTokens: new(int64(0)), Extensions: []extensions.UsageValue{{TypeRef: privateRef, Value: json.RawMessage(`"private-valid-schema-marker"`)}, {TypeRef: publicRef, Value: json.RawMessage(`"public-value"`)}}, RawUsage: &kernel.RawUsageFragment{ProviderDefinition: extensions.Ref{Kind: "provider-definition", ID: "fixture", ContractVersion: 1}, API: "fixture", Value: json.RawMessage(`{"input_tokens":9007199254740993}`)}}
			if err := d.store.SaveUsageEvent(context.Background(), event); err != nil {
				t.Fatal(err)
			}
			rows, err := d.store.UsageEvents(1)
			if err != nil || len(rows) != 1 {
				t.Fatalf("seeded usage: %#v %v", rows, err)
			}
			id := rows[0].ID
			public := d.handleIPC(context.Background(), IPCRequest{Method: "usage.detail", Params: map[string]any{"id": id, "includeExtensions": true}})
			if !public.OK {
				t.Fatal(public.Error)
			}
			encoded, _ := json.Marshal(public.Result)
			if bytes.Contains(encoded, []byte("private-valid-schema-marker")) || bytes.Contains(encoded, []byte("rawUsage")) {
				t.Fatalf("public class request read private/raw data: %s", encoded)
			}
			var detail store.UsageDetailRecord
			if err := json.Unmarshal(encoded, &detail); err != nil || len(detail.Extensions) != 1 || detail.Extensions[0].Descriptor.Ref != publicRef {
				t.Fatalf("public detail selection: %#v %v", detail, err)
			}
			for _, options := range []UsageDetailOptions{{IncludeExtensions: true, IncludePrivate: true}, {IncludeRaw: true}} {
				params := map[string]any{"id": id, "includeExtensions": options.IncludeExtensions, "includePrivate": options.IncludePrivate, "includeRaw": options.IncludeRaw}
				response := d.handleIPC(context.Background(), IPCRequest{Method: "usage.detail", Params: params})
				want := allowPrivate
				if options.IncludeRaw {
					want = allowRaw
				}
				if response.OK != want {
					t.Fatalf("wrong independent grant result: private=%v raw=%v options=%#v response=%#v", allowPrivate, allowRaw, options, response)
				}
				body, _ := json.Marshal(UsageDetailRequest{ID: id, UsageDetailOptions: options})
				request, _ := http.NewRequest(http.MethodPost, "http://"+d.Addresses().HTTPControl+"/api/usage/detail", bytes.NewReader(body))
				request.Header.Set("Authorization", "Bearer control-key")
				httpResponse, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
				if err != nil {
					t.Fatal(err)
				}
				data, _ := io.ReadAll(httpResponse.Body)
				_ = httpResponse.Body.Close()
				status := http.StatusForbidden
				if want {
					status = http.StatusOK
				}
				if httpResponse.StatusCode != status {
					t.Fatalf("HTTP class grant mismatch: options=%#v status=%d body=%s", options, httpResponse.StatusCode, data)
				}
				if want {
					var selected store.UsageDetailRecord
					if err := json.Unmarshal(data, &selected); err != nil {
						t.Fatal(err)
					}
					if options.IncludeRaw && selected.RawUsage == nil || options.IncludePrivate && len(selected.Extensions) != 2 {
						t.Fatalf("HTTP grant did not select requested classes: %#v", selected)
					}
				} else if bytes.Contains(data, []byte("private-valid-schema-marker")) || bytes.Contains(data, []byte("9007199254740993")) {
					t.Fatal("HTTP denial disclosed payload")
				}
			}
			body, _ := json.Marshal(UsageDetailRequest{ID: id, UsageDetailOptions: UsageDetailOptions{IncludeExtensions: true}})
			for _, auth := range []string{"", "Bearer inference-key", "Bearer control-key"} {
				request, _ := http.NewRequest(http.MethodPost, "http://"+d.Addresses().HTTPControl+"/api/usage/detail", bytes.NewReader(body))
				request.Header.Set("Authorization", auth)
				response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
				if err != nil {
					t.Fatal(err)
				}
				data, _ := io.ReadAll(response.Body)
				_ = response.Body.Close()
				want := http.StatusUnauthorized
				if auth == "Bearer control-key" {
					want = http.StatusOK
				}
				if response.StatusCode != want || bytes.Contains(data, []byte("private-valid-schema-marker")) {
					t.Fatalf("control auth/privacy: status=%d body=%s", response.StatusCode, data)
				}
			}
		}
	}
}

func TestUsageDetailNeverServesExpiredRawWhileCleanupLags(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "expired.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SetUsageCapturePolicy(kernel.UsageCapturePolicy{Enabled: true, MaxBytes: 1024}); err != nil {
		t.Fatal(err)
	}
	event := kernel.UsageEvent{At: time.Now().Add(-48 * time.Hour), InputTokens: new(int64(0)), RawUsage: &kernel.RawUsageFragment{ProviderDefinition: extensions.Ref{Kind: "provider-definition", ID: "fixture", ContractVersion: 1}, API: "fixture", Value: json.RawMessage(`{"input_tokens":7}`)}}
	if err := s.SaveUsageEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	rows, err := s.UsageEvents(1)
	if err != nil || len(rows) != 1 {
		t.Fatalf("old raw seed unavailable: %v", err)
	}
	policy := DefaultUsagePolicy()
	policy.Retention = 24 * time.Hour
	policy.AllowManagementDetails = true
	policy.AllowRawDetails = true
	d := &Daemon{usagePolicy: policy, store: s}
	response := d.handleIPC(context.Background(), IPCRequest{Method: "usage.detail", Params: map[string]any{"id": rows[0].ID, "includeRaw": true}})
	if response.OK {
		t.Fatal("expired raw was disclosed before physical retention cleanup")
	}
	remaining, err := s.UsageEvents(1)
	if err != nil || len(remaining) != 1 {
		t.Fatal("test must establish read-time gating without relying on cleanup")
	}
}
