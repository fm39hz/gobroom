package store

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	db, err := sql.Open("sqlite", withConnectionPragmas(path))
	if err != nil {
		return nil, err
	}
	s := &Store{DB: db}
	if err := s.Migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// withConnectionPragmas installs settings in the driver's DSN so every
// physical connection opened by database/sql receives them. Executing PRAGMA
// statements once through *sql.DB configures only one pooled connection.
func withConnectionPragmas(path string) string {
	dsn := path
	if dsn == ":memory:" {
		dsn = "file::memory:?cache=shared"
	}
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	return dsn + separator + "_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)"
}

func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) Migrate() error {
	_, err := s.DB.Exec(`
CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY);
CREATE TABLE IF NOT EXISTS provider_nodes (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, base_url TEXT NOT NULL,
  protocol TEXT NOT NULL, definition_id TEXT NOT NULL DEFAULT '', prefix TEXT NOT NULL DEFAULT '', models_path TEXT NOT NULL DEFAULT '/models',
  auth_mode TEXT NOT NULL DEFAULT 'api_key', enabled INTEGER NOT NULL DEFAULT 1,
  config_json TEXT NOT NULL DEFAULT '{}', created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS connections (
  id TEXT PRIMARY KEY, provider_node_id TEXT NOT NULL REFERENCES provider_nodes(id) ON DELETE CASCADE,
  name TEXT NOT NULL, email TEXT NOT NULL DEFAULT '', credential_type TEXT NOT NULL, secret_ref TEXT NOT NULL DEFAULT '',
  priority INTEGER NOT NULL DEFAULT 100, enabled INTEGER NOT NULL DEFAULT 1,
  state_json TEXT NOT NULL DEFAULT '{}', created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS model_catalog (
  id TEXT PRIMARY KEY, provider_node_id TEXT REFERENCES provider_nodes(id) ON DELETE CASCADE,
  kind TEXT NOT NULL, external_id TEXT NOT NULL DEFAULT '', display_name TEXT NOT NULL,
  capabilities_json TEXT NOT NULL DEFAULT '{}', limits_json TEXT NOT NULL DEFAULT '{}', overrides_json TEXT NOT NULL DEFAULT '{}',
  raw_json TEXT NOT NULL DEFAULT '{}', enabled INTEGER NOT NULL DEFAULT 1,
  last_seen_at TEXT, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_model_catalog_node ON model_catalog(provider_node_id);
CREATE TABLE IF NOT EXISTS custom_model_connections (
  model_id TEXT NOT NULL REFERENCES model_catalog(id) ON DELETE CASCADE,
  connection_id TEXT NOT NULL REFERENCES connections(id) ON DELETE CASCADE,
  PRIMARY KEY(model_id,connection_id)
);
CREATE INDEX IF NOT EXISTS idx_custom_model_connections_connection ON custom_model_connections(connection_id,model_id);
CREATE INDEX IF NOT EXISTS idx_custom_model_identity ON model_catalog(provider_node_id,external_id) WHERE kind='custom' AND external_id<>'';
CREATE TABLE IF NOT EXISTS usage_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT, timestamp TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  logical_model TEXT, provider_node_id TEXT, external_model TEXT, connection_id TEXT,
  status TEXT, latency_ms INTEGER NOT NULL DEFAULT 0, input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0, estimated_cost REAL NOT NULL DEFAULT 0,
  compatibility_fidelity TEXT NOT NULL DEFAULT '', compatibility_losses_json TEXT NOT NULL DEFAULT '[]',
  compatibility_plan_json TEXT NOT NULL DEFAULT 'null'
);
CREATE INDEX IF NOT EXISTS idx_usage_events_timestamp ON usage_events(timestamp);
CREATE TABLE IF NOT EXISTS quota_snapshots (
  id TEXT PRIMARY KEY, provider_node_id TEXT NOT NULL, connection_id TEXT,
  model_ref TEXT, window_name TEXT NOT NULL, used REAL NOT NULL DEFAULT 0,
  limit_value REAL, remaining REAL, reset_at TEXT, source TEXT NOT NULL DEFAULT 'provider',
  metadata_json TEXT NOT NULL DEFAULT '{}', updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_quota_lookup ON quota_snapshots(provider_node_id,connection_id,model_ref,window_name);
CREATE TABLE IF NOT EXISTS connection_runtime (
  connection_id TEXT PRIMARY KEY REFERENCES connections(id) ON DELETE CASCADE,
  status TEXT NOT NULL DEFAULT 'active', consecutive_failures INTEGER NOT NULL DEFAULT 0,
  cooldown_until TEXT, rate_limited_until TEXT, backoff_level INTEGER NOT NULL DEFAULT 0,
  last_error TEXT, error_code TEXT, last_used_at TEXT, consecutive_use_count INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS model_availability (
  connection_id TEXT NOT NULL REFERENCES connections(id) ON DELETE CASCADE,
  model_ref TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'available',
  blocked_until TEXT, reason TEXT, consecutive_failures INTEGER NOT NULL DEFAULT 0,
  backoff_level INTEGER NOT NULL DEFAULT 0, last_error TEXT, error_code TEXT,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(connection_id, model_ref)
);
CREATE INDEX IF NOT EXISTS idx_model_availability_until ON model_availability(blocked_until);
CREATE TABLE IF NOT EXISTS connection_model_catalog (
  connection_id TEXT NOT NULL REFERENCES connections(id) ON DELETE CASCADE,
  provider_node_id TEXT NOT NULL REFERENCES provider_nodes(id) ON DELETE CASCADE,
  external_model_id TEXT NOT NULL,
  status TEXT NOT NULL CHECK(status IN ('available','not_listed')),
  source TEXT NOT NULL,
  observed_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(connection_id,external_model_id)
);
CREATE INDEX IF NOT EXISTS idx_connection_model_catalog_provider ON connection_model_catalog(provider_node_id,external_model_id,status);
CREATE TABLE IF NOT EXISTS proxy_pools (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, kind TEXT NOT NULL DEFAULT 'http',
  endpoint TEXT NOT NULL DEFAULT '', no_proxy TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1, health TEXT NOT NULL DEFAULT 'unknown',
  config_json TEXT NOT NULL DEFAULT '{}', updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS connection_transports (
  connection_id TEXT PRIMARY KEY REFERENCES connections(id) ON DELETE CASCADE,
  proxy_pool_id TEXT REFERENCES proxy_pools(id) ON DELETE SET NULL,
  proxy_url TEXT NOT NULL DEFAULT '', no_proxy TEXT NOT NULL DEFAULT '',
  relay_url TEXT NOT NULL DEFAULT '', config_json TEXT NOT NULL DEFAULT '{}',
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS provider_extensions (
  connection_id TEXT NOT NULL REFERENCES connections(id) ON DELETE CASCADE,
  namespace TEXT NOT NULL, data_json TEXT NOT NULL DEFAULT '{}',
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(connection_id, namespace)
);
CREATE TABLE IF NOT EXISTS provider_sessions (
  connection_id TEXT NOT NULL REFERENCES connections(id) ON DELETE CASCADE,
  namespace TEXT NOT NULL, session_key TEXT NOT NULL, state_json TEXT NOT NULL DEFAULT '{}',
  expires_at TEXT, last_used_at TEXT, updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(connection_id, namespace, session_key)
);
CREATE INDEX IF NOT EXISTS idx_provider_sessions_expiry ON provider_sessions(expires_at);
CREATE TABLE IF NOT EXISTS transform_bindings (
  id TEXT PRIMARY KEY,
  transform_kind TEXT NOT NULL,
  transform_id TEXT NOT NULL,
  contract_version INTEGER NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 0,
  scope_kind TEXT NOT NULL,
  scope_id TEXT NOT NULL DEFAULT '',
  ordering INTEGER NOT NULL DEFAULT 0,
  failure_mode TEXT NOT NULL DEFAULT 'fail_closed',
  options_json TEXT NOT NULL DEFAULT 'null',
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_transform_bindings_ref ON transform_bindings(transform_kind,transform_id,contract_version,enabled);
CREATE TABLE IF NOT EXISTS usage_daily (
  date_key TEXT PRIMARY KEY, requests INTEGER NOT NULL DEFAULT 0,
  prompt_tokens INTEGER NOT NULL DEFAULT 0, completion_tokens INTEGER NOT NULL DEFAULT 0,
  estimated_cost REAL NOT NULL DEFAULT 0, updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value_json TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS provider_definitions (
  id TEXT NOT NULL, contract_version INTEGER NOT NULL,
  definition_json TEXT NOT NULL, updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(id,contract_version)
);
-- Typed model graph: discovered routes, physical identities and role combos.
CREATE TABLE IF NOT EXISTS physical_models (
  name TEXT PRIMARY KEY, identity_json TEXT NOT NULL DEFAULT '{}', reasoning_json TEXT NOT NULL DEFAULT '{}', loss_policy_json TEXT NOT NULL DEFAULT '{}', allow_compatible_sources INTEGER NOT NULL DEFAULT 0, allow_dynamic_sources INTEGER NOT NULL DEFAULT 0, policy_json TEXT NOT NULL DEFAULT '{"ref":{"kind":"strategy","id":"ordered-fallback","contractVersion":1},"config":{}}',
  capabilities_json TEXT NOT NULL DEFAULT '{}', limits_json TEXT NOT NULL DEFAULT '{}', discoverable INTEGER NOT NULL DEFAULT 0,
  enabled INTEGER NOT NULL DEFAULT 1, updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS physical_model_sources (
  physical_name TEXT NOT NULL REFERENCES physical_models(name) ON DELETE CASCADE,
  position INTEGER NOT NULL, route_id TEXT NOT NULL REFERENCES model_catalog(id) ON DELETE RESTRICT,
  fidelity TEXT NOT NULL DEFAULT 'unknown', evidence_json TEXT NOT NULL DEFAULT '[]',
  PRIMARY KEY (physical_name, position), UNIQUE (physical_name, route_id)
);
CREATE INDEX IF NOT EXISTS idx_physical_sources_route ON physical_model_sources(route_id);
CREATE TABLE IF NOT EXISTS combo_models (
  name TEXT PRIMARY KEY, reasoning_json TEXT NOT NULL DEFAULT '{}', loss_policy_json TEXT NOT NULL DEFAULT '{}', strategy_json TEXT NOT NULL DEFAULT '{"ref":{"kind":"strategy","id":"ordered-fallback","contractVersion":1},"config":{}}',
  discoverable INTEGER NOT NULL DEFAULT 0, enabled INTEGER NOT NULL DEFAULT 1,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS combo_model_members (
  combo_name TEXT NOT NULL REFERENCES combo_models(name) ON DELETE CASCADE,
  position INTEGER NOT NULL, ref_kind TEXT NOT NULL CHECK(ref_kind IN ('physical','combo')),
  ref_id TEXT NOT NULL, options_json TEXT NOT NULL DEFAULT '{}',
  PRIMARY KEY (combo_name, position)
);
CREATE INDEX IF NOT EXISTS idx_combo_members_reference ON combo_model_members(ref_kind, ref_id);
`)
	// Additive migration for databases created before prefix became a first-class field.
	_, _ = s.DB.Exec(`ALTER TABLE provider_nodes ADD COLUMN prefix TEXT NOT NULL DEFAULT ''`)
	_, _ = s.DB.Exec(`ALTER TABLE provider_nodes ADD COLUMN definition_id TEXT NOT NULL DEFAULT ''`)
	_, _ = s.DB.Exec(`ALTER TABLE connections ADD COLUMN email TEXT NOT NULL DEFAULT ''`)
	_, _ = s.DB.Exec(`ALTER TABLE usage_events ADD COLUMN estimated_cost REAL NOT NULL DEFAULT 0`)
	_, _ = s.DB.Exec(`ALTER TABLE usage_events ADD COLUMN request_class TEXT NOT NULL DEFAULT ''`)
	_, _ = s.DB.Exec(`ALTER TABLE usage_events ADD COLUMN session_id TEXT NOT NULL DEFAULT ''`)
	_, _ = s.DB.Exec(`ALTER TABLE usage_events ADD COLUMN ttft_ms INTEGER NOT NULL DEFAULT 0`)
	_, _ = s.DB.Exec(`ALTER TABLE usage_events ADD COLUMN output_tokens_per_second REAL NOT NULL DEFAULT 0`)
	_, _ = s.DB.Exec(`ALTER TABLE usage_events ADD COLUMN compatibility_fidelity TEXT NOT NULL DEFAULT ''`)
	_, _ = s.DB.Exec(`ALTER TABLE usage_events ADD COLUMN compatibility_losses_json TEXT NOT NULL DEFAULT '[]'`)
	_, _ = s.DB.Exec(`ALTER TABLE usage_events ADD COLUMN compatibility_plan_json TEXT NOT NULL DEFAULT 'null'`)
	_, _ = s.DB.Exec(`ALTER TABLE transform_bindings ADD COLUMN failure_mode TEXT NOT NULL DEFAULT 'fail_closed'`)
	_, _ = s.DB.Exec(`ALTER TABLE model_catalog ADD COLUMN limits_json TEXT NOT NULL DEFAULT '{}'`)
	_, _ = s.DB.Exec(`ALTER TABLE physical_models ADD COLUMN limits_json TEXT NOT NULL DEFAULT '{}'`)
	_, _ = s.DB.Exec(`ALTER TABLE physical_models ADD COLUMN identity_json TEXT NOT NULL DEFAULT '{}'`)
	_, _ = s.DB.Exec(`ALTER TABLE physical_model_sources ADD COLUMN fidelity TEXT NOT NULL DEFAULT 'unknown'`)
	_, _ = s.DB.Exec(`ALTER TABLE physical_model_sources ADD COLUMN evidence_json TEXT NOT NULL DEFAULT '[]'`)
	_, _ = s.DB.Exec(`ALTER TABLE physical_models ADD COLUMN reasoning_json TEXT NOT NULL DEFAULT '{}'`)
	_, _ = s.DB.Exec(`ALTER TABLE combo_models ADD COLUMN reasoning_json TEXT NOT NULL DEFAULT '{}'`)
	_, _ = s.DB.Exec(`ALTER TABLE physical_models ADD COLUMN loss_policy_json TEXT NOT NULL DEFAULT '{}'`)
	_, _ = s.DB.Exec(`ALTER TABLE combo_models ADD COLUMN loss_policy_json TEXT NOT NULL DEFAULT '{}'`)
	_, _ = s.DB.Exec(`ALTER TABLE physical_models ADD COLUMN allow_compatible_sources INTEGER NOT NULL DEFAULT 0`)
	_, _ = s.DB.Exec(`ALTER TABLE physical_models ADD COLUMN allow_dynamic_sources INTEGER NOT NULL DEFAULT 0`)
	_, _ = s.DB.Exec(`UPDATE provider_nodes SET definition_id=CASE protocol WHEN 'openai_chat' THEN 'openai-compatible-chat' WHEN 'chat' THEN 'openai-compatible-chat' WHEN 'openai_responses' THEN 'openai-compatible-responses' WHEN 'responses' THEN 'openai-compatible-responses' WHEN 'anthropic' THEN 'anthropic-messages' ELSE definition_id END WHERE definition_id=''`)
	return err
}

type ProviderNode struct {
	ID, Name, BaseURL, Protocol, DefinitionID, Prefix, ModelsPath, AuthMode string
	Enabled                                                                 bool
}

type ConnectionRecord struct {
	ID, ProviderNodeID, Name, Email, CredentialType string
	Priority                                        int
	Enabled                                         bool
}

type CreateConnectionInput struct {
	ProviderNodeID, Name, CredentialType, Secret string
	Priority                                     int
}

type UpdateConnectionInput struct {
	ID, Name, CredentialType, Secret string
	Priority                         int
	Enabled                          *bool
}

func (s *Store) ProviderNodes() ([]ProviderNode, error) {
	rows, err := s.DB.Query(`SELECT id,name,base_url,protocol,definition_id,prefix,models_path,auth_mode,enabled FROM provider_nodes ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ProviderNode
	for rows.Next() {
		var n ProviderNode
		var enabled int
		if err := rows.Scan(&n.ID, &n.Name, &n.BaseURL, &n.Protocol, &n.DefinitionID, &n.Prefix, &n.ModelsPath, &n.AuthMode, &enabled); err != nil {
			return nil, err
		}
		n.Enabled = enabled == 1
		result = append(result, n)
	}
	return result, rows.Err()
}

func (s *Store) ProviderNode(id string) (ProviderNode, error) {
	row := s.DB.QueryRow(`SELECT id,name,base_url,protocol,definition_id,prefix,models_path,auth_mode,enabled FROM provider_nodes WHERE id=?`, id)
	var n ProviderNode
	var enabled int
	if err := row.Scan(&n.ID, &n.Name, &n.BaseURL, &n.Protocol, &n.DefinitionID, &n.Prefix, &n.ModelsPath, &n.AuthMode, &enabled); err != nil {
		return n, err
	}
	n.Enabled = enabled == 1
	return n, nil
}

// ProviderNodeByConnection resolves the owning provider without returning the
// connection's secret material.
func (s *Store) ProviderNodeByConnection(connectionID string) (ProviderNode, error) {
	var nodeID string
	if err := s.DB.QueryRow(`SELECT provider_node_id FROM connections WHERE id=?`, connectionID).Scan(&nodeID); err != nil {
		return ProviderNode{}, err
	}
	return s.ProviderNode(nodeID)
}

func (s *Store) DeleteProviderNode(id string) error {
	if id == "" {
		return fmt.Errorf("provider node ID is required")
	}
	result, err := s.DB.Exec(`DELETE FROM provider_nodes WHERE id=?`, id)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return fmt.Errorf("provider node %q not found", id)
	}
	return nil
}

func (s *Store) Connections(nodeID string) ([]ConnectionRecord, error) {
	query := `SELECT id,provider_node_id,name,email,credential_type,priority,enabled FROM connections ORDER BY provider_node_id,priority,id`
	args := []any{}
	if nodeID != "" {
		query = `SELECT id,provider_node_id,name,email,credential_type,priority,enabled FROM connections WHERE provider_node_id=? ORDER BY priority,id`
		args = append(args, nodeID)
	}
	rows, err := s.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ConnectionRecord
	for rows.Next() {
		var item ConnectionRecord
		var enabled int
		if err := rows.Scan(&item.ID, &item.ProviderNodeID, &item.Name, &item.Email, &item.CredentialType, &item.Priority, &enabled); err != nil {
			return nil, err
		}
		item.Enabled = enabled == 1
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) CreateConnection(input CreateConnectionInput) (ConnectionRecord, error) {
	if input.ProviderNodeID == "" || input.Name == "" || input.CredentialType == "" {
		return ConnectionRecord{}, fmt.Errorf("provider node ID, name and credential type are required")
	}
	if _, err := s.ProviderNode(input.ProviderNodeID); err != nil {
		return ConnectionRecord{}, fmt.Errorf("provider node: %w", err)
	}
	if input.Priority < 0 {
		input.Priority = 100
	}
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return ConnectionRecord{}, err
	}
	item := ConnectionRecord{ID: "conn_" + fmt.Sprintf("%x", buf), ProviderNodeID: input.ProviderNodeID, Name: input.Name, CredentialType: input.CredentialType, Priority: input.Priority, Enabled: true}
	_, err := s.DB.Exec(`INSERT INTO connections(id,provider_node_id,name,email,credential_type,secret_ref,priority,enabled) VALUES(?,?,?,?,?,?,?,1)`, item.ID, item.ProviderNodeID, item.Name, item.Email, item.CredentialType, input.Secret, item.Priority)
	return item, err
}

func (s *Store) DeleteConnection(id string) error {
	if id == "" {
		return fmt.Errorf("connection ID is required")
	}
	result, err := s.DB.Exec(`DELETE FROM connections WHERE id=?`, id)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return fmt.Errorf("connection %q not found", id)
	}
	return nil
}

func (s *Store) UpdateConnection(input UpdateConnectionInput) (ConnectionRecord, error) {
	rows, err := s.Connections("")
	if err != nil {
		return ConnectionRecord{}, err
	}
	var current ConnectionRecord
	for _, item := range rows {
		if item.ID == input.ID {
			current = item
			break
		}
	}
	if current.ID == "" {
		return ConnectionRecord{}, fmt.Errorf("connection %q not found", input.ID)
	}
	if input.Name != "" {
		current.Name = input.Name
	}
	if input.CredentialType != "" {
		current.CredentialType = input.CredentialType
	}
	if input.Priority > 0 {
		current.Priority = input.Priority
	}
	if input.Enabled != nil {
		current.Enabled = *input.Enabled
	}
	query := `UPDATE connections SET name=?,credential_type=?,priority=?,enabled=?,updated_at=CURRENT_TIMESTAMP`
	args := []any{current.Name, current.CredentialType, current.Priority, boolInt(current.Enabled)}
	if input.Secret != "" {
		query += `,secret_ref=?`
		args = append(args, input.Secret)
	}
	query += ` WHERE id=?`
	args = append(args, current.ID)
	if _, err := s.DB.Exec(query, args...); err != nil {
		return ConnectionRecord{}, err
	}
	return current, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

type ConnectionRuntime struct {
	ConnectionID, Status, CooldownUntil, RateLimitedUntil  string
	ConsecutiveFailures, BackoffLevel, ConsecutiveUseCount int
	LastError, ErrorCode, LastUsedAt                       string
}

func (s *Store) ConnectionRuntimes() ([]ConnectionRuntime, error) {
	rows, err := s.DB.Query(`SELECT connection_id,status,consecutive_failures,cooldown_until,rate_limited_until,backoff_level,last_error,error_code,last_used_at,consecutive_use_count FROM connection_runtime`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ConnectionRuntime
	for rows.Next() {
		var item ConnectionRuntime
		var cooldown, limited, lastError, errorCode, lastUsed sql.NullString
		if err := rows.Scan(&item.ConnectionID, &item.Status, &item.ConsecutiveFailures, &cooldown, &limited, &item.BackoffLevel, &lastError, &errorCode, &lastUsed, &item.ConsecutiveUseCount); err != nil {
			return nil, err
		}
		item.CooldownUntil, item.RateLimitedUntil, item.LastError, item.ErrorCode, item.LastUsedAt = nullString(cooldown), nullString(limited), nullString(lastError), nullString(errorCode), nullString(lastUsed)
		result = append(result, item)
	}
	return result, rows.Err()
}

type ModelAvailability struct {
	ConnectionID, ModelRef, Status, BlockedUntil, Reason string
	ConsecutiveFailures, BackoffLevel                    int
	LastError, ErrorCode                                 string
}

func (s *Store) ModelAvailabilities() ([]ModelAvailability, error) {
	rows, err := s.DB.Query(`SELECT connection_id,model_ref,status,COALESCE(blocked_until,''),COALESCE(reason,''),consecutive_failures,backoff_level,COALESCE(last_error,''),COALESCE(error_code,'') FROM model_availability`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ModelAvailability
	for rows.Next() {
		var item ModelAvailability
		if err := rows.Scan(&item.ConnectionID, &item.ModelRef, &item.Status, &item.BlockedUntil, &item.Reason, &item.ConsecutiveFailures, &item.BackoffLevel, &item.LastError, &item.ErrorCode); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

type ConnectionModelEntitlement struct {
	ConnectionID, ProviderNodeID, ExternalModelID string
	Status, Source, ObservedAt                    string
}

const (
	EntitlementAvailable = "available"
	EntitlementNotListed = "not_listed"
	EntitlementUnknown   = "unknown"
)

// RecordConnectionModelSnapshot stores positive account evidence. Only a
// complete upstream snapshot is authoritative enough to mark previously known
// routes absent; incomplete pages can never revoke access evidence.
func (s *Store) RecordConnectionModelSnapshot(connectionID, providerNodeID string, externalIDs []string, complete bool, source string) error {
	if connectionID == "" || providerNodeID == "" {
		return fmt.Errorf("connection ID and provider node ID are required")
	}
	if source == "" {
		source = "models_endpoint"
	}
	var actualNodeID string
	if err := s.DB.QueryRow(`SELECT provider_node_id FROM connections WHERE id=?`, connectionID).Scan(&actualNodeID); err != nil {
		return fmt.Errorf("load connection %q: %w", connectionID, err)
	}
	if actualNodeID != providerNodeID {
		return fmt.Errorf("connection %q does not belong to provider node %q", connectionID, providerNodeID)
	}
	seen := make(map[string]struct{}, len(externalIDs))
	for _, externalID := range externalIDs {
		if strings.TrimSpace(externalID) == "" {
			return fmt.Errorf("model snapshot contains an empty upstream ID")
		}
		seen[externalID] = struct{}{}
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if complete {
		if _, err := tx.Exec(`UPDATE connection_model_catalog SET status=?,source=?,observed_at=CURRENT_TIMESTAMP WHERE connection_id=? AND provider_node_id=?`, EntitlementNotListed, source, connectionID, providerNodeID); err != nil {
			return err
		}
		rows, err := tx.Query(`SELECT external_id FROM model_catalog WHERE provider_node_id=? AND kind='discovered' AND enabled=1`, providerNodeID)
		if err != nil {
			return err
		}
		var importedIDs []string
		for rows.Next() {
			var externalID string
			if err := rows.Scan(&externalID); err != nil {
				rows.Close()
				return err
			}
			importedIDs = append(importedIDs, externalID)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, externalID := range importedIDs {
			if _, present := seen[externalID]; present {
				continue
			}
			if _, err := tx.Exec(`INSERT INTO connection_model_catalog(connection_id,provider_node_id,external_model_id,status,source,observed_at) VALUES(?,?,?,?,?,CURRENT_TIMESTAMP) ON CONFLICT(connection_id,external_model_id) DO UPDATE SET status=excluded.status,source=excluded.source,observed_at=CURRENT_TIMESTAMP`, connectionID, providerNodeID, externalID, EntitlementNotListed, source); err != nil {
				return err
			}
		}
	}
	for externalID := range seen {
		if _, err := tx.Exec(`INSERT INTO connection_model_catalog(connection_id,provider_node_id,external_model_id,status,source,observed_at) VALUES(?,?,?,?,?,CURRENT_TIMESTAMP) ON CONFLICT(connection_id,external_model_id) DO UPDATE SET provider_node_id=excluded.provider_node_id,status=excluded.status,source=excluded.source,observed_at=CURRENT_TIMESTAMP`, connectionID, providerNodeID, externalID, EntitlementAvailable, source); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ConnectionModelEntitlements(providerNodeID string) ([]ConnectionModelEntitlement, error) {
	query := `SELECT connection_id,provider_node_id,external_model_id,status,source,observed_at FROM connection_model_catalog`
	args := []any{}
	if providerNodeID != "" {
		query += ` WHERE provider_node_id=?`
		args = append(args, providerNodeID)
	}
	query += ` ORDER BY connection_id,external_model_id`
	rows, err := s.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ConnectionModelEntitlement
	for rows.Next() {
		var item ConnectionModelEntitlement
		if err := rows.Scan(&item.ConnectionID, &item.ProviderNodeID, &item.ExternalModelID, &item.Status, &item.Source, &item.ObservedAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

type ProxyPool struct {
	ID, Name, Kind, Endpoint, NoProxy, Health string
	Enabled                                   bool
	Config                                    map[string]any
}

type ConnectionTransport struct {
	ConnectionID, ProxyPoolID, ProxyURL, NoProxy, RelayURL string
	Config                                                 map[string]any
}

type ProviderExtension struct {
	ConnectionID, Namespace string
	Data                    map[string]any
}

type ProviderSession struct {
	ConnectionID, Namespace, SessionKey, ExpiresAt, LastUsedAt string
	State                                                      kernel.SessionState
}

func (s *Store) ConnectionRuntime(id string) (ConnectionRuntime, bool, error) {
	var item ConnectionRuntime
	var cooldown, limited, lastUsed, lastError, errorCode sql.NullString
	row := s.DB.QueryRow(`SELECT connection_id,status,consecutive_failures,cooldown_until,rate_limited_until,backoff_level,last_error,error_code,last_used_at,consecutive_use_count FROM connection_runtime WHERE connection_id=?`, id)
	if err := row.Scan(&item.ConnectionID, &item.Status, &item.ConsecutiveFailures, &cooldown, &limited, &item.BackoffLevel, &lastError, &errorCode, &lastUsed, &item.ConsecutiveUseCount); err != nil {
		if err == sql.ErrNoRows {
			return ConnectionRuntime{}, false, nil
		}
		return ConnectionRuntime{}, false, err
	}
	item.CooldownUntil, item.RateLimitedUntil, item.LastError, item.ErrorCode, item.LastUsedAt = nullString(cooldown), nullString(limited), nullString(lastError), nullString(errorCode), nullString(lastUsed)
	return item, true, nil
}

func (s *Store) SaveConnectionRuntime(item ConnectionRuntime) error {
	if item.ConnectionID == "" {
		return fmt.Errorf("connection ID is required")
	}
	if item.Status == "" {
		item.Status = "active"
	}
	_, err := s.DB.Exec(`INSERT INTO connection_runtime(connection_id,status,consecutive_failures,cooldown_until,rate_limited_until,backoff_level,last_error,error_code,last_used_at,consecutive_use_count,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP) ON CONFLICT(connection_id) DO UPDATE SET status=excluded.status,consecutive_failures=excluded.consecutive_failures,cooldown_until=excluded.cooldown_until,rate_limited_until=excluded.rate_limited_until,backoff_level=excluded.backoff_level,last_error=excluded.last_error,error_code=excluded.error_code,last_used_at=excluded.last_used_at,consecutive_use_count=excluded.consecutive_use_count,updated_at=CURRENT_TIMESTAMP`, item.ConnectionID, item.Status, item.ConsecutiveFailures, nullable(item.CooldownUntil), nullable(item.RateLimitedUntil), item.BackoffLevel, nullable(item.LastError), nullable(item.ErrorCode), nullable(item.LastUsedAt), item.ConsecutiveUseCount)
	return err
}

func (s *Store) SaveModelAvailability(item ModelAvailability) error {
	if item.ConnectionID == "" || item.ModelRef == "" {
		return fmt.Errorf("connection ID and model reference are required")
	}
	if item.Status == "" {
		item.Status = "available"
	}
	_, err := s.DB.Exec(`INSERT INTO model_availability(connection_id,model_ref,status,blocked_until,reason,consecutive_failures,backoff_level,last_error,error_code,updated_at) VALUES(?,?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP) ON CONFLICT(connection_id,model_ref) DO UPDATE SET status=excluded.status,blocked_until=excluded.blocked_until,reason=excluded.reason,consecutive_failures=excluded.consecutive_failures,backoff_level=excluded.backoff_level,last_error=excluded.last_error,error_code=excluded.error_code,updated_at=CURRENT_TIMESTAMP`, item.ConnectionID, item.ModelRef, item.Status, nullable(item.BlockedUntil), nullable(item.Reason), item.ConsecutiveFailures, item.BackoffLevel, nullable(item.LastError), nullable(item.ErrorCode))
	return err
}

func (s *Store) SaveConnectionTransport(item ConnectionTransport) error {
	config, err := json.Marshal(item.Config)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(`INSERT INTO connection_transports(connection_id,proxy_pool_id,proxy_url,no_proxy,relay_url,config_json,updated_at) VALUES(?,?,?,?,?,?,CURRENT_TIMESTAMP) ON CONFLICT(connection_id) DO UPDATE SET proxy_pool_id=excluded.proxy_pool_id,proxy_url=excluded.proxy_url,no_proxy=excluded.no_proxy,relay_url=excluded.relay_url,config_json=excluded.config_json,updated_at=CURRENT_TIMESTAMP`, item.ConnectionID, nullable(item.ProxyPoolID), item.ProxyURL, item.NoProxy, item.RelayURL, string(config))
	return err
}

func (s *Store) SaveProxyPool(item ProxyPool) error {
	config, err := json.Marshal(item.Config)
	if err != nil {
		return err
	}
	if item.Kind == "" {
		item.Kind = "http"
	}
	_, err = s.DB.Exec(`INSERT INTO proxy_pools(id,name,kind,endpoint,no_proxy,enabled,health,config_json,updated_at) VALUES(?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP) ON CONFLICT(id) DO UPDATE SET name=excluded.name,kind=excluded.kind,endpoint=excluded.endpoint,no_proxy=excluded.no_proxy,enabled=excluded.enabled,health=excluded.health,config_json=excluded.config_json,updated_at=CURRENT_TIMESTAMP`, item.ID, item.Name, item.Kind, item.Endpoint, item.NoProxy, boolInt(item.Enabled), item.Health, string(config))
	return err
}

func (s *Store) SaveProviderExtension(item ProviderExtension) error {
	data, err := json.Marshal(item.Data)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(`INSERT INTO provider_extensions(connection_id,namespace,data_json,updated_at) VALUES(?,?,?,CURRENT_TIMESTAMP) ON CONFLICT(connection_id,namespace) DO UPDATE SET data_json=excluded.data_json,updated_at=CURRENT_TIMESTAMP`, item.ConnectionID, item.Namespace, string(data))
	return err
}

func (s *Store) SaveProviderSession(item ProviderSession) error {
	state, err := json.Marshal(item.State)
	if err != nil {
		return err
	}
	if item.ExpiresAt == "" && !item.State.ExpiresAt.IsZero() {
		item.ExpiresAt = item.State.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	if item.LastUsedAt == "" {
		item.LastUsedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	_, err = s.DB.Exec(`INSERT INTO provider_sessions(connection_id,namespace,session_key,state_json,expires_at,last_used_at,updated_at) VALUES(?,?,?,?,?,?,CURRENT_TIMESTAMP) ON CONFLICT(connection_id,namespace,session_key) DO UPDATE SET state_json=excluded.state_json,expires_at=excluded.expires_at,last_used_at=excluded.last_used_at,updated_at=CURRENT_TIMESTAMP`, item.ConnectionID, item.Namespace, item.SessionKey, string(state), nullable(item.ExpiresAt), nullable(item.LastUsedAt))
	return err
}

func (s *Store) ProviderSessions() ([]ProviderSession, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rows, err := s.DB.Query(`SELECT connection_id,namespace,session_key,state_json,COALESCE(expires_at,''),COALESCE(last_used_at,'') FROM provider_sessions WHERE expires_at IS NULL OR expires_at>? ORDER BY updated_at DESC LIMIT 16384`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sessions []ProviderSession
	for rows.Next() {
		var item ProviderSession
		var state string
		if err := rows.Scan(&item.ConnectionID, &item.Namespace, &item.SessionKey, &state, &item.ExpiresAt, &item.LastUsedAt); err != nil {
			return nil, err
		}
		if err := decodeSessionState(state, &item.State); err != nil {
			return nil, fmt.Errorf("provider session %q/%q state: %w", item.Namespace, item.SessionKey, err)
		}
		if item.ExpiresAt != "" {
			item.State.ExpiresAt, err = time.Parse(time.RFC3339Nano, item.ExpiresAt)
			if err != nil {
				return nil, fmt.Errorf("provider session %q/%q expiry: %w", item.Namespace, item.SessionKey, err)
			}
		}
		sessions = append(sessions, item)
	}
	return sessions, rows.Err()
}

func decodeSessionState(raw string, target *kernel.SessionState) error {
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func (s *Store) PruneExpiredProviderSessions(now time.Time) (int64, error) {
	result, err := s.DB.Exec(`DELETE FROM provider_sessions WHERE expires_at IS NOT NULL AND expires_at<=?`, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func nullString(value sql.NullString) string {
	if value.Valid {
		return value.String
	}
	return ""
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *Store) Setting(key string) (string, bool, error) {
	var value string
	if err := s.DB.QueryRow(`SELECT value_json FROM settings WHERE key=?`, key).Scan(&value); err != nil {
		if err == sql.ErrNoRows {
			return "", false, nil
		}
		return "", false, err
	}
	return value, true, nil
}

func (s *Store) SetSetting(key, value string) error {
	if key == "" {
		return fmt.Errorf("setting key is required")
	}
	_, err := s.DB.Exec(`INSERT INTO settings(key,value_json) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json`, key, value)
	return err
}

const CompatibilityLossCeilingSetting = "compatibility.loss-ceiling"

func (s *Store) CompatibilityLossCeiling() (kernel.LossPolicyCeiling, bool, error) {
	value, exists, err := s.Setting(CompatibilityLossCeilingSetting)
	if err != nil || !exists {
		return kernel.LossPolicyCeiling{}, exists, err
	}
	var ceiling kernel.LossPolicyCeiling
	if err := json.Unmarshal([]byte(value), &ceiling); err != nil {
		return kernel.LossPolicyCeiling{}, true, fmt.Errorf("decode compatibility loss ceiling: %w", err)
	}
	if err := kernel.ValidateLossPolicyCeiling(ceiling); err != nil {
		return kernel.LossPolicyCeiling{}, true, fmt.Errorf("validate compatibility loss ceiling: %w", err)
	}
	return ceiling, !ceiling.IsEmpty(), nil
}

func (s *Store) SetCompatibilityLossCeiling(ceiling kernel.LossPolicyCeiling) error {
	if err := kernel.ValidateLossPolicyCeiling(ceiling); err != nil {
		return err
	}
	encoded, err := json.Marshal(ceiling)
	if err != nil {
		return fmt.Errorf("encode compatibility loss ceiling: %w", err)
	}
	if ceiling.IsEmpty() {
		_, err := s.DB.Exec(`DELETE FROM settings WHERE key=?`, CompatibilityLossCeilingSetting)
		return err
	}
	return s.SetSetting(CompatibilityLossCeilingSetting, string(encoded))
}

type Credential struct{ ID, Type, Secret string }

func (s *Store) ConnectionCredential(nodeID string) (Credential, bool) {
	row := s.DB.QueryRow(`SELECT id,credential_type,secret_ref FROM connections WHERE provider_node_id=? AND enabled=1 ORDER BY priority,id LIMIT 1`, nodeID)
	var c Credential
	if err := row.Scan(&c.ID, &c.Type, &c.Secret); err != nil {
		return Credential{}, false
	}
	return c, true
}

func (s *Store) ConnectionCredentialByID(id string) (Credential, bool) {
	row := s.DB.QueryRow(`SELECT id,credential_type,secret_ref FROM connections WHERE id=? AND enabled=1`, id)
	var c Credential
	if err := row.Scan(&c.ID, &c.Type, &c.Secret); err != nil {
		return Credential{}, false
	}
	return c, true
}

func (s *Store) UpdateConnectionSecret(id, secret string) error {
	if id == "" || secret == "" {
		return fmt.Errorf("connection ID and secret are required")
	}
	result, err := s.DB.Exec(`UPDATE connections SET secret_ref=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, secret, id)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return fmt.Errorf("connection %q not found", id)
	}
	return nil
}

// UpdateConnectionSecretIfUnchanged performs a credential-generation fence:
// a delayed OAuth exchange or refresh cannot overwrite a credential that was
// changed while its network request was in flight.
func (s *Store) UpdateConnectionSecretIfUnchanged(id, expected, secret string) error {
	if id == "" || secret == "" {
		return fmt.Errorf("connection ID and replacement secret are required")
	}
	result, err := s.DB.Exec(`UPDATE connections SET secret_ref=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND secret_ref=?`, secret, id, expected)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return fmt.Errorf("connection %q credential changed or connection was removed during authorization", id)
	}
	return nil
}

func (s *Store) SaveUsageEvent(event kernel.UsageEvent) error {
	dateKey := event.At.UTC().Format("2006-01-02")
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	losses, err := json.Marshal(event.CompatibilityLosses)
	if err != nil {
		return fmt.Errorf("encode usage compatibility losses: %w", err)
	}
	plan, err := json.Marshal(event.CompatibilityPlan)
	if err != nil {
		return fmt.Errorf("encode usage compatibility plan: %w", err)
	}
	if _, err = tx.Exec(`INSERT INTO usage_events(timestamp,logical_model,provider_node_id,external_model,connection_id,status,latency_ms,input_tokens,output_tokens,estimated_cost,request_class,session_id,ttft_ms,output_tokens_per_second,compatibility_fidelity,compatibility_losses_json,compatibility_plan_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, event.At.UTC().Format(time.RFC3339Nano), event.LogicalModel, event.ProviderNodeID, event.ExternalModel, event.ConnectionID, event.Status, event.Latency.Milliseconds(), event.InputTokens, event.OutputTokens, event.EstimatedCost, event.RequestClass, event.SessionID, event.TTFT.Milliseconds(), event.OutputTokensPerSecond, event.CompatibilityFidelity, string(losses), string(plan)); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO usage_daily(date_key,requests,prompt_tokens,completion_tokens,estimated_cost,updated_at) VALUES(?,1,?,?,?,CURRENT_TIMESTAMP) ON CONFLICT(date_key) DO UPDATE SET requests=requests+1,prompt_tokens=prompt_tokens+excluded.prompt_tokens,completion_tokens=completion_tokens+excluded.completion_tokens,estimated_cost=estimated_cost+excluded.estimated_cost,updated_at=CURRENT_TIMESTAMP`, dateKey, event.InputTokens, event.OutputTokens, event.EstimatedCost); err != nil {
		return err
	}
	return tx.Commit()
}

type UsageRecord struct {
	ID                                                                           int64
	Timestamp, LogicalModel, ProviderNodeID, ExternalModel, ConnectionID, Status string
	LatencyMS, InputTokens, OutputTokens                                         int64
	EstimatedCost                                                                float64
	RequestClass, SessionID                                                      string
	TTFTMS                                                                       int64
	OutputTokensPerSecond                                                        float64
	CompatibilityFidelity                                                        kernel.CompatibilityFidelity     `json:"compatibilityFidelity,omitempty"`
	CompatibilityLosses                                                          []kernel.LossRecord              `json:"compatibilityLosses,omitempty"`
	CompatibilityPlan                                                            *kernel.CompatibilityPlanSummary `json:"compatibilityPlan,omitempty"`
}

type UsageSummary struct {
	DateKey                                  string  `json:"dateKey"`
	Requests, PromptTokens, CompletionTokens int64   `json:",omitempty"`
	EstimatedCost                            float64 `json:"estimatedCost"`
}

func (s *Store) PruneUsage(before time.Time) (int64, error) {
	if before.IsZero() {
		return 0, fmt.Errorf("retention cutoff is required")
	}
	result, err := s.DB.Exec(`DELETE FROM usage_events WHERE timestamp < ?`, before.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	if _, err := s.DB.Exec(`DELETE FROM usage_daily WHERE date_key < ?`, before.UTC().Format("2006-01-02")); err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) UsageSummary(limit int) ([]UsageSummary, error) {
	if limit <= 0 || limit > 366 {
		limit = 30
	}
	rows, err := s.DB.Query(`SELECT date_key,requests,prompt_tokens,completion_tokens,estimated_cost FROM usage_daily ORDER BY date_key DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []UsageSummary
	for rows.Next() {
		var item UsageSummary
		if err := rows.Scan(&item.DateKey, &item.Requests, &item.PromptTokens, &item.CompletionTokens, &item.EstimatedCost); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) UsageEvents(limit int) ([]UsageRecord, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.DB.Query(`SELECT id,timestamp,COALESCE(logical_model,''),COALESCE(provider_node_id,''),COALESCE(external_model,''),COALESCE(connection_id,''),COALESCE(status,''),latency_ms,input_tokens,output_tokens,estimated_cost,COALESCE(request_class,''),COALESCE(session_id,''),ttft_ms,output_tokens_per_second,COALESCE(compatibility_fidelity,''),COALESCE(compatibility_losses_json,'[]'),COALESCE(compatibility_plan_json,'null') FROM usage_events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []UsageRecord
	for rows.Next() {
		var item UsageRecord
		var losses, plan string
		if err := rows.Scan(&item.ID, &item.Timestamp, &item.LogicalModel, &item.ProviderNodeID, &item.ExternalModel, &item.ConnectionID, &item.Status, &item.LatencyMS, &item.InputTokens, &item.OutputTokens, &item.EstimatedCost, &item.RequestClass, &item.SessionID, &item.TTFTMS, &item.OutputTokensPerSecond, &item.CompatibilityFidelity, &losses, &plan); err != nil {
			return nil, err
		}
		if err := decodeJSON(losses, &item.CompatibilityLosses); err != nil {
			return nil, fmt.Errorf("usage event %d compatibility losses: %w", item.ID, err)
		}
		if err := decodeJSON(plan, &item.CompatibilityPlan); err != nil {
			return nil, fmt.Errorf("usage event %d compatibility plan: %w", item.ID, err)
		}
		result = append(result, item)
	}
	return result, rows.Err()
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
	Name, Prefix, BaseURL, Protocol, DefinitionID, ModelsPath, AuthMode string
}

type UpdateProviderNodeInput struct {
	ID                                                                  string
	Name, Prefix, BaseURL, Protocol, DefinitionID, ModelsPath, AuthMode string
}

func DefaultDefinitionForProtocol(protocol string) string {
	switch protocol {
	case "openai_responses", "responses":
		return "openai-compatible-responses"
	case "anthropic":
		return "anthropic-messages"
	case "gemini", "google_gemini":
		return "gemini"
	default:
		return "openai-compatible-chat"
	}
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
	if input.DefinitionID == "" {
		input.DefinitionID = DefaultDefinitionForProtocol(input.Protocol)
	}
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return ProviderNode{}, err
	}
	node := ProviderNode{ID: "node_" + fmt.Sprintf("%x", buf), Name: input.Name, Prefix: input.Prefix, BaseURL: input.BaseURL, Protocol: input.Protocol, DefinitionID: input.DefinitionID, ModelsPath: input.ModelsPath, AuthMode: input.AuthMode, Enabled: true}
	_, err := s.DB.Exec(`INSERT INTO provider_nodes(id,name,base_url,protocol,definition_id,prefix,models_path,auth_mode,enabled) VALUES(?,?,?,?,?,?,?,?,1)`, node.ID, node.Name, node.BaseURL, node.Protocol, node.DefinitionID, node.Prefix, node.ModelsPath, node.AuthMode)
	return node, err
}

func (s *Store) UpdateProviderNode(input UpdateProviderNodeInput) (ProviderNode, error) {
	current, err := s.ProviderNode(input.ID)
	if err != nil {
		return ProviderNode{}, err
	}
	if input.Name != "" {
		current.Name = input.Name
	}
	if input.Prefix != "" {
		current.Prefix = input.Prefix
	}
	if input.BaseURL != "" {
		current.BaseURL = input.BaseURL
	}
	if input.Protocol != "" {
		current.Protocol = input.Protocol
	}
	if input.ModelsPath != "" {
		current.ModelsPath = input.ModelsPath
	}
	if input.AuthMode != "" {
		current.AuthMode = input.AuthMode
	}
	if input.DefinitionID != "" {
		current.DefinitionID = input.DefinitionID
	}
	_, err = s.DB.Exec(`UPDATE provider_nodes SET name=?,prefix=?,base_url=?,protocol=?,definition_id=?,models_path=?,auth_mode=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, current.Name, current.Prefix, current.BaseURL, current.Protocol, current.DefinitionID, current.ModelsPath, current.AuthMode, current.ID)
	return current, err
}

type Model struct {
	ID, NodeID, Kind, ExternalID, DisplayName string
	Profile                                   kernel.CapabilityProfile
	Limits                                    kernel.TokenLimits
	ConnectionIDs                             []string `json:"connectionIds,omitempty"`
}

func (s *Store) Models() ([]Model, error) {
	rows, err := s.DB.Query(`SELECT id,COALESCE(provider_node_id,''),kind,external_id,display_name,capabilities_json,limits_json FROM model_catalog WHERE enabled=1 ORDER BY display_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Model
	for rows.Next() {
		var m Model
		var caps, limits string
		if err := rows.Scan(&m.ID, &m.NodeID, &m.Kind, &m.ExternalID, &m.DisplayName, &caps, &limits); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(caps), &m.Profile)
		_ = json.Unmarshal([]byte(limits), &m.Limits)
		result = append(result, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	connectionRows, err := s.DB.Query(`SELECT model_id,connection_id FROM custom_model_connections ORDER BY model_id,connection_id`)
	if err != nil {
		return nil, err
	}
	defer connectionRows.Close()
	assignments := make(map[string][]string)
	for connectionRows.Next() {
		var modelID, connectionID string
		if err := connectionRows.Scan(&modelID, &connectionID); err != nil {
			return nil, err
		}
		assignments[modelID] = append(assignments[modelID], connectionID)
	}
	if err := connectionRows.Err(); err != nil {
		return nil, err
	}
	for index := range result {
		result[index].ConnectionIDs = assignments[result[index].ID]
	}
	return result, nil
}

type RouteRecord struct {
	ID, NodeID, Prefix, ExternalModel, Protocol, DefinitionID string
	BaseURL, AuthMode, CredentialID, CredentialType           string
	Profile                                                   kernel.CapabilityProfile
	Limits                                                    kernel.TokenLimits
	Enabled                                                   bool
}

func (s *Store) Routes() ([]RouteRecord, error) {
	rows, err := s.DB.Query(`SELECT m.id,COALESCE(m.provider_node_id,''),COALESCE(n.prefix,''),COALESCE(n.protocol,''),COALESCE(n.definition_id,''),COALESCE(n.base_url,''),COALESCE(n.auth_mode,''),m.external_id,m.enabled,
	COALESCE(c.id,''),COALESCE(c.credential_type,''),COALESCE(c.secret_ref,''),COALESCE(m.capabilities_json,'{}'),COALESCE(m.limits_json,'{}')
FROM model_catalog m LEFT JOIN provider_nodes n ON n.id=m.provider_node_id
LEFT JOIN connections c ON c.provider_node_id=m.provider_node_id AND c.enabled=1
LEFT JOIN connection_model_catalog e ON e.connection_id=c.id AND e.provider_node_id=m.provider_node_id AND e.external_model_id=m.external_id AND e.status='available'
LEFT JOIN custom_model_connections assignment ON assignment.model_id=m.id AND assignment.connection_id=c.id
WHERE m.enabled=1 AND ((m.kind='custom' AND assignment.connection_id IS NOT NULL) OR (m.kind<>'custom' AND e.connection_id IS NOT NULL)) ORDER BY m.id,c.priority,c.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []RouteRecord
	for rows.Next() {
		var r RouteRecord
		var enabled int
		var capabilities, limits string
		var credentialSecret string
		if err := rows.Scan(&r.ID, &r.NodeID, &r.Prefix, &r.Protocol, &r.DefinitionID, &r.BaseURL, &r.AuthMode, &r.ExternalModel, &enabled, &r.CredentialID, &r.CredentialType, &credentialSecret, &capabilities, &limits); err != nil {
			return nil, err
		}
		r.Enabled = enabled == 1
		_ = json.Unmarshal([]byte(capabilities), &r.Profile)
		_ = json.Unmarshal([]byte(limits), &r.Limits)
		if r.CredentialID != "" {
			r.ID = r.ID + "@" + r.CredentialID
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

type UpsertCatalogModelInput struct {
	ID, ProviderNodeID, Kind, ExternalID, DisplayName string
	Profile                                           kernel.CapabilityProfile
	Limits                                            kernel.TokenLimits
	Overrides                                         map[string]any
	Raw                                               map[string]any
	ConnectionIDs                                     []string `json:"connectionIds,omitempty"`
}

func (s *Store) UpsertCatalogModel(input UpsertCatalogModelInput) error {
	if input.ID == "" || input.DisplayName == "" {
		return fmt.Errorf("model ID and display name are required")
	}
	if input.Kind == "" {
		input.Kind = "custom"
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if input.Kind == "custom" {
		if input.ProviderNodeID == "" || input.ExternalID == "" {
			return fmt.Errorf("custom models require a provider and exact upstream model ID")
		}
		if len(input.ConnectionIDs) == 0 {
			return fmt.Errorf("custom models must be assigned to at least one connection")
		}
		var existingCount int
		var existingID string
		if err := tx.QueryRow(`SELECT count(*),COALESCE(min(id),'') FROM model_catalog WHERE provider_node_id=? AND kind='custom' AND external_id=?`, input.ProviderNodeID, input.ExternalID).Scan(&existingCount, &existingID); err != nil {
			return err
		}
		if existingCount > 1 {
			return fmt.Errorf("custom upstream identity %q has duplicate catalog rows; resolve them before editing", input.ExternalID)
		}
		if existingCount == 1 && existingID != input.ID {
			return fmt.Errorf("custom model %q already exists as catalog ID %q; edit its connection assignments instead of creating a duplicate", input.ExternalID, existingID)
		}
	} else if len(input.ConnectionIDs) > 0 {
		return fmt.Errorf("connection assignments are supported only for custom models")
	}
	seenConnections := make(map[string]bool, len(input.ConnectionIDs))
	for _, connectionID := range input.ConnectionIDs {
		if connectionID == "" || seenConnections[connectionID] {
			return fmt.Errorf("custom model connection IDs must be non-empty and unique")
		}
		seenConnections[connectionID] = true
		var owner string
		if err := tx.QueryRow(`SELECT provider_node_id FROM connections WHERE id=?`, connectionID).Scan(&owner); err != nil {
			return fmt.Errorf("custom model connection %q: %w", connectionID, err)
		}
		if owner != input.ProviderNodeID {
			return fmt.Errorf("custom model connection %q belongs to provider %q, not %q", connectionID, owner, input.ProviderNodeID)
		}
	}
	caps, _ := json.Marshal(input.Profile)
	limits, _ := json.Marshal(input.Limits)
	overrides, _ := json.Marshal(input.Overrides)
	raw, _ := json.Marshal(input.Raw)
	if _, err = tx.Exec(`INSERT INTO model_catalog(id,provider_node_id,kind,external_id,display_name,capabilities_json,limits_json,overrides_json,raw_json,enabled,last_seen_at,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)
ON CONFLICT(id) DO UPDATE SET provider_node_id=excluded.provider_node_id,kind=excluded.kind,external_id=excluded.external_id,
display_name=excluded.display_name,capabilities_json=excluded.capabilities_json,limits_json=excluded.limits_json,overrides_json=excluded.overrides_json,
raw_json=excluded.raw_json,enabled=1,updated_at=CURRENT_TIMESTAMP`, input.ID, input.ProviderNodeID, input.Kind, input.ExternalID, input.DisplayName, string(caps), string(limits), string(overrides), string(raw)); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM custom_model_connections WHERE model_id=?`, input.ID); err != nil {
		return err
	}
	for _, connectionID := range input.ConnectionIDs {
		if _, err = tx.Exec(`INSERT INTO custom_model_connections(model_id,connection_id) VALUES(?,?)`, input.ID, connectionID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteCatalogModel(id string) error {
	_, err := s.DB.Exec(`DELETE FROM model_catalog WHERE id=?`, id)
	return err
}

func stringValue(value any) string { result, _ := value.(string); return result }
