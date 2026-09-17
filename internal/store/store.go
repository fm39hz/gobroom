package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/quota"
	_ "modernc.org/sqlite"
)

type Store struct{ DB *sql.DB }

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}
	s := &Store{DB: db}
	if err := s.Migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) Migrate() error {
	_, err := s.DB.Exec(`
CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY);
CREATE TABLE IF NOT EXISTS provider_nodes (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, base_url TEXT NOT NULL,
  protocol TEXT NOT NULL, prefix TEXT NOT NULL DEFAULT '', models_path TEXT NOT NULL DEFAULT '/models',
  auth_mode TEXT NOT NULL DEFAULT 'api_key', enabled INTEGER NOT NULL DEFAULT 1,
  config_json TEXT NOT NULL DEFAULT '{}', created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS connections (
  id TEXT PRIMARY KEY, provider_node_id TEXT NOT NULL REFERENCES provider_nodes(id) ON DELETE CASCADE,
  name TEXT NOT NULL, credential_type TEXT NOT NULL, secret_ref TEXT NOT NULL DEFAULT '',
  priority INTEGER NOT NULL DEFAULT 100, enabled INTEGER NOT NULL DEFAULT 1,
  state_json TEXT NOT NULL DEFAULT '{}', created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS model_catalog (
  id TEXT PRIMARY KEY, provider_node_id TEXT REFERENCES provider_nodes(id) ON DELETE CASCADE,
  kind TEXT NOT NULL, external_id TEXT NOT NULL DEFAULT '', display_name TEXT NOT NULL,
  capabilities_json TEXT NOT NULL DEFAULT '{}', overrides_json TEXT NOT NULL DEFAULT '{}',
  raw_json TEXT NOT NULL DEFAULT '{}', enabled INTEGER NOT NULL DEFAULT 1,
  last_seen_at TEXT, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_model_catalog_node ON model_catalog(provider_node_id);
CREATE TABLE IF NOT EXISTS logical_models (
  name TEXT PRIMARY KEY, target_ref TEXT NOT NULL, kind TEXT NOT NULL DEFAULT 'alias',
  options_json TEXT NOT NULL DEFAULT '{}', enabled INTEGER NOT NULL DEFAULT 1,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS combos (
  name TEXT PRIMARY KEY, strategy TEXT NOT NULL DEFAULT 'fallback',
  sticky_limit INTEGER NOT NULL DEFAULT 1, options_json TEXT NOT NULL DEFAULT '{}',
  enabled INTEGER NOT NULL DEFAULT 1, updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS combo_members (
  combo_name TEXT NOT NULL REFERENCES combos(name) ON DELETE CASCADE,
  position INTEGER NOT NULL, target_ref TEXT NOT NULL, options_json TEXT NOT NULL DEFAULT '{}',
  PRIMARY KEY (combo_name, position)
);
CREATE TABLE IF NOT EXISTS published_models (
  name TEXT PRIMARY KEY, target_ref TEXT NOT NULL,
  owned_by TEXT NOT NULL DEFAULT 'gobroom',
  metadata_json TEXT NOT NULL DEFAULT '{}', enabled INTEGER NOT NULL DEFAULT 1,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS usage_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT, timestamp TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  logical_model TEXT, provider_node_id TEXT, external_model TEXT, connection_id TEXT,
  status TEXT, latency_ms INTEGER NOT NULL DEFAULT 0, input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_usage_events_timestamp ON usage_events(timestamp);
CREATE TABLE IF NOT EXISTS quota_snapshots (
  id TEXT PRIMARY KEY, provider_node_id TEXT NOT NULL, connection_id TEXT,
  model_ref TEXT, window_name TEXT NOT NULL, used REAL NOT NULL DEFAULT 0,
  limit_value REAL, remaining REAL, reset_at TEXT, source TEXT NOT NULL DEFAULT 'provider',
  metadata_json TEXT NOT NULL DEFAULT '{}', updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_quota_lookup ON quota_snapshots(provider_node_id,connection_id,model_ref,window_name);
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value_json TEXT NOT NULL);
`)
	// Additive migration for databases created before prefix became a first-class field.
	_, _ = s.DB.Exec(`ALTER TABLE provider_nodes ADD COLUMN prefix TEXT NOT NULL DEFAULT ''`)
	return err
}

type ProviderNode struct {
	ID, Name, BaseURL, Protocol, Prefix, ModelsPath, AuthMode string
	Enabled                                                   bool
}

func (s *Store) ProviderNodes() ([]ProviderNode, error) {
	rows, err := s.DB.Query(`SELECT id,name,base_url,protocol,prefix,models_path,auth_mode,enabled FROM provider_nodes ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ProviderNode
	for rows.Next() {
		var n ProviderNode
		var enabled int
		if err := rows.Scan(&n.ID, &n.Name, &n.BaseURL, &n.Protocol, &n.Prefix, &n.ModelsPath, &n.AuthMode, &enabled); err != nil {
			return nil, err
		}
		n.Enabled = enabled == 1
		result = append(result, n)
	}
	return result, rows.Err()
}

func (s *Store) ProviderNode(id string) (ProviderNode, error) {
	row := s.DB.QueryRow(`SELECT id,name,base_url,protocol,prefix,models_path,auth_mode,enabled FROM provider_nodes WHERE id=?`, id)
	var n ProviderNode
	var enabled int
	if err := row.Scan(&n.ID, &n.Name, &n.BaseURL, &n.Protocol, &n.Prefix, &n.ModelsPath, &n.AuthMode, &enabled); err != nil {
		return n, err
	}
	n.Enabled = enabled == 1
	return n, nil
}

type Credential struct{ Type, Secret string }

func (s *Store) ConnectionCredential(nodeID string) (Credential, bool) {
	row := s.DB.QueryRow(`SELECT credential_type,secret_ref FROM connections WHERE provider_node_id=? AND enabled=1 ORDER BY priority,id LIMIT 1`)
	var c Credential
	if err := row.Scan(&c.Type, &c.Secret); err != nil {
		return Credential{}, false
	}
	return c, true
}

func (s *Store) SaveUsageEvent(event kernel.UsageEvent) error {
	_, err := s.DB.Exec(`INSERT INTO usage_events(timestamp,logical_model,provider_node_id,external_model,connection_id,status,latency_ms,input_tokens,output_tokens) VALUES(?,?,?,?,?,?,?,?,?)`, event.At.UTC().Format(time.RFC3339Nano), event.LogicalModel, event.ProviderNodeID, event.ExternalModel, event.ConnectionID, event.Status, event.Latency.Milliseconds(), event.InputTokens, event.OutputTokens)
	return err
}

func (s *Store) SaveQuotaSnapshot(snapshot quota.Snapshot) error {
	metadata := "{}"
	_, err := s.DB.Exec(`INSERT INTO quota_snapshots(id,provider_node_id,connection_id,model_ref,window_name,used,limit_value,remaining,reset_at,source,metadata_json,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP) ON CONFLICT(id) DO UPDATE SET used=excluded.used,limit_value=excluded.limit_value,remaining=excluded.remaining,reset_at=excluded.reset_at,source=excluded.source,updated_at=CURRENT_TIMESTAMP`, quotaID(snapshot), snapshot.ProviderNodeID, snapshot.ConnectionID, snapshot.ModelRef, snapshot.WindowName, snapshot.Used, snapshot.Limit, snapshot.Remaining, timeString(snapshot.ResetAt), snapshot.Source, metadata)
	return err
}

func (s *Store) QuotaSnapshots() ([]quota.Snapshot, error) {
	rows, err := s.DB.Query(`SELECT provider_node_id,COALESCE(connection_id,''),COALESCE(model_ref,''),window_name,used,limit_value,remaining,reset_at,source FROM quota_snapshots`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []quota.Snapshot
	for rows.Next() {
		var p quota.Snapshot
		var limit, remaining sql.NullFloat64
		var reset, source string
		if err := rows.Scan(&p.ProviderNodeID, &p.ConnectionID, &p.ModelRef, &p.WindowName, &p.Used, &limit, &remaining, &reset, &source); err != nil {
			return nil, err
		}
		if limit.Valid {
			v := limit.Float64
			p.Limit = &v
		}
		if remaining.Valid {
			v := remaining.Float64
			p.Remaining = &v
		}
		if reset != "" {
			if t, e := time.Parse(time.RFC3339, reset); e == nil {
				p.ResetAt = &t
			}
		}
		p.Source = source
		result = append(result, p)
	}
	return result, rows.Err()
}

func quotaID(s quota.Snapshot) string {
	return s.ProviderNodeID + "|" + s.ConnectionID + "|" + s.ModelRef + "|" + s.WindowName
}
func timeString(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Format(time.RFC3339)
}

type CreateProviderNodeInput struct {
	Name, Prefix, BaseURL, Protocol, ModelsPath, AuthMode string
}

func (s *Store) CreateProviderNode(input CreateProviderNodeInput) (ProviderNode, error) {
	if input.Name == "" || input.Prefix == "" || input.BaseURL == "" || input.Protocol == "" {
		return ProviderNode{}, fmt.Errorf("name, prefix, base URL and protocol are required")
	}
	if input.ModelsPath == "" {
		input.ModelsPath = "/models"
	}
	if input.AuthMode == "" {
		input.AuthMode = "api_key"
	}
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return ProviderNode{}, err
	}
	node := ProviderNode{ID: "node_" + fmt.Sprintf("%x", buf), Name: input.Name, Prefix: input.Prefix, BaseURL: input.BaseURL, Protocol: input.Protocol, ModelsPath: input.ModelsPath, AuthMode: input.AuthMode, Enabled: true}
	_, err := s.DB.Exec(`INSERT INTO provider_nodes(id,name,base_url,protocol,prefix,models_path,auth_mode,enabled) VALUES(?,?,?,?,?,?,?,1)`, node.ID, node.Name, node.BaseURL, node.Protocol, node.Prefix, node.ModelsPath, node.AuthMode)
	return node, err
}

type Model struct {
	ID, NodeID, Kind, ExternalID, DisplayName string
	Capabilities                              map[string]bool
}

func (s *Store) Models() ([]Model, error) {
	rows, err := s.DB.Query(`SELECT id,COALESCE(provider_node_id,''),kind,external_id,display_name,capabilities_json FROM model_catalog WHERE enabled=1 ORDER BY display_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Model
	for rows.Next() {
		var m Model
		var caps string
		if err := rows.Scan(&m.ID, &m.NodeID, &m.Kind, &m.ExternalID, &m.DisplayName, &caps); err != nil {
			return nil, err
		}
		m.Capabilities = map[string]bool{}
		_ = json.Unmarshal([]byte(caps), &m.Capabilities)
		result = append(result, m)
	}
	return result, rows.Err()
}

func (s *Store) Combos() ([]map[string]any, error) {
	rows, err := s.DB.Query(`SELECT name,strategy,sticky_limit FROM combos WHERE enabled=1 ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []map[string]any
	for rows.Next() {
		var name, strategy string
		var sticky int
		if err := rows.Scan(&name, &strategy, &sticky); err != nil {
			return nil, err
		}
		result = append(result, map[string]any{"name": name, "strategy": strategy, "stickyLimit": sticky})
	}
	return result, rows.Err()
}

type PublicModel struct {
	Name, TargetRef, OwnedBy string
	Metadata                 map[string]any
}

// PublicModels is separate from Models and Combos. Internal helper combos are
// hidden unless explicitly published here.
func (s *Store) PublicModels() ([]PublicModel, error) {
	rows, err := s.DB.Query(`SELECT name,target_ref,owned_by,metadata_json FROM published_models WHERE enabled=1 ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []PublicModel
	for rows.Next() {
		var m PublicModel
		var metadata string
		if err := rows.Scan(&m.Name, &m.TargetRef, &m.OwnedBy, &metadata); err != nil {
			return nil, err
		}
		m.Metadata = map[string]any{}
		_ = json.Unmarshal([]byte(metadata), &m.Metadata)
		result = append(result, m)
	}
	return result, rows.Err()
}

func (s *Store) UpsertPublicModel(m PublicModel) error {
	metadata, err := json.Marshal(m.Metadata)
	if err != nil {
		return err
	}
	if m.OwnedBy == "" {
		m.OwnedBy = "gobroom"
	}
	_, err = s.DB.Exec(`INSERT INTO published_models(name,target_ref,owned_by,metadata_json,enabled,updated_at)
VALUES(?,?,?,?,1,CURRENT_TIMESTAMP)
ON CONFLICT(name) DO UPDATE SET target_ref=excluded.target_ref, owned_by=excluded.owned_by,
metadata_json=excluded.metadata_json, enabled=1, updated_at=CURRENT_TIMESTAMP`,
		m.Name, m.TargetRef, m.OwnedBy, string(metadata))
	return err
}

func (s *Store) DeletePublicModel(name string) error {
	_, err := s.DB.Exec(`DELETE FROM published_models WHERE name=?`, name)
	return err
}

type RouteRecord struct {
	ID, NodeID, Prefix, ExternalModel, Protocol                       string
	BaseURL, AuthMode, CredentialID, CredentialType, CredentialSecret string
	Enabled                                                           bool
}

func (s *Store) Routes() ([]RouteRecord, error) {
	rows, err := s.DB.Query(`SELECT m.id,COALESCE(m.provider_node_id,''),COALESCE(n.prefix,''),COALESCE(n.protocol,''),COALESCE(n.base_url,''),COALESCE(n.auth_mode,''),m.external_id,m.enabled,
COALESCE((SELECT id FROM connections c WHERE c.provider_node_id=m.provider_node_id AND c.enabled=1 ORDER BY c.priority,c.id LIMIT 1),''),
COALESCE((SELECT credential_type FROM connections c WHERE c.provider_node_id=m.provider_node_id AND c.enabled=1 ORDER BY c.priority,c.id LIMIT 1),''),
COALESCE((SELECT secret_ref FROM connections c WHERE c.provider_node_id=m.provider_node_id AND c.enabled=1 ORDER BY c.priority,c.id LIMIT 1),'')
FROM model_catalog m LEFT JOIN provider_nodes n ON n.id=m.provider_node_id
WHERE m.enabled=1 ORDER BY m.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []RouteRecord
	for rows.Next() {
		var r RouteRecord
		var enabled int
		if err := rows.Scan(&r.ID, &r.NodeID, &r.Prefix, &r.Protocol, &r.BaseURL, &r.AuthMode, &r.ExternalModel, &enabled, &r.CredentialID, &r.CredentialType, &r.CredentialSecret); err != nil {
			return nil, err
		}
		if r.Protocol == "" {
			r.Protocol = "chat"
		}
		r.Enabled = enabled == 1
		result = append(result, r)
	}
	return result, rows.Err()
}

type LogicalModelRecord struct{ Name, TargetRef string }

func (s *Store) LogicalModels() ([]LogicalModelRecord, error) {
	rows, err := s.DB.Query(`SELECT name,target_ref FROM logical_models WHERE enabled=1 ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []LogicalModelRecord
	for rows.Next() {
		var item LogicalModelRecord
		if err := rows.Scan(&item.Name, &item.TargetRef); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

type ComboRecord struct {
	Name, Strategy string
	StickyLimit    int
	Members        []string
}

func (s *Store) ComboDetails() ([]ComboRecord, error) {
	rows, err := s.DB.Query(`SELECT name,strategy,sticky_limit FROM combos WHERE enabled=1 ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ComboRecord
	for rows.Next() {
		var item ComboRecord
		if err := rows.Scan(&item.Name, &item.Strategy, &item.StickyLimit); err != nil {
			return nil, err
		}
		members, err := s.DB.Query(`SELECT target_ref FROM combo_members WHERE combo_name=? ORDER BY position`, item.Name)
		if err != nil {
			return nil, err
		}
		for members.Next() {
			var ref string
			if err := members.Scan(&ref); err != nil {
				members.Close()
				return nil, err
			}
			item.Members = append(item.Members, ref)
		}
		members.Close()
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) UpsertLogicalModel(name, targetRef string) error {
	if name == "" || targetRef == "" {
		return fmt.Errorf("name and target reference are required")
	}
	_, err := s.DB.Exec(`INSERT INTO logical_models(name,target_ref,kind,enabled,updated_at) VALUES(?,?, 'alias',1,CURRENT_TIMESTAMP)
ON CONFLICT(name) DO UPDATE SET target_ref=excluded.target_ref, enabled=1, updated_at=CURRENT_TIMESTAMP`, name, targetRef)
	return err
}

func (s *Store) DeleteLogicalModel(name string) error {
	_, err := s.DB.Exec(`DELETE FROM logical_models WHERE name=?`, name)
	return err
}

type UpsertCatalogModelInput struct {
	ID, ProviderNodeID, Kind, ExternalID, DisplayName string
	Capabilities                                      map[string]bool
	Overrides                                         map[string]any
	Raw                                               map[string]any
}

func (s *Store) UpsertCatalogModel(input UpsertCatalogModelInput) error {
	if input.ID == "" || input.DisplayName == "" {
		return fmt.Errorf("model ID and display name are required")
	}
	if input.Kind == "" {
		input.Kind = "custom"
	}
	caps, _ := json.Marshal(input.Capabilities)
	overrides, _ := json.Marshal(input.Overrides)
	raw, _ := json.Marshal(input.Raw)
	_, err := s.DB.Exec(`INSERT INTO model_catalog(id,provider_node_id,kind,external_id,display_name,capabilities_json,overrides_json,raw_json,enabled,last_seen_at,updated_at)
VALUES(?,?,?,?,?,?,?,?,1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)
ON CONFLICT(id) DO UPDATE SET provider_node_id=excluded.provider_node_id,kind=excluded.kind,external_id=excluded.external_id,
display_name=excluded.display_name,capabilities_json=excluded.capabilities_json,overrides_json=excluded.overrides_json,
raw_json=excluded.raw_json,enabled=1,updated_at=CURRENT_TIMESTAMP`, input.ID, input.ProviderNodeID, input.Kind, input.ExternalID, input.DisplayName, string(caps), string(overrides), string(raw))
	return err
}

func (s *Store) DeleteCatalogModel(id string) error {
	_, err := s.DB.Exec(`DELETE FROM model_catalog WHERE id=?`, id)
	return err
}

func (s *Store) UpsertCombo(record ComboRecord) error {
	if record.Name == "" {
		return fmt.Errorf("combo name is required")
	}
	if record.Strategy == "" {
		record.Strategy = "fallback"
	}
	if record.StickyLimit < 1 {
		record.StickyLimit = 1
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO combos(name,strategy,sticky_limit,enabled,updated_at) VALUES(?,?,?,1,CURRENT_TIMESTAMP)
ON CONFLICT(name) DO UPDATE SET strategy=excluded.strategy,sticky_limit=excluded.sticky_limit,enabled=1,updated_at=CURRENT_TIMESTAMP`, record.Name, record.Strategy, record.StickyLimit); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM combo_members WHERE combo_name=?`, record.Name); err != nil {
		return err
	}
	for i, ref := range record.Members {
		if ref == "" {
			return fmt.Errorf("combo member %d is empty", i)
		}
		if _, err = tx.Exec(`INSERT INTO combo_members(combo_name,position,target_ref) VALUES(?,?,?)`, record.Name, i, ref); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteCombo(name string) error {
	_, err := s.DB.Exec(`DELETE FROM combos WHERE name=?`, name)
	return err
}

func stringValue(value any) string { result, _ := value.(string); return result }
