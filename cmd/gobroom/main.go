package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/fm39hz/gobroom/internal/daemon"
	"github.com/spf13/cobra"
)

var version = "dev"

func main() {
	defaults, err := daemon.DefaultConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	root := &cobra.Command{Use: "gobroom", Short: "GoBroom control client"}
	root.Version = version
	status := &cobra.Command{Use: "status", Short: "show daemon status", RunE: func(cmd *cobra.Command, _ []string) error {
		response, err := daemon.CallIPC(context.Background(), defaults.IPCPath, daemon.IPCRequest{ID: "status", Method: "status"})
		if err != nil {
			return fmt.Errorf("gobroomd is unreachable via IPC: %w", err)
		}
		value := response.Result
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(value)
	}}
	root.AddCommand(status)
	reload := &cobra.Command{Use: "reload", Short: "reload daemon snapshot", RunE: func(cmd *cobra.Command, _ []string) error {
		response, err := daemon.CallIPC(context.Background(), defaults.IPCPath, daemon.IPCRequest{ID: "reload", Method: "reload"})
		if err != nil { return err }
		return json.NewEncoder(os.Stdout).Encode(response.Result)
	}}
	root.AddCommand(reload)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
