package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/fm39hz/gobroom/internal/kernel"
)

func (s *Store) TransformBindings() ([]kernel.TransformBinding, error) {
	rows, err := s.DB.Query(`SELECT id,transform_kind,transform_id,contract_version,enabled,scope_kind,scope_id,ordering,failure_mode,options_json FROM transform_bindings ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]kernel.TransformBinding, 0)
	for rows.Next() {
		var binding kernel.TransformBinding
		var enabled int
		var options string
		if err := rows.Scan(&binding.ID, &binding.TransformRef.Kind, &binding.TransformRef.ID, &binding.TransformRef.ContractVersion, &enabled, &binding.Scope.Kind, &binding.Scope.ID, &binding.Order, &binding.FailureMode, &options); err != nil {
			return nil, err
		}
		binding.Enabled = enabled != 0
		if options != "" && options != "null" {
			binding.Options = json.RawMessage(options)
		}
		if err := kernel.ValidateTransformBinding(binding); err != nil {
			return nil, fmt.Errorf("stored transform binding %q: %w", binding.ID, err)
		}
		result = append(result, binding)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Scope.Kind != result[j].Scope.Kind {
			return result[i].Scope.Kind < result[j].Scope.Kind
		}
		if result[i].Scope.ID != result[j].Scope.ID {
			return result[i].Scope.ID < result[j].Scope.ID
		}
		if result[i].Order != result[j].Order {
			return result[i].Order < result[j].Order
		}
		return result[i].ID < result[j].ID
	})
	return result, nil
}

func (s *Store) UpsertTransformBinding(binding kernel.TransformBinding) error {
	if err := kernel.ValidateTransformBinding(binding); err != nil {
		return err
	}
	return upsertTransformBinding(s.DB, binding)
}

func UpsertTransformBindingInTx(tx *sql.Tx, binding kernel.TransformBinding) error {
	if tx == nil {
		return fmt.Errorf("transform binding transaction is required")
	}
	return upsertTransformBinding(tx, binding)
}

func (s *Store) DeleteTransformBinding(id string) error {
	if id == "" {
		return fmt.Errorf("transform binding ID is required")
	}
	_, err := s.DB.Exec(`DELETE FROM transform_bindings WHERE id=?`, id)
	return err
}

type transformBindingExecutor interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func upsertTransformBinding(executor transformBindingExecutor, binding kernel.TransformBinding) error {
	if err := kernel.ValidateTransformBinding(binding); err != nil {
		return err
	}
	options := "null"
	if len(binding.Options) > 0 {
		options = string(binding.Options)
	}
	_, err := executor.Exec(`INSERT INTO transform_bindings(id,transform_kind,transform_id,contract_version,enabled,scope_kind,scope_id,ordering,failure_mode,options_json,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP)
ON CONFLICT(id) DO UPDATE SET transform_kind=excluded.transform_kind,transform_id=excluded.transform_id,contract_version=excluded.contract_version,enabled=excluded.enabled,scope_kind=excluded.scope_kind,scope_id=excluded.scope_id,ordering=excluded.ordering,failure_mode=excluded.failure_mode,options_json=excluded.options_json,updated_at=CURRENT_TIMESTAMP`, binding.ID, binding.TransformRef.Kind, binding.TransformRef.ID, binding.TransformRef.ContractVersion, boolInt(binding.Enabled), binding.Scope.Kind, binding.Scope.ID, binding.Order, binding.FailureMode, options)
	return err
}
