package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fm39hz/gobroom/internal/api"
	"github.com/fm39hz/gobroom/internal/artifacts"
	"github.com/fm39hz/gobroom/internal/controlplane"
	"github.com/fm39hz/gobroom/internal/discovery"
	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/quota"
	runtimehealth "github.com/fm39hz/gobroom/internal/runtime"
	sessionruntime "github.com/fm39hz/gobroom/internal/runtime/sessionstore"
	"github.com/fm39hz/gobroom/internal/store"
	usageworker "github.com/fm39hz/gobroom/internal/usage"
)

type Config struct {
	DBPath                    string
	IPCPath                   string
	HTTPEnabled               bool
	HTTPAddr                  string
	HTTPControl               bool
	HTTPControlAddr           string
	HTTPToken                 string
	ProviderManifestDir       string
	QuotaPolling              bool
	ArtifactStoreDir          string
	ArtifactStoreMaxBytes     int64
	ArtifactStoreMaxBodyBytes int64
}

type Daemon struct {
	config              Config
	store               *store.Store
	artifactStore       *artifacts.Store
	server              *api.Server
	ipc                 *IPCServer
	http                *http.Server
	httpControl         *http.Server
	kernel              *kernel.Kernel
	providerBindings    map[string]provider.RuntimeBinding
	providerRegistry    *provider.RuntimeRegistry
	providerCatalog     []provider.DefinitionMetadata
	strategyCatalog     *kernel.StrategyCatalog
	strategyDefinitions []kernel.StrategyDefinition
	extensionCatalog    extensions.CatalogView
	authFlows           map[string]provider.AuthFlow
	providerAuthModes   map[string]string
	providerAuthFlowIDs map[string]string
	sessionCache        *sessionruntime.Cache
	policy              *runtimehealth.PolicyGate
	usageCancel         context.CancelFunc
	mu                  sync.Mutex
	oauthMu             sync.Mutex
	oauth               *authorizationSessions
	runCtx              context.Context
	authWG              sync.WaitGroup
	credentialRefreshMu sync.Mutex
	credentialRefreshes map[string]*sync.Mutex
	logMu               sync.RWMutex
	logs                []LogRecord
	stop                func()
}

type LogRecord struct {
	At         time.Time           `json:"at"`
	Level      string              `json:"level"`
	Message    string              `json:"message"`
	RequestID  string              `json:"requestId,omitempty"`
	Method     string              `json:"method,omitempty"`
	Path       string              `json:"path,omitempty"`
	Status     int                 `json:"status,omitempty"`
	DurationMS int64               `json:"durationMs,omitempty"`
	Bytes      int64               `json:"bytes,omitempty"`
	Model      string              `json:"model,omitempty"`
	Operation  normalize.Operation `json:"operation,omitempty"`
}

func (d *Daemon) appendLog(level, message string) {
	record := LogRecord{At: time.Now(), Level: level, Message: message}
	d.storeLog(record)
	logLevel := slog.LevelInfo
	switch strings.ToLower(level) {
	case "warn", "warning":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	case "debug":
		logLevel = slog.LevelDebug
	}
	slog.Log(context.Background(), logLevel, message)
}

func (d *Daemon) appendAccessLog(entry api.AccessLogRecord) {
	level := "info"
	logLevel := slog.LevelInfo
	if entry.Status >= 500 {
		level, logLevel = "error", slog.LevelError
	} else if entry.Status >= 400 {
		level, logLevel = "warn", slog.LevelWarn
	}
	record := LogRecord{
		At: entry.At, Level: level, Message: "HTTP request", RequestID: entry.RequestID,
		Method: entry.Method, Path: entry.Path, Status: entry.Status, DurationMS: entry.DurationMS,
		Bytes: entry.Bytes, Model: entry.Model, Operation: entry.Operation,
	}
	d.storeLog(record)
	slog.Log(context.Background(), logLevel, record.Message,
		"request_id", record.RequestID, "method", record.Method, "path", record.Path,
		"status", record.Status, "duration_ms", record.DurationMS, "bytes", record.Bytes,
		"model", record.Model, "operation", record.Operation,
	)
}

func (d *Daemon) storeLog(record LogRecord) {
	d.logMu.Lock()
	defer d.logMu.Unlock()
	d.logs = append(d.logs, record)
	if len(d.logs) > 256 {
		d.logs = d.logs[len(d.logs)-256:]
	}
}

type healthEvent struct {
	route kernel.Route
	state runtimehealth.RouteHealth
}

func New(config Config) *Daemon { return &Daemon{config: config} }

func (d *Daemon) Start(ctx context.Context) error {
	s, err := store.Open(d.config.DBPath)
	if err != nil {
		return err
	}
	d.store = s
	d.appendLog("info", "daemon starting")
	runtimeRegistry, err := provider.NewRuntimeRegistry()
	if err != nil {
		_ = s.Close()
		return err
	}
	d.sessionCache, err = sessionruntime.New(s)
	if err != nil {
		_ = s.Close()
		return err
	}
	if err := runtimeRegistry.ReplaceSessionStore(d.sessionCache); err != nil {
		_ = s.Close()
		return err
	}
	if d.config.ProviderManifestDir != "" {
		if err := runtimeRegistry.LoadDefinitionDir(d.config.ProviderManifestDir); err != nil {
			_ = s.Close()
			return fmt.Errorf("load provider manifests: %w", err)
		}
	}
	d.providerRegistry = runtimeRegistry
	d.providerCatalog, err = runtimeRegistry.DefinitionCatalog()
	if err != nil {
		_ = s.Close()
		return fmt.Errorf("build provider definition catalog: %w", err)
	}
	runtimeBindings, err := runtimeRegistry.BuildBindings()
	if err != nil {
		_ = s.Close()
		return err
	}
	operationSnapshot, err := runtimeRegistry.OperationSnapshot()
	if err != nil {
		_ = s.Close()
		return fmt.Errorf("build operation catalog: %w", err)
	}
	d.extensionCatalog, err = runtimeRegistry.ExtensionCatalog()
	if err != nil {
		_ = s.Close()
		return fmt.Errorf("build extension catalog: %w", err)
	}
	extensionSnapshot, err := runtimeRegistry.FreezeCatalog()
	if err != nil {
		_ = s.Close()
		return fmt.Errorf("resolve frozen extension catalog: %w", err)
	}
	d.strategyCatalog, err = kernel.NewStrategyCatalog(extensionSnapshot)
	if err != nil {
		_ = s.Close()
		return fmt.Errorf("bind strategy catalog: %w", err)
	}
	d.strategyDefinitions = d.strategyCatalog.Definitions()
	d.providerBindings = runtimeBindings
	d.authFlows = runtimeRegistry.AuthFlowsForBindings(runtimeBindings)
	d.providerAuthModes = runtimeRegistry.DefaultCredentialTypes()
	d.providerAuthFlowIDs = runtimeRegistry.AuthFlowIDsByDefinition()
	d.server = api.NewServerWithRuntimeBindings(s, runtimeBindings, d.strategyCatalog)
	d.server.SetAccessLogger(d.appendAccessLog)
	d.server.SetProviderDefinitionCatalog(d.providerCatalog)
	d.server.SetExtensionCatalog(d.extensionCatalog)
	if err := d.server.SetOperationSnapshot(operationSnapshot); err != nil {
		_ = s.Close()
		return fmt.Errorf("bind data-plane operation catalog: %w", err)
	}
	d.server.SetDataPlaneToken(d.config.HTTPToken)
	ctx, cancel := context.WithCancel(ctx)
	d.runCtx = ctx
	started := false
	var bodyStore *artifacts.Store
	defer func() {
		if !started {
			cancel()
			if bodyStore != nil {
				_ = bodyStore.Close()
			}
		}
	}()
	if d.server.Control() == nil {
		_ = s.Close()
		if err := d.server.InitializationError(); err != nil {
			return fmt.Errorf("initialize control plane: %w", err)
		}
		return fmt.Errorf("initialize control plane: manager is unavailable without an initialization error")
	}
	d.policy = runtimehealth.NewPolicyGate()
	d.policy.SetEnrichmentContext(ctx)
	if snapshots, loadErr := d.store.QuotaSnapshots(); loadErr == nil {
		for _, snapshot := range snapshots {
			d.policy.SetQuota(snapshot)
		}
	}
	quotaPersistence := runtimehealth.NewQuotaSnapshotQueue(d.store, 512, func(snapshot quota.Snapshot, err error) {
		d.appendLog("error", fmt.Sprintf("persist quota evidence %s/%s/%s/%s: %v", snapshot.ProviderNodeID, snapshot.ConnectionID, snapshot.ModelRef, snapshot.WindowName, err))
	})
	d.policy.SetOutcomeQuotaObserver(func(snapshot quota.Snapshot) {
		if !quotaPersistence.Enqueue(snapshot) {
			d.appendLog("warn", "quota evidence persistence queue is full; latest snapshot was not queued")
		}
	})
	d.kernel, err = kernel.New(d.server.Control().Snapshot(), d.policy, 256)
	if err != nil {
		_ = s.Close()
		return err
	}
	maxArtifactBytes := d.config.ArtifactStoreMaxBytes
	if maxArtifactBytes <= 0 {
		maxArtifactBytes = 256 << 20
	}
	maxArtifactBodyBytes := d.config.ArtifactStoreMaxBodyBytes
	if maxArtifactBodyBytes <= 0 || maxArtifactBodyBytes > maxArtifactBytes {
		maxArtifactBodyBytes = min(maxArtifactBytes, int64(64<<20))
	}
	bodyStore, err = artifacts.NewStore(d.config.ArtifactStoreDir, artifacts.Limits{MaxBytes: maxArtifactBytes, MaxBodyBytes: maxArtifactBodyBytes, DefaultTTL: time.Hour})
	if err != nil {
		_ = s.Close()
		return fmt.Errorf("initialize artifact body store: %w", err)
	}
	d.artifactStore = bodyStore
	d.kernel.ArtifactStore = bodyStore
	d.server.SetArtifactStore(bodyStore)
	d.kernel.Scheduler.SetStrategyDefinitions(d.strategyDefinitions)
	d.kernel.Features, err = kernel.NewFeatureRegistryFromCatalog(extensionSnapshot)
	if err != nil {
		_ = s.Close()
		return fmt.Errorf("bind feature evaluators: %w", err)
	}
	d.kernel.Transforms, err = kernel.NewRequestTransformRegistryFromCatalog(extensionSnapshot)
	if err != nil {
		_ = s.Close()
		return fmt.Errorf("bind request transforms: %w", err)
	}
	d.kernel.ResponseTransforms, err = kernel.NewResponseTransformRegistryFromCatalog(extensionSnapshot)
	if err != nil {
		_ = s.Close()
		return fmt.Errorf("bind response transforms: %w", err)
	}
	if err := d.kernel.ValidateTransformBindings(d.kernel.Snapshots.Load().TransformBindings); err != nil {
		_ = s.Close()
		return fmt.Errorf("validate configured transform bindings: %w", err)
	}
	d.kernel.Operations = operationSnapshot
	d.kernel.Adapters = runtimeRegistry.AdaptersForBindings(runtimeBindings)
	d.kernel.ErrorClassifiers = runtimeRegistry.ErrorClassifiersForBindings(runtimeBindings)
	d.kernel.UsageSources = runtimeRegistry.UsageSources()
	d.kernel.SessionStores = runtimeRegistry.SessionStores()
	opportunisticQuota := &runtimehealth.OpportunisticQuota{
		Sources:    runtimeRegistry.QuotaSources(),
		Endpoints:  runtimeRegistry.Endpoints(),
		Transports: runtimeRegistry.Transports(),
		Credential: func(ctx context.Context, route kernel.Route) (kernel.Credential, error) {
			credential, ok := d.store.ConnectionCredentialByID(route.CredentialID)
			if !ok {
				return kernel.Credential{}, fmt.Errorf("connection %q is unavailable", route.CredentialID)
			}
			flow, ok := d.authFlows[route.AuthFlowID]
			if !ok {
				return kernel.Credential{}, fmt.Errorf("auth flow %q is unavailable", route.AuthFlowID)
			}
			return flow.Resolve(ctx, provider.AuthInput{ConnectionID: route.CredentialID, Type: credential.Type, Secret: credential.Secret})
		},
		Record: func(snapshot quota.Snapshot) {
			d.policy.SetQuota(snapshot)
			if !quotaPersistence.Enqueue(snapshot) {
				d.appendLog("warn", "opportunistic quota snapshot persistence queue is full")
			}
		},
	}
	d.policy.SetQuotaEnricher(opportunisticQuota.Trigger)
	quotaPersistenceDone := make(chan struct{})
	go func() {
		defer close(quotaPersistenceDone)
		quotaPersistence.Run()
	}()
	quotaPollerDone := make(chan struct{})
	go func() {
		defer close(quotaPollerDone)
		(runtimehealth.QuotaPoller{
			Sources:    runtimeRegistry.QuotaSources(),
			Endpoints:  runtimeRegistry.Endpoints(),
			Transports: runtimeRegistry.Transports(),
			Snapshot:   func() kernel.Snapshot { return d.kernel.Snapshots.Load() },
			Credential: func(ctx context.Context, route kernel.Route) (kernel.Credential, error) {
				credential, ok := d.store.ConnectionCredentialByID(route.CredentialID)
				if !ok {
					return kernel.Credential{}, fmt.Errorf("connection %q is unavailable", route.CredentialID)
				}
				flow, ok := d.authFlows[route.AuthFlowID]
				if !ok {
					return kernel.Credential{}, fmt.Errorf("auth flow %q is unavailable", route.AuthFlowID)
				}
				return flow.Resolve(ctx, provider.AuthInput{ConnectionID: route.CredentialID, Type: credential.Type, Secret: credential.Secret})
			},
			Record: func(snapshot quota.Snapshot) {
				d.policy.SetQuota(snapshot)
				if !quotaPersistence.Enqueue(snapshot) {
					d.appendLog("warn", "quota poll snapshot persistence queue is full")
				}
			},
			Enabled: d.config.QuotaPolling,
		}).Run(ctx)
	}()
	d.kernel.ResolveCredential = func(_ context.Context, route kernel.Route) (kernel.Credential, error) {
		credential, ok := d.store.ConnectionCredentialByID(route.CredentialID)
		if !ok {
			return kernel.Credential{}, fmt.Errorf("connection %q is unavailable", route.CredentialID)
		}
		flow, ok := d.authFlows[route.AuthFlowID]
		if !ok {
			return kernel.Credential{}, fmt.Errorf("auth flow %q is unavailable", route.AuthFlowID)
		}
		return flow.Resolve(context.Background(), provider.AuthInput{ConnectionID: route.CredentialID, Type: credential.Type, Secret: credential.Secret})
	}
	d.kernel.RefreshCredential = func(ctx context.Context, route kernel.Route, current kernel.Credential) (kernel.Credential, error) {
		unlock := d.lockCredentialRefresh(route.CredentialID)
		defer unlock()
		stored, ok := d.store.ConnectionCredentialByID(route.CredentialID)
		if !ok {
			return kernel.Credential{}, fmt.Errorf("connection %q is unavailable", route.CredentialID)
		}
		flow, ok := d.authFlows[route.AuthFlowID]
		if !ok {
			return kernel.Credential{}, fmt.Errorf("auth flow %q is unavailable", route.AuthFlowID)
		}
		latest, err := flow.Resolve(ctx, provider.AuthInput{ConnectionID: route.CredentialID, Type: stored.Type, Secret: stored.Secret})
		if err != nil {
			return kernel.Credential{}, err
		}
		if latest.Secret != "" && latest.Secret != current.Secret {
			return latest, nil
		}
		refreshed, err := flow.Refresh(ctx, current)
		if err != nil {
			return kernel.Credential{}, err
		}
		secret, err := refreshedCredentialSecret(refreshed)
		if err != nil {
			return kernel.Credential{}, err
		}
		if err := d.store.UpdateConnectionSecretIfUnchanged(route.CredentialID, stored.Secret, secret); err != nil {
			return kernel.Credential{}, err
		}
		return refreshed, nil
	}
	healthEvents := make(chan healthEvent, 256)
	d.policy.Health.SetObserver(func(route kernel.Route, state runtimehealth.RouteHealth) {
		select {
		case healthEvents <- healthEvent{route: route, state: state}:
		default:
		}
	})
	runtimeRows, _ := d.store.ConnectionRuntimes()
	for _, item := range runtimeRows {
		for _, route := range d.kernel.Snapshots.Load().Routes {
			if route.CredentialID != item.ConnectionID {
				continue
			}
			d.policy.Health.Restore(route, runtimehealth.RouteHealth{Failures: item.ConsecutiveFailures, CooldownUntil: parseTime(item.CooldownUntil), LastError: item.LastError, LastCause: kernel.OutcomeCause(item.ErrorCode), LastScope: kernel.ScopeConnection})
		}
	}
	availabilityRows, _ := d.store.ModelAvailabilities()
	for _, item := range availabilityRows {
		blockedUntil := parseTime(item.BlockedUntil)
		if blockedUntil.IsZero() || !blockedUntil.After(time.Now()) {
			continue
		}
		for _, route := range d.kernel.Snapshots.Load().Routes {
			if route.CredentialID != item.ConnectionID || route.ExternalModel != item.ModelRef {
				continue
			}
			state := runtimehealth.RouteHealth{Failures: item.ConsecutiveFailures, CooldownUntil: blockedUntil, LastError: item.LastError, LastCause: kernel.OutcomeCause(item.Reason), LastScope: kernel.OutcomeScope(item.ErrorCode)}
			d.policy.Health.Restore(route, state)
		}
	}
	healthEventsDone := make(chan struct{})
	go func() {
		defer close(healthEventsDone)
		persist := func(event healthEvent) {
			_ = d.store.SaveConnectionRuntime(store.ConnectionRuntime{ConnectionID: event.route.CredentialID, Status: healthStatus(event.state), ConsecutiveFailures: event.state.Failures, CooldownUntil: formatTime(event.state.CooldownUntil), LastError: event.state.LastError, ErrorCode: string(event.state.LastCause)})
			_ = d.store.SaveModelAvailability(store.ModelAvailability{ConnectionID: event.route.CredentialID, ModelRef: event.route.ExternalModel, Status: healthStatus(event.state), BlockedUntil: formatTime(event.state.CooldownUntil), Reason: string(event.state.LastCause), ErrorCode: string(event.state.LastScope), LastError: event.state.LastError, ConsecutiveFailures: event.state.Failures})
		}
		for {
			select {
			case <-ctx.Done():
				for {
					select {
					case event := <-healthEvents:
						persist(event)
					default:
						return
					}
				}
			case event := <-healthEvents:
				persist(event)
			}
		}
	}()
	d.server.SetExecutor(func(ctx context.Context, request normalize.Request, writer http.ResponseWriter) error {
		return d.kernel.Execute(ctx, request, kernel.Credential{}, writer)
	})
	d.server.SetReloadHook(func() error {
		snapshot := d.server.Control().Snapshot()
		if err := d.kernel.ValidateTransformBindings(snapshot.TransformBindings); err != nil {
			return err
		}
		return d.kernel.PublishSnapshot(snapshot)
	})
	usageCtx, usageCancel := context.WithCancel(ctx)
	d.usageCancel = usageCancel
	go (usageworker.Worker{Store: s, Events: d.kernel.Events}).Run(usageCtx)
	sessionDone := make(chan struct{})
	go func() {
		defer close(sessionDone)
		d.sessionCache.Run(ctx)
	}()

	d.stop = func() {
		cancel()
		d.cancelAuthorizationSessions()
		d.authWG.Wait()
		if d.usageCancel != nil {
			d.usageCancel()
		}
		if d.kernel != nil {
			d.kernel.Close()
		}
		<-sessionDone
		<-healthEventsDone
		<-quotaPollerDone
		quotaPersistence.Close()
		<-quotaPersistenceDone
		if d.artifactStore != nil {
			_ = d.artifactStore.Close()
			d.artifactStore = nil
		}
		_ = d.store.Close()
	}

	d.ipc = NewIPCServer(d.config.IPCPath, d.handleIPC)
	if err := d.ipc.Start(ctx); err != nil {
		d.stop()
		return err
	}

	if d.config.HTTPEnabled {
		d.http = &http.Server{Addr: d.config.HTTPAddr, Handler: d.server.HandlerWithOptions(api.HandlerOptions{DataPlane: true})}
		go func() {
			if err := d.http.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				fmt.Fprintf(os.Stderr, "[gobroomd] HTTP: %v\n", err)
			}
		}()
	}
	if d.config.HTTPControl {
		controlAddr := d.config.HTTPControlAddr
		if controlAddr == "" {
			controlAddr = "127.0.0.1:2713"
		}
		d.httpControl = &http.Server{Addr: controlAddr, Handler: d.server.HandlerWithOptions(api.HandlerOptions{ControlPlane: true})}
		go func() {
			if err := d.httpControl.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				fmt.Fprintf(os.Stderr, "[gobroomd] HTTP control: %v\n", err)
			}
		}()
	}
	started = true
	return nil
}

func (d *Daemon) lockCredentialRefresh(connectionID string) func() {
	d.credentialRefreshMu.Lock()
	if d.credentialRefreshes == nil {
		d.credentialRefreshes = make(map[string]*sync.Mutex)
	}
	lock := d.credentialRefreshes[connectionID]
	if lock == nil {
		lock = &sync.Mutex{}
		d.credentialRefreshes[connectionID] = lock
	}
	d.credentialRefreshMu.Unlock()
	lock.Lock()
	return lock.Unlock
}

func (d *Daemon) providerNodeForConnection(connectionID string) (string, error) {
	if connectionID == "" {
		return "", fmt.Errorf("connectionID is required")
	}
	connections, err := d.store.Connections("")
	if err != nil {
		return "", err
	}
	for _, connection := range connections {
		if connection.ID == connectionID && connection.Enabled {
			return connection.ProviderNodeID, nil
		}
	}
	return "", fmt.Errorf("enabled connection %q was not found", connectionID)
}

func refreshedCredentialSecret(credential kernel.Credential) (string, error) {
	if credential.RefreshToken == "" && credential.ClientSecret == "" && credential.ExpiresAt.IsZero() {
		return credential.Secret, nil
	}
	return provider.EncodeOAuthCredential(credential)
}

func (d *Daemon) Wait(ctx context.Context) error { <-ctx.Done(); return d.Stop(context.Background()) }

func (d *Daemon) Stop(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stop == nil {
		return nil
	}
	if d.http != nil {
		_ = d.http.Shutdown(ctx)
	}
	if d.httpControl != nil {
		_ = d.httpControl.Shutdown(ctx)
	}
	if d.ipc != nil {
		_ = d.ipc.Close()
	}
	d.stop()
	d.stop = nil
	d.oauthMu.Lock()
	d.oauth = nil
	d.oauthMu.Unlock()
	return nil
}

func (d *Daemon) handleIPC(ctx context.Context, request IPCRequest) IPCResponse {
	switch request.Method {
	case "status":
		return IPCResponse{ID: request.ID, OK: true, Result: d.server.Status()}
	case "config.export":
		bundle, err := controlplane.ExportBundle(d.store, d.strategyCatalog.Extensions())
		if err != nil {
			return fail(request, err.Error())
		}
		return success(request, bundle)
	case "config.validate":
		var bundle controlplane.ConfigBundle
		if err := decodeParams(request.Params, &bundle); err != nil {
			return fail(request, err.Error())
		}
		if err := controlplane.ValidateBundleWithStrategies(bundle, d.strategyCatalog); err != nil {
			return fail(request, err.Error())
		}
		if d.kernel != nil {
			if err := d.kernel.ValidateTransformBindings(bundle.TransformBindings); err != nil {
				return fail(request, err.Error())
			}
		}
		return success(request, map[string]any{"valid": true, "version": bundle.Version})
	case "config.diff":
		var desired controlplane.ConfigBundle
		if err := decodeParams(request.Params, &desired); err != nil {
			return fail(request, err.Error())
		}
		current, err := controlplane.ExportBundle(d.store, d.strategyCatalog.Extensions())
		if err != nil {
			return fail(request, err.Error())
		}
		diff, err := controlplane.DiffBundle(current, desired, d.strategyCatalog)
		if err != nil {
			return fail(request, err.Error())
		}
		if d.kernel != nil {
			if err := d.kernel.ValidateTransformBindings(desired.TransformBindings); err != nil {
				return fail(request, err.Error())
			}
		}
		return success(request, diff)
	case "config.apply":
		var bundle controlplane.ConfigBundle
		if err := decodeParams(request.Params, &bundle); err != nil {
			return fail(request, err.Error())
		}
		if d.kernel != nil {
			if err := d.kernel.ValidateTransformBindings(bundle.TransformBindings); err != nil {
				return fail(request, err.Error())
			}
		}
		if err := controlplane.ApplyBundle(d.store, bundle, d.strategyCatalog); err != nil {
			return fail(request, err.Error())
		}
		if err := d.server.Reload(); err != nil {
			return fail(request, err.Error())
		}
		d.appendLog("info", "typed config bundle applied")
		return success(request, d.server.Status())
	case "reload":
		if err := d.server.Reload(); err != nil {
			return IPCResponse{ID: request.ID, OK: false, Error: err.Error()}
		}
		d.appendLog("info", "snapshot reloaded")
		return IPCResponse{ID: request.ID, OK: true, Result: d.server.Status()}
	case "resolve":
		name := stringParam(request.Params, "model")
		if name == "" {
			return fail(request, "model is required")
		}
		if d.server.Control() == nil {
			return fail(request, "control plane unavailable")
		}
		resolved, err := d.server.Control().Resolve(name)
		if err != nil {
			return fail(request, err.Error())
		}
		return success(request, resolved)
	case "providers.list":
		items, err := d.store.ProviderNodes()
		if err != nil {
			return fail(request, err.Error())
		}
		return success(request, items)
	case "providers.catalog":
		if d.providerRegistry == nil {
			return fail(request, "provider definitions unavailable")
		}
		return success(request, d.providerCatalog)
	case "extensions.catalog":
		if d.providerRegistry == nil {
			return fail(request, "extension catalog unavailable")
		}
		return success(request, d.extensionCatalog)
	case "providers.refresh_models":
		var input struct {
			NodeID       string   `json:"nodeID"`
			ConnectionID string   `json:"connectionID"`
			ModelIDs     []string `json:"modelIDs"`
		}
		if err := decodeParams(request.Params, &input); err != nil {
			return fail(request, err.Error())
		}
		if input.NodeID == "" {
			return fail(request, "nodeID is required")
		}
		service := discovery.Service{Store: d.store, Bindings: d.providerBindings}
		var result discovery.Result
		var err error
		if input.ConnectionID == "" {
			if input.ModelIDs != nil {
				return fail(request, "selective model import requires a connection ID")
			}
			result, err = service.RefreshNode(ctx, input.NodeID)
		} else {
			result, err = service.ImportConnectionModels(ctx, input.NodeID, input.ConnectionID, input.ModelIDs)
		}
		if err != nil {
			return fail(request, err.Error())
		}
		if err := d.server.Reload(); err != nil {
			return fail(request, err.Error())
		}
		return success(request, result)
	case "health.list":
		if d.policy == nil {
			return fail(request, "runtime policy unavailable")
		}
		return success(request, d.policy.Health.Snapshot())
	case "quota.list":
		if d.policy == nil {
			return fail(request, "runtime policy unavailable")
		}
		return success(request, d.policy.Quotas())
	case "usage.list":
		limit := 100
		if raw, ok := request.Params["limit"].(float64); ok && int(raw) > 0 {
			limit = int(raw)
		}
		items, err := d.store.UsageEvents(limit)
		if err != nil {
			return fail(request, err.Error())
		}
		return success(request, items)
	case "usage.summary":
		items, err := d.store.UsageSummary(30)
		if err != nil {
			return fail(request, err.Error())
		}
		return success(request, items)
	case "usage.prune":
		cutoff := stringParam(request.Params, "before")
		before, err := time.Parse(time.RFC3339, cutoff)
		if err != nil {
			return fail(request, "before must be RFC3339")
		}
		deleted, err := d.store.PruneUsage(before)
		if err != nil {
			return fail(request, err.Error())
		}
		d.appendLog("info", fmt.Sprintf("pruned %d usage events", deleted))
		return success(request, map[string]any{"deleted": deleted, "before": before})
	case "logs.list":
		limit := 100
		if raw, ok := request.Params["limit"].(float64); ok {
			if raw < 1 || raw > 256 || raw != float64(int(raw)) {
				return fail(request, "limit must be an integer between 1 and 256")
			}
			limit = int(raw)
		}
		d.logMu.RLock()
		start := len(d.logs) - limit
		if start < 0 {
			start = 0
		}
		logs := append([]LogRecord(nil), d.logs[start:]...)
		d.logMu.RUnlock()
		return success(request, logs)
	case "routes.explain":
		model := stringParam(request.Params, "model")
		if model == "" {
			return fail(request, "model is required")
		}
		resolved, err := d.kernel.Resolve(model)
		if err != nil {
			return fail(request, err.Error())
		}
		requestClass := stringParam(request.Params, "requestClass")
		sessionID := stringParam(request.Params, "sessionID")
		return success(request, d.policy.ExplainRoutes(resolved.Candidates, time.Now(), requestClass, sessionID))
	case "quota.set":
		if d.policy == nil {
			return fail(request, "runtime policy unavailable")
		}
		var snapshot quota.Snapshot
		if err := decodeParams(request.Params, &snapshot); err != nil {
			return fail(request, err.Error())
		}
		d.policy.SetQuota(snapshot)
		if err := d.store.SaveQuotaSnapshot(snapshot); err != nil {
			return fail(request, err.Error())
		}
		return success(request, snapshot)
	case "providers.create":
		var input store.CreateProviderNodeInput
		if err := decodeParams(request.Params, &input); err != nil {
			return fail(request, err.Error())
		}
		d.applyProviderDefaults(&input)
		if err := d.validatePrefix(input.Prefix, input.DefinitionID, input.Protocol); err != nil {
			return fail(request, err.Error())
		}
		item, err := d.store.CreateProviderNode(input)
		if err != nil {
			return fail(request, err.Error())
		}
		if err := d.server.Reload(); err != nil {
			return fail(request, err.Error())
		}
		return success(request, item)
	case "providers.update":
		var input store.UpdateProviderNodeInput
		if err := decodeParams(request.Params, &input); err != nil {
			return fail(request, err.Error())
		}
		if input.ID == "" {
			return fail(request, "id is required")
		}
		if input.DefinitionID != "" && input.AuthMode == "" {
			if credentialType := d.providerAuthModes[input.DefinitionID]; credentialType != "" {
				input.AuthMode = credentialType
			}
		}
		if input.Prefix != "" {
			if err := d.validatePrefixUpdate(input.ID, input.Prefix, input.DefinitionID); err != nil {
				return fail(request, err.Error())
			}
		}
		item, err := d.store.UpdateProviderNode(input)
		if err != nil {
			return fail(request, err.Error())
		}
		if err := d.server.Reload(); err != nil {
			return fail(request, err.Error())
		}
		return success(request, item)
	case "providers.delete":
		id := stringParam(request.Params, "id")
		if id == "" {
			return fail(request, "id is required")
		}
		if err := d.server.Control().ValidateProviderDelete(id); err != nil {
			return fail(request, err.Error())
		}
		if err := d.store.DeleteProviderNode(id); err != nil {
			return fail(request, err.Error())
		}
		if err := d.server.Reload(); err != nil {
			return fail(request, err.Error())
		}
		return success(request, map[string]string{"deleted": id})
	case "connections.list":
		nodeID := stringParam(request.Params, "nodeID")
		items, err := d.store.Connections(nodeID)
		if err != nil {
			return fail(request, err.Error())
		}
		return success(request, items)
	case "auth.authorization.start":
		challenge, err := d.startAuthorization(stringParam(request.Params, "connectionID"))
		if err != nil {
			return fail(request, err.Error())
		}
		return success(request, challenge)
	case "auth.authorization.complete":
		sessionID := stringParam(request.Params, "sessionID")
		state := stringParam(request.Params, "state")
		code := stringParam(request.Params, "code")
		callbackURL := stringParam(request.Params, "callbackURL")
		if callbackURL != "" {
			parsedCode, parsedState, err := authorizationCallbackCode(callbackURL)
			if err != nil {
				return fail(request, err.Error())
			}
			code, state = parsedCode, parsedState
		}
		result, err := d.completeAuthorization(ctx, sessionID, state, code, callbackURL)
		if err != nil {
			return fail(request, err.Error())
		}
		return success(request, result)
	case "auth.authorization.cancel":
		if err := d.cancelAuthorization(stringParam(request.Params, "sessionID")); err != nil {
			return fail(request, err.Error())
		}
		return success(request, map[string]string{"status": "cancelled"})
	case "auth.device.start":
		result, err := d.startDeviceAuthorization(ctx, stringParam(request.Params, "connectionID"))
		if err != nil {
			return fail(request, err.Error())
		}
		return success(request, result)
	case "auth.device.get":
		result, err := d.deviceAuthorizationStatus(stringParam(request.Params, "sessionID"))
		if err != nil {
			return fail(request, err.Error())
		}
		return success(request, result)
	case "auth.device.cancel":
		if err := d.cancelDeviceAuthorization(stringParam(request.Params, "sessionID")); err != nil {
			return fail(request, err.Error())
		}
		return success(request, map[string]string{"status": "cancelled"})
	case "connections.test":
		connectionID := stringParam(request.Params, "connectionID")
		nodeID, err := d.providerNodeForConnection(connectionID)
		if err != nil {
			return fail(request, err.Error())
		}
		result, err := (discovery.Service{Store: d.store, Bindings: d.providerBindings}).TestConnection(ctx, nodeID, connectionID)
		if err != nil {
			return fail(request, err.Error())
		}
		return success(request, result)
	case "connections.preview_models":
		connectionID := stringParam(request.Params, "connectionID")
		nodeID, err := d.providerNodeForConnection(connectionID)
		if err != nil {
			return fail(request, err.Error())
		}
		result, err := (discovery.Service{Store: d.store, Bindings: d.providerBindings}).PreviewConnectionModels(ctx, nodeID, connectionID)
		if err != nil {
			return fail(request, err.Error())
		}
		return success(request, result)
	case "connections.create":
		var input store.CreateConnectionInput
		if err := decodeParams(request.Params, &input); err != nil {
			return fail(request, err.Error())
		}
		if input.CredentialType == "" {
			if node, err := d.store.ProviderNode(input.ProviderNodeID); err == nil {
				input.CredentialType = node.AuthMode
			}
		}
		if err := d.validateConnectionSecret(ctx, input.ProviderNodeID, input.CredentialType, input.Secret); err != nil {
			return fail(request, err.Error())
		}
		item, err := d.store.CreateConnection(input)
		if err != nil {
			return fail(request, err.Error())
		}
		if err := d.server.Reload(); err != nil {
			return fail(request, err.Error())
		}
		return success(request, item)
	case "connections.update":
		var input store.UpdateConnectionInput
		if err := decodeParams(request.Params, &input); err != nil {
			return fail(request, err.Error())
		}
		if input.ID == "" {
			return fail(request, "id is required")
		}
		connections, err := d.store.Connections("")
		if err != nil {
			return fail(request, err.Error())
		}
		var existing *store.ConnectionRecord
		for index := range connections {
			if connections[index].ID == input.ID {
				existing = &connections[index]
				break
			}
		}
		if existing == nil {
			return fail(request, fmt.Sprintf("connection %q not found", input.ID))
		}
		if input.CredentialType == "" {
			input.CredentialType = existing.CredentialType
		}
		node, err := d.store.ProviderNode(existing.ProviderNodeID)
		if err != nil {
			return fail(request, err.Error())
		}
		if input.CredentialType != node.AuthMode {
			return fail(request, fmt.Sprintf("credential type %q does not match provider auth mode %q", input.CredentialType, node.AuthMode))
		}
		if input.Secret != "" {
			if err := d.validateConnectionSecret(ctx, existing.ProviderNodeID, input.CredentialType, input.Secret); err != nil {
				return fail(request, err.Error())
			}
		}
		item, err := d.store.UpdateConnection(input)
		if err != nil {
			return fail(request, err.Error())
		}
		if err := d.server.Reload(); err != nil {
			return fail(request, err.Error())
		}
		return success(request, item)
	case "connections.delete":
		id := stringParam(request.Params, "id")
		if id == "" {
			return fail(request, "id is required")
		}
		if err := d.store.DeleteConnection(id); err != nil {
			return fail(request, err.Error())
		}
		if err := d.server.Reload(); err != nil {
			return fail(request, err.Error())
		}
		return success(request, map[string]string{"deleted": id})
	case "models.list":
		items, err := d.store.Models()
		if err != nil {
			return fail(request, err.Error())
		}
		return success(request, items)
	case "discovered_models.list":
		items, err := d.store.DiscoveredRoutes(stringParam(request.Params, "providerNodeID"))
		if err != nil {
			return fail(request, err.Error())
		}
		return success(request, items)
	case "physical_models.list":
		items, err := d.store.PhysicalModels()
		if err != nil {
			return fail(request, err.Error())
		}
		snapshot := d.server.Control().Snapshot()
		for index := range items {
			var routes []kernel.Route
			node := kernel.ModelNode{Kind: kernel.ModelPhysical, AllowCompatibleSources: items[index].AllowCompatibleSources, AllowDynamicSources: items[index].AllowDynamicSources}
			for _, source := range items[index].Sources {
				member := kernel.MemberRef{Fidelity: source.Fidelity, Evidence: source.Evidence}
				if !kernel.PhysicalSourceAllowed(node, member) {
					continue
				}
				for id, route := range snapshot.Routes {
					base := id
					if at := strings.IndexByte(base, '@'); at >= 0 {
						base = base[:at]
					}
					if base == source.RouteID {
						routes = append(routes, route)
					}
				}
			}
			items[index].Projection = kernel.ProjectProfiles(items[index].Profile, routes)
		}
		return success(request, items)
	case "physical_models.upsert":
		var item store.PhysicalModel
		if err := decodeParams(request.Params, &item); err != nil {
			return fail(request, err.Error())
		}
		if item.Policy.Ref == (extensions.Ref{}) {
			item.Policy.Ref = kernel.StrategyRef("ordered-fallback", 1)
		}
		if err := d.strategyCatalog.Validate(item.Policy.Ref, item.Policy.Config); err != nil {
			return fail(request, err.Error())
		}
		if err := d.store.UpsertPhysicalModel(item); err != nil {
			return fail(request, err.Error())
		}
		if err := d.server.Reload(); err != nil {
			return fail(request, err.Error())
		}
		return success(request, item)
	case "physical_models.delete":
		name := stringParam(request.Params, "name")
		if name == "" {
			return fail(request, "name is required")
		}
		if err := d.store.DeletePhysicalModel(name); err != nil {
			return fail(request, err.Error())
		}
		if err := d.server.Reload(); err != nil {
			return fail(request, err.Error())
		}
		return success(request, map[string]string{"deleted": name})
	case "combo_models.list":
		items, err := d.store.ComboModels()
		if err != nil {
			return fail(request, err.Error())
		}
		return success(request, items)
	case "strategies.list":
		return success(request, d.strategyDefinitions)
	case "combo_models.upsert":
		var item store.ComboModel
		if err := decodeParams(request.Params, &item); err != nil {
			return fail(request, err.Error())
		}
		if item.Strategy.Ref == (extensions.Ref{}) {
			item.Strategy.Ref = kernel.StrategyRef("ordered-fallback", 1)
		}
		if err := d.strategyCatalog.Validate(item.Strategy.Ref, item.Strategy.Config); err != nil {
			return fail(request, err.Error())
		}
		if err := d.store.UpsertComboModel(item); err != nil {
			return fail(request, err.Error())
		}
		if err := d.server.Reload(); err != nil {
			return fail(request, err.Error())
		}
		return success(request, item)
	case "combo_models.delete":
		name := stringParam(request.Params, "name")
		if name == "" {
			return fail(request, "name is required")
		}
		if err := d.store.DeleteComboModel(name); err != nil {
			return fail(request, err.Error())
		}
		if err := d.server.Reload(); err != nil {
			return fail(request, err.Error())
		}
		return success(request, map[string]string{"deleted": name})
	case "custom_models.upsert":
		var item store.UpsertCatalogModelInput
		if err := decodeParams(request.Params, &item); err != nil {
			return fail(request, err.Error())
		}
		if err := d.store.UpsertCatalogModel(item); err != nil {
			return fail(request, err.Error())
		}
		if err := d.server.Reload(); err != nil {
			return fail(request, err.Error())
		}
		return success(request, item)
	case "custom_models.delete":
		id := stringParam(request.Params, "id")
		if id == "" {
			return fail(request, "id is required")
		}
		if err := d.store.DeleteCatalogModel(id); err != nil {
			return fail(request, err.Error())
		}
		if err := d.server.Reload(); err != nil {
			return fail(request, err.Error())
		}
		return success(request, map[string]string{"deleted": id})
	case "shutdown":
		go d.Stop(context.Background())
		return IPCResponse{ID: request.ID, OK: true, Result: map[string]string{"status": "stopping"}}
	default:
		return IPCResponse{ID: request.ID, OK: false, Error: "unknown method: " + request.Method}
	}
}

func (d *Daemon) applyProviderDefaults(input *store.CreateProviderNodeInput) {
	if input.DefinitionID == "" {
		input.DefinitionID = store.DefaultDefinitionForProtocol(input.Protocol)
	}
	if input.AuthMode == "" {
		input.AuthMode = d.providerAuthModes[input.DefinitionID]
	}
}

func (d *Daemon) validateConnectionSecret(ctx context.Context, providerNodeID, credentialType, secret string) error {
	node, err := d.store.ProviderNode(providerNodeID)
	if err != nil {
		return fmt.Errorf("provider node: %w", err)
	}
	if node.ID == "" {
		return fmt.Errorf("provider node %q not found", providerNodeID)
	}
	if credentialType != node.AuthMode {
		return fmt.Errorf("credential type %q does not match provider auth mode %q", credentialType, node.AuthMode)
	}
	if credentialType == "none" && secret != "" {
		return fmt.Errorf("provider auth mode none does not accept credential secret")
	}
	flowID := d.providerAuthFlowIDs[node.DefinitionID]
	flow, ok := d.authFlows[flowID]
	if flowID == "" || !ok {
		return fmt.Errorf("auth flow for provider definition %q is unavailable", node.DefinitionID)
	}
	_, err = flow.Resolve(ctx, provider.AuthInput{Type: credentialType, Secret: secret})
	if err != nil {
		return fmt.Errorf("validate connection credential: %w", err)
	}
	return nil
}

func success(request IPCRequest, result any) IPCResponse {
	return IPCResponse{ID: request.ID, OK: true, Result: result}
}
func fail(request IPCRequest, message string) IPCResponse {
	return IPCResponse{ID: request.ID, OK: false, Error: message}
}
func decodeParams(params map[string]any, target any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}
func stringParam(params map[string]any, key string) string {
	value, _ := params[key].(string)
	return value
}

func (d *Daemon) validatePrefix(prefix, definitionID, protocol string) error {
	registry := provider.NewPrefixRegistry()
	items, err := d.store.ProviderNodes()
	if err != nil {
		return err
	}
	for _, item := range items {
		if err := registry.AddNode(item.Prefix, item.DefinitionID); err != nil {
			return err
		}
	}
	if definitionID == "" {
		definitionID = store.DefaultDefinitionForProtocol(protocol)
	}
	return registry.AddNode(prefix, definitionID)
}

func (d *Daemon) validatePrefixUpdate(id, prefix, definitionID string) error {
	registry := provider.NewPrefixRegistry()
	items, err := d.store.ProviderNodes()
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.ID == id {
			if definitionID == "" {
				definitionID = item.DefinitionID
			}
			continue
		}
		if err := registry.AddNode(item.Prefix, item.DefinitionID); err != nil {
			return err
		}
	}
	if definitionID == "" {
		definitionID = "openai-compatible-chat"
	}
	return registry.AddNode(prefix, definitionID)
}

func DefaultConfig() (Config, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return Config{}, err
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return Config{}, err
	}
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = filepath.Join(os.TempDir(), "gobroom")
	}
	return Config{DBPath: filepath.Join(configDir, "gobroom", "gobroom.db"), IPCPath: filepath.Join(runtimeDir, "gobroom.sock"), HTTPEnabled: true, HTTPAddr: "127.0.0.1:2712", HTTPControl: false, HTTPControlAddr: "127.0.0.1:2713", ProviderManifestDir: filepath.Join(configDir, "gobroom", "providers"), ArtifactStoreDir: filepath.Join(cacheDir, "gobroom", "artifacts")}, nil
}

func (d *Daemon) UptimeHint() time.Duration { return 0 }

func parseTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func healthStatus(state runtimehealth.RouteHealth) string {
	if !state.CooldownUntil.IsZero() && state.CooldownUntil.After(time.Now()) {
		return "cooldown"
	}
	return "available"
}
