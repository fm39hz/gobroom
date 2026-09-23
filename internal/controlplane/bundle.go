package controlplane

import (
	"encoding/json"
	"fmt"
	"github.com/fm39hz/gobroom/internal/store"
)

type ConfigBundle struct {
	Version     int                      `json:"version"`
	Providers   []store.ProviderNode     `json:"providers"`
	Connections []store.ConnectionRecord `json:"connections"`
	Models      []store.Model            `json:"models"`
	Physical    []store.PhysicalModel    `json:"physicalModels"`
	Combos      []store.ComboModel       `json:"comboModels"`
}

type BundleDiff struct {
	ProvidersAdded, ProvidersRemoved, ConnectionsAdded, ConnectionsRemoved int      `json:"providersAdded"`
	ModelsAdded, ModelsRemoved, PhysicalAdded, PhysicalRemoved             int      `json:"modelsAdded"`
	CombosAdded, CombosRemoved                                             int      `json:"combosAdded"`
	Changes                                                                []string `json:"changes"`
}

func ExportBundle(s *store.Store) (ConfigBundle, error) {
	providers, err := s.ProviderNodes()
	if err != nil {
		return ConfigBundle{}, err
	}
	connections, err := s.Connections("")
	if err != nil {
		return ConfigBundle{}, err
	}
	models, err := s.Models()
	if err != nil {
		return ConfigBundle{}, err
	}
	physical, err := s.PhysicalModels()
	if err != nil {
		return ConfigBundle{}, err
	}
	combos, err := s.ComboModels()
	if err != nil {
		return ConfigBundle{}, err
	}
	return ConfigBundle{Version: 1, Providers: providers, Connections: connections, Models: models, Physical: physical, Combos: combos}, nil
}

func ValidateBundle(bundle ConfigBundle) error {
	if bundle.Version != 1 {
		return fmt.Errorf("unsupported config bundle version %d", bundle.Version)
	}
	providers := map[string]bool{}
	for _, item := range bundle.Providers {
		if item.ID == "" {
			return fmt.Errorf("provider ID is required")
		}
		if providers[item.ID] {
			return fmt.Errorf("duplicate provider %q", item.ID)
		}
		providers[item.ID] = true
	}
	for _, item := range bundle.Connections {
		if item.ID == "" || !providers[item.ProviderNodeID] {
			return fmt.Errorf("connection %q references unknown provider", item.ID)
		}
	}
	models := map[string]bool{}
	for _, item := range bundle.Models {
		if item.ID == "" {
			return fmt.Errorf("model ID is required")
		}
		models[item.ID] = true
	}
	physical := map[string]bool{}
	for _, item := range bundle.Physical {
		if item.Name == "" {
			return fmt.Errorf("physical model name is required")
		}
		if physical[item.Name] {
			return fmt.Errorf("duplicate physical model %q", item.Name)
		}
		physical[item.Name] = true
		for _, source := range item.Sources {
			if !models[source.RouteID] {
				return fmt.Errorf("physical %q references unknown route %q", item.Name, source.RouteID)
			}
		}
	}
	for _, item := range bundle.Combos {
		if item.Name == "" {
			return fmt.Errorf("combo model name is required")
		}
		for _, member := range item.Members {
			if member.Kind == store.PhysicalReference && !physical[member.ID] {
				return fmt.Errorf("combo %q references unknown physical %q", item.Name, member.ID)
			}
		}
	}
	return nil
}

func DiffBundle(current, desired ConfigBundle) (BundleDiff, error) {
	if err := ValidateBundle(desired); err != nil {
		return BundleDiff{}, err
	}
	result := BundleDiff{}
	result.ProvidersAdded, result.ProvidersRemoved = countDelta(providerIDs(current.Providers), providerIDs(desired.Providers), "provider", &result.Changes)
	result.ConnectionsAdded, result.ConnectionsRemoved = countDelta(connectionIDs(current.Connections), connectionIDs(desired.Connections), "connection", &result.Changes)
	result.ModelsAdded, result.ModelsRemoved = countDelta(modelIDs(current.Models), modelIDs(desired.Models), "discovered model", &result.Changes)
	result.PhysicalAdded, result.PhysicalRemoved = countDelta(physicalIDs(current.Physical), physicalIDs(desired.Physical), "physical model", &result.Changes)
	result.CombosAdded, result.CombosRemoved = countDelta(comboIDs(current.Combos), comboIDs(desired.Combos), "combo model", &result.Changes)
	return result, nil
}

func ApplyBundle(s *store.Store, bundle ConfigBundle) error {
	if err := ValidateBundle(bundle); err != nil {
		return err
	}
	current, err := ExportBundle(s)
	if err != nil {
		return err
	}
	secrets := map[string]string{}
	for _, connection := range current.Connections {
		var secret string
		if err := s.DB.QueryRow(`SELECT secret_ref FROM connections WHERE id=?`, connection.ID).Scan(&secret); err == nil {
			secrets[connection.ID] = secret
		}
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, provider := range bundle.Providers {
		if _, err = tx.Exec(`INSERT INTO provider_nodes(id,name,base_url,protocol,definition_id,prefix,models_path,auth_mode,enabled,updated_at) VALUES(?,?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP) ON CONFLICT(id) DO UPDATE SET name=excluded.name,base_url=excluded.base_url,protocol=excluded.protocol,definition_id=excluded.definition_id,prefix=excluded.prefix,models_path=excluded.models_path,auth_mode=excluded.auth_mode,enabled=excluded.enabled,updated_at=CURRENT_TIMESTAMP`, provider.ID, provider.Name, provider.BaseURL, provider.Protocol, provider.DefinitionID, provider.Prefix, provider.ModelsPath, provider.AuthMode, boolInt(provider.Enabled)); err != nil {
			return err
		}
	}
	for _, connection := range bundle.Connections {
		secret := secrets[connection.ID]
		if _, err = tx.Exec(`INSERT INTO connections(id,provider_node_id,name,email,credential_type,secret_ref,priority,enabled,state_json,updated_at) VALUES(?,?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP) ON CONFLICT(id) DO UPDATE SET provider_node_id=excluded.provider_node_id,name=excluded.name,email=excluded.email,credential_type=excluded.credential_type,priority=excluded.priority,enabled=excluded.enabled,updated_at=CURRENT_TIMESTAMP`, connection.ID, connection.ProviderNodeID, connection.Name, connection.Email, connection.CredentialType, secret, connection.Priority, boolInt(connection.Enabled), `{}`); err != nil {
			return err
		}
	}
	for _, model := range bundle.Models {
		profile, _ := json.Marshal(model.Profile)
		limits, _ := json.Marshal(model.Limits)
		if _, err = tx.Exec(`INSERT INTO model_catalog(id,provider_node_id,kind,external_id,display_name,capabilities_json,limits_json,overrides_json,raw_json,enabled,last_seen_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?, ?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP) ON CONFLICT(id) DO UPDATE SET provider_node_id=excluded.provider_node_id,kind=excluded.kind,external_id=excluded.external_id,display_name=excluded.display_name,capabilities_json=excluded.capabilities_json,limits_json=excluded.limits_json,enabled=excluded.enabled,updated_at=CURRENT_TIMESTAMP`, model.ID, model.NodeID, model.Kind, model.ExternalID, model.DisplayName, string(profile), string(limits), `{}`, `{}`, boolInt(true)); err != nil {
			return err
		}
	}
	for _, item := range bundle.Physical {
		identity, _ := json.Marshal(item.Identity)
		reasoning, _ := json.Marshal(item.Reasoning)
		policy, _ := json.Marshal(item.Policy)
		profile, _ := json.Marshal(item.Profile)
		limits, _ := json.Marshal(item.Limits)
		if _, err = tx.Exec(`INSERT INTO physical_models(name,identity_json,reasoning_json,policy_json,capabilities_json,limits_json,discoverable,enabled,updated_at) VALUES(?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP) ON CONFLICT(name) DO UPDATE SET identity_json=excluded.identity_json,reasoning_json=excluded.reasoning_json,policy_json=excluded.policy_json,capabilities_json=excluded.capabilities_json,limits_json=excluded.limits_json,discoverable=excluded.discoverable,enabled=excluded.enabled,updated_at=CURRENT_TIMESTAMP`, item.Name, string(identity), string(reasoning), string(policy), string(profile), string(limits), boolInt(item.Discoverable), boolInt(item.Enabled)); err != nil {
			return err
		}
		if _, err = tx.Exec(`DELETE FROM physical_model_sources WHERE physical_name=?`, item.Name); err != nil {
			return err
		}
		for position, source := range item.Sources {
			evidence, _ := json.Marshal(source.Evidence)
			if source.Fidelity == "" {
				source.Fidelity = "unknown"
			}
			if _, err = tx.Exec(`INSERT INTO physical_model_sources(physical_name,position,route_id,fidelity,evidence_json) VALUES(?,?,?,?,?)`, item.Name, position, source.RouteID, source.Fidelity, string(evidence)); err != nil {
				return err
			}
		}
	}
	for _, item := range bundle.Combos {
		reasoning, _ := json.Marshal(item.Reasoning)
		strategy, _ := json.Marshal(item.Strategy)
		if _, err = tx.Exec(`INSERT INTO combo_models(name,reasoning_json,strategy_json,discoverable,enabled,updated_at) VALUES(?,?,?,?,?,CURRENT_TIMESTAMP) ON CONFLICT(name) DO UPDATE SET reasoning_json=excluded.reasoning_json,strategy_json=excluded.strategy_json,discoverable=excluded.discoverable,enabled=excluded.enabled,updated_at=CURRENT_TIMESTAMP`, item.Name, string(reasoning), string(strategy), boolInt(item.Discoverable), boolInt(item.Enabled)); err != nil {
			return err
		}
		if _, err = tx.Exec(`DELETE FROM combo_model_members WHERE combo_name=?`, item.Name); err != nil {
			return err
		}
		for position, member := range item.Members {
			if _, err = tx.Exec(`INSERT INTO combo_model_members(combo_name,position,ref_kind,ref_id) VALUES(?,?,?,?)`, item.Name, position, member.Kind, member.ID); err != nil {
				return err
			}
		}
	}
	// Remove objects absent from the desired bundle only after all references are rebuilt.
	for _, item := range current.Combos {
		if !containsCombo(bundle.Combos, item.Name) {
			if _, err = tx.Exec(`DELETE FROM combo_models WHERE name=?`, item.Name); err != nil {
				return err
			}
		}
	}
	for _, item := range current.Physical {
		if !containsPhysical(bundle.Physical, item.Name) {
			if _, err = tx.Exec(`DELETE FROM physical_models WHERE name=?`, item.Name); err != nil {
				return err
			}
		}
	}
	for _, item := range current.Models {
		if !containsModel(bundle.Models, item.ID) {
			if _, err = tx.Exec(`DELETE FROM model_catalog WHERE id=?`, item.ID); err != nil {
				return err
			}
		}
	}
	for _, item := range current.Connections {
		if !containsConnection(bundle.Connections, item.ID) {
			if _, err = tx.Exec(`DELETE FROM connections WHERE id=?`, item.ID); err != nil {
				return err
			}
		}
	}
	for _, item := range current.Providers {
		if !containsProvider(bundle.Providers, item.ID) {
			if _, err = tx.Exec(`DELETE FROM provider_nodes WHERE id=?`, item.ID); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
func containsProvider(items []store.ProviderNode, id string) bool {
	for _, item := range items {
		if item.ID == id {
			return true
		}
	}
	return false
}
func containsConnection(items []store.ConnectionRecord, id string) bool {
	for _, item := range items {
		if item.ID == id {
			return true
		}
	}
	return false
}
func containsModel(items []store.Model, id string) bool {
	for _, item := range items {
		if item.ID == id {
			return true
		}
	}
	return false
}
func containsPhysical(items []store.PhysicalModel, id string) bool {
	for _, item := range items {
		if item.Name == id {
			return true
		}
	}
	return false
}
func containsCombo(items []store.ComboModel, id string) bool {
	for _, item := range items {
		if item.Name == id {
			return true
		}
	}
	return false
}

func countDelta(current, desired map[string]bool, kind string, changes *[]string) (int, int) {
	added, removed := 0, 0
	for id := range desired {
		if !current[id] {
			added++
			*changes = append(*changes, "+ "+kind+" "+id)
		}
	}
	for id := range current {
		if !desired[id] {
			removed++
			*changes = append(*changes, "- "+kind+" "+id)
		}
	}
	return added, removed
}
func providerIDs(items []store.ProviderNode) map[string]bool {
	result := map[string]bool{}
	for _, item := range items {
		result[item.ID] = true
	}
	return result
}
func connectionIDs(items []store.ConnectionRecord) map[string]bool {
	result := map[string]bool{}
	for _, item := range items {
		result[item.ID] = true
	}
	return result
}
func modelIDs(items []store.Model) map[string]bool {
	result := map[string]bool{}
	for _, item := range items {
		result[item.ID] = true
	}
	return result
}
func physicalIDs(items []store.PhysicalModel) map[string]bool {
	result := map[string]bool{}
	for _, item := range items {
		result[item.Name] = true
	}
	return result
}
func comboIDs(items []store.ComboModel) map[string]bool {
	result := map[string]bool{}
	for _, item := range items {
		result[item.Name] = true
	}
	return result
}
