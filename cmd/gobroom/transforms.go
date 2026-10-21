package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/fm39hz/gobroom/pkg/provider"
	"github.com/spf13/cobra"
)

const maxTransformInputBytes = 1024 * 1024

func transformCommands() *cobra.Command {
	command := &cobra.Command{Use: "transforms", Short: "manage transform bindings"}
	command.AddCommand(listCommand("list", "transform_bindings.list", nil))

	var inputFile string
	upsert := &cobra.Command{
		Use:   "upsert",
		Short: "create or update a transform binding from typed JSON",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			binding, err := readTransformBinding(cmd.InOrStdin(), inputFile)
			if err != nil {
				return err
			}
			return invokeTransformBinding("transform_bindings.upsert", binding)
		},
	}
	upsert.Flags().StringVarP(&inputFile, "file", "f", "", "transform binding JSON file or - for stdin")
	_ = upsert.MarkFlagRequired("file")
	command.AddCommand(upsert)

	var id string
	remove := &cobra.Command{Use: "delete", Short: "delete a transform binding", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		if strings.TrimSpace(id) == "" {
			return errors.New("transform binding ID is required")
		}
		return invoke("transform_bindings.delete", map[string]any{"id": id})
	}}
	remove.Flags().StringVar(&id, "id", "", "transform binding ID")
	_ = remove.MarkFlagRequired("id")
	command.AddCommand(remove)
	return command
}

func readTransformBinding(stdin io.Reader, path string) (provider.TransformBinding, error) {
	var binding provider.TransformBinding
	reader := stdin
	var file *os.File
	if path != "-" {
		var err error
		file, err = os.Open(path)
		if err != nil {
			return binding, errors.New("cannot read transform binding file")
		}
		defer file.Close()
		reader = file
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxTransformInputBytes+1))
	if err != nil {
		return binding, errors.New("cannot read transform binding input")
	}
	if len(data) > maxTransformInputBytes {
		return binding, fmt.Errorf("transform binding input exceeds %d MiB", maxTransformInputBytes/(1024*1024))
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&binding); err != nil {
		return binding, errors.New("transform binding must be a valid typed JSON object")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return binding, errors.New("transform binding input must contain exactly one JSON value")
	}
	if strings.TrimSpace(binding.ID) == "" {
		return binding, errors.New("transform binding ID is required")
	}
	if err := binding.TransformRef.Validate(); err != nil {
		return binding, errors.New("transform binding requires a valid exact transform reference")
	}
	if binding.TransformRef.Kind != provider.RequestTransformKind && binding.TransformRef.Kind != provider.ResponseTransformKind {
		return binding, errors.New("transform binding reference kind must be request_transform or response_transform")
	}
	return binding, nil
}

func invokeTransformBinding(method string, binding provider.TransformBinding) error {
	encoded, err := json.Marshal(binding)
	if err != nil {
		return errors.New("cannot encode transform binding request")
	}
	var params map[string]any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&params); err != nil {
		return errors.New("cannot encode transform binding request")
	}
	return invoke(method, params)
}
