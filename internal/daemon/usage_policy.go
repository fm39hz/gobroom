package daemon

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/fm39hz/gobroom/internal/api"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/store"
	usageworker "github.com/fm39hz/gobroom/internal/usage"
)

type UsageDetailOptions = usageworker.DetailOptions

type UsageDetailRequest struct {
	ID int64 `json:"id"`
	UsageDetailOptions
}

// UsagePolicy is a host-side grant, separate from module classification and
// caller authorization. Capturing data never grants permission to read it.
type UsagePolicy struct {
	MaxValues              int
	MaxExtensionBytes      int64
	CaptureRaw             bool
	MaxRawBytes            int64
	Retention              time.Duration
	AllowManagementDetails bool
	AllowPrivateDetails    bool
	AllowRawDetails        bool
}

func DefaultUsagePolicy() UsagePolicy {
	limits := kernel.DefaultUsageLimits()
	return UsagePolicy{MaxValues: limits.MaxValues, MaxExtensionBytes: limits.MaxExtensionBytes, MaxRawBytes: kernel.DefaultUsageCapturePolicy().MaxBytes, Retention: 30 * 24 * time.Hour}
}

// Control authentication is independent of inference access. This wrapper
// protects every HTTP management route; Unix IPC retains socket ownership.
func (d *Daemon) controlHTTPHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/usage/detail", func(w http.ResponseWriter, r *http.Request) {
		var input UsageDetailRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil || input.ID <= 0 {
			http.Error(w, "invalid usage detail request", http.StatusBadRequest)
			return
		}
		if err := d.usagePolicy.Authorize(input.UsageDetailOptions); err != nil {
			http.Error(w, "usage detail denied by host policy", http.StatusForbidden)
			return
		}
		item, err := d.fetchUsageDetail(r.Context(), input.ID, input.UsageDetailOptions)
		if err != nil {
			http.Error(w, "usage detail unavailable", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(item)
	})
	mux.Handle("/", d.server.HandlerWithOptions(api.HandlerOptions{ControlPlane: true}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := "Bearer " + d.config.HTTPControlToken
		if d.config.HTTPControlToken == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(want)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// Retention is also a disclosure boundary. Cleanup may legitimately lag under
// load; read only common metadata first and deny expired payloads even then.
// Callers must authorize the requested classes before entering this method.
func (d *Daemon) fetchUsageDetail(ctx context.Context, id int64, options UsageDetailOptions) (store.UsageDetailRecord, error) {
	metadata, err := d.store.UsageDetail(ctx, id, UsageDetailOptions{})
	if err != nil {
		return store.UsageDetailRecord{}, err
	}
	at, err := time.Parse(time.RFC3339Nano, metadata.Event.Timestamp)
	if err != nil || at.Before(time.Now().Add(-d.usagePolicy.Retention)) {
		return store.UsageDetailRecord{}, fmt.Errorf("usage detail expired or unavailable")
	}
	if !options.IncludeExtensions && !options.IncludeRaw {
		return metadata, nil
	}
	return d.store.UsageDetail(ctx, id, options)
}

func (policy UsagePolicy) Validate() error {
	if policy.MaxValues <= 0 || policy.MaxExtensionBytes <= 0 || policy.MaxRawBytes <= 0 || policy.Retention <= 0 {
		return fmt.Errorf("usage bounds and retention must be positive")
	}
	if (policy.AllowPrivateDetails || policy.AllowRawDetails) && !policy.AllowManagementDetails {
		return fmt.Errorf("private/raw usage grants require management details")
	}
	return nil
}

func (policy UsagePolicy) Authorize(options UsageDetailOptions) error {
	if !policy.AllowManagementDetails {
		return fmt.Errorf("usage details are disabled by host policy")
	}
	if options.IncludePrivate && !options.IncludeExtensions {
		return fmt.Errorf("private usage requires extension details")
	}
	if options.IncludePrivate && !policy.AllowPrivateDetails {
		return fmt.Errorf("private usage details are disabled by host policy")
	}
	if options.IncludeRaw && !policy.AllowRawDetails {
		return fmt.Errorf("raw usage details are disabled by host policy")
	}
	return nil
}
