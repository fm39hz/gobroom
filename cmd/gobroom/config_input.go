package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// Bound raw input at the IPC ceiling. CallIPC separately bounds the complete
// encoded envelope, including method/ID framing and JSON re-encoding overhead.
const maxConfigInputBytes = 32 << 20

func decodeConfigInput(input io.Reader) (map[string]any, error) {
	data, err := io.ReadAll(io.LimitReader(input, maxConfigInputBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read config JSON: %w", err)
	}
	if len(data) > maxConfigInputBytes {
		return nil, fmt.Errorf("config JSON exceeds %d byte limit", maxConfigInputBytes)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("config JSON must contain exactly one value")
		}
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok || object == nil {
		return nil, fmt.Errorf("config JSON must be a nonnull object")
	}
	return object, nil
}
