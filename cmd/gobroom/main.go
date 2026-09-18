package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/fm39hz/gobroom/internal/daemon"
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
	ipcPath = defaults.IPCPath

	root := &cobra.Command{Use: "gobroom", Short: "GoBroom control client", Version: version}
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
	root.AddCommand(healthCommand(), quotaCommand())
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
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
	var name, prefix, baseURL, protocol, modelsPath, authMode, providerID string
	create := &cobra.Command{Use: "create", Short: "create provider node", RunE: func(*cobra.Command, []string) error {
		return invoke("providers.create", map[string]any{"name": name, "prefix": prefix, "baseUrl": baseURL, "protocol": protocol, "modelsPath": modelsPath, "authMode": authMode})
	}}
	create.Flags().StringVar(&name, "name", "", "display name")
	create.Flags().StringVar(&prefix, "prefix", "", "wire prefix")
	create.Flags().StringVar(&baseURL, "base-url", "", "provider base URL")
	create.Flags().StringVar(&protocol, "protocol", "openai_chat", "protocol")
	create.Flags().StringVar(&modelsPath, "models-path", "/models", "models endpoint path")
	create.Flags().StringVar(&authMode, "auth-mode", "api_key", "authentication mode")
	providers.AddCommand(create)
	update := &cobra.Command{Use: "update", Short: "update provider node", RunE: func(*cobra.Command, []string) error {
		return invoke("providers.update", map[string]any{"id": providerID, "name": name, "prefix": prefix, "baseUrl": baseURL, "protocol": protocol, "modelsPath": modelsPath, "authMode": authMode})
	}}
	bindProviderFlags(update, &providerID, &name, &prefix, &baseURL, &protocol, &modelsPath, &authMode)
	providers.AddCommand(update)
	providers.AddCommand(idCommand("delete", "delete provider node", "providers.delete", "id", &providerID))
	refresh := idCommand("refresh-models", "refresh provider model catalog", "providers.refresh_models", "nodeID", &providerID)
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
	createConn.Flags().StringVar(&credentialType, "credential-type", "api_key", "credential type")
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

	logical := &cobra.Command{Use: "logical-models", Short: "manage logical model aliases"}
	var logicalName, targetRef string
	logical.AddCommand(listCommand("list", "logical_models.list", nil))
	logicalUpsert := &cobra.Command{Use: "upsert", Short: "upsert logical model", RunE: func(*cobra.Command, []string) error {
		return invoke("logical_models.upsert", map[string]any{"name": logicalName, "targetRef": targetRef})
	}}
	logicalUpsert.Flags().StringVar(&logicalName, "name", "", "logical model name")
	logicalUpsert.Flags().StringVar(&targetRef, "target", "", "target reference")
	logical.AddCommand(logicalUpsert, idCommand("delete", "delete logical model", "logical_models.delete", "name", &logicalName))

	combos := &cobra.Command{Use: "combos", Short: "manage fallback combos"}
	var comboName, comboStrategy, comboMembers string
	combos.AddCommand(listCommand("list", "combos.list", nil))
	comboUpsert := &cobra.Command{Use: "upsert", Short: "upsert combo", RunE: func(*cobra.Command, []string) error {
		return invoke("combos.upsert", map[string]any{"name": comboName, "strategy": comboStrategy, "members": splitCSV(comboMembers)})
	}}
	comboUpsert.Flags().StringVar(&comboName, "name", "", "combo name")
	comboUpsert.Flags().StringVar(&comboStrategy, "strategy", "fallback", "fallback, round_robin, round_robin_fallback or weighted")
	comboUpsert.Flags().StringVar(&comboMembers, "members", "", "comma-separated members")
	combos.AddCommand(comboUpsert, idCommand("delete", "delete combo", "combos.delete", "name", &comboName))

	public := &cobra.Command{Use: "public-models", Short: "manage published models"}
	var publicName, ownedBy string
	public.AddCommand(listCommand("list", "public_models.list", nil))
	publicUpsert := &cobra.Command{Use: "upsert", Short: "publish model", RunE: func(*cobra.Command, []string) error {
		return invoke("public_models.upsert", map[string]any{"name": publicName, "targetRef": targetRef, "ownedBy": ownedBy})
	}}
	publicUpsert.Flags().StringVar(&publicName, "name", "", "public model name")
	publicUpsert.Flags().StringVar(&targetRef, "target", "", "target reference")
	publicUpsert.Flags().StringVar(&ownedBy, "owned-by", "gobroom", "owner label")
	public.AddCommand(publicUpsert, idCommand("delete", "unpublish model", "public_models.delete", "name", &publicName))

	return []*cobra.Command{providers, connections, models, logical, combos, public}
}

func healthCommand() *cobra.Command {
	return listCommand("health", "health.list", nil)
}
func quotaCommand() *cobra.Command { return listCommand("quota", "quota.list", nil) }

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

func bindProviderFlags(cmd *cobra.Command, id, name, prefix, baseURL, protocol, modelsPath, authMode *string) {
	cmd.Flags().StringVar(id, "id", "", "provider node ID")
	cmd.Flags().StringVar(name, "name", "", "display name")
	cmd.Flags().StringVar(prefix, "prefix", "", "wire prefix")
	cmd.Flags().StringVar(baseURL, "base-url", "", "provider base URL")
	cmd.Flags().StringVar(protocol, "protocol", "", "protocol")
	cmd.Flags().StringVar(modelsPath, "models-path", "", "models endpoint path")
	cmd.Flags().StringVar(authMode, "auth-mode", "", "authentication mode")
}

func splitCSV(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
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
