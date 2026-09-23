package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/fm39hz/gobroom/internal/kernel"
)

// ModelReference is a typed edge in the model graph. Route references are
// represented separately by RouteReference and always point at model_catalog.
type ModelReference struct {
	Kind ModelReferenceKind `json:"kind"`
	ID   string             `json:"id"`
}

type ModelReferenceKind string

const (
	PhysicalReference ModelReferenceKind = "physical"
	ComboReference    ModelReferenceKind = "combo"
)

type RouteReference struct {
	RouteID string `json:"routeId"`
}

// StrategySpec binds a registered strategy primitive to its typed JSON
// configuration. The store persists the whole value as JSON so primitive
// implementations can evolve without adding schema columns.
type StrategySpec struct {
	ID     string         `json:"id"`
	Config map[string]any `json:"config,omitempty"`
}

type DiscoveredRoute struct {
	ID             string                   `json:"id"`
	ProviderNodeID string                   `json:"providerNodeId,omitempty"`
	ProviderPrefix string                   `json:"providerPrefix,omitempty"`
	Kind           string                   `json:"kind"`
	ExternalID     string                   `json:"externalId"`
	DisplayName    string                   `json:"displayName"`
	Profile        kernel.CapabilityProfile `json:"profile,omitempty"`
	Enabled        bool                     `json:"enabled"`
	LastSeenAt     string                   `json:"lastSeenAt,omitempty"`
}

type PhysicalModel struct {
	Name         string                   `json:"name"`
	Sources      []RouteReference         `json:"sources"`
	Policy       StrategySpec             `json:"policy"`
	Profile      kernel.CapabilityProfile `json:"profile,omitempty"`
	Discoverable bool                     `json:"discoverable"`
	Enabled      bool                     `json:"enabled"`
}

type ComboModel struct {
	Name         string           `json:"name"`
	Members      []ModelReference `json:"members"`
	Strategy     StrategySpec     `json:"strategy"`
	Discoverable bool             `json:"discoverable"`
	Enabled      bool             `json:"enabled"`
}

var ErrModelReferenced = errors.New("model is referenced by another model")

// DiscoveredRoutes returns the provider-backed route inventory already held
// in model_catalog. Custom provider routes are included alongside discovered
// routes; no duplicate catalog table or one-time data copy is needed.
func (s *Store) DiscoveredRoutes(providerNodeID string) ([]DiscoveredRoute, error) {
	query := `SELECT m.id,COALESCE(m.provider_node_id,''),COALESCE(n.prefix,''),m.kind,m.external_id,m.display_name,m.capabilities_json,m.enabled,COALESCE(m.last_seen_at,'')
FROM model_catalog m LEFT JOIN provider_nodes n ON n.id=m.provider_node_id
WHERE m.provider_node_id IS NOT NULL AND m.kind IN ('discovered','custom')`
	args := []any{}
	if providerNodeID != "" {
		query += ` AND m.provider_node_id=?`
		args = append(args, providerNodeID)
	}
	query += ` ORDER BY n.prefix,m.display_name,m.external_id,m.id`
	rows, err := s.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []DiscoveredRoute
	for rows.Next() {
		var item DiscoveredRoute
		var caps string
		var enabled int
		if err := rows.Scan(&item.ID, &item.ProviderNodeID, &item.ProviderPrefix, &item.Kind, &item.ExternalID, &item.DisplayName, &caps, &enabled, &item.LastSeenAt); err != nil {
			return nil, err
		}
		if err := decodeJSON(caps, &item.Profile); err != nil {
			return nil, fmt.Errorf("route %q profile: %w", item.ID, err)
		}
		item.Enabled = enabled != 0
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) PhysicalModels() ([]PhysicalModel, error) {
	rows, err := s.DB.Query(`SELECT name,policy_json,capabilities_json,discoverable,enabled FROM physical_models WHERE enabled=1 ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []PhysicalModel
	for rows.Next() {
		var item PhysicalModel
		var policy, caps string
		var discoverable, enabled int
		if err := rows.Scan(&item.Name, &policy, &caps, &discoverable, &enabled); err != nil {
			return nil, err
		}
		if err := decodeJSON(policy, &item.Policy); err != nil {
			return nil, fmt.Errorf("physical model %q policy: %w", item.Name, err)
		}
		if err := decodeJSON(caps, &item.Profile); err != nil {
			return nil, fmt.Errorf("physical model %q profile: %w", item.Name, err)
		}
		item.Discoverable, item.Enabled = discoverable != 0, enabled != 0
		item.Sources, err = s.physicalSources(item.Name)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) PhysicalModel(name string) (PhysicalModel, error) {
	var item PhysicalModel
	var policy, caps string
	var discoverable, enabled int
	err := s.DB.QueryRow(`SELECT name,policy_json,capabilities_json,discoverable,enabled FROM physical_models WHERE name=?`, name).Scan(&item.Name, &policy, &caps, &discoverable, &enabled)
	if err != nil {
		return item, err
	}
	if err := decodeJSON(policy, &item.Policy); err != nil {
		return item, fmt.Errorf("physical model %q policy: %w", name, err)
	}
	if err := decodeJSON(caps, &item.Profile); err != nil {
		return item, fmt.Errorf("physical model %q profile: %w", name, err)
	}
	item.Discoverable, item.Enabled = discoverable != 0, enabled != 0
	item.Sources, err = s.physicalSources(name)
	return item, err
}

func (s *Store) physicalSources(name string) ([]RouteReference, error) {
	rows, err := s.DB.Query(`SELECT route_id FROM physical_model_sources WHERE physical_name=? ORDER BY position`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []RouteReference
	for rows.Next() {
		var item RouteReference
		if err := rows.Scan(&item.RouteID); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) UpsertPhysicalModel(item PhysicalModel) error {
	if item.Name == "" {
		return fmt.Errorf("physical model name is required")
	}
	if item.Policy.ID == "" {
		item.Policy.ID = "ordered-fallback"
	}
	policy, err := json.Marshal(item.Policy)
	if err != nil {
		return fmt.Errorf("encode physical model policy: %w", err)
	}
	caps, err := json.Marshal(item.Profile)
	if err != nil {
		return fmt.Errorf("encode physical model profile: %w", err)
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO physical_models(name,policy_json,capabilities_json,discoverable,enabled,updated_at) VALUES(?,?,?,?,?,CURRENT_TIMESTAMP)
ON CONFLICT(name) DO UPDATE SET policy_json=excluded.policy_json,capabilities_json=excluded.capabilities_json,discoverable=excluded.discoverable,enabled=excluded.enabled,updated_at=CURRENT_TIMESTAMP`, item.Name, string(policy), string(caps), boolInt(item.Discoverable), boolInt(item.Enabled)); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM physical_model_sources WHERE physical_name=?`, item.Name); err != nil {
		return err
	}
	for position, source := range item.Sources {
		if source.RouteID == "" {
			return fmt.Errorf("physical model source %d has empty route ID", position)
		}
		var count int
		if err = tx.QueryRow(`SELECT count(*) FROM model_catalog WHERE id=? AND provider_node_id IS NOT NULL AND kind IN ('discovered','custom')`, source.RouteID).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			return fmt.Errorf("route %q is not a discovered or custom provider route", source.RouteID)
		}
		if _, err = tx.Exec(`INSERT INTO physical_model_sources(physical_name,position,route_id) VALUES(?,?,?)`, item.Name, position, source.RouteID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DeletePhysicalModel(name string) error {
	var count int
	if err := s.DB.QueryRow(`SELECT count(*) FROM combo_model_members WHERE ref_kind='physical' AND ref_id=?`, name).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("physical model %q: %w", name, ErrModelReferenced)
	}
	result, err := s.DB.Exec(`DELETE FROM physical_models WHERE name=?`, name)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return fmt.Errorf("physical model %q not found", name)
	}
	return nil
}

func (s *Store) ComboModels() ([]ComboModel, error) {
	rows, err := s.DB.Query(`SELECT name,strategy_json,discoverable,enabled FROM combo_models WHERE enabled=1 ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ComboModel
	for rows.Next() {
		var item ComboModel
		var strategy string
		var discoverable, enabled int
		if err := rows.Scan(&item.Name, &strategy, &discoverable, &enabled); err != nil {
			return nil, err
		}
		if err := decodeJSON(strategy, &item.Strategy); err != nil {
			return nil, fmt.Errorf("combo model %q strategy: %w", item.Name, err)
		}
		item.Discoverable, item.Enabled = discoverable != 0, enabled != 0
		item.Members, err = s.comboMembers(item.Name)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) ComboModel(name string) (ComboModel, error) {
	var item ComboModel
	var strategy string
	var discoverable, enabled int
	err := s.DB.QueryRow(`SELECT name,strategy_json,discoverable,enabled FROM combo_models WHERE name=?`, name).Scan(&item.Name, &strategy, &discoverable, &enabled)
	if err != nil {
		return item, err
	}
	if err := decodeJSON(strategy, &item.Strategy); err != nil {
		return item, fmt.Errorf("combo model %q strategy: %w", name, err)
	}
	item.Discoverable, item.Enabled = discoverable != 0, enabled != 0
	item.Members, err = s.comboMembers(name)
	return item, err
}

func (s *Store) comboMembers(name string) ([]ModelReference, error) {
	rows, err := s.DB.Query(`SELECT ref_kind,ref_id FROM combo_model_members WHERE combo_name=? ORDER BY position`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ModelReference
	for rows.Next() {
		var item ModelReference
		if err := rows.Scan(&item.Kind, &item.ID); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) UpsertComboModel(item ComboModel) error {
	if item.Name == "" {
		return fmt.Errorf("combo model name is required")
	}
	if item.Strategy.ID == "" {
		item.Strategy.ID = "ordered-fallback"
	}
	strategy, err := json.Marshal(item.Strategy)
	if err != nil {
		return fmt.Errorf("encode combo strategy: %w", err)
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO combo_models(name,strategy_json,discoverable,enabled,updated_at) VALUES(?,?,?,?,CURRENT_TIMESTAMP)
ON CONFLICT(name) DO UPDATE SET strategy_json=excluded.strategy_json,discoverable=excluded.discoverable,enabled=excluded.enabled,updated_at=CURRENT_TIMESTAMP`, item.Name, string(strategy), boolInt(item.Discoverable), boolInt(item.Enabled)); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM combo_model_members WHERE combo_name=?`, item.Name); err != nil {
		return err
	}
	for position, member := range item.Members {
		if member.ID == "" {
			return fmt.Errorf("combo member %d has empty reference ID", position)
		}
		if member.Kind != PhysicalReference && member.Kind != ComboReference {
			return fmt.Errorf("combo member %d has unsupported reference kind %q", position, member.Kind)
		}
		if member.Kind == ComboReference && member.ID == item.Name {
			return fmt.Errorf("combo model %q cannot reference itself", item.Name)
		}
		var count int
		table := "physical_models"
		if member.Kind == ComboReference {
			table = "combo_models"
		}
		if err = tx.QueryRow(`SELECT count(*) FROM `+table+` WHERE name=? AND enabled=1`, member.ID).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			return fmt.Errorf("combo member %s %q does not exist or is disabled", member.Kind, member.ID)
		}
		if _, err = tx.Exec(`INSERT INTO combo_model_members(combo_name,position,ref_kind,ref_id) VALUES(?,?,?,?)`, item.Name, position, member.Kind, member.ID); err != nil {
			return err
		}
	}
	cycle, err := comboHasCycle(tx, item.Name)
	if err != nil {
		return err
	}
	if cycle {
		return fmt.Errorf("combo model %q introduces a reference cycle", item.Name)
	}
	return tx.Commit()
}

func comboHasCycle(tx *sql.Tx, root string) (bool, error) {
	visiting := map[string]bool{}
	visited := map[string]bool{}
	var visit func(string) (bool, error)
	visit = func(name string) (bool, error) {
		if visiting[name] {
			return true, nil
		}
		if visited[name] {
			return false, nil
		}
		visiting[name] = true
		rows, err := tx.Query(`SELECT ref_id FROM combo_model_members WHERE combo_name=? AND ref_kind='combo'`, name)
		if err != nil {
			return false, err
		}
		for rows.Next() {
			var child string
			if err := rows.Scan(&child); err != nil {
				rows.Close()
				return false, err
			}
			cycle, err := visit(child)
			if err != nil || cycle {
				rows.Close()
				return cycle, err
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return false, err
		}
		rows.Close()
		delete(visiting, name)
		visited[name] = true
		return false, nil
	}
	return visit(root)
}

func (s *Store) DeleteComboModel(name string) error {
	var count int
	if err := s.DB.QueryRow(`SELECT count(*) FROM combo_model_members WHERE ref_kind='combo' AND ref_id=?`, name).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("combo model %q: %w", name, ErrModelReferenced)
	}
	result, err := s.DB.Exec(`DELETE FROM combo_models WHERE name=?`, name)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return fmt.Errorf("combo model %q not found", name)
	}
	return nil
}

func decodeJSON(raw string, target any) error {
	if raw == "" {
		return nil
	}
	return json.Unmarshal([]byte(raw), target)
}
