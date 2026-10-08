package store

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/fm39hz/gobroom/internal/provider"
)

func (s *Store) ProviderDefinitions() ([]provider.ProviderDefinition, error) {
	rows, err := s.DB.Query(`SELECT id,contract_version,definition_json FROM provider_definitions ORDER BY id,contract_version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []provider.ProviderDefinition
	for rows.Next() {
		var id, encoded string
		var contractVersion uint64
		if err := rows.Scan(&id, &contractVersion, &encoded); err != nil {
			return nil, err
		}
		definition, err := provider.DecodeProviderDefinitionJSON([]byte(encoded))
		if err != nil {
			return nil, fmt.Errorf("stored provider definition %q: %w", id, err)
		}
		if definition.ID != id || definition.ContractVersion != contractVersion {
			return nil, fmt.Errorf("stored provider definition key %q@%d does not match its payload", id, contractVersion)
		}
		if portable, reason := provider.ProviderDefinitionPortable(definition); !portable {
			return nil, fmt.Errorf("stored provider definition %q is not portable: %s", id, reason)
		}
		result = append(result, definition)
	}
	return result, rows.Err()
}

func ReplaceProviderDefinitionsInTx(tx *sql.Tx, definitions []provider.ProviderDefinition) error {
	if tx == nil {
		return fmt.Errorf("provider definition transaction is required")
	}
	seen := make(map[string]bool, len(definitions))
	for _, definition := range definitions {
		if definition.ContractVersion != 1 || definition.ID == "" || definition.Version == "" || definition.DisplayName == "" {
			return fmt.Errorf("portable provider definition requires contract version 1, ID, version and display name")
		}
		if seen[definition.ID] {
			return fmt.Errorf("duplicate portable provider definition %q", definition.ID)
		}
		seen[definition.ID] = true
		if portable, reason := provider.ProviderDefinitionPortable(definition); !portable {
			return fmt.Errorf("provider definition %q cannot be embedded: %s", definition.ID, reason)
		}
	}
	if _, err := tx.Exec(`DELETE FROM provider_definitions`); err != nil {
		return err
	}
	for _, definition := range definitions {
		encoded, err := json.Marshal(definition)
		if err != nil {
			return fmt.Errorf("encode provider definition %q: %w", definition.ID, err)
		}
		if _, err := tx.Exec(`INSERT INTO provider_definitions(id,contract_version,definition_json,updated_at) VALUES(?,?,?,CURRENT_TIMESTAMP)`, definition.ID, definition.ContractVersion, string(encoded)); err != nil {
			return err
		}
	}
	return nil
}
