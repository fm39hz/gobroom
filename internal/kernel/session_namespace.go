package kernel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/fm39hz/gobroom/internal/extensions"
)

const SessionStoreNamespaceVersion uint64 = 1

// SessionStoreNamespace is provider continuity's durable authority boundary.
// Token revisions are intentionally absent: renewal preserves the principal,
// whereas account or endpoint replacement cannot inherit its response IDs.
type SessionStoreNamespace struct {
	Version             uint64         `json:"version"`
	DefinitionRef       extensions.Ref `json:"definitionRef"`
	OperationRef        extensions.Ref `json:"operationRef"`
	ModelID             string         `json:"modelId"`
	Endpoint            string         `json:"endpoint"`
	PrincipalGeneration string         `json:"principalGeneration"`
}

func (n SessionStoreNamespace) Validate() error {
	if n.Version != SessionStoreNamespaceVersion || n.DefinitionRef.Kind != "provider-definition" || n.DefinitionRef.Validate() != nil || n.OperationRef.Kind != "operation" || n.OperationRef.Validate() != nil || n.ModelID == "" || n.PrincipalGeneration == "" {
		return fmt.Errorf("provider session requires the current exact authority namespace")
	}
	if len(n.ModelID) > 4096 || len(n.Endpoint) > 8192 || len(n.PrincipalGeneration) > 4096 {
		return fmt.Errorf("provider session namespace exceeds its bounds")
	}
	return nil
}

func SessionStoreNamespaceFor(route Route) (SessionStoreNamespace, error) {
	n := SessionStoreNamespace{Version: SessionStoreNamespaceVersion, DefinitionRef: route.DefinitionRef, OperationRef: route.SessionOperationRef, ModelID: route.ExternalModel, Endpoint: route.BaseURL, PrincipalGeneration: route.HealthGeneration.Connection}
	if err := n.Validate(); err != nil {
		return SessionStoreNamespace{}, err
	}
	return n, nil
}

func EncodeSessionStoreNamespace(route Route) (string, error) {
	n, err := SessionStoreNamespaceFor(route)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(n)
	if err != nil {
		return "", fmt.Errorf("encode provider session namespace")
	}
	return string(encoded), nil
}

func DecodeSessionStoreNamespace(raw string) (SessionStoreNamespace, error) {
	if len(raw) == 0 || len(raw) > 32<<10 {
		return SessionStoreNamespace{}, fmt.Errorf("invalid provider session namespace size")
	}
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	var n SessionStoreNamespace
	if decoder.Decode(&n) != nil || decoder.Decode(new(any)) != io.EOF {
		return SessionStoreNamespace{}, fmt.Errorf("invalid provider session namespace document")
	}
	if err := n.Validate(); err != nil {
		return SessionStoreNamespace{}, err
	}
	return n, nil
}
