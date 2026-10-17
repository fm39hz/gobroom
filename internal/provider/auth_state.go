package provider

import "fmt"

// MaxCredentialStateBytes bounds provider-owned durable state. This is not a
// wire credential: only its owning auth primitive interprets the encoded value.
const MaxCredentialStateBytes = 64 << 10

type CredentialState struct {
	encoded string
}

func NewCredentialState(encoded string) (CredentialState, error) {
	state := CredentialState{encoded: encoded}
	return state, state.Validate()
}

func (s CredentialState) Validate() error {
	if len(s.encoded) == 0 || len(s.encoded) > MaxCredentialStateBytes {
		return fmt.Errorf("credential state must contain 1..%d bytes", MaxCredentialStateBytes)
	}
	return nil
}

// Encoded explicitly crosses the secret-storage boundary. Generic formatting
// and JSON must never accidentally expose provider state in logs or IPC.
func (s CredentialState) Encoded() string            { return s.encoded }
func (CredentialState) String() string               { return "[redacted]" }
func (CredentialState) GoString() string             { return "provider.CredentialState{[redacted]}" }
func (CredentialState) MarshalJSON() ([]byte, error) { return []byte(`"[redacted]"`), nil }
