package kernel

import (
	"encoding/json"
	"fmt"
)

const MaxProviderResponseErrorBytes = 64 << 10

// ProviderResponseError carries bounded, private canonical {error:{...}}
// evidence. Its Error string is deliberately independent of provider payloads.
// Only a decoder-origin failure may reach the host's normalization callback.
type ProviderResponseError struct{ data json.RawMessage }

func NewProviderResponseError(data []byte) (*ProviderResponseError, error) {
	if !validProviderResponseError(data) {
		return nil, fmt.Errorf("invalid provider response error evidence")
	}
	return &ProviderResponseError{data: append(json.RawMessage(nil), data...)}, nil
}

func (*ProviderResponseError) Error() string { return "provider reported a terminal response error" }

func (e *ProviderResponseError) Data() json.RawMessage {
	if e == nil {
		return nil
	}
	return append(json.RawMessage(nil), e.data...)
}

func validProviderResponseError(data []byte) bool {
	if len(data) == 0 || len(data) > MaxProviderResponseErrorBytes {
		return false
	}
	var envelope struct {
		Error map[string]json.RawMessage `json:"error"`
	}
	return json.Unmarshal(data, &envelope) == nil && envelope.Error != nil
}
