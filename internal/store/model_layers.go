package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

// ModelReference is a typed edge in the model graph. Route references are
// represented separately by RouteReference and always point at model_catalog.
type ModelReference struct {
	Kind   ModelReferenceKind `json:"kind"`
	ID     string             `json:"id"`
	Weight int                `json:"weight,omitempty"`
}

type ModelReferenceKind string

const (
	PhysicalReference ModelReferenceKind = "physical"
	ComboReference    ModelReferenceKind = "combo"
)

type RouteReference struct {
	RouteID  string                `json:"routeId"`
	Fidelity kernel.SourceFidelity `json:"fidelity,omitempty"`
	Evidence []kernel.Evidence     `json:"evidence,omitempty"`
}

// StrategySpec binds a registered strategy primitive to its typed JSON
// configuration. The store persists the whole value as JSON so primitive
// implementations can evolve without adding schema columns.
type StrategySpec struct {
	Ref    extensions.Ref `json:"ref"`
	Config map[string]any `json:"config,omitempty"`
}

type DiscoveredRoute struct {
	ID                     string                    `json:"id"`
	ProviderNodeID         string                    `json:"providerNodeId,omitempty"`
	ProviderPrefix         string                    `json:"providerPrefix,omitempty"`
	Kind                   string                    `json:"kind"`
	ExternalID             string                    `json:"externalId"`
	DisplayName            string                    `json:"displayName"`
	Profile                kernel.CapabilityProfile  `json:"profile,omitempty"`
	Limits                 kernel.TokenLimits        `json:"limits,omitempty"`
	ConnectionAvailability []ConnectionModelEvidence `json:"connectionAvailability,omitempty"`
	Enabled                bool                      `json:"enabled"`
	LastSeenAt             string                    `json:"lastSeenAt,omitempty"`
}

type ConnectionModelEvidence struct {
	ConnectionID   string `json:"connectionId"`
	ConnectionName string `json:"connectionName"`
	Status         string `json:"status"`
	Source         string `json:"source,omitempty"`
	ObservedAt     string `json:"observedAt,omitempty"`
}

type PhysicalModel struct {
	Name                   string                   `json:"name"`
	Identity               kernel.PhysicalIdentity  `json:"identity"`
	Sources                []RouteReference         `json:"sources"`
	Policy                 StrategySpec             `json:"policy"`
	Profile                kernel.CapabilityProfile `json:"profile,omitempty"`
	Limits                 kernel.TokenLimits       `json:"limits,omitempty"`
	Projection             kernel.ProfileProjection `json:"projection,omitempty"`
	Reasoning              normalize.ThinkingIntent `json:"reasoning,omitempty"`
	AllowCompatibleSources bool                     `json:"allowCompatibleSources,omitempty"`
	AllowDynamicSources    bool                     `json:"allowDynamicSources,omitempty"`
	Discoverable           bool                     `json:"discoverable"`
	Enabled                bool                     `json:"enabled"`
}

type ComboModel struct {
	Name         string                   `json:"name"`
	Members      []ModelReference         `json:"members"`
	Strategy     StrategySpec             `json:"strategy"`
	Discoverable bool                     `json:"discoverable"`
	Enabled      bool                     `json:"enabled"`
	Reasoning    normalize.ThinkingIntent `json:"reasoning,omitempty"`
}

var ErrModelReferenced = errors.New("model is referenced by another model")

// DiscoveredRoutes returns the provider-backed route inventory already held
// in model_catalog. Custom provider routes are included alongside discovered
// routes; no duplicate catalog table or one-time data copy is needed.
func (s *Store) DiscoveredRoutes(providerNodeID string) ([]DiscoveredRoute, error) {
	query := `SELECT m.id,COALESCE(m.provider_node_id,''),COALESCE(n.prefix,''),m.kind,m.external_id,m.display_name,m.capabilities_json,m.limits_json,m.enabled,COALESCE(m.last_seen_at,'')
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
		var caps, limits string
		var enabled int
		if err := rows.Scan(&item.ID, &item.ProviderNodeID, &item.ProviderPrefix, &item.Kind, &item.ExternalID, &item.DisplayName, &caps, &limits, &enabled, &item.LastSeenAt); err != nil {
			return nil, err
		}
		if err := decodeJSON(caps, &item.Profile); err != nil {
			return nil, fmt.Errorf("route %q profile: %w", item.ID, err)
		}
		if err := decodeJSON(limits, &item.Limits); err != nil {
			return nil, fmt.Errorf("route %q limits: %w", item.ID, err)
		}
		item.Enabled = enabled != 0
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	connections, err := s.Connections(providerNodeID)
	if err != nil {
		return nil, err
	}
	entitlements, err := s.ConnectionModelEntitlements(providerNodeID)
	if err != nil {
		return nil, err
	}
	byConnectionRoute := make(map[string]ConnectionModelEntitlement, len(entitlements))
	for _, item := range entitlements {
		byConnectionRoute[item.ConnectionID+"\x00"+item.ExternalModelID] = item
	}
	for index := range result {
		item := &result[index]
		if item.Kind == "custom" {
			item.ConnectionAvailability = []ConnectionModelEvidence{{Status: "provider_assertion", Source: "user"}}
			continue
		}
		for _, connection := range connections {
			if !connection.Enabled || connection.ProviderNodeID != item.ProviderNodeID {
				continue
			}
			evidence := ConnectionModelEvidence{ConnectionID: connection.ID, ConnectionName: connection.Name, Status: "unknown"}
			if recorded, ok := byConnectionRoute[connection.ID+"\x00"+item.ExternalID]; ok {
				evidence.Status, evidence.Source, evidence.ObservedAt = recorded.Status, recorded.Source, recorded.ObservedAt
			}
			item.ConnectionAvailability = append(item.ConnectionAvailability, evidence)
		}
	}
	return result, nil
}

func (s *Store) PhysicalModels() ([]PhysicalModel, error) {
	rows, err := s.DB.Query(`SELECT name,identity_json,reasoning_json,allow_compatible_sources,allow_dynamic_sources,policy_json,capabilities_json,limits_json,discoverable,enabled FROM physical_models WHERE enabled=1 ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []PhysicalModel
	for rows.Next() {
		var item PhysicalModel
		var identity, reasoning, policy, caps, limits string
		var allowCompatible, allowDynamic int
		var discoverable, enabled int
		if err := rows.Scan(&item.Name, &identity, &reasoning, &allowCompatible, &allowDynamic, &policy, &caps, &limits, &discoverable, &enabled); err != nil {
			return nil, err
		}
		if err := decodeJSON(policy, &item.Policy); err != nil {
			return nil, fmt.Errorf("physical model %q policy: %w", item.Name, err)
		}
		if err := decodeJSON(identity, &item.Identity); err != nil {
			return nil, fmt.Errorf("physical model %q identity: %w", item.Name, err)
		}
		if err := decodeJSON(reasoning, &item.Reasoning); err != nil {
			return nil, fmt.Errorf("physical model %q reasoning: %w", item.Name, err)
		}
		if err := decodeJSON(caps, &item.Profile); err != nil {
			return nil, fmt.Errorf("physical model %q profile: %w", item.Name, err)
		}
		if err := decodeJSON(limits, &item.Limits); err != nil {
			return nil, fmt.Errorf("physical model %q limits: %w", item.Name, err)
		}
		item.Discoverable, item.Enabled = discoverable != 0, enabled != 0
		item.AllowCompatibleSources, item.AllowDynamicSources = allowCompatible != 0, allowDynamic != 0
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
	var identity, reasoning, policy, caps, limits string
	var discoverable, enabled int
	var allowCompatible, allowDynamic int
	err := s.DB.QueryRow(`SELECT name,identity_json,reasoning_json,allow_compatible_sources,allow_dynamic_sources,policy_json,capabilities_json,limits_json,discoverable,enabled FROM physical_models WHERE name=?`, name).Scan(&item.Name, &identity, &reasoning, &allowCompatible, &allowDynamic, &policy, &caps, &limits, &discoverable, &enabled)
	if err != nil {
		return item, err
	}
	if err := decodeJSON(policy, &item.Policy); err != nil {
		return item, fmt.Errorf("physical model %q policy: %w", name, err)
	}
	if err := decodeJSON(identity, &item.Identity); err != nil {
		return item, fmt.Errorf("physical model %q identity: %w", name, err)
	}
	if err := decodeJSON(reasoning, &item.Reasoning); err != nil {
		return item, fmt.Errorf("physical model %q reasoning: %w", name, err)
	}
	if err := decodeJSON(caps, &item.Profile); err != nil {
		return item, fmt.Errorf("physical model %q profile: %w", name, err)
	}
	if err := decodeJSON(limits, &item.Limits); err != nil {
		return item, fmt.Errorf("physical model %q limits: %w", name, err)
	}
	item.Discoverable, item.Enabled = discoverable != 0, enabled != 0
	item.AllowCompatibleSources, item.AllowDynamicSources = allowCompatible != 0, allowDynamic != 0
	item.Sources, err = s.physicalSources(name)
	return item, err
}

func (s *Store) physicalSources(name string) ([]RouteReference, error) {
	rows, err := s.DB.Query(`SELECT route_id,fidelity,evidence_json FROM physical_model_sources WHERE physical_name=? ORDER BY position`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []RouteReference
	for rows.Next() {
		var item RouteReference
		var evidence string
		if err := rows.Scan(&item.RouteID, &item.Fidelity, &evidence); err != nil {
			return nil, err
		}
		if err := decodeJSON(evidence, &item.Evidence); err != nil {
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
	if item.Policy.Ref == (extensions.Ref{}) {
		item.Policy.Ref = kernel.StrategyRef("ordered-fallback", 1)
	}
	if err := validateStrategySpec(item.Policy); err != nil {
		return fmt.Errorf("physical model %q source policy: %w", item.Name, err)
	}
	if item.Identity.CanonicalName == "" {
		item.Identity.CanonicalName = item.Name
	}
	identity, err := json.Marshal(item.Identity)
	if err != nil {
		return fmt.Errorf("encode physical model identity: %w", err)
	}
	reasoning, err := json.Marshal(item.Reasoning)
	if err != nil {
		return fmt.Errorf("encode physical model reasoning: %w", err)
	}
	policy, err := json.Marshal(item.Policy)
	if err != nil {
		return fmt.Errorf("encode physical model policy: %w", err)
	}
	caps, err := json.Marshal(item.Profile)
	if err != nil {
		return fmt.Errorf("encode physical model profile: %w", err)
	}
	limits, err := json.Marshal(item.Limits)
	if err != nil {
		return fmt.Errorf("encode physical model limits: %w", err)
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO physical_models(name,identity_json,reasoning_json,allow_compatible_sources,allow_dynamic_sources,policy_json,capabilities_json,limits_json,discoverable,enabled,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP)
ON CONFLICT(name) DO UPDATE SET identity_json=excluded.identity_json,reasoning_json=excluded.reasoning_json,allow_compatible_sources=excluded.allow_compatible_sources,allow_dynamic_sources=excluded.allow_dynamic_sources,policy_json=excluded.policy_json,capabilities_json=excluded.capabilities_json,limits_json=excluded.limits_json,discoverable=excluded.discoverable,enabled=excluded.enabled,updated_at=CURRENT_TIMESTAMP`, item.Name, string(identity), string(reasoning), boolInt(item.AllowCompatibleSources), boolInt(item.AllowDynamicSources), string(policy), string(caps), string(limits), boolInt(item.Discoverable), boolInt(item.Enabled)); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM physical_model_sources WHERE physical_name=?`, item.Name); err != nil {
		return err
	}
	for position, source := range item.Sources {
		if source.RouteID == "" {
			return fmt.Errorf("physical model source %d has empty route ID", position)
		}
		switch source.Fidelity {
		case "", kernel.FidelityExact, kernel.FidelityAlias, kernel.FidelityCompatible, kernel.FidelityDynamic, kernel.FidelityUnknown:
		default:
			return fmt.Errorf("physical model source %d has invalid fidelity %q", position, source.Fidelity)
		}
		if source.Fidelity == kernel.FidelityAlias && len(source.Evidence) == 0 {
			return fmt.Errorf("physical model source %d alias fidelity requires evidence", position)
		}
		var count int
		if err = tx.QueryRow(`SELECT count(*) FROM model_catalog WHERE id=? AND provider_node_id IS NOT NULL AND kind IN ('discovered','custom')`, source.RouteID).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			return fmt.Errorf("route %q is not a discovered or custom provider route", source.RouteID)
		}
		if source.Fidelity == "" {
			source.Fidelity = kernel.FidelityUnknown
		}
		evidence, marshalErr := json.Marshal(source.Evidence)
		if marshalErr != nil {
			return marshalErr
		}
		if _, err = tx.Exec(`INSERT INTO physical_model_sources(physical_name,position,route_id,fidelity,evidence_json) VALUES(?,?,?,?,?)`, item.Name, position, source.RouteID, source.Fidelity, string(evidence)); err != nil {
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
	rows, err := s.DB.Query(`SELECT name,reasoning_json,strategy_json,discoverable,enabled FROM combo_models WHERE enabled=1 ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ComboModel
	for rows.Next() {
		var item ComboModel
		var reasoning, strategy string
		var discoverable, enabled int
		if err := rows.Scan(&item.Name, &reasoning, &strategy, &discoverable, &enabled); err != nil {
			return nil, err
		}
		if err := decodeJSON(strategy, &item.Strategy); err != nil {
			return nil, fmt.Errorf("combo model %q strategy: %w", item.Name, err)
		}
		if err := decodeJSON(reasoning, &item.Reasoning); err != nil {
			return nil, fmt.Errorf("combo model %q reasoning: %w", item.Name, err)
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
	var reasoning, strategy string
	var discoverable, enabled int
	err := s.DB.QueryRow(`SELECT name,reasoning_json,strategy_json,discoverable,enabled FROM combo_models WHERE name=?`, name).Scan(&item.Name, &reasoning, &strategy, &discoverable, &enabled)
	if err != nil {
		return item, err
	}
	if err := decodeJSON(strategy, &item.Strategy); err != nil {
		return item, fmt.Errorf("combo model %q strategy: %w", name, err)
	}
	if err := decodeJSON(reasoning, &item.Reasoning); err != nil {
		return item, fmt.Errorf("combo model %q reasoning: %w", name, err)
	}
	item.Discoverable, item.Enabled = discoverable != 0, enabled != 0
	item.Members, err = s.comboMembers(name)
	return item, err
}

func (s *Store) comboMembers(name string) ([]ModelReference, error) {
	rows, err := s.DB.Query(`SELECT ref_kind,ref_id,options_json FROM combo_model_members WHERE combo_name=? ORDER BY position`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ModelReference
	for rows.Next() {
		var item ModelReference
		var options struct {
			Weight int `json:"weight,omitempty"`
		}
		var rawOptions string
		if err := rows.Scan(&item.Kind, &item.ID, &rawOptions); err != nil {
			return nil, err
		}
		if err := decodeJSON(rawOptions, &options); err != nil {
			return nil, fmt.Errorf("combo model %q member options: %w", name, err)
		}
		item.Weight = options.Weight
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) UpsertComboModel(item ComboModel) error {
	if item.Name == "" {
		return fmt.Errorf("combo model name is required")
	}
	if item.Strategy.Ref == (extensions.Ref{}) {
		item.Strategy.Ref = kernel.StrategyRef("ordered-fallback", 1)
	}
	if err := validateStrategySpec(item.Strategy); err != nil {
		return fmt.Errorf("combo model %q strategy: %w", item.Name, err)
	}
	strategy, err := json.Marshal(item.Strategy)
	if err != nil {
		return fmt.Errorf("encode combo strategy: %w", err)
	}
	reasoning, err := json.Marshal(item.Reasoning)
	if err != nil {
		return fmt.Errorf("encode combo reasoning: %w", err)
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO combo_models(name,reasoning_json,strategy_json,discoverable,enabled,updated_at) VALUES(?,?,?,?,?,CURRENT_TIMESTAMP)
ON CONFLICT(name) DO UPDATE SET reasoning_json=excluded.reasoning_json,strategy_json=excluded.strategy_json,discoverable=excluded.discoverable,enabled=excluded.enabled,updated_at=CURRENT_TIMESTAMP`, item.Name, string(reasoning), string(strategy), boolInt(item.Discoverable), boolInt(item.Enabled)); err != nil {
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
		if member.Weight < 0 {
			return fmt.Errorf("combo member %d has negative weight", position)
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
		options, err := json.Marshal(struct {
			Weight int `json:"weight,omitempty"`
		}{Weight: member.Weight})
		if err != nil {
			return fmt.Errorf("encode combo member %d options: %w", position, err)
		}
		if _, err = tx.Exec(`INSERT INTO combo_model_members(combo_name,position,ref_kind,ref_id,options_json) VALUES(?,?,?,?,?)`, item.Name, position, member.Kind, member.ID, string(options)); err != nil {
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

func validateStrategySpec(spec StrategySpec) error {
	if err := spec.Ref.Validate(); err != nil {
		return fmt.Errorf("strategy requires an exact versioned reference: %w", err)
	}
	if spec.Ref.Kind != kernel.StrategyExtensionKind {
		return fmt.Errorf("strategy ref kind must be %q", kernel.StrategyExtensionKind)
	}
	return nil
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
