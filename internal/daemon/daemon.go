package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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
	"golang.org/x/sync/semaphore"
)

type Config struct {
	DBPath                    string
	RuntimeSpoolPath          string
	RuntimeSpoolMaxBytes      int64
	RuntimeSpoolMaxRecords    int
	RuntimeSpoolMaxAttempts   int
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
	providerModules     []provider.RuntimeModule
	store               *store.Store
	persistenceSpool    *runtimehealth.DurablePersistenceSpool
	artifactStore       *artifacts.Store
	server              *api.Server
	ipc                 *IPCServer
	ipcLease            *ipcSocket
	http                *http.Server
	httpControl         *http.Server
	listeners           httpListeners
	addresses           atomic.Pointer[ListenerAddresses]
	kernel              *kernel.Kernel
	providerBindings    map[string]provider.RuntimeBinding
	providerRegistry    *provider.RuntimeRegistry
	providerCatalog     []provider.DefinitionMetadata
	strategyCatalog     *kernel.StrategyCatalog
	strategyDefinitions []kernel.StrategyDefinition
	extensionCatalog    extensions.CatalogView
	authFlows           map[string]provider.AuthFlow
	providerAuthModes   map[extensions.Ref]string
	providerAuthFlowIDs map[extensions.Ref]map[string]string
	sessionCache        *sessionruntime.Cache
	policy              *runtimehealth.PolicyGate
	quotaPersistence    *runtimehealth.QuotaSnapshotQueue
	transformBindingsMu sync.Mutex
	usageCancel         context.CancelFunc
	mu                  sync.Mutex
	oauthMu             sync.Mutex
	oauth               *authorizationSessions
	oauthCallbackMu     sync.Mutex
	oauthCallbacks      map[string]*oauthCallbackListener
	runCtx              context.Context
	runCancel           context.CancelFunc
	authWG              sync.WaitGroup
	credentialRefreshMu sync.Mutex
	credentialRefreshes map[string]*credentialRefreshLock
	activationWake      chan struct{}
	activationDone      chan struct{}
	logMu               sync.RWMutex
	logs                []LogRecord
	stop                func()
	stopErr             error
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

func New(config Config) *Daemon { return NewWithProviderModules(config) }

// NewWithProviderModules lets a statically linked daemon build add reusable
// provider implementations at the composition root. Provider manifests and
// ordinary TUI/CLI workflows remain data-driven and unchanged.
func NewWithProviderModules(config Config, modules ...provider.RuntimeModule) *Daemon {
	return &Daemon{config: config, providerModules: append([]provider.RuntimeModule(nil), modules...)}
}

func (d *Daemon) Start(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stop != nil {
		return fmt.Errorf("daemon is already started")
	}
	d.stopErr = nil
	if err := ctx.Err(); err != nil {
		return err
	}
	listeners, err := reserveHTTPListeners(ctx, d.config)
	if err != nil {
		return err
	}
	started := false
	defer func() {
		if !started {
			listeners.close()
		}
	}()
	ipcReservation, err := acquireIPCSocket(ctx, d.config.IPCPath)
	if err != nil {
		return err
	}
	defer func() {
		if !started {
			_ = ipcReservation.close()
		}
	}()
	s, err := store.Open(d.config.DBPath)
	if err != nil {
		return err
	}
	d.store = s
	d.appendLog("info", "daemon starting")
	spoolPath := d.config.RuntimeSpoolPath
	if spoolPath == "" && d.config.DBPath != ":memory:" {
		spoolPath = d.config.DBPath + ".runtime-spool.json"
	}
	if spoolPath != "" {
		d.persistenceSpool, err = runtimehealth.OpenDurablePersistenceSpool(spoolPath, d.config.RuntimeSpoolMaxBytes, d.config.RuntimeSpoolMaxRecords, d.config.RuntimeSpoolMaxAttempts)
		if err != nil {
			_ = s.Close()
			return fmt.Errorf("open runtime persistence spool: %w", err)
		}
		if count := d.persistenceSpool.DeadLetterCount(); count > 0 {
			d.appendLog("error", fmt.Sprintf("runtime persistence spool has %d dead-letter records at %s", count, d.persistenceSpool.Path()))
		}
	}
	runtimeRegistry, err := provider.NewRuntimeRegistryWithModules(d.providerModules...)
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
	if err := d.loadStoredProviderDefinitions(runtimeRegistry); err != nil {
		_ = s.Close()
		return err
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
	for _, ingress := range runtimeRegistry.ClientIngresses() {
		if err := d.server.RegisterOperationIngress(ingress); err != nil {
			_ = s.Close()
			return fmt.Errorf("register client ingress %q: %w", ingress.IngressRef().Key(), err)
		}
	}
	d.server.SetDataPlaneToken(d.config.HTTPToken)
	ctx, cancel := context.WithCancel(ctx)
	d.runCtx = ctx
	d.runCancel = cancel
	var bodyStore *artifacts.Store
	defer func() {
		if !started {
			cancel()
			d.runCancel = nil
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
	quotaPersistence := runtimehealth.NewQuotaSnapshotQueue(quotaSnapshotSinkFunc(d.persistQuotaSnapshot), 512, func(snapshot quota.Snapshot, err error) {
		d.appendLog("error", fmt.Sprintf("persist quota evidence %s/%s/%s/%s: %v", snapshot.ProviderNodeID, snapshot.ConnectionID, snapshot.ModelRef, snapshot.WindowName, err))
	})
	d.quotaPersistence = quotaPersistence
	d.policy.SetOutcomeQuotaObserver(func(snapshot quota.Snapshot) {
		if !quotaPersistence.Enqueue(snapshot) {
			d.appendLog("warn", "quota evidence was not queued (queue full or configuration reset in progress)")
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
		Sources:    runtimeRegistry.QuotaSourcesForBindings(runtimeBindings),
		Endpoints:  runtimeRegistry.Endpoints(),
		Transports: runtimeRegistry.Transports(),
		Credential: d.resolveCredential,
		Record: func(snapshot quota.Snapshot) {
			resolved, accepted := d.policy.UpsertQuota(snapshot)
			if !accepted {
				return
			}
			if !quotaPersistence.Enqueue(resolved) {
				d.appendLog("warn", "opportunistic quota snapshot was not queued (queue full or configuration reset in progress)")
			}
		},
		OnError: func(route kernel.Route, stage, code string) {
			d.appendLog("warn", fmt.Sprintf("quota source failure: provider=%s connection=%s source=%s stage=%s code=%s", route.NodeID, route.CredentialID, route.QuotaSourceRef.ID, stage, code))
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
			Sources:    runtimeRegistry.QuotaSourcesForBindings(runtimeBindings),
			Endpoints:  runtimeRegistry.Endpoints(),
			Transports: runtimeRegistry.Transports(),
			Snapshot:   func() kernel.Snapshot { return d.kernel.Snapshots.Load() },
			Credential: d.resolveCredential,
			Record: func(snapshot quota.Snapshot) {
				resolved, accepted := d.policy.UpsertQuota(snapshot)
				if !accepted {
					return
				}
				if !quotaPersistence.Enqueue(resolved) {
					d.appendLog("warn", "quota poll snapshot was not queued (queue full or configuration reset in progress)")
				}
			},
			OnError: func(route kernel.Route, stage, code string) {
				d.appendLog("warn", fmt.Sprintf("quota source failure: provider=%s connection=%s source=%s stage=%s code=%s", route.NodeID, route.CredentialID, route.QuotaSourceRef.ID, stage, code))
			},
			Enabled: d.config.QuotaPolling,
		}).Run(ctx)
	}()
	d.kernel.ResolveCredential = d.resolveCredential
	d.kernel.RefreshCredential = d.refreshCredential
	healthQueue := runtimehealth.NewHealthObservationQueue(8192, func(observation runtimehealth.HealthObservation) error {
		return d.persistHealthObservation(observation)
	}, func(observation runtimehealth.HealthObservation, err error) {
		slog.Error("health state persistence failed", "scope", observation.State.LastScope, "route", observation.Route.ID, "error", err)
	})
	healthEventsDone := make(chan struct{})
	go func() {
		defer close(healthEventsDone)
		healthQueue.Run()
	}()
	d.policy.Health.SetObserver(func(route kernel.Route, state runtimehealth.RouteHealth) {
		if !healthQueue.Enqueue(runtimehealth.HealthObservation{Route: route, State: state}) {
			slog.Error("health state queue rejected observation", "scope", state.LastScope, "route", route.ID)
		}
	})
	initialRoutes := d.kernel.Snapshots.Load().Routes
	d.policy.SetGenerations(initialRoutes)
	quotaSnapshots, err := d.store.QuotaSnapshots()
	if err != nil {
		return fmt.Errorf("restore quota snapshots: %w", err)
	}
	for _, snapshot := range quotaSnapshots {
		if d.policy.Health.QuotaGenerationCurrent(snapshot) {
			d.policy.SetQuota(snapshot)
		}
	}
	if err := d.restoreHealthForRoutes(initialRoutes); err != nil {
		return fmt.Errorf("restore scoped health state: %w", err)
	}
	spoolReplayDone := d.runPersistenceSpoolReplay(ctx, healthQueue, quotaPersistence)
	d.server.SetExecutor(func(ctx context.Context, request normalize.Request, writer http.ResponseWriter) error {
		return d.kernel.Execute(ctx, request, kernel.Credential{}, writer)
	})
	d.server.SetReloadHook(func(snapshot kernel.Snapshot) (kernel.Snapshot, error) {
		if err := d.kernel.ValidateTransformBindings(snapshot.TransformBindings); err != nil {
			return d.kernel.Snapshots.Load(), err
		}
		if err := kernel.ValidateSnapshot(snapshot); err != nil {
			return d.kernel.Snapshots.Load(), err
		}
		previous := d.kernel.Snapshots.Load()
		d.policy.SetGenerations(snapshot.Routes)
		if err := d.restoreHealthForRoutes(snapshot.Routes); err != nil {
			d.policy.SetGenerations(previous.Routes)
			return d.kernel.Snapshots.Load(), err
		}
		if err := d.kernel.PublishSnapshot(snapshot); err != nil {
			d.policy.SetGenerations(previous.Routes)
			return d.kernel.Snapshots.Load(), err
		}
		return d.kernel.Snapshots.Load(), nil
	})
	d.activationWake, d.activationDone = make(chan struct{}, 1), make(chan struct{})
	go d.runCredentialActivations(ctx)
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
		<-d.activationDone
		d.closeOAuthCallbackListeners()
		if d.usageCancel != nil {
			d.usageCancel()
		}
		if d.kernel != nil {
			d.kernel.Close()
		}
		<-spoolReplayDone
		healthQueue.Close()
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
	d.ipc.operationProbe = d.serveOperationProbe
	d.ipc.afterReply = func(request IPCRequest, response IPCResponse) {
		if request.Method == "shutdown" && response.OK {
			go d.Stop(context.Background())
		}
	}
	if err := ctx.Err(); err != nil {
		d.stop()
		d.stop = nil
		return err
	}
	if err := d.ipc.startReserved(ctx, ipcReservation); err != nil {
		d.stop()
		d.stop = nil
		return err
	}
	d.ipcLease = ipcReservation

	addresses := ListenerAddresses{IPC: d.config.IPCPath}
	d.listeners = listeners
	if listeners.data != nil {
		addresses.HTTP = listeners.data.Addr().String()
		d.http = newDaemonHTTPServer(addresses.HTTP, d.server.HandlerWithOptions(api.HandlerOptions{DataPlane: true}), ctx)
		d.serveHTTP(d.http, listeners.data, "data plane")
	}
	if listeners.control != nil {
		addresses.HTTPControl = listeners.control.Addr().String()
		d.httpControl = newDaemonHTTPServer(addresses.HTTPControl, d.server.HandlerWithOptions(api.HandlerOptions{ControlPlane: true}), ctx)
		d.serveHTTP(d.httpControl, listeners.control, "control plane")
	}
	d.addresses.Store(&addresses)
	started = true
	return nil
}

func persistHealthState(stateStore *store.Store, route kernel.Route, state runtimehealth.RouteHealth) error {
	switch state.LastScope {
	case kernel.ScopeOperation:
		return stateStore.SaveOperationRuntime(store.OperationRuntime{ConnectionID: route.CredentialID, ModelRef: route.ExternalModel, OperationRef: state.OperationRef, Status: healthStatus(state), BlockedUntil: formatTime(state.CooldownUntil), LastError: state.LastError, Cause: string(state.LastCause), Evidence: string(state.LastEvidence), Confidence: state.Confidence, Generation: state.Generation, Failures: state.Failures, Successes: state.Successes, LastAttempt: formatTime(state.LastAttempt), LastSuccess: formatTime(state.LastSuccess), ObservedAt: state.LastAttempt})
	case kernel.ScopeConnection:
		return stateStore.SaveConnectionRuntime(store.ConnectionRuntime{ConnectionID: route.CredentialID, Status: healthStatus(state), Generation: route.HealthGeneration.For(state.LastScope), ConsecutiveFailures: state.Failures, CooldownUntil: formatTime(state.CooldownUntil), LastError: state.LastError, ErrorCode: string(state.LastCause)})
	case kernel.ScopeProvider:
		return stateStore.SaveProviderRuntime(store.ProviderRuntime{ProviderNodeID: route.NodeID, Status: healthStatus(state), Generation: route.HealthGeneration.For(state.LastScope), ConsecutiveFailures: state.Failures, CooldownUntil: formatTime(state.CooldownUntil), LastError: state.LastError, ErrorCode: string(state.LastCause)})
	case kernel.ScopeRoute, kernel.ScopeRouteConnection:
		return stateStore.SaveModelAvailability(store.ModelAvailability{ConnectionID: route.CredentialID, ModelRef: route.ExternalModel, Status: healthStatus(state), Generation: route.HealthGeneration.For(state.LastScope), BlockedUntil: formatTime(state.CooldownUntil), Reason: string(state.LastCause), ErrorCode: string(state.LastScope), LastError: state.LastError, ConsecutiveFailures: state.Failures})
	default:
		return fmt.Errorf("unsupported health scope %q", state.LastScope)
	}
}

func (d *Daemon) restoreHealthForRoutes(routes map[string]kernel.Route) error {
	operations, err := d.store.OperationRuntimes()
	if err != nil {
		return err
	}
	for _, item := range operations {
		for _, route := range routes {
			binding, exists := route.OperationBindings[normalize.Operation(item.OperationRef.ID)]
			if !exists || binding.ContractVersion != item.OperationRef.ContractVersion || route.CredentialID != item.ConnectionID || route.ExternalModel != item.ModelRef || item.Generation != route.HealthGeneration.For(kernel.ScopeOperation) {
				continue
			}
			route.SessionOperationRef = item.OperationRef
			d.policy.Health.Restore(route, runtimehealth.RouteHealth{OperationRef: item.OperationRef, Failures: item.Failures, Successes: item.Successes, Invalid: item.Status == "invalid", Generation: item.Generation, CooldownUntil: parseTime(item.BlockedUntil), LastError: item.LastError, LastCause: kernel.OutcomeCause(item.Cause), LastScope: kernel.ScopeOperation, LastEvidence: kernel.EvidenceSource(item.Evidence), Confidence: item.Confidence, LastAttempt: parseTime(item.LastAttempt), LastSuccess: parseTime(item.LastSuccess)})
		}
	}
	connections, err := d.store.ConnectionRuntimes()
	if err != nil {
		return err
	}
	for _, item := range connections {
		for _, route := range routes {
			if route.CredentialID != item.ConnectionID || item.Generation != route.HealthGeneration.For(kernel.ScopeConnection) {
				continue
			}
			d.policy.Health.Restore(route, runtimehealth.RouteHealth{Failures: item.ConsecutiveFailures, Invalid: item.Status == "invalid", Generation: item.Generation, CooldownUntil: parseTime(item.CooldownUntil), LastError: item.LastError, LastCause: kernel.OutcomeCause(item.ErrorCode), LastScope: kernel.ScopeConnection})
		}
	}
	providers, err := d.store.ProviderRuntimes()
	if err != nil {
		return err
	}
	for _, item := range providers {
		for _, route := range routes {
			if route.NodeID != item.ProviderNodeID || item.Generation != route.HealthGeneration.For(kernel.ScopeProvider) {
				continue
			}
			d.policy.Health.Restore(route, runtimehealth.RouteHealth{Failures: item.ConsecutiveFailures, Invalid: item.Status == "invalid", Generation: item.Generation, CooldownUntil: parseTime(item.CooldownUntil), LastError: item.LastError, LastCause: kernel.OutcomeCause(item.ErrorCode), LastScope: kernel.ScopeProvider})
		}
	}
	availability, err := d.store.ModelAvailabilities()
	if err != nil {
		return err
	}
	for _, item := range availability {
		blockedUntil := parseTime(item.BlockedUntil)
		invalid := item.Status == "invalid"
		if !invalid && (blockedUntil.IsZero() || !blockedUntil.After(time.Now())) {
			continue
		}
		scope := kernel.OutcomeScope(item.ErrorCode)
		if scope != kernel.ScopeRoute && scope != kernel.ScopeRouteConnection {
			continue
		}
		for _, route := range routes {
			if route.CredentialID != item.ConnectionID || route.ExternalModel != item.ModelRef || item.Generation != route.HealthGeneration.For(scope) {
				continue
			}
			d.policy.Health.Restore(route, runtimehealth.RouteHealth{Failures: item.ConsecutiveFailures, Invalid: invalid, Generation: item.Generation, CooldownUntil: blockedUntil, LastError: item.LastError, LastCause: kernel.OutcomeCause(item.Reason), LastScope: scope})
		}
	}
	return nil
}

func (d *Daemon) resetConnectionRoutingState(connectionID string) error {
	reset := func() error { return d.store.ResetConnectionRoutingState(connectionID) }
	var err error
	if d.quotaPersistence != nil {
		err = d.quotaPersistence.ResetConnection(connectionID, reset)
	} else {
		err = reset()
	}
	if err == nil && d.policy != nil {
		d.policy.ClearQuotasForConnection(connectionID)
	}
	return err
}

func (d *Daemon) resetProviderRoutingState(providerNodeID string) error {
	reset := func() error { return d.store.ResetProviderRoutingState(providerNodeID) }
	var err error
	if d.quotaPersistence != nil {
		err = d.quotaPersistence.ResetProvider(providerNodeID, reset)
	} else {
		err = reset()
	}
	if err == nil && d.policy != nil {
		d.policy.ClearQuotasForProvider(providerNodeID)
	}
	return err
}

func resetProviderHealth(gate *runtimehealth.HealthGate, routes map[string]kernel.Route, connections []store.ConnectionRecord, nodeID string) {
	var providerRoute kernel.Route
	seenConnections := make(map[string]struct{})
	for _, route := range routes {
		if route.NodeID != nodeID {
			continue
		}
		if providerRoute.ID == "" {
			providerRoute = route
		}
		gate.ResetScope(route, kernel.ScopeRoute)
		gate.ResetScope(route, kernel.ScopeRouteConnection)
		if _, seen := seenConnections[route.CredentialID]; !seen {
			gate.ResetScope(route, kernel.ScopeConnection)
			seenConnections[route.CredentialID] = struct{}{}
		}
	}
	for _, connection := range connections {
		if connection.ProviderNodeID != nodeID {
			continue
		}
		if _, seen := seenConnections[connection.ID]; seen {
			continue
		}
		gate.ResetScope(kernel.Route{NodeID: nodeID, CredentialID: connection.ID}, kernel.ScopeConnection)
	}
	if providerRoute.ID == "" {
		providerRoute.NodeID = nodeID
	}
	gate.ResetScope(providerRoute, kernel.ScopeProvider)
}

func resetConnectionHealth(gate *runtimehealth.HealthGate, routes map[string]kernel.Route, connectionID, nodeID string) {
	found := false
	connectionReset := false
	for _, route := range routes {
		if route.CredentialID != connectionID {
			continue
		}
		found = true
		if nodeID == "" {
			nodeID = route.NodeID
		}
		gate.ResetScope(route, kernel.ScopeRoute)
		gate.ResetScope(route, kernel.ScopeRouteConnection)
		if !connectionReset {
			gate.ResetScope(route, kernel.ScopeConnection)
			connectionReset = true
		}
	}
	if !found {
		gate.ResetScope(kernel.Route{NodeID: nodeID, CredentialID: connectionID}, kernel.ScopeConnection)
	}
}

func (d *Daemon) loadStoredProviderDefinitions(runtimeRegistry *provider.RuntimeRegistry) error {
	definitions, err := d.store.ProviderDefinitions()
	if err != nil {
		return fmt.Errorf("load stored provider definitions: %w", err)
	}
	for _, definition := range definitions {
		if err := runtimeRegistry.RegisterDefinitionIfAbsent(definition); err != nil {
			return fmt.Errorf("register stored provider definition %q: %w", definition.ID, err)
		}
	}
	return nil
}

type credentialRefreshLock struct {
	semaphore *semaphore.Weighted
	users     int
}

func (d *Daemon) lockCredentialRefresh(ctx context.Context, connectionID string) (func(), error) {
	d.credentialRefreshMu.Lock()
	if d.credentialRefreshes == nil {
		d.credentialRefreshes = make(map[string]*credentialRefreshLock)
	}
	lock := d.credentialRefreshes[connectionID]
	if lock == nil {
		lock = &credentialRefreshLock{semaphore: semaphore.NewWeighted(1)}
		d.credentialRefreshes[connectionID] = lock
	}
	lock.users++
	d.credentialRefreshMu.Unlock()
	drop := func() {
		d.credentialRefreshMu.Lock()
		lock.users--
		if lock.users == 0 {
			delete(d.credentialRefreshes, connectionID)
		}
		d.credentialRefreshMu.Unlock()
	}
	if err := lock.semaphore.Acquire(ctx, 1); err != nil {
		drop()
		return nil, err
	}
	return func() { lock.semaphore.Release(1); drop() }, nil
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

func (d *Daemon) Wait(ctx context.Context) error {
	d.mu.Lock()
	runCtx := d.runCtx
	d.mu.Unlock()
	if runCtx == nil {
		return fmt.Errorf("daemon has not started")
	}
	select {
	case <-ctx.Done():
	case <-runCtx.Done():
	}
	return d.Stop(context.Background())
}

func newDaemonHTTPServer(address string, handler http.Handler, base context.Context) *http.Server {
	return &http.Server{
		Addr:    address,
		Handler: handler,
		BaseContext: func(net.Listener) context.Context {
			return base
		},
	}
}

func (d *Daemon) serveHTTP(server *http.Server, listener net.Listener, plane string) {
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			d.appendLog("error", fmt.Sprintf("HTTP %s stopped: %v", plane, err))
		}
	}()
}

func (d *Daemon) Stop(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stop == nil {
		return d.stopErr
	}
	// Cancel active request contexts before Shutdown waits for in-flight
	// handlers. Otherwise an unbounded streaming request can prevent Stop (and
	// Wait, which uses a background shutdown context) from ever reaching cleanup.
	if d.runCancel != nil {
		d.runCancel()
	}
	if d.http != nil {
		if err := d.http.Shutdown(ctx); err != nil {
			_ = d.http.Close()
		}
	}
	if d.httpControl != nil {
		if err := d.httpControl.Shutdown(ctx); err != nil {
			_ = d.httpControl.Close()
		}
	}
	d.listeners.close()
	var ipcErr error
	if d.ipc != nil {
		ipcErr = d.ipc.Close()
	}
	d.stop()
	if d.ipcLease != nil {
		ipcErr = errors.Join(ipcErr, d.ipcLease.close())
	}
	d.stop = nil
	d.runCancel = nil
	d.http = nil
	d.httpControl = nil
	d.ipc = nil
	d.ipcLease = nil
	d.listeners = httpListeners{}
	d.addresses.Store(nil)
	d.oauthMu.Lock()
	d.oauth = nil
	d.oauthMu.Unlock()
	d.stopErr = ipcErr
	return ipcErr
}

func (d *Daemon) handleIPC(ctx context.Context, request IPCRequest) IPCResponse {
	switch request.Method {
	case "status":
		return IPCResponse{ID: request.ID, OK: true, Result: d.server.Status()}
	case "credential_activation.list":
		var input struct {
			Limit int `json:"limit"`
		}
		if err := decodeParams(request.Params, &input); err != nil {
			return fail(request, err.Error())
		}
		items, err := d.store.CredentialActivations(ctx, input.Limit)
		if err != nil {
			return fail(request, err.Error())
		}
		views := make([]CredentialActivationView, 0, len(items))
		for _, item := range items {
			views = append(views, d.currentActivationView(item))
		}
		return success(request, views)
	case "credential_activation.get", "credential_activation.retry":
		var input struct {
			ConnectionID string `json:"connectionID"`
		}
		if err := decodeParams(request.Params, &input); err != nil {
			return fail(request, err.Error())
		}
		if input.ConnectionID == "" {
			return fail(request, "connectionID is required")
		}
		if request.Method == "credential_activation.retry" {
			view, err := d.retryCredentialActivation(ctx, input.ConnectionID)
			if err != nil {
				return fail(request, err.Error())
			}
			return success(request, view)
		}
		a, ok, err := d.store.CredentialActivation(ctx, input.ConnectionID)
		if err != nil {
			return fail(request, err.Error())
		}
		if !ok {
			return fail(request, "credential activation not found")
		}
		return success(request, d.currentActivationView(a))
	case "compatibility.loss-ceiling.get":
		ceiling, configured, err := d.store.CompatibilityLossCeiling()
		if err != nil {
			return fail(request, err.Error())
		}
		return success(request, map[string]any{"configured": configured, "ceiling": ceiling})
	case "compatibility.loss-ceiling.set":
		var ceiling kernel.LossPolicyCeiling
		if err := decodeParams(request.Params, &ceiling); err != nil {
			return fail(request, err.Error())
		}
		if err := d.store.SetCompatibilityLossCeiling(ceiling); err != nil {
			return fail(request, err.Error())
		}
		if _, err := d.server.Reload(); err != nil {
			return fail(request, err.Error())
		}
		return success(request, d.server.Status())
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
		_, candidateStrategies, err := d.runtimeForBundle(bundle)
		if err != nil {
			return fail(request, err.Error())
		}
		if err := controlplane.ValidateBundleWithStrategies(bundle, candidateStrategies); err != nil {
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
		_, candidateStrategies, err := d.runtimeForBundle(desired)
		if err != nil {
			return fail(request, err.Error())
		}
		currentRuntime, _, err := d.runtimeWithProviderDefinitions(nil)
		if err != nil {
			return fail(request, err.Error())
		}
		currentCatalog, err := currentRuntime.FreezeCatalog()
		if err != nil {
			return fail(request, err.Error())
		}
		current, err := controlplane.ExportBundle(d.store, currentCatalog)
		if err != nil {
			return fail(request, err.Error())
		}
		diff, err := controlplane.DiffBundle(current, desired, candidateStrategies)
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
		candidateRegistry, candidateStrategies, err := d.runtimeForBundle(bundle)
		if err != nil {
			return fail(request, err.Error())
		}
		if d.kernel != nil {
			if err := d.kernel.ValidateTransformBindings(bundle.TransformBindings); err != nil {
				return fail(request, err.Error())
			}
		}
		if err := controlplane.ApplyBundle(d.store, bundle, candidateStrategies); err != nil {
			return fail(request, err.Error())
		}
		restartRequired := false
		for _, definition := range bundle.ProviderDefinitions {
			if !d.providerRegistry.HasDefinition(definition) && candidateRegistry.HasDefinition(definition) {
				restartRequired = true
				break
			}
		}
		if restartRequired {
			d.appendLog("warn", "portable provider definitions applied; daemon restart required to bind the new frozen catalog")
			return success(request, map[string]any{"status": "applied", "restartRequired": true, "message": "restart gobroomd to activate provider definitions"})
		}
		if _, err := d.server.Reload(); err != nil {
			return fail(request, err.Error())
		}
		d.appendLog("info", "typed config bundle applied")
		return success(request, d.server.Status())
	case "reload":
		if _, err := d.server.Reload(); err != nil {
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
	case "transform_bindings.list":
		items, err := d.store.TransformBindings()
		if err != nil {
			return fail(request, err.Error())
		}
		return success(request, items)
	case "transform_bindings.upsert":
		var binding kernel.TransformBinding
		if err := decodeParams(request.Params, &binding); err != nil {
			return fail(request, err.Error())
		}
		if err := d.upsertTransformBinding(binding); err != nil {
			return fail(request, err.Error())
		}
		return success(request, binding)
	case "transform_bindings.delete":
		id := stringParam(request.Params, "id")
		if id == "" {
			return fail(request, "id is required")
		}
		if err := d.deleteTransformBinding(id); err != nil {
			return fail(request, err.Error())
		}
		return success(request, map[string]string{"deleted": id})
	case "providers.refresh_models":
		var input struct {
			NodeID           string   `json:"nodeID"`
			ConnectionID     string   `json:"connectionID"`
			ModelIDs         []string `json:"modelIDs"`
			EntitlementsOnly bool     `json:"entitlementsOnly"`
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
			if input.ModelIDs != nil || input.EntitlementsOnly {
				return fail(request, "selective or entitlements-only model import requires a connection ID")
			}
			result, err = service.RefreshNode(ctx, input.NodeID)
		} else {
			result, err = service.ImportConnectionModels(ctx, input.NodeID, input.ConnectionID, discovery.ModelImportOptions{ModelIDs: input.ModelIDs, EntitlementsOnly: input.EntitlementsOnly})
		}
		if err != nil {
			return fail(request, err.Error())
		}
		if result.Evidence == provider.ModelCatalogConnectionObserved {
			availableModels := make(map[string]struct{}, len(result.ModelIDs))
			for _, modelID := range result.ModelIDs {
				availableModels[modelID] = struct{}{}
			}
			for _, route := range d.kernel.Snapshots.Load().Routes {
				if route.NodeID != result.NodeID || route.CredentialID != result.ConnectionID {
					continue
				}
				if _, found := availableModels[route.ExternalModel]; !found {
					continue
				}
				d.policy.Health.ClearInvalid(route, kernel.ScopeRoute)
				d.policy.Health.ClearInvalid(route, kernel.ScopeRouteConnection)
			}
		}
		if _, err := d.server.Reload(); err != nil {
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
	case "persistence.status":
		if d.persistenceSpool == nil {
			return success(request, runtimehealth.PersistenceSpoolStatus{})
		}
		return success(request, d.persistenceSpool.Status())
	case "persistence.retry":
		if d.persistenceSpool == nil {
			return fail(request, "durable runtime persistence spool is disabled")
		}
		requeued, err := d.persistenceSpool.RequeueDeadLetters()
		if err != nil {
			return fail(request, err.Error())
		}
		if requeued > 0 {
			d.appendLog("info", fmt.Sprintf("requeued %d runtime persistence dead letters", requeued))
		}
		return success(request, map[string]any{"requeued": requeued})
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
		explanations := d.policy.ExplainRoutes(resolved.Candidates, time.Now(), requestClass, sessionID)
		if rawRequest, hasRequest := request.Params["request"]; hasRequest {
			body, ok := rawRequest.(map[string]any)
			if !ok {
				return fail(request, "request must be a JSON object")
			}
			body["model"] = model
			requestPath := stringParam(request.Params, "requestPath")
			if requestPath == "" {
				requestPath = "/v1/chat/completions"
			}
			headers := make(http.Header)
			if sessionID != "" {
				headers.Set("x-session-id", sessionID)
			}
			normalized, normalizeErr := normalize.Map(requestPath, headers, body)
			if normalizeErr != nil {
				return fail(request, "normalize compatibility request: "+normalizeErr.Error())
			}
			var semanticSample struct {
				Operation                string                   `json:"operation"`
				OperationContractVersion uint64                   `json:"operationContractVersion"`
				OperationPayload         json.RawMessage          `json:"operationPayload"`
				Artifacts                []extensions.ArtifactRef `json:"artifacts"`
			}
			encodedSample, marshalErr := json.Marshal(body)
			if marshalErr != nil {
				return fail(request, "encode compatibility request sample: "+marshalErr.Error())
			}
			if decodeErr := json.Unmarshal(encodedSample, &semanticSample); decodeErr != nil {
				return fail(request, "decode compatibility operation sample: "+decodeErr.Error())
			}
			if semanticSample.Operation != "" {
				if semanticSample.OperationContractVersion == 0 {
					return fail(request, "operationContractVersion is required with a non-chat operation sample")
				}
				normalized.Request.Operation = normalize.Operation(semanticSample.Operation)
				normalized.Request.OperationContractVersion = semanticSample.OperationContractVersion
			} else if semanticSample.OperationContractVersion != 0 {
				return fail(request, "operation is required with operationContractVersion")
			}
			if len(semanticSample.OperationPayload) > 0 {
				normalized.Request.OperationPayload = append(json.RawMessage(nil), semanticSample.OperationPayload...)
			}
			if semanticSample.Artifacts != nil {
				normalized.Request.Artifacts = make([]extensions.ArtifactRef, len(semanticSample.Artifacts))
				for index, artifact := range semanticSample.Artifacts {
					normalized.Request.Artifacts[index] = artifact.Clone()
				}
			}
			compatibility, explainErr := d.kernel.ExplainCompatibility(ctx, model, normalized.Request)
			if explainErr != nil {
				return fail(request, "explain request compatibility: "+explainErr.Error())
			}
			for index := range resolved.Candidates {
				resolved.Candidates[index].SessionOperationRef = extensions.Ref{Kind: "operation", ID: string(normalized.Request.Operation), ContractVersion: normalized.Request.OperationContractVersion}
			}
			explanations = d.policy.ExplainRoutes(resolved.Candidates, time.Now(), requestClass, sessionID)
			byRoute := make(map[string][]kernel.RouteCompatibilityExplanation, len(compatibility))
			for _, item := range compatibility {
				byRoute[item.RouteID] = append(byRoute[item.RouteID], item)
			}
			for index := range explanations {
				explanations[index].Compatibility = byRoute[explanations[index].Route.ID]
			}
		}
		return success(request, explanations)
	case "quota.set":
		if d.policy == nil {
			return fail(request, "runtime policy unavailable")
		}
		var snapshot quota.Snapshot
		if err := decodeParams(request.Params, &snapshot); err != nil {
			return fail(request, err.Error())
		}
		if snapshot.Generation == "" && !d.policy.Health.AttachQuotaGeneration(&snapshot) {
			return fail(request, "quota snapshot does not match an active provider/connection/model")
		}
		if snapshot.Authority == quota.AuthorityUnknown {
			snapshot.Authority = quota.AuthorityExplicit
		}
		resolved, accepted := d.policy.UpsertQuota(snapshot)
		if !accepted {
			return fail(request, "quota snapshot belongs to an obsolete provider configuration")
		}
		if err := d.store.SaveQuotaSnapshot(resolved); err != nil {
			return fail(request, err.Error())
		}
		return success(request, resolved)
	case "providers.create":
		var input store.CreateProviderNodeInput
		if err := decodeParams(request.Params, &input); err != nil {
			return fail(request, err.Error())
		}
		if err := d.applyProviderDefaults(&input); err != nil {
			return fail(request, err.Error())
		}
		if err := d.validatePrefix(input.Prefix, input.DefinitionID, input.Protocol); err != nil {
			return fail(request, err.Error())
		}
		item, err := d.store.CreateProviderNode(input)
		if err != nil {
			return fail(request, err.Error())
		}
		if _, err := d.server.Reload(); err != nil {
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
		previousProvider, err := d.store.ProviderNode(input.ID)
		if err != nil {
			return fail(request, err.Error())
		}
		definitionID := input.DefinitionID
		if definitionID == "" {
			definitionID = previousProvider.DefinitionID
		}
		definitionVersion := input.DefinitionContractVersion
		if definitionVersion == 0 {
			definitionVersion = previousProvider.DefinitionContractVersion
			if input.DefinitionID != "" {
				definitionVersion = 1
			}
			if definitionVersion == 0 {
				definitionVersion = 1
			}
		}
		definitionRef := provider.ProviderDefinitionRef(definitionID, definitionVersion)
		if input.AuthMode == "" && (input.DefinitionID != "" || input.DefinitionContractVersion != 0) {
			input.AuthMode = d.providerAuthModes[definitionRef]
		}
		if input.DefinitionContractVersion != 0 || input.DefinitionID != "" {
			input.DefinitionContractVersion = definitionVersion
		}
		if input.AuthMode != "" && d.authFlowID(definitionRef, input.AuthMode) == "" {
			return fail(request, fmt.Sprintf("provider definition %q does not declare default credential type %q", definitionID, input.AuthMode))
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
		if item.BaseURL != previousProvider.BaseURL || item.Protocol != previousProvider.Protocol || item.DefinitionID != previousProvider.DefinitionID || item.DefinitionContractVersion != previousProvider.DefinitionContractVersion || item.ModelsPath != previousProvider.ModelsPath || item.AuthMode != previousProvider.AuthMode {
			if err := d.resetProviderRoutingState(item.ID); err != nil {
				return fail(request, fmt.Sprintf("provider updated but health reset failed: %v", err))
			}
			connections, loadErr := d.store.Connections("")
			if loadErr != nil {
				slog.Error("load connections while clearing provider health", "provider", item.ID, "error", loadErr)
			} else {
				resetProviderHealth(d.policy.Health, d.kernel.Snapshots.Load().Routes, connections, item.ID)
			}
		}
		if _, err := d.server.Reload(); err != nil {
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
		if _, err := d.server.Reload(); err != nil {
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
	case "auth.authorization.get":
		result, err := d.authorizationStatus(stringParam(request.Params, "sessionID"))
		if err != nil {
			return fail(request, err.Error())
		}
		return success(request, result)
	case "auth.authorization.complete":
		sessionID := stringParam(request.Params, "sessionID")
		state := stringParam(request.Params, "state")
		code := stringParam(request.Params, "code")
		callbackURL := stringParam(request.Params, "callbackURL")
		if callbackURL != "" {
			parsedCode, parsedState, err := authorizationCallbackCode(callbackURL)
			if err != nil {
				if errors.Is(err, errOAuthAuthorizationDenied) {
					if denyErr := d.denyAuthorization(sessionID, parsedState, callbackURL); denyErr != nil {
						return fail(request, denyErr.Error())
					}
					return fail(request, err.Error())
				}
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
	case "connections.test_inference":
		result, err := (discovery.Service{Store: d.store, Bindings: d.providerBindings}).TestInference(ctx, stringParam(request.Params, "connectionID"), stringParam(request.Params, "modelID"))
		if err != nil {
			return fail(request, err.Error())
		}
		return success(request, result)
	case "connections.test_operation":
		var input OperationProbeRequest
		if err := decodeParams(request.Params, &input); err != nil {
			return fail(request, err.Error())
		}
		for _, artifact := range input.Artifacts {
			if artifact.Body != nil {
				return fail(request, "operation-probe JSON cannot reference daemon body leases")
			}
		}
		if d.providerRegistry == nil {
			return fail(request, "provider runtime registry is unavailable")
		}
		operationSnapshot, err := d.providerRegistry.OperationSnapshot()
		if err != nil {
			return fail(request, err.Error())
		}
		result, err := (discovery.Service{Store: d.store, Bindings: d.providerBindings, Operations: operationSnapshot, Artifacts: d.artifactStore}).TestOperation(ctx, input)
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
		if _, err := d.server.Reload(); err != nil {
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
		credentialTypeChanged := input.CredentialType != "" && input.CredentialType != existing.CredentialType
		resetHealth := input.Secret != "" || credentialTypeChanged || (input.Enabled != nil && *input.Enabled != existing.Enabled)
		if input.CredentialType == "" {
			input.CredentialType = existing.CredentialType
		}
		if input.Secret != "" || credentialTypeChanged {
			if err := d.validateConnectionSecret(ctx, existing.ProviderNodeID, input.CredentialType, input.Secret); err != nil {
				return fail(request, err.Error())
			}
		}
		item, err := d.store.UpdateConnection(input)
		if err != nil {
			return fail(request, err.Error())
		}
		if resetHealth {
			if err := d.resetConnectionRoutingState(item.ID); err != nil {
				return fail(request, fmt.Sprintf("connection updated but health reset failed: %v", err))
			}
			resetConnectionHealth(d.policy.Health, d.kernel.Snapshots.Load().Routes, item.ID, existing.ProviderNodeID)
		}
		if _, err := d.server.Reload(); err != nil {
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
		if _, err := d.server.Reload(); err != nil {
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
		if _, err := d.server.Reload(); err != nil {
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
		if _, err := d.server.Reload(); err != nil {
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
		if _, err := d.server.Reload(); err != nil {
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
		if _, err := d.server.Reload(); err != nil {
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
		if _, err := d.server.Reload(); err != nil {
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
		if _, err := d.server.Reload(); err != nil {
			return fail(request, err.Error())
		}
		return success(request, map[string]string{"deleted": id})
	case "shutdown":
		return IPCResponse{ID: request.ID, OK: true, Result: map[string]string{"status": "stopping"}}
	default:
		return IPCResponse{ID: request.ID, OK: false, Error: "unknown method: " + request.Method}
	}
}

func (d *Daemon) upsertTransformBinding(binding kernel.TransformBinding) error {
	if err := kernel.ValidateTransformBinding(binding); err != nil {
		return err
	}
	if d.kernel == nil {
		return fmt.Errorf("kernel is unavailable for transform binding validation")
	}
	d.transformBindingsMu.Lock()
	defer d.transformBindingsMu.Unlock()
	bindings, err := d.store.TransformBindings()
	if err != nil {
		return err
	}
	replaced := false
	for index := range bindings {
		if bindings[index].ID == binding.ID {
			bindings[index] = binding
			replaced = true
			break
		}
	}
	if !replaced {
		bindings = append(bindings, binding)
	}
	if err := d.kernel.ValidateTransformBindings(bindings); err != nil {
		return err
	}
	if err := d.store.UpsertTransformBinding(binding); err != nil {
		return err
	}
	if d.server != nil {
		_, reloadErr := d.server.Reload()
		return reloadErr
	}
	return nil
}

func (d *Daemon) deleteTransformBinding(id string) error {
	if d.kernel == nil {
		return fmt.Errorf("kernel is unavailable for transform binding validation")
	}
	d.transformBindingsMu.Lock()
	defer d.transformBindingsMu.Unlock()
	bindings, err := d.store.TransformBindings()
	if err != nil {
		return err
	}
	remaining := make([]kernel.TransformBinding, 0, len(bindings))
	found := false
	for _, binding := range bindings {
		if binding.ID == id {
			found = true
			continue
		}
		remaining = append(remaining, binding)
	}
	if !found {
		return fmt.Errorf("transform binding %q not found", id)
	}
	if err := d.kernel.ValidateTransformBindings(remaining); err != nil {
		return err
	}
	if err := d.store.DeleteTransformBinding(id); err != nil {
		return err
	}
	if d.server != nil {
		_, reloadErr := d.server.Reload()
		return reloadErr
	}
	return nil
}

func (d *Daemon) runtimeForBundle(bundle controlplane.ConfigBundle) (*provider.RuntimeRegistry, *kernel.StrategyCatalog, error) {
	runtimeRegistry, strategies, err := d.runtimeWithProviderDefinitions(bundle.ProviderDefinitions)
	if err != nil {
		return nil, nil, err
	}
	if err := controlplane.ValidateBundleWithStrategies(bundle, strategies); err != nil {
		return nil, nil, err
	}
	return runtimeRegistry, strategies, nil
}

func (d *Daemon) runtimeWithProviderDefinitions(extra []provider.ProviderDefinition) (*provider.RuntimeRegistry, *kernel.StrategyCatalog, error) {
	runtimeRegistry, err := provider.NewRuntimeRegistry()
	if err != nil {
		return nil, nil, err
	}
	if d.sessionCache != nil {
		if err := runtimeRegistry.ReplaceSessionStore(d.sessionCache); err != nil {
			return nil, nil, err
		}
	}
	if d.config.ProviderManifestDir != "" {
		if err := runtimeRegistry.LoadDefinitionDir(d.config.ProviderManifestDir); err != nil {
			return nil, nil, fmt.Errorf("load provider manifests for config validation: %w", err)
		}
	}
	stored, err := d.store.ProviderDefinitions()
	if err != nil {
		return nil, nil, err
	}
	definitions := append(stored, extra...)
	for _, definition := range definitions {
		if err := runtimeRegistry.RegisterDefinitionIfAbsent(definition); err != nil {
			return nil, nil, fmt.Errorf("provider definition %q: %w", definition.ID, err)
		}
	}
	if _, err := runtimeRegistry.BuildBindings(); err != nil {
		return nil, nil, err
	}
	extensionSnapshot, err := runtimeRegistry.FreezeCatalog()
	if err != nil {
		return nil, nil, err
	}
	strategies, err := kernel.NewStrategyCatalog(extensionSnapshot)
	if err != nil {
		return nil, nil, err
	}
	return runtimeRegistry, strategies, nil
}

func (d *Daemon) applyProviderDefaults(input *store.CreateProviderNodeInput) error {
	if input.DefinitionID == "" {
		input.DefinitionID = store.DefaultDefinitionForProtocol(input.Protocol)
	}
	if input.DefinitionContractVersion == 0 {
		input.DefinitionContractVersion = 1
	}
	definitionRef := provider.ProviderDefinitionRef(input.DefinitionID, input.DefinitionContractVersion)
	if input.AuthMode == "" {
		input.AuthMode = d.providerAuthModes[definitionRef]
	}
	if input.AuthMode == "" || d.authFlowID(definitionRef, input.AuthMode) == "" {
		return fmt.Errorf("provider definition %q does not declare default credential type %q", input.DefinitionID, input.AuthMode)
	}
	return nil
}

func (d *Daemon) authFlowID(definitionRef extensions.Ref, credentialType string) string {
	return d.providerAuthFlowIDs[definitionRef][credentialType]
}

func (d *Daemon) validateConnectionSecret(ctx context.Context, providerNodeID, credentialType, secret string) error {
	node, err := d.store.ProviderNode(providerNodeID)
	if err != nil {
		return fmt.Errorf("provider node: %w", err)
	}
	if node.ID == "" {
		return fmt.Errorf("provider node %q not found", providerNodeID)
	}
	flowID := d.authFlowID(node.DefinitionRef(), credentialType)
	flow, ok := d.authFlows[flowID]
	if flowID == "" || !ok {
		return fmt.Errorf("provider definition %q does not declare credential type %q", node.DefinitionID, credentialType)
	}
	if credentialType == "none" && secret != "" {
		return fmt.Errorf("credential type none does not accept credential secret")
	}
	if strings.TrimSpace(secret) == "" {
		authorizationCode, deviceAuthorization := provider.InteractiveAuthModes(flow)
		if authorizationCode || deviceAuthorization {
			return nil
		}
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
	return Config{DBPath: filepath.Join(configDir, "gobroom", "gobroom.db"), RuntimeSpoolPath: filepath.Join(configDir, "gobroom", "runtime-spool.json"), IPCPath: filepath.Join(runtimeDir, "gobroom.sock"), HTTPEnabled: true, HTTPAddr: defaultHTTPAddr, HTTPControl: false, HTTPControlAddr: defaultHTTPControlAddr, ProviderManifestDir: filepath.Join(configDir, "gobroom", "providers"), ArtifactStoreDir: filepath.Join(cacheDir, "gobroom", "artifacts")}, nil
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
	if state.Invalid {
		return "invalid"
	}
	if !state.CooldownUntil.IsZero() && state.CooldownUntil.After(time.Now()) {
		return "cooldown"
	}
	return "available"
}
