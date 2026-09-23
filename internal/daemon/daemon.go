package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fm39hz/gobroom/internal/api"
	"github.com/fm39hz/gobroom/internal/discovery"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/quota"
	runtimehealth "github.com/fm39hz/gobroom/internal/runtime"
	"github.com/fm39hz/gobroom/internal/store"
	usageworker "github.com/fm39hz/gobroom/internal/usage"
)

type Config struct {
	DBPath              string
	IPCPath             string
	HTTPEnabled         bool
	HTTPAddr            string
	HTTPControl         bool
	HTTPControlAddr     string
	ProviderManifestDir string
	QuotaPolling        bool
}

type Daemon struct {
	config              Config
	store               *store.Store
	server              *api.Server
	ipc                 *IPCServer
	http                *http.Server
	httpControl         *http.Server
	kernel              *kernel.Kernel
	policy              *runtimehealth.PolicyGate
	usageCancel         context.CancelFunc
	mu                  sync.Mutex
	credentialRefreshMu sync.Mutex
	logMu               sync.RWMutex
	logs                []LogRecord
	stop                func()
}

type LogRecord struct {
	At      time.Time `json:"at"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

func (d *Daemon) appendLog(level, message string) {
	d.logMu.Lock()
	defer d.logMu.Unlock()
	d.logs = append(d.logs, LogRecord{At: time.Now(), Level: level, Message: message})
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
	d.server = api.NewServer(s)
	ctx, cancel := context.WithCancel(ctx)
	started := false
	defer func() {
		if !started {
			cancel()
		}
	}()
	if d.server.Control() == nil {
		_ = s.Close()
		return fmt.Errorf("cannot initialize control plane")
	}
	d.policy = runtimehealth.NewPolicyGate()
	if snapshots, loadErr := d.store.QuotaSnapshots(); loadErr == nil {
		for _, snapshot := range snapshots {
			d.policy.SetQuota(snapshot)
		}
	}
	d.kernel, err = kernel.New(d.server.Control().Snapshot(), d.policy, 256)
	if err != nil {
		_ = s.Close()
		return err
	}
	runtimeRegistry, err := provider.NewRuntimeRegistry()
	if err != nil {
		_ = s.Close()
		return err
	}
	if d.config.ProviderManifestDir != "" {
		if err := runtimeRegistry.LoadDefinitionDir(d.config.ProviderManifestDir); err != nil {
			_ = s.Close()
			return fmt.Errorf("load provider manifests: %w", err)
		}
	}
	if _, err := runtimeRegistry.BuildBindings(); err != nil {
		_ = s.Close()
		return err
	}
	d.kernel.Adapters = runtimeRegistry.Adapters()
	d.kernel.ErrorClassifiers = runtimeRegistry.ErrorClassifiers()
	opportunisticQuota := &runtimehealth.OpportunisticQuota{
		Sources: runtimeRegistry.QuotaSources(),
		Credential: func(ctx context.Context, route kernel.Route) (kernel.Credential, error) {
			credential, ok := d.store.ConnectionCredentialByID(route.CredentialID)
			if !ok {
				return kernel.Credential{}, fmt.Errorf("connection %q is unavailable", route.CredentialID)
			}
			flow, ok := runtimeRegistry.Auth.Resolve(authFlowID(credential.Type))
			if !ok {
				return kernel.Credential{}, fmt.Errorf("auth flow %q is unavailable", credential.Type)
			}
			return flow.Resolve(ctx, provider.AuthInput{ConnectionID: route.CredentialID, Type: credential.Type, Secret: credential.Secret})
		},
		Record: func(snapshot quota.Snapshot) {
			d.policy.SetQuota(snapshot)
			_ = d.store.SaveQuotaSnapshot(snapshot)
		},
	}
	d.policy.SetQuotaEnricher(opportunisticQuota.Trigger)
	go (runtimehealth.QuotaPoller{
		Sources:  runtimeRegistry.QuotaSources(),
		Snapshot: func() kernel.Snapshot { return d.kernel.Snapshots.Load() },
		Credential: func(ctx context.Context, route kernel.Route) (kernel.Credential, error) {
			credential, ok := d.store.ConnectionCredentialByID(route.CredentialID)
			if !ok {
				return kernel.Credential{}, fmt.Errorf("connection %q is unavailable", route.CredentialID)
			}
			flow, ok := runtimeRegistry.Auth.Resolve(authFlowID(credential.Type))
			if !ok {
				return kernel.Credential{}, fmt.Errorf("auth flow %q is unavailable", credential.Type)
			}
			return flow.Resolve(ctx, provider.AuthInput{ConnectionID: route.CredentialID, Type: credential.Type, Secret: credential.Secret})
		},
		Record: func(snapshot quota.Snapshot) {
			d.policy.SetQuota(snapshot)
			_ = d.store.SaveQuotaSnapshot(snapshot)
		},
		Enabled: d.config.QuotaPolling,
	}).Run(ctx)
	d.kernel.ResolveCredential = func(_ context.Context, route kernel.Route) (kernel.Credential, error) {
		credential, ok := d.store.ConnectionCredentialByID(route.CredentialID)
		if !ok {
			return kernel.Credential{}, fmt.Errorf("connection %q is unavailable", route.CredentialID)
		}
		flow, ok := runtimeRegistry.Auth.Resolve(authFlowID(credential.Type))
		if !ok {
			return kernel.Credential{}, fmt.Errorf("auth flow %q is unavailable", credential.Type)
		}
		return flow.Resolve(context.Background(), provider.AuthInput{ConnectionID: route.CredentialID, Type: credential.Type, Secret: credential.Secret})
	}
	d.kernel.RefreshCredential = func(ctx context.Context, route kernel.Route, current kernel.Credential) (kernel.Credential, error) {
		d.credentialRefreshMu.Lock()
		defer d.credentialRefreshMu.Unlock()
		flow, ok := runtimeRegistry.Auth.Resolve(authFlowID(current.Type))
		if !ok {
			return kernel.Credential{}, fmt.Errorf("auth flow %q is unavailable", current.Type)
		}
		refreshed, err := flow.Refresh(ctx, current)
		if err != nil {
			return kernel.Credential{}, err
		}
		secret := refreshed.Secret
		if refreshed.RefreshToken != "" {
			encoded, encodeErr := json.Marshal(map[string]any{"access_token": refreshed.Secret, "refresh_token": refreshed.RefreshToken, "expires_at": refreshed.ExpiresAt})
			if encodeErr != nil {
				return kernel.Credential{}, encodeErr
			}
			secret = string(encoded)
		}
		if err := d.store.UpdateConnectionSecret(route.CredentialID, secret); err != nil {
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
			d.policy.Health.Restore(route, runtimehealth.RouteHealth{Failures: item.ConsecutiveFailures, CooldownUntil: parseTime(item.CooldownUntil), LastError: item.LastError})
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
			state := runtimehealth.RouteHealth{Failures: item.ConsecutiveFailures, CooldownUntil: blockedUntil, LastError: item.LastError}
			d.policy.Health.Restore(route, state)
		}
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case event := <-healthEvents:
				_ = d.store.SaveConnectionRuntime(store.ConnectionRuntime{ConnectionID: event.route.CredentialID, Status: healthStatus(event.state), ConsecutiveFailures: event.state.Failures, CooldownUntil: formatTime(event.state.CooldownUntil), LastError: event.state.LastError, ErrorCode: string(event.state.LastCause)})
				_ = d.store.SaveModelAvailability(store.ModelAvailability{ConnectionID: event.route.CredentialID, ModelRef: event.route.ExternalModel, Status: healthStatus(event.state), BlockedUntil: formatTime(event.state.CooldownUntil), Reason: string(event.state.LastCause), ErrorCode: string(event.state.LastScope), LastError: event.state.LastError, ConsecutiveFailures: event.state.Failures})
			}
		}
	}()
	d.server.SetExecutor(func(ctx context.Context, request normalize.Request, writer http.ResponseWriter) error {
		return d.kernel.Execute(ctx, request, kernel.Credential{}, writer)
	})
	d.server.SetReloadHook(func() error { return d.kernel.PublishSnapshot(d.server.Control().Snapshot()) })
	usageCtx, usageCancel := context.WithCancel(ctx)
	d.usageCancel = usageCancel
	go (usageworker.Worker{Store: s, Events: d.kernel.Events}).Run(usageCtx)

	d.stop = func() {
		cancel()
		if d.usageCancel != nil {
			d.usageCancel()
		}
		if d.kernel != nil {
			d.kernel.Close()
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
	return nil
}

func (d *Daemon) handleIPC(ctx context.Context, request IPCRequest) IPCResponse {
	switch request.Method {
	case "status":
		return IPCResponse{ID: request.ID, OK: true, Result: d.server.Status()}
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
	case "providers.refresh_models":
		nodeID := stringParam(request.Params, "nodeID")
		if nodeID == "" {
			return fail(request, "nodeID is required")
		}
		result, err := (discovery.Service{Store: d.store}).RefreshNode(ctx, nodeID)
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
		d.logMu.RLock()
		logs := append([]LogRecord(nil), d.logs...)
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
		if err := d.validatePrefix(input.Prefix, input.DefinitionID); err != nil {
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
	case "connections.create":
		var input store.CreateConnectionInput
		if err := decodeParams(request.Params, &input); err != nil {
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
			for _, source := range items[index].Sources {
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
			items[index].Projection = kernel.ProjectProfiles(routes)
		}
		return success(request, items)
	case "physical_models.upsert":
		var item store.PhysicalModel
		if err := decodeParams(request.Params, &item); err != nil {
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
	case "combo_models.upsert":
		var item store.ComboModel
		if err := decodeParams(request.Params, &item); err != nil {
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

func (d *Daemon) validatePrefix(prefix, definitionID string) error {
	registry := provider.NewPrefixRegistry()
	items, err := d.store.ProviderNodes()
	if err != nil {
		return err
	}
	for _, item := range items {
		if err := registry.AddCustom(item.Prefix, item.ID); err != nil {
			return err
		}
	}
	if definitionID == "" {
		definitionID = "openai-compatible-chat"
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
			continue
		}
		if err := registry.AddCustom(item.Prefix, item.ID); err != nil {
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
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = filepath.Join(os.TempDir(), "gobroom")
	}
	return Config{DBPath: filepath.Join(configDir, "gobroom", "gobroom.db"), IPCPath: filepath.Join(runtimeDir, "gobroom.sock"), HTTPEnabled: true, HTTPAddr: "127.0.0.1:2712", HTTPControl: false, HTTPControlAddr: "127.0.0.1:2713"}, nil
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

func authFlowID(credentialType string) string {
	switch credentialType {
	case "api_key", "apikey", "bearer", "access_token", "static-secret":
		return "static-secret"
	default:
		return credentialType
	}
}
