package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/fm39hz/gobroom/internal/daemon"
	"github.com/fm39hz/gobroom/internal/tui"
	"github.com/spf13/cobra"
)

var version = "dev"

var (
	ipcPath    string
	outputJSON bool
)

func main() {
	defaults, err := daemon.DefaultConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	root := newRootCommand(defaults.IPCPath, tui.Run)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type tuiRunner func(ipcPath, appVersion string) error

func newRootCommand(defaultIPC string, runTUI tuiRunner) *cobra.Command {
	ipcPath = defaultIPC
	root := &cobra.Command{
		Use:     "gobroom",
		Short:   "GoBroom control client",
		Version: version,
		Args:    cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return runTUI(ipcPath, version)
		},
	}
	root.PersistentFlags().StringVar(&ipcPath, "ipc", ipcPath, "daemon IPC socket")
	root.PersistentFlags().BoolVar(&outputJSON, "json", true, "print JSON output")

	root.AddCommand(simpleCommand("status", "show daemon status", "status", nil))
	root.AddCommand(simpleCommand("reload", "reload daemon snapshot", "reload", nil))
	var resolveModel string
	resolve := &cobra.Command{Use: "resolve", Short: "resolve a published model", RunE: func(*cobra.Command, []string) error {
		return invoke("resolve", map[string]any{"model": resolveModel})
	}}
	resolve.Flags().StringVar(&resolveModel, "model", "", "published model name")
	root.AddCommand(resolve)
	root.AddCommand(resourceCommands()...)
	root.AddCommand(extensionCommands())
	root.AddCommand(healthCommand(), quotaCommand(), usageCommand(), usageSummaryCommand(), usagePruneCommand(), routeExplainCommand(), configExportCommand(), configValidateCommand(), configDiffCommand(), configApplyCommand())
	return root
}

func simpleCommand(use, short, method string, flags map[string]*string) *cobra.Command {
	cmd := &cobra.Command{Use: use, Short: short, RunE: func(cmd *cobra.Command, _ []string) error {
		params := map[string]any{}
		if flags != nil {
			for name, ptr := range flags {
				if ptr != nil && *ptr != "" {
					params[name] = *ptr
				}
			}
		}
		return invoke(method, params)
	}}
	for name, ptr := range flags {
		cmd.Flags().StringVar(ptr, name, "", name)
	}
	return cmd
}

func resourceCommands() []*cobra.Command {
	providers := &cobra.Command{Use: "providers", Short: "manage provider nodes"}
	providers.AddCommand(listCommand("list", "providers.list", nil))
	providers.AddCommand(listCommand("catalog", "providers.catalog", nil))
	var name, prefix, baseURL, protocol, definitionID, modelsPath, authMode, providerID string
	create := &cobra.Command{Use: "create", Short: "create provider node", RunE: func(*cobra.Command, []string) error {
		return invoke("providers.create", map[string]any{"name": name, "prefix": prefix, "baseUrl": baseURL, "protocol": protocol, "definitionID": definitionID, "modelsPath": modelsPath, "authMode": authMode})
	}}
	create.Flags().StringVar(&name, "name", "", "display name")
	create.Flags().StringVar(&prefix, "prefix", "", "wire prefix")
	create.Flags().StringVar(&baseURL, "base-url", "", "provider base URL")
	create.Flags().StringVar(&protocol, "protocol", "openai_chat", "protocol")
	create.Flags().StringVar(&definitionID, "definition-id", "", "provider definition ID")
	create.Flags().StringVar(&modelsPath, "models-path", "/models", "models endpoint path")
	create.Flags().StringVar(&authMode, "auth-mode", "", "credential type (defaults from provider definition)")
	providers.AddCommand(create)
	update := &cobra.Command{Use: "update", Short: "update provider node", RunE: func(*cobra.Command, []string) error {
		return invoke("providers.update", map[string]any{"id": providerID, "name": name, "prefix": prefix, "baseUrl": baseURL, "protocol": protocol, "definitionID": definitionID, "modelsPath": modelsPath, "authMode": authMode})
	}}
	bindProviderFlags(update, &providerID, &name, &prefix, &baseURL, &protocol, &definitionID, &modelsPath, &authMode)
	providers.AddCommand(update)
	providers.AddCommand(idCommand("delete", "delete provider node", "providers.delete", "id", &providerID))
	refresh := idCommand("refresh-models", "refresh provider model catalog", "providers.refresh_models", "nodeID", &providerID)
	var refreshConnectionID string
	refresh.Flags().StringVar(&refreshConnectionID, "connection-id", "", "enabled connection to use (defaults to highest priority)")
	refresh.RunE = func(*cobra.Command, []string) error {
		return invoke("providers.refresh_models", map[string]any{"nodeID": providerID, "connectionID": refreshConnectionID})
	}
	providers.AddCommand(refresh)

	connections := &cobra.Command{Use: "connections", Short: "manage provider connections"}
	var nodeID, connID, connName, credentialType, secret string
	var priority int
	connections.AddCommand(listCommand("list", "connections.list", map[string]*string{"nodeID": &nodeID}))
	createConn := &cobra.Command{Use: "create", Short: "create connection", RunE: func(*cobra.Command, []string) error {
		return invoke("connections.create", map[string]any{"providerNodeID": nodeID, "name": connName, "credentialType": credentialType, "secret": secret, "priority": priority})
	}}
	createConn.Flags().StringVar(&nodeID, "node-id", "", "provider node ID")
	createConn.Flags().StringVar(&connName, "name", "", "connection name")
	createConn.Flags().StringVar(&credentialType, "credential-type", "", "credential type (defaults from provider definition)")
	createConn.Flags().StringVar(&secret, "secret", "", "credential secret")
	createConn.Flags().IntVar(&priority, "priority", 100, "selection priority")
	connections.AddCommand(createConn)
	updateConn := &cobra.Command{Use: "update", Short: "update connection", RunE: func(*cobra.Command, []string) error {
		return invoke("connections.update", map[string]any{"id": connID, "name": connName, "credentialType": credentialType, "secret": secret, "priority": priority})
	}}
	updateConn.Flags().StringVar(&connID, "id", "", "connection ID")
	updateConn.Flags().StringVar(&connName, "name", "", "connection name")
	updateConn.Flags().StringVar(&credentialType, "credential-type", "", "credential type")
	updateConn.Flags().StringVar(&secret, "secret", "", "new secret")
	updateConn.Flags().IntVar(&priority, "priority", 0, "selection priority")
	connections.AddCommand(updateConn)
	connections.AddCommand(idCommand("delete", "delete connection", "connections.delete", "id", &connID))
	testConn := &cobra.Command{Use: "test", Short: "test one connection's model-list endpoint without importing", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		return invoke("connections.test", map[string]any{"connectionID": connID})
	}}
	testConn.Flags().StringVar(&connID, "connection-id", "", "enabled connection ID")
	connections.AddCommand(testConn)
	previewConnModels := &cobra.Command{Use: "preview-models", Short: "review one connection's full model catalog without importing", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		return invoke("connections.preview_models", map[string]any{"connectionID": connID})
	}}
	previewConnModels.Flags().StringVar(&connID, "connection-id", "", "enabled connection ID")
	connections.AddCommand(previewConnModels)
	var selectedModelIDs []string
	var entitlementsOnly bool
	refreshConnModels := &cobra.Command{Use: "refresh-models", Short: "import all or selected models using one connection", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		params := map[string]any{"nodeID": nodeID, "connectionID": connID}
		if entitlementsOnly && len(selectedModelIDs) > 0 {
			return fmt.Errorf("--entitlements-only cannot be combined with --model-id")
		}
		if entitlementsOnly {
			params["modelIDs"] = []string{}
		} else if selectedModelIDs != nil {
			params["modelIDs"] = selectedModelIDs
		}
		return invoke("providers.refresh_models", params)
	}}
	refreshConnModels.Flags().StringVar(&nodeID, "node-id", "", "provider node ID")
	refreshConnModels.Flags().StringVar(&connID, "connection-id", "", "enabled connection ID")
	refreshConnModels.Flags().StringArrayVar(&selectedModelIDs, "model-id", nil, "exact upstream model ID to import (repeat to select a subset; omit for all)")
	refreshConnModels.Flags().BoolVar(&entitlementsOnly, "entitlements-only", false, "record the connection's catalog evidence without importing route IDs")
	connections.AddCommand(refreshConnModels)

	models := &cobra.Command{Use: "models", Short: "manage model catalog"}
	models.AddCommand(listCommand("list", "models.list", nil))
	var modelID, modelNodeID, modelKind, externalID, displayName string
	custom := &cobra.Command{Use: "upsert", Short: "upsert custom model", RunE: func(*cobra.Command, []string) error {
		return invoke("custom_models.upsert", map[string]any{"id": modelID, "providerNodeID": modelNodeID, "kind": modelKind, "externalID": externalID, "displayName": displayName})
	}}
	custom.Flags().StringVar(&modelID, "id", "", "catalog ID")
	custom.Flags().StringVar(&modelNodeID, "node-id", "", "provider node ID")
	custom.Flags().StringVar(&modelKind, "kind", "custom", "model kind")
	custom.Flags().StringVar(&externalID, "external-id", "", "upstream model ID")
	custom.Flags().StringVar(&displayName, "display-name", "", "display name")
	models.AddCommand(custom, idCommand("delete", "delete catalog model", "custom_models.delete", "id", &modelID))

	physical := &cobra.Command{Use: "physical-models", Short: "manage physical model identities"}
	var physicalName, physicalPolicy, physicalPolicyOptions string
	var physicalSources []string
	var physicalDiscoverable bool
	var allowCompatibleSources, allowDynamicSources bool
	physical.AddCommand(listCommand("list", "physical_models.list", nil))
	physicalUpsert := &cobra.Command{Use: "upsert", Short: "create or update a physical model", RunE: func(*cobra.Command, []string) error {
		sources := make([]map[string]any, 0, len(physicalSources))
		for _, source := range physicalSources {
			sources = append(sources, map[string]any{"routeId": source, "fidelity": "alias", "evidence": []map[string]any{{"source": "user_assertion", "confidence": 0.5, "note": "manually grouped by config author"}}})
		}
		policyOptions := map[string]any{}
		if strings.TrimSpace(physicalPolicyOptions) != "" {
			if err := json.Unmarshal([]byte(physicalPolicyOptions), &policyOptions); err != nil || policyOptions == nil {
				if err == nil {
					err = fmt.Errorf("must be a JSON object")
				}
				return fmt.Errorf("policy options: %w", err)
			}
		}
		return invoke("physical_models.upsert", map[string]any{"name": physicalName, "sources": sources, "policy": map[string]any{"id": physicalPolicy, "config": policyOptions}, "allowCompatibleSources": allowCompatibleSources, "allowDynamicSources": allowDynamicSources, "discoverable": physicalDiscoverable, "enabled": true})
	}}
	physicalUpsert.Flags().StringVar(&physicalName, "name", "", "physical model name")
	physicalUpsert.Flags().StringArrayVar(&physicalSources, "source", nil, "discovered route ID (repeat for multiple sources)")
	physicalUpsert.Flags().StringVar(&physicalPolicy, "policy", "ordered-fallback", "source policy primitive ID")
	physicalUpsert.Flags().StringVar(&physicalPolicyOptions, "policy-options", "{}", "JSON object validated against the selected strategy schema")
	physicalUpsert.Flags().BoolVar(&physicalDiscoverable, "discoverable", false, "include this model in /v1/models")
	physicalUpsert.Flags().BoolVar(&allowCompatibleSources, "allow-compatible-sources", false, "allow compatible revisions as Physical candidates")
	physicalUpsert.Flags().BoolVar(&allowDynamicSources, "allow-dynamic-sources", false, "allow dynamic upstream routing sources")
	physical.AddCommand(physicalUpsert, idCommand("delete", "delete physical model", "physical_models.delete", "name", &physicalName))

	typedCombos := &cobra.Command{Use: "combo-models", Short: "manage typed combo models"}
	var typedComboName, typedComboStrategy string
	var typedComboOptions string
	var typedMembers []string
	var comboDiscoverable bool
	typedCombos.AddCommand(listCommand("list", "combo_models.list", nil))
	typedCombos.AddCommand(listCommand("strategies", "strategies.list", nil))
	typedComboUpsert := &cobra.Command{Use: "upsert", Short: "create or update a combo model", RunE: func(*cobra.Command, []string) error {
		members := make([]map[string]any, 0, len(typedMembers))
		for index, raw := range typedMembers {
			var member struct {
				Kind   string `json:"kind"`
				ID     string `json:"id"`
				Weight int    `json:"weight,omitempty"`
			}
			if err := json.Unmarshal([]byte(raw), &member); err != nil {
				return fmt.Errorf("member %d: invalid JSON: %w", index+1, err)
			}
			if (member.Kind != "physical" && member.Kind != "combo") || member.ID == "" {
				return fmt.Errorf("member %d: kind must be physical or combo and id is required", index+1)
			}
			item := map[string]any{"kind": member.Kind, "id": member.ID}
			if member.Weight > 0 {
				item["weight"] = member.Weight
			}
			members = append(members, item)
		}
		options := map[string]any{}
		if strings.TrimSpace(typedComboOptions) != "" {
			if err := json.Unmarshal([]byte(typedComboOptions), &options); err != nil || options == nil {
				if err == nil {
					err = fmt.Errorf("must be a JSON object")
				}
				return fmt.Errorf("strategy options: %w", err)
			}
		}
		return invoke("combo_models.upsert", map[string]any{"name": typedComboName, "members": members, "strategy": map[string]any{"id": typedComboStrategy, "config": options}, "discoverable": comboDiscoverable, "enabled": true})
	}}
	typedComboUpsert.Flags().StringVar(&typedComboName, "name", "", "combo model name")
	typedComboUpsert.Flags().StringArrayVar(&typedMembers, "member", nil, `ordered typed member JSON, e.g. {"kind":"physical","id":"qwen-3.7-max","weight":3}`)
	typedComboUpsert.Flags().StringVar(&typedComboStrategy, "strategy", "ordered-fallback", "execution strategy primitive ID")
	typedComboUpsert.Flags().StringVar(&typedComboOptions, "strategy-options", "{}", "JSON object validated against the selected strategy schema")
	typedComboUpsert.Flags().BoolVar(&comboDiscoverable, "discoverable", false, "include this combo in /v1/models")
	typedCombos.AddCommand(typedComboUpsert, idCommand("delete", "delete combo model", "combo_models.delete", "name", &typedComboName))

	return []*cobra.Command{providers, connections, models, physical, typedCombos}
}

func extensionCommands() *cobra.Command {
	command := &cobra.Command{Use: "extensions", Short: "inspect versioned extension contracts"}
	command.AddCommand(listCommand("catalog", "extensions.catalog", nil))
	return command
}

func healthCommand() *cobra.Command {
	return listCommand("health", "health.list", nil)
}
func quotaCommand() *cobra.Command        { return listCommand("quota", "quota.list", nil) }
func usageCommand() *cobra.Command        { return listCommand("usage", "usage.list", nil) }
func usageSummaryCommand() *cobra.Command { return listCommand("usage-summary", "usage.summary", nil) }
func usagePruneCommand() *cobra.Command {
	var before string
	cmd := &cobra.Command{Use: "usage-prune", Short: "prune usage before a cutoff", RunE: func(*cobra.Command, []string) error { return invoke("usage.prune", map[string]any{"before": before}) }}
	cmd.Flags().StringVar(&before, "before", "", "RFC3339 cutoff")
	return cmd
}

func configExportCommand() *cobra.Command {
	return listCommand("config-export", "config.export", nil)
}
func configValidateCommand() *cobra.Command {
	return &cobra.Command{Use: "config-validate", Short: "validate config bundle JSON", RunE: func(cmd *cobra.Command, _ []string) error {
		var raw map[string]any
		if err := json.NewDecoder(cmd.InOrStdin()).Decode(&raw); err != nil {
			return err
		}
		return invoke("config.validate", raw)
	}}
}
func configDiffCommand() *cobra.Command {
	return &cobra.Command{Use: "config-diff", Short: "show config bundle dry-run diff", RunE: func(cmd *cobra.Command, _ []string) error {
		var raw map[string]any
		if err := json.NewDecoder(cmd.InOrStdin()).Decode(&raw); err != nil {
			return err
		}
		return invoke("config.diff", raw)
	}}
}
func configApplyCommand() *cobra.Command {
	return &cobra.Command{Use: "config-apply", Short: "atomically apply a config bundle JSON", RunE: func(cmd *cobra.Command, _ []string) error {
		var raw map[string]any
		if err := json.NewDecoder(cmd.InOrStdin()).Decode(&raw); err != nil {
			return err
		}
		return invoke("config.apply", raw)
	}}
}
func routeExplainCommand() *cobra.Command {
	var model, requestClass, sessionID string
	cmd := &cobra.Command{Use: "route-explain", Short: "explain effective route order", RunE: func(*cobra.Command, []string) error {
		return invoke("routes.explain", map[string]any{"model": model, "requestClass": requestClass, "sessionID": sessionID})
	}}
	cmd.Flags().StringVar(&model, "model", "", "exposed model")
	cmd.Flags().StringVar(&requestClass, "request-class", "", "request class bucket")
	cmd.Flags().StringVar(&sessionID, "session", "", "session affinity ID")
	return cmd
}

func listCommand(use, method string, flags map[string]*string) *cobra.Command {
	return simpleCommand(use, method, method, flags)
}

func idCommand(use, short, method, flag string, value *string) *cobra.Command {
	cmd := &cobra.Command{Use: use, Short: short, RunE: func(*cobra.Command, []string) error {
		return invoke(method, map[string]any{flag: *value})
	}}
	cmd.Flags().StringVar(value, flag, "", flag)
	return cmd
}

func bindProviderFlags(cmd *cobra.Command, id, name, prefix, baseURL, protocol, definitionID, modelsPath, authMode *string) {
	cmd.Flags().StringVar(id, "id", "", "provider node ID")
	cmd.Flags().StringVar(name, "name", "", "display name")
	cmd.Flags().StringVar(prefix, "prefix", "", "wire prefix")
	cmd.Flags().StringVar(baseURL, "base-url", "", "provider base URL")
	cmd.Flags().StringVar(protocol, "protocol", "", "protocol")
	cmd.Flags().StringVar(definitionID, "definition-id", "", "provider definition ID")
	cmd.Flags().StringVar(modelsPath, "models-path", "", "models endpoint path")
	cmd.Flags().StringVar(authMode, "auth-mode", "", "authentication mode")
}

func invoke(method string, params map[string]any) error {
	response, err := daemon.CallIPC(context.Background(), ipcPath, daemon.IPCRequest{ID: "cli", Method: method, Params: params})
	if err != nil {
		return err
	}
	if outputJSON {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(response.Result)
	}
	_, err = fmt.Fprintln(os.Stdout, response.Result)
	return err
}
