package store

import (
	"database/sql"
	"fmt"
)

// UsageResetCount reports discarded rows when ambiguous legacy usage tables
// are replaced during the nullable usage cutover.
type UsageResetCount struct {
	Events int64 `json:"events"`
	Daily  int64 `json:"daily"`
}

func (s *Store) UsageResetCounts() UsageResetCount { return s.usageReset }

func (s *Store) migrateUsageSchema() error {
	var reset UsageResetCount
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	eventsExists, err := usageTableExists(tx, "usage_events")
	if err != nil {
		return err
	}
	dailyExists, err := usageTableExists(tx, "usage_daily")
	if err != nil {
		return err
	}
	current := eventsExists && dailyExists
	if current {
		current, err = usageEventsHavePresenceShape(tx)
		if err != nil {
			return err
		}
	}
	if current {
		current, err = usageDailyHavePresenceShape(tx)
		if err != nil {
			return err
		}
	}
	if !current {
		if eventsExists {
			if err := tx.QueryRow(`SELECT count(*) FROM usage_events`).Scan(&reset.Events); err != nil {
				return err
			}
		}
		if dailyExists {
			if err := tx.QueryRow(`SELECT count(*) FROM usage_daily`).Scan(&reset.Daily); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`
DROP TABLE IF EXISTS usage_extension_values;
DROP TABLE IF EXISTS usage_extension_descriptor_schemas;
DROP TABLE IF EXISTS usage_extension_descriptors;
DROP TABLE IF EXISTS usage_extension_schemas;
DROP TABLE IF EXISTS usage_events;
DROP TABLE IF EXISTS usage_daily;`); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(usageEventsDDL); err != nil {
		return err
	}
	if _, err := tx.Exec(usageDailyDDL); err != nil {
		return err
	}
	if _, err := tx.Exec(usageExtensionDDL); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.usageReset = reset
	return nil
}

const usageEventsDDL = `CREATE TABLE IF NOT EXISTS usage_events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	timestamp TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	logical_model TEXT, provider_node_id TEXT, external_model TEXT, connection_id TEXT,
	status TEXT, latency_ms INTEGER,
	input_tokens INTEGER, output_tokens INTEGER, cached_read_tokens INTEGER,
	cached_write_tokens INTEGER, reasoning_tokens INTEGER, estimated_cost REAL,
	accounting_issues_json TEXT NOT NULL DEFAULT '[]',
	request_class TEXT NOT NULL DEFAULT '', session_id TEXT NOT NULL DEFAULT '',
	invocation_id TEXT NOT NULL DEFAULT '', attempt_id TEXT NOT NULL DEFAULT '',
	ttft_ms INTEGER, output_tokens_per_second REAL,
	compatibility_fidelity TEXT NOT NULL DEFAULT '',
	compatibility_losses_json TEXT NOT NULL DEFAULT '[]',
	compatibility_plan_json TEXT NOT NULL DEFAULT 'null'
);
CREATE INDEX IF NOT EXISTS idx_usage_events_timestamp ON usage_events(timestamp);
CREATE UNIQUE INDEX IF NOT EXISTS idx_usage_events_attempt_id ON usage_events(attempt_id) WHERE attempt_id != '';`

const usageDailyDDL = `CREATE TABLE IF NOT EXISTS usage_daily (
	date_key TEXT PRIMARY KEY,
	attempts INTEGER NOT NULL DEFAULT 0,
	incomplete_attempts INTEGER NOT NULL DEFAULT 0,
	input_tokens_sum INTEGER NOT NULL DEFAULT 0, input_tokens_reported INTEGER NOT NULL DEFAULT 0,
	output_tokens_sum INTEGER NOT NULL DEFAULT 0, output_tokens_reported INTEGER NOT NULL DEFAULT 0,
	cached_read_tokens_sum INTEGER NOT NULL DEFAULT 0, cached_read_tokens_reported INTEGER NOT NULL DEFAULT 0,
	cached_write_tokens_sum INTEGER NOT NULL DEFAULT 0, cached_write_tokens_reported INTEGER NOT NULL DEFAULT 0,
	reasoning_tokens_sum INTEGER NOT NULL DEFAULT 0, reasoning_tokens_reported INTEGER NOT NULL DEFAULT 0,
	estimated_cost_sum REAL NOT NULL DEFAULT 0, estimated_cost_reported INTEGER NOT NULL DEFAULT 0,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);`

const usageExtensionDDL = `CREATE TABLE IF NOT EXISTS usage_extension_schemas (
	kind TEXT NOT NULL, id TEXT NOT NULL, contract_version INTEGER NOT NULL,
	digest TEXT NOT NULL, document BLOB NOT NULL,
	PRIMARY KEY(kind,id,contract_version)
);
CREATE TABLE IF NOT EXISTS usage_extension_descriptors (
	kind TEXT NOT NULL, id TEXT NOT NULL, contract_version INTEGER NOT NULL,
	descriptor_json BLOB NOT NULL, schema_pins_json BLOB NOT NULL,
	PRIMARY KEY(kind,id,contract_version)
);
CREATE TABLE IF NOT EXISTS usage_extension_descriptor_schemas (
	descriptor_kind TEXT NOT NULL, descriptor_id TEXT NOT NULL, descriptor_version INTEGER NOT NULL,
	schema_kind TEXT NOT NULL, schema_id TEXT NOT NULL, schema_version INTEGER NOT NULL,
	schema_digest TEXT NOT NULL, is_root INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY(descriptor_kind,descriptor_id,descriptor_version,schema_kind,schema_id,schema_version),
	FOREIGN KEY(descriptor_kind,descriptor_id,descriptor_version)
		REFERENCES usage_extension_descriptors(kind,id,contract_version) ON DELETE CASCADE,
	FOREIGN KEY(schema_kind,schema_id,schema_version)
		REFERENCES usage_extension_schemas(kind,id,contract_version) ON DELETE RESTRICT
);
CREATE TABLE IF NOT EXISTS usage_extension_values (
	event_id INTEGER NOT NULL REFERENCES usage_events(id) ON DELETE CASCADE,
	descriptor_kind TEXT NOT NULL, descriptor_id TEXT NOT NULL, descriptor_version INTEGER NOT NULL,
	value_json BLOB NOT NULL,
	PRIMARY KEY(event_id,descriptor_kind,descriptor_id,descriptor_version),
	FOREIGN KEY(descriptor_kind,descriptor_id,descriptor_version)
		REFERENCES usage_extension_descriptors(kind,id,contract_version) ON DELETE RESTRICT
);
CREATE INDEX IF NOT EXISTS idx_usage_extension_values_descriptor
	ON usage_extension_values(descriptor_kind,descriptor_id,descriptor_version);`

func usageTableExists(tx *sql.Tx, name string) (bool, error) {
	var exists bool
	err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name=?)`, name).Scan(&exists)
	return exists, err
}

func usageColumnShape(tx *sql.Tx, table string) (map[string]bool, error) {
	rows, err := tx.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[name] = notNull != 0
	}
	return columns, rows.Err()
}

func usageEventsHavePresenceShape(tx *sql.Tx) (bool, error) {
	columns, err := usageColumnShape(tx, "usage_events")
	if err != nil {
		return false, err
	}
	for _, name := range []string{"timestamp", "logical_model", "provider_node_id", "external_model", "connection_id", "status", "latency_ms", "input_tokens", "output_tokens", "cached_read_tokens", "cached_write_tokens", "reasoning_tokens", "estimated_cost", "accounting_issues_json", "request_class", "session_id", "invocation_id", "attempt_id", "ttft_ms", "output_tokens_per_second", "compatibility_fidelity", "compatibility_losses_json", "compatibility_plan_json"} {
		if _, ok := columns[name]; !ok {
			return false, nil
		}
	}
	for _, name := range []string{"latency_ms", "input_tokens", "output_tokens", "cached_read_tokens", "cached_write_tokens", "reasoning_tokens", "estimated_cost", "ttft_ms", "output_tokens_per_second"} {
		if columns[name] {
			return false, nil
		}
	}
	return true, nil
}

func usageDailyHavePresenceShape(tx *sql.Tx) (bool, error) {
	columns, err := usageColumnShape(tx, "usage_daily")
	if err != nil {
		return false, err
	}
	for _, name := range []string{"date_key", "attempts", "incomplete_attempts", "input_tokens_sum", "input_tokens_reported", "output_tokens_sum", "output_tokens_reported", "cached_read_tokens_sum", "cached_read_tokens_reported", "cached_write_tokens_sum", "cached_write_tokens_reported", "reasoning_tokens_sum", "reasoning_tokens_reported", "estimated_cost_sum", "estimated_cost_reported", "updated_at"} {
		if _, ok := columns[name]; !ok {
			return false, nil
		}
	}
	return true, nil
}

func (count UsageResetCount) String() string {
	return fmt.Sprintf("events=%d daily=%d", count.Events, count.Daily)
}
