package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

const maxSetupInputBytes = 64 * 1024

type setupStartParams struct {
	ConnectionID string `json:"connectionID"`
}

type setupSessionParams struct {
	SessionID string `json:"sessionID"`
}

type setupAdvanceParams struct {
	SessionID string         `json:"sessionID"`
	ActionID  string         `json:"actionID"`
	Revision  uint64         `json:"revision"`
	Value     map[string]any `json:"value"`
}

func setupCommand() *cobra.Command {
	command := &cobra.Command{Use: "setup", Short: "run interactive connection setup"}
	var connectionID, sessionID string

	start := &cobra.Command{Use: "start", Short: "start connection setup", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		params := setupStartParams{ConnectionID: connectionID}
		return invokeSetup("auth.setup.start", params)
	}}
	start.Flags().StringVar(&connectionID, "connection-id", "", "connection ID")
	_ = start.MarkFlagRequired("connection-id")
	command.AddCommand(start)

	get := &cobra.Command{Use: "get", Short: "show connection setup status", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		params := setupSessionParams{SessionID: sessionID}
		return invokeSetup("auth.setup.get", params)
	}}
	get.Flags().StringVar(&sessionID, "session-id", "", "setup session ID")
	_ = get.MarkFlagRequired("session-id")
	command.AddCommand(get)

	cancel := &cobra.Command{Use: "cancel", Short: "cancel connection setup", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		params := setupSessionParams{SessionID: sessionID}
		return invokeSetup("auth.setup.cancel", params)
	}}
	cancel.Flags().StringVar(&sessionID, "session-id", "", "setup session ID")
	_ = cancel.MarkFlagRequired("session-id")
	command.AddCommand(cancel)

	var actionID, inputFile string
	var revision uint64
	submit := &cobra.Command{Use: "submit", Short: "submit an action response as a JSON object", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		value, err := readSetupValue(cmd.InOrStdin(), inputFile)
		if err != nil {
			return err
		}
		params := setupAdvanceParams{SessionID: sessionID, ActionID: actionID, Revision: revision, Value: value}
		return invokeSetup("auth.setup.advance", params)
	}}
	submit.Flags().StringVar(&sessionID, "session-id", "", "setup session ID")
	submit.Flags().StringVar(&actionID, "action-id", "", "setup action ID")
	submit.Flags().Uint64Var(&revision, "revision", 0, "setup revision returned by get/start")
	submit.Flags().StringVar(&inputFile, "input-file", "", "JSON object file or - for stdin")
	_ = submit.MarkFlagRequired("session-id")
	_ = submit.MarkFlagRequired("action-id")
	_ = submit.MarkFlagRequired("revision")
	_ = submit.MarkFlagRequired("input-file")
	command.AddCommand(submit)
	return command
}

func invokeSetup(method string, params any) error {
	encoded, err := json.Marshal(params)
	if err != nil {
		return errors.New("cannot encode setup request")
	}
	var request map[string]any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&request); err != nil {
		return errors.New("cannot encode setup request")
	}
	return invoke(method, request)
}

func readSetupValue(stdin io.Reader, path string) (map[string]any, error) {
	reader := stdin
	var file *os.File
	if path != "-" {
		var err error
		file, err = os.Open(path)
		if err != nil {
			return nil, errors.New("cannot read setup input file")
		}
		defer file.Close()
		reader = file
	}

	data, err := io.ReadAll(io.LimitReader(reader, maxSetupInputBytes+1))
	if err != nil {
		return nil, errors.New("cannot read setup input")
	}
	if len(data) > maxSetupInputBytes {
		return nil, fmt.Errorf("setup input exceeds %d KiB", maxSetupInputBytes/1024)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil || value == nil {
		return nil, errors.New("setup input must be a JSON object")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("setup input must contain exactly one JSON object")
	}
	return value, nil
}
