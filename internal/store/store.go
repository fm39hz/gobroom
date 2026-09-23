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
CREATE TABLE IF NOT EXISTS usage_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT, timestamp TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  logical_model TEXT, provider_node_id TEXT, external_model TEXT, connection_id TEXT,
  status TEXT, latency_ms INTEGER NOT NULL DEFAULT 0, input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0, estimated_cost REAL NOT NULL DEFAULT 0
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
CREATE TABLE IF NOT EXISTS usage_daily (
  date_key TEXT PRIMARY KEY, requests INTEGER NOT NULL DEFAULT 0,
  prompt_tokens INTEGER NOT NULL DEFAULT 0, completion_tokens INTEGER NOT NULL DEFAULT 0,
  estimated_cost REAL NOT NULL DEFAULT 0, updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value_json TEXT NOT NULL);
-- Typed model graph: discovered routes, physical identities and role combos.
CREATE TABLE IF NOT EXISTS physical_models (
  name TEXT PRIMARY KEY, identity_json TEXT NOT NULL DEFAULT '{}', policy_json TEXT NOT NULL DEFAULT '{"id":"ordered-fallback","config":{}}',
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
  name TEXT PRIMARY KEY, strategy_json TEXT NOT NULL DEFAULT '{"id":"ordered-fallback","config":{}}',
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
	_, _ = s.DB.Exec(`ALTER TABLE model_catalog ADD COLUMN limits_json TEXT NOT NULL DEFAULT '{}'`)
	_, _ = s.DB.Exec(`ALTER TABLE physical_models ADD COLUMN limits_json TEXT NOT NULL DEFAULT '{}'`)
	_, _ = s.DB.Exec(`ALTER TABLE physical_models ADD COLUMN identity_json TEXT NOT NULL DEFAULT '{}'`)
	_, _ = s.DB.Exec(`ALTER TABLE physical_model_sources ADD COLUMN fidelity TEXT NOT NULL DEFAULT 'unknown'`)
	_, _ = s.DB.Exec(`ALTER TABLE physical_model_sources ADD COLUMN evidence_json TEXT NOT NULL DEFAULT '[]'`)
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
	State                                                      map[string]any
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
	_, err = s.DB.Exec(`INSERT INTO provider_sessions(connection_id,namespace,session_key,state_json,expires_at,last_used_at,updated_at) VALUES(?,?,?,?,?,?,CURRENT_TIMESTAMP) ON CONFLICT(connection_id,namespace,session_key) DO UPDATE SET state_json=excluded.state_json,expires_at=excluded.expires_at,last_used_at=excluded.last_used_at,updated_at=CURRENT_TIMESTAMP`, item.ConnectionID, item.Namespace, item.SessionKey, string(state), nullable(item.ExpiresAt), nullable(item.LastUsedAt))
	return err
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

type Credential struct{ Type, Secret string }

func (s *Store) ConnectionCredential(nodeID string) (Credential, bool) {
	row := s.DB.QueryRow(`SELECT credential_type,secret_ref FROM connections WHERE provider_node_id=? AND enabled=1 ORDER BY priority,id LIMIT 1`)
	var c Credential
	if err := row.Scan(&c.Type, &c.Secret); err != nil {
		return Credential{}, false
	}
	return c, true
}

func (s *Store) ConnectionCredentialByID(id string) (Credential, bool) {
	row := s.DB.QueryRow(`SELECT credential_type,secret_ref FROM connections WHERE id=? AND enabled=1`, id)
	var c Credential
	if err := row.Scan(&c.Type, &c.Secret); err != nil {
		return Credential{}, false
	}
	return c, true
}

func (s *Store) SaveUsageEvent(event kernel.UsageEvent) error {
	dateKey := event.At.UTC().Format("2006-01-02")
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO usage_events(timestamp,logical_model,provider_node_id,external_model,connection_id,status,latency_ms,input_tokens,output_tokens,estimated_cost,request_class,session_id,ttft_ms,output_tokens_per_second) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, event.At.UTC().Format(time.RFC3339Nano), event.LogicalModel, event.ProviderNodeID, event.ExternalModel, event.ConnectionID, event.Status, event.Latency.Milliseconds(), event.InputTokens, event.OutputTokens, event.EstimatedCost, event.RequestClass, event.SessionID, event.TTFT.Milliseconds(), event.OutputTokensPerSecond); err != nil {
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
}

func (s *Store) UsageEvents(limit int) ([]UsageRecord, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.DB.Query(`SELECT id,timestamp,COALESCE(logical_model,''),COALESCE(provider_node_id,''),COALESCE(external_model,''),COALESCE(connection_id,''),COALESCE(status,''),latency_ms,input_tokens,output_tokens,estimated_cost,COALESCE(request_class,''),COALESCE(session_id,''),ttft_ms,output_tokens_per_second FROM usage_events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []UsageRecord
	for rows.Next() {
		var item UsageRecord
		if err := rows.Scan(&item.ID, &item.Timestamp, &item.LogicalModel, &item.ProviderNodeID, &item.ExternalModel, &item.ConnectionID, &item.Status, &item.LatencyMS, &item.InputTokens, &item.OutputTokens, &item.EstimatedCost, &item.RequestClass, &item.SessionID, &item.TTFTMS, &item.OutputTokensPerSecond); err != nil {
			return nil, err
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

func defaultDefinitionForProtocol(protocol string) string {
	switch protocol {
	case "openai_responses", "responses":
		return "openai-compatible-responses"
	case "anthropic":
		return "anthropic-messages"
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
		input.DefinitionID = defaultDefinitionForProtocol(input.Protocol)
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
	return result, rows.Err()
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
WHERE m.enabled=1 ORDER BY m.id,c.priority,c.id`)
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
		if r.Protocol == "" {
			r.Protocol = "chat"
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
}

func (s *Store) UpsertCatalogModel(input UpsertCatalogModelInput) error {
	if input.ID == "" || input.DisplayName == "" {
		return fmt.Errorf("model ID and display name are required")
	}
	if input.Kind == "" {
		input.Kind = "custom"
	}
	caps, _ := json.Marshal(input.Profile)
	limits, _ := json.Marshal(input.Limits)
	overrides, _ := json.Marshal(input.Overrides)
	raw, _ := json.Marshal(input.Raw)
	_, err := s.DB.Exec(`INSERT INTO model_catalog(id,provider_node_id,kind,external_id,display_name,capabilities_json,limits_json,overrides_json,raw_json,enabled,last_seen_at,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)
ON CONFLICT(id) DO UPDATE SET provider_node_id=excluded.provider_node_id,kind=excluded.kind,external_id=excluded.external_id,
display_name=excluded.display_name,capabilities_json=excluded.capabilities_json,limits_json=excluded.limits_json,overrides_json=excluded.overrides_json,
raw_json=excluded.raw_json,enabled=1,updated_at=CURRENT_TIMESTAMP`, input.ID, input.ProviderNodeID, input.Kind, input.ExternalID, input.DisplayName, string(caps), string(limits), string(overrides), string(raw))
	return err
}

func (s *Store) DeleteCatalogModel(id string) error {
	_, err := s.DB.Exec(`DELETE FROM model_catalog WHERE id=?`, id)
	return err
}

func stringValue(value any) string { result, _ := value.(string); return result }
