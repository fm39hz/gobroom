package main

import (
	"fmt"
	"io"

	"github.com/fm39hz/gobroom/internal/jsonvalue"
)

// Bound raw input at the IPC ceiling. CallIPC separately bounds the complete
// encoded envelope, including method/ID framing and JSON re-encoding overhead.
const maxJSONInputBytes = 32 << 20

func decodeJSONInput(input io.Reader) (map[string]any, error) {
	data, err := io.ReadAll(io.LimitReader(input, maxJSONInputBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read JSON input: %w", err)
	}
	if len(data) > maxJSONInputBytes {
		return nil, fmt.Errorf("JSON input exceeds %d byte limit", maxJSONInputBytes)
	}
	return jsonvalue.Object(data)
}
