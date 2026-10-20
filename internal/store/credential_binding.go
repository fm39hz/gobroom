package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// CredentialBinding contains the live metadata that identifies a connection's
// credential binding. It deliberately excludes secret material.
type CredentialBinding struct {
	ID, Type, Revision, PrincipalGeneration string
	Owner                                   CredentialOwner
}

func (b CredentialBinding) OperationalGeneration() string {
	return configGeneration(b.ID, b.Type, b.PrincipalGeneration)
}

// ConnectionCredentialBinding reads the current enabled binding for a
// connection without selecting its secret.
func (s *Store) ConnectionCredentialBinding(ctx context.Context, id string) (CredentialBinding, error) {
	return readCredentialBinding(ctx, s.DB, id)
}

type credentialBindingReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readCredentialBinding(ctx context.Context, reader credentialBindingReader, id string) (CredentialBinding, error) {
	var binding CredentialBinding
	err := reader.QueryRowContext(ctx, `SELECT c.id,c.credential_type,r.revision,cp.revision,p.id,p.definition_id,p.definition_contract_version
FROM connections c
JOIN connection_credential_revisions r ON r.connection_id=c.id
JOIN connection_principals cp ON cp.connection_id=c.id
JOIN provider_nodes p ON p.id=c.provider_node_id
WHERE c.id=? AND c.enabled=1`, id).Scan(
		&binding.ID, &binding.Type, &binding.Revision, &binding.PrincipalGeneration,
		&binding.Owner.ProviderNodeID, &binding.Owner.Definition.ID, &binding.Owner.Definition.ContractVersion,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CredentialBinding{}, fmt.Errorf("credential binding unavailable")
		}
		return CredentialBinding{}, err
	}
	binding.Owner.Definition.Kind = "provider-definition"
	return binding, nil
}
