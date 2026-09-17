package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/gorouter/gorouter/internal/daemon"
	"github.com/spf13/cobra"
)

func main() {
	defaults, err := daemon.DefaultConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	root := &cobra.Command{Use: "gorouter", Short: "GoRouter control client"}
	status := &cobra.Command{Use: "status", Short: "show daemon status", RunE: func(cmd *cobra.Command, _ []string) error {
		response, err := daemon.CallIPC(context.Background(), defaults.IPCPath, daemon.IPCRequest{ID: "status", Method: "status"})
		if err != nil {
			return fmt.Errorf("gorouterd is unreachable via IPC: %w", err)
		}
		value := response.Result
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(value)
	}}
	root.AddCommand(status)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
