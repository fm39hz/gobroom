package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/fm39hz/gobroom/internal/daemon"
	"github.com/fm39hz/gobroom/internal/termtext"
	"github.com/spf13/cobra"
)

func usageDetailCommand() *cobra.Command {
	var includePrivate, includeRaw bool
	cmd := &cobra.Command{
		Use:   "detail <id>",
		Short: "inspect one usage event with explicitly selected detail classes",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil || id <= 0 {
				return fmt.Errorf("usage detail ID must be a positive 64-bit integer")
			}
			response, err := daemon.CallIPC(cmd.Context(), ipcPath, daemon.IPCRequest{ID: "cli", Method: "usage.detail", Params: map[string]any{
				"id":                id,
				"includeExtensions": true,
				"includePrivate":    includePrivate,
				"includeRaw":        includeRaw,
			}})
			if err != nil {
				return err
			}
			raw, err := json.Marshal(response.Result)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(os.Stdout, termtext.JSON(raw))
			return err
		},
	}
	cmd.Flags().BoolVar(&includePrivate, "include-private", false, "request private extension details (requires host authorization)")
	cmd.Flags().BoolVar(&includeRaw, "include-raw", false, "request raw usage data (requires host authorization)")
	return cmd
}
