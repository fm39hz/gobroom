package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type CatalogImportInput struct {
	Expected    CredentialBinding
	Node        ProviderNode
	Models      []UpsertCatalogModelInput
	ExternalIDs []string
	Complete    bool
	Source      string
	Static      bool
}

func (s *Store) ImportConnectionCatalog(ctx context.Context, input CatalogImportInput) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	live, err := readCredentialBinding(ctx, tx, input.Expected.ID)
	if err != nil {
		return err
	}
	if live != input.Expected {
		return fmt.Errorf("credential binding changed")
	}
	var node ProviderNode
	err = tx.QueryRowContext(ctx, `SELECT id,definition_id,definition_contract_version,base_url,models_path,protocol,enabled FROM provider_nodes WHERE id=?`, input.Node.ID).Scan(&node.ID, &node.DefinitionID, &node.DefinitionContractVersion, &node.BaseURL, &node.ModelsPath, &node.Protocol, &node.Enabled)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("provider node unavailable")
		}
		return err
	}
	if node.ID != input.Expected.Owner.ProviderNodeID || node.ID != input.Node.ID || node.DefinitionID != input.Node.DefinitionID || node.DefinitionContractVersion != input.Node.DefinitionContractVersion || node.BaseURL != input.Node.BaseURL || node.ModelsPath != input.Node.ModelsPath || node.Protocol != input.Node.Protocol || node.Enabled != input.Node.Enabled {
		return fmt.Errorf("provider node definition changed")
	}
	for _, model := range input.Models {
		if model.Kind != "discovered" || model.ProviderNodeID != node.ID || len(model.ConnectionIDs) != 0 {
			return fmt.Errorf("catalog import accepts only discovered models owned by provider %q without connection assignments", node.ID)
		}
		if err := upsertCatalogModelTx(tx, model); err != nil {
			return err
		}
	}
	if input.Static {
		ids := make([]string, 0, len(input.Models))
		for _, model := range input.Models {
			ids = append(ids, model.ID)
		}
		if err := setStaticCatalogAssignmentsTx(tx, live.ID, node.ID, ids); err != nil {
			return err
		}
	} else {
		if err := recordConnectionModelSnapshotTx(tx, live.ID, node.ID, input.ExternalIDs, input.Complete, input.Source); err != nil {
			return err
		}
		if err := clearInvalidModelHealthTx(tx, live.ID, input.ExternalIDs); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return tx.Commit()
}
