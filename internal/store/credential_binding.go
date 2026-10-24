package store

import (
	"context"
	"database/sql"
	"errors"
)

var ErrCredentialBindingUnavailable = errors.New("credential binding unavailable")

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
	return scanCredentialBinding(s.runtime.credentialBinding.QueryRowContext(ctx, id))
}

type credentialBindingReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readCredentialBinding(ctx context.Context, reader credentialBindingReader, id string) (CredentialBinding, error) {
	return scanCredentialBinding(reader.QueryRowContext(ctx, credentialBindingSQL, id))
}

func scanCredentialBinding(row *sql.Row) (CredentialBinding, error) {
	var binding CredentialBinding
	err := row.Scan(
		&binding.ID, &binding.Type, &binding.Revision, &binding.PrincipalGeneration,
		&binding.Owner.ProviderNodeID, &binding.Owner.Definition.ID, &binding.Owner.Definition.ContractVersion,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CredentialBinding{}, ErrCredentialBindingUnavailable
		}
		return CredentialBinding{}, err
	}
	binding.Owner.Definition.Kind = "provider-definition"
	return binding, nil
}
