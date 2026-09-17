package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fm39hz/gobroom/internal/adapter/anthropic"
	openai "github.com/fm39hz/gobroom/internal/adapter/openai"
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
	DBPath      string
	IPCPath     string
	HTTPEnabled bool
	HTTPAddr    string
	HTTPControl bool
}

type Daemon struct {
	config      Config
	store       *store.Store
	server      *api.Server
	ipc         *IPCServer
	http        *http.Server
	kernel      *kernel.Kernel
	policy      *runtimehealth.PolicyGate
	usageCancel context.CancelFunc
	mu          sync.Mutex
	stop        func()
}

func New(config Config) *Daemon { return &Daemon{config: config} }

func (d *Daemon) Start(ctx context.Context) error {
	s, err := store.Open(d.config.DBPath)
	if err != nil {
		return err
	}
	d.store = s
	d.server = api.NewServer(s)
	ctx, cancel := context.WithCancel(ctx)
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
	d.kernel.Adapters[kernel.ProtocolOpenAIChat] = openai.Chat{}
	d.kernel.Adapters[kernel.ProtocolOpenAIResponses] = openai.Responses{}
	d.kernel.Adapters[kernel.ProtocolAnthropic] = anthropic.Messages{}
	d.kernel.ResolveCredential = func(_ context.Context, route kernel.Route) (kernel.Credential, error) {
		credential, ok := d.store.ConnectionCredentialByID(route.CredentialID)
		if !ok {
			return kernel.Credential{}, fmt.Errorf("connection %q is unavailable", route.CredentialID)
		}
		return kernel.Credential{ConnectionID: route.CredentialID, Type: credential.Type, Secret: credential.Secret}, nil
	}
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
		d.http = &http.Server{Addr: d.config.HTTPAddr, Handler: d.server.HandlerWithOptions(api.HandlerOptions{ControlPlane: d.config.HTTPControl, DataPlane: true})}
		go func() {
			if err := d.http.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				fmt.Fprintf(os.Stderr, "[gobroomd] HTTP: %v\n", err)
			}
		}()
	}
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
		if err := d.validatePrefix(input.Prefix); err != nil {
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
			if err := d.validatePrefixUpdate(input.ID, input.Prefix); err != nil {
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
	case "combos.list":
		items, err := d.store.ComboDetails()
		if err != nil {
			return fail(request, err.Error())
		}
		return success(request, items)
	case "combos.upsert":
		var item store.ComboRecord
		if err := decodeParams(request.Params, &item); err != nil {
			return fail(request, err.Error())
		}
		if err := d.server.Control().ValidateCombo(item); err != nil {
			return fail(request, err.Error())
		}
		if err := d.store.UpsertCombo(item); err != nil {
			return fail(request, err.Error())
		}
		if err := d.server.Reload(); err != nil {
			return fail(request, err.Error())
		}
		return success(request, item)
	case "combos.delete":
		name := stringParam(request.Params, "name")
		if name == "" {
			return fail(request, "name is required")
		}
		if err := d.server.Control().ValidateComboDelete(name); err != nil {
			return fail(request, err.Error())
		}
		if err := d.store.DeleteCombo(name); err != nil {
			return fail(request, err.Error())
		}
		if err := d.server.Reload(); err != nil {
			return fail(request, err.Error())
		}
		return success(request, map[string]string{"deleted": name})
	case "logical_models.list":
		items, err := d.store.LogicalModels()
		if err != nil {
			return fail(request, err.Error())
		}
		return success(request, items)
	case "logical_models.upsert":
		var item store.LogicalModelRecord
		if err := decodeParams(request.Params, &item); err != nil {
			return fail(request, err.Error())
		}
		if err := d.server.Control().ValidateLogicalModel(item); err != nil {
			return fail(request, err.Error())
		}
		if err := d.store.UpsertLogicalModel(item.Name, item.TargetRef); err != nil {
			return fail(request, err.Error())
		}
		if err := d.server.Reload(); err != nil {
			return fail(request, err.Error())
		}
		return success(request, item)
	case "logical_models.delete":
		name := stringParam(request.Params, "name")
		if name == "" {
			return fail(request, "name is required")
		}
		if err := d.server.Control().ValidateLogicalModelDelete(name); err != nil {
			return fail(request, err.Error())
		}
		if err := d.store.DeleteLogicalModel(name); err != nil {
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
	case "public_models.list":
		items, err := d.store.PublicModels()
		if err != nil {
			return fail(request, err.Error())
		}
		return success(request, items)
	case "public_models.upsert":
		var item store.PublicModel
		if err := decodeParams(request.Params, &item); err != nil {
			return fail(request, err.Error())
		}
		if err := d.server.Control().ValidatePublicModel(item); err != nil {
			return fail(request, err.Error())
		}
		if err := d.store.UpsertPublicModel(item); err != nil {
			return fail(request, err.Error())
		}
		if err := d.server.Reload(); err != nil {
			return fail(request, err.Error())
		}
		return success(request, item)
	case "public_models.delete":
		name := stringParam(request.Params, "name")
		if name == "" {
			return fail(request, "name is required")
		}
		if err := d.store.DeletePublicModel(name); err != nil {
			return fail(request, err.Error())
		}
		if err := d.server.Reload(); err != nil {
			return fail(request, err.Error())
		}
		return success(request, map[string]string{"deleted": name})
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

func (d *Daemon) validatePrefix(prefix string) error {
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
	return registry.AddCustom(prefix, "pending")
}

func (d *Daemon) validatePrefixUpdate(id, prefix string) error {
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
	return registry.AddCustom(prefix, id)
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
	return Config{DBPath: filepath.Join(configDir, "gobroom", "gobroom.db"), IPCPath: filepath.Join(runtimeDir, "gobroom.sock"), HTTPEnabled: true, HTTPAddr: "127.0.0.1:20127", HTTPControl: false}, nil
}

func (d *Daemon) UptimeHint() time.Duration { return 0 }
