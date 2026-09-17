package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

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
  protocol TEXT NOT NULL, models_path TEXT NOT NULL DEFAULT '/models',
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
  owned_by TEXT NOT NULL DEFAULT 'gorouter',
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
	return err
}

type ProviderNode struct {
	ID, Name, BaseURL, Protocol, ModelsPath, AuthMode string
	Enabled                                           bool
}

func (s *Store) ProviderNodes() ([]ProviderNode, error) {
	rows, err := s.DB.Query(`SELECT id,name,base_url,protocol,models_path,auth_mode,enabled FROM provider_nodes ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ProviderNode
	for rows.Next() {
		var n ProviderNode
		var enabled int
		if err := rows.Scan(&n.ID, &n.Name, &n.BaseURL, &n.Protocol, &n.ModelsPath, &n.AuthMode, &enabled); err != nil {
			return nil, err
		}
		n.Enabled = enabled == 1
		result = append(result, n)
	}
	return result, rows.Err()
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
		m.OwnedBy = "gorouter"
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
