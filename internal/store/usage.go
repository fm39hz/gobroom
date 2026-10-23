package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/usage"
)

const usageRecordSelect = `SELECT id,timestamp,COALESCE(logical_model,''),COALESCE(provider_node_id,''),COALESCE(external_model,''),COALESCE(connection_id,''),COALESCE(status,''),COALESCE(invocation_id,''),COALESCE(attempt_id,''),latency_ms,input_tokens,output_tokens,cached_read_tokens,cached_write_tokens,reasoning_tokens,estimated_cost,COALESCE(accounting_issues_json,'[]'),COALESCE(request_class,''),COALESCE(session_id,''),ttft_ms,output_tokens_per_second,COALESCE(compatibility_fidelity,''),COALESCE(compatibility_losses_json,'[]'),COALESCE(compatibility_plan_json,'null') FROM usage_events`

const (
	storeIssueRawCaptureDisabled = "raw_usage_capture_disabled"
	storeIssueInvalidRawUsage    = "invalid_raw_usage"
)

type usageRowScanner interface {
	Scan(...any) error
}

func scanUsageRecord(row usageRowScanner) (UsageRecord, error) {
	var record UsageRecord
	var losses, plan, accountingIssues string
	if err := row.Scan(&record.ID, &record.Timestamp, &record.LogicalModel, &record.ProviderNodeID, &record.ExternalModel, &record.ConnectionID, &record.Status, &record.InvocationID, &record.AttemptID, &record.LatencyMS, &record.InputTokens, &record.OutputTokens, &record.CachedReadTokens, &record.CachedWriteTokens, &record.ReasoningTokens, &record.EstimatedCost, &accountingIssues, &record.RequestClass, &record.SessionID, &record.TTFTMS, &record.OutputTokensPerSecond, &record.CompatibilityFidelity, &losses, &plan); err != nil {
		return UsageRecord{}, err
	}
	if err := decodeJSON(accountingIssues, &record.AccountingIssues); err != nil {
		return UsageRecord{}, fmt.Errorf("usage event %d accounting issues: %w", record.ID, err)
	}
	if err := decodeJSON(losses, &record.CompatibilityLosses); err != nil {
		return UsageRecord{}, fmt.Errorf("usage event %d compatibility losses: %w", record.ID, err)
	}
	if err := decodeJSON(plan, &record.CompatibilityPlan); err != nil {
		return UsageRecord{}, fmt.Errorf("usage event %d compatibility plan: %w", record.ID, err)
	}
	return record, nil
}

func (s *Store) SaveUsageEvent(ctx context.Context, event kernel.UsageEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	preparedRaw, rawIssue := s.prepareRawUsage(event.RawUsage)
	event.RawUsage = nil
	if rawIssue != "" {
		event.AccountingIssues = append(event.AccountingIssues, kernel.UsageIssue{Code: rawIssue})
	}
	preparedExtensions, err := s.prepareUsageExtensions(event.Extensions)
	if err != nil {
		return err
	}
	event = event.SanitizeCommon()
	if preparedRaw != nil {
		event.RawUsage = preparedRaw
	}
	at := event.At.UTC()
	dateKey := at.Format("2006-01-02")
	tx, err := s.DB.BeginTx(ctx, nil)
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
	accountingIssues, err := json.Marshal(event.BoundedIssues())
	if err != nil {
		return fmt.Errorf("encode usage accounting issues: %w", err)
	}
	var latencyMS, ttftMS *int64
	if event.Latency != 0 {
		value := event.Latency.Milliseconds()
		latencyMS = &value
	}
	if !event.FirstByteAt.IsZero() || event.TTFT > 0 {
		value := event.TTFT.Milliseconds()
		ttftMS = &value
	}
	result, err := tx.StmtContext(ctx, s.runtime.usageEvent).ExecContext(ctx, at.Format(time.RFC3339Nano), event.LogicalModel, event.ProviderNodeID, event.ExternalModel, event.ConnectionID, event.Status, latencyMS, event.InputTokens, event.OutputTokens, event.CachedReadTokens, event.CachedWriteTokens, event.ReasoningTokens, event.EstimatedCost, string(accountingIssues), event.RequestClass, event.SessionID, ttftMS, event.OutputTokensPerSecond, event.CompatibilityFidelity, string(losses), string(plan), event.InvocationID, event.AttemptID, at.Unix(), at.Nanosecond())
	if err != nil {
		return err
	}
	eventID, err := result.LastInsertId()
	if err != nil {
		return err
	}
	if preparedRaw != nil {
		if _, err := tx.ExecContext(ctx, `INSERT INTO usage_raw_fragments(event_id,provider_kind,provider_id,provider_version,api,value_json) VALUES(?,?,?,?,?,?)`,
			eventID, preparedRaw.ProviderDefinition.Kind, preparedRaw.ProviderDefinition.ID,
			preparedRaw.ProviderDefinition.ContractVersion, preparedRaw.API, []byte(preparedRaw.Value)); err != nil {
			return err
		}
	}
	if err := s.persistUsageExtensionsTx(ctx, tx, eventID, preparedExtensions); err != nil {
		return err
	}
	if _, err = tx.StmtContext(ctx, s.runtime.usageDaily).ExecContext(ctx, dateKey,
		len(event.AccountingIssues) > 0 || !event.HasReportedUsage(),
		event.InputTokens, event.InputTokens, event.OutputTokens, event.OutputTokens,
		event.CachedReadTokens, event.CachedReadTokens, event.CachedWriteTokens, event.CachedWriteTokens,
		event.ReasoningTokens, event.ReasoningTokens, event.EstimatedCost, event.EstimatedCost); err != nil {
		return err
	}
	return tx.Commit()
}

// SetUsageCapturePolicy binds a positive raw retention bound. The policy is
// store-local and never grants read access; details still require explicit
// options from an already-authorized caller.
func (s *Store) SetUsageCapturePolicy(policy kernel.UsageCapturePolicy) error {
	if policy.MaxBytes <= 0 {
		return fmt.Errorf("raw usage capture limit must be positive")
	}
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	if s.usageCaptureSet {
		if s.usageCapture == policy {
			return nil
		}
		return fmt.Errorf("raw usage capture policy is already bound")
	}
	s.usageCapture = policy
	s.usageCaptureSet = true
	return nil
}

func (s *Store) prepareRawUsage(fragment *kernel.RawUsageFragment) (*kernel.RawUsageFragment, string) {
	if fragment == nil {
		return nil, ""
	}
	s.usageMu.RLock()
	policy := s.usageCapture
	s.usageMu.RUnlock()
	if !policy.Enabled {
		return nil, storeIssueRawCaptureDisabled
	}
	selected, issues := kernel.SelectRawUsage(policy, fragment.ProviderDefinition, fragment.API, fragment.Value)
	if selected == nil || len(issues) != 0 {
		return nil, storeIssueInvalidRawUsage
	}
	return selected, ""
}

type UsageRecord struct {
	ID                                                                                         int64
	Timestamp, LogicalModel, ProviderNodeID, ExternalModel, ConnectionID, Status               string
	InvocationID                                                                               string `json:"invocationId"`
	AttemptID                                                                                  string `json:"attemptId"`
	LatencyMS, InputTokens, OutputTokens, CachedReadTokens, CachedWriteTokens, ReasoningTokens *int64
	EstimatedCost, OutputTokensPerSecond                                                       *float64
	RequestClass, SessionID                                                                    string
	TTFTMS                                                                                     *int64
	AccountingIssues                                                                           []kernel.UsageIssue              `json:"accountingIssues,omitempty"`
	CompatibilityFidelity                                                                      kernel.CompatibilityFidelity     `json:"compatibilityFidelity,omitempty"`
	CompatibilityLosses                                                                        []kernel.LossRecord              `json:"compatibilityLosses,omitempty"`
	CompatibilityPlan                                                                          *kernel.CompatibilityPlanSummary `json:"compatibilityPlan,omitempty"`
}

type UsageMetricSummary struct {
	Sum      int64 `json:"sum"`
	Reported int64 `json:"reported"`
}

type UsageCostSummary struct {
	Sum      float64 `json:"sum"`
	Reported int64   `json:"reported"`
}

type UsageSummary struct {
	DateKey            string             `json:"dateKey"`
	Attempts           int64              `json:"attempts"`
	IncompleteAttempts int64              `json:"incompleteAttempts"`
	Input              UsageMetricSummary `json:"input"`
	Output             UsageMetricSummary `json:"output"`
	CachedRead         UsageMetricSummary `json:"cachedRead"`
	CachedWrite        UsageMetricSummary `json:"cachedWrite"`
	Reasoning          UsageMetricSummary `json:"reasoning"`
	EstimatedCost      UsageCostSummary   `json:"estimatedCost"`
}

// PruneUsage removes all expired events in one transaction for manual pruning.
func (s *Store) PruneUsage(ctx context.Context, before time.Time) (int64, error) {
	return s.pruneUsage(ctx, before, 0)
}

// PruneUsageBatch removes at most limit expired events, oldest timestamp first
// with event ID as the tie breaker. A partial day retains its full aggregate.
func (s *Store) PruneUsageBatch(ctx context.Context, before time.Time, limit int) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if limit <= 0 {
		return 0, fmt.Errorf("usage prune batch limit must be positive")
	}
	return s.pruneUsage(ctx, before, limit)
}

// pruneUsage shares transaction and cleanup semantics between the full manual
// operation (limit zero) and a bounded batch (positive limit).
func (s *Store) pruneUsage(ctx context.Context, before time.Time, limit int) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if before.IsZero() {
		return 0, fmt.Errorf("retention cutoff is required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	cutoff := before.UTC()
	deleteEvents := `DELETE FROM usage_events WHERE (timestamp_seconds,timestamp_nanos) < (?,?)`
	args := []any{cutoff.Unix(), cutoff.Nanosecond()}
	if limit > 0 {
		deleteEvents = `DELETE FROM usage_events WHERE id IN (
			SELECT id FROM usage_events WHERE (timestamp_seconds,timestamp_nanos) < (?,?)
			ORDER BY timestamp_seconds,timestamp_nanos,id LIMIT ?
		)`
		args = append(args, limit)
	}
	result, err := tx.ExecContext(ctx, deleteEvents, args...)
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM usage_daily WHERE date_key < ? AND NOT EXISTS (
		SELECT 1 FROM usage_events e WHERE e.timestamp >= usage_daily.date_key
		AND e.timestamp < date(usage_daily.date_key,'+1 day')
	)`, before.UTC().Format("2006-01-02")); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM usage_extension_descriptors WHERE NOT EXISTS (
		SELECT 1 FROM usage_extension_values v WHERE v.descriptor_kind=usage_extension_descriptors.kind
		AND v.descriptor_id=usage_extension_descriptors.id AND v.descriptor_version=usage_extension_descriptors.contract_version
	)`); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM usage_extension_schemas WHERE NOT EXISTS (
		SELECT 1 FROM usage_extension_descriptor_schemas ds WHERE ds.schema_kind=usage_extension_schemas.kind
		AND ds.schema_id=usage_extension_schemas.id AND ds.schema_version=usage_extension_schemas.contract_version
	)`); err != nil {
		return 0, err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return deleted, nil
}

func (s *Store) UsageSummary(limit int) ([]UsageSummary, error) {
	if limit <= 0 || limit > 366 {
		limit = 30
	}
	rows, err := s.DB.Query(`SELECT date_key,attempts,incomplete_attempts,input_tokens_sum,input_tokens_reported,output_tokens_sum,output_tokens_reported,cached_read_tokens_sum,cached_read_tokens_reported,cached_write_tokens_sum,cached_write_tokens_reported,reasoning_tokens_sum,reasoning_tokens_reported,estimated_cost_sum,estimated_cost_reported FROM usage_daily ORDER BY date_key DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []UsageSummary
	for rows.Next() {
		var item UsageSummary
		if err := rows.Scan(&item.DateKey, &item.Attempts, &item.IncompleteAttempts,
			&item.Input.Sum, &item.Input.Reported, &item.Output.Sum, &item.Output.Reported,
			&item.CachedRead.Sum, &item.CachedRead.Reported, &item.CachedWrite.Sum, &item.CachedWrite.Reported,
			&item.Reasoning.Sum, &item.Reasoning.Reported, &item.EstimatedCost.Sum, &item.EstimatedCost.Reported); err != nil {
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
	rows, err := s.DB.Query(usageRecordSelect+` ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []UsageRecord
	for rows.Next() {
		item, err := scanUsageRecord(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// UsageDetailRecord contains common event fields and the immutable typed
// extension values captured with the event.
type UsageDetailRecord struct {
	Event      UsageRecord              `json:"event"`
	Extensions []UsageDetailValue       `json:"extensions,omitempty"`
	RawUsage   *kernel.RawUsageFragment `json:"rawUsage,omitempty"`
}

// UsageDetailValue pairs an owned raw JSON value with its historical,
// non-executable descriptor snapshot.
type UsageDetailValue struct {
	Descriptor extensions.UsageDescriptorSnapshot `json:"descriptor"`
	Value      json.RawMessage                    `json:"value"`
}

type preparedUsageDescriptor struct {
	ref              extensions.Ref
	descriptorJSON   []byte
	schemaPinsJSON   []byte
	publicDescriptor extensions.UsageDescriptorSnapshot
	lock             extensions.DependencyLock
	schemas          map[extensions.Ref]extensions.Schema
}

type preparedUsageValue struct {
	descriptor preparedUsageDescriptor
	value      []byte
}

// SetUsageCatalog binds the frozen catalog whose exact contracts authorize
// durable extension usage writes. Reads never depend on this binding.
func (s *Store) SetUsageCatalog(snapshot *extensions.Snapshot) error {
	if snapshot == nil || snapshot.Fingerprint() == "" {
		return fmt.Errorf("a frozen usage catalog is required")
	}
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	if s.usageCatalog != nil {
		if s.usageCatalog.Fingerprint() == snapshot.Fingerprint() {
			return nil
		}
		return fmt.Errorf("usage catalog is already bound to a different snapshot")
	}
	s.usageCatalog = snapshot
	return nil
}

// SetUsageLimits binds positive repository-side bounds. The first explicit
// binding may replace defaults; later calls must repeat the same policy.
func (s *Store) SetUsageLimits(limits kernel.UsageLimits) error {
	if limits.MaxValues <= 0 || limits.MaxExtensionBytes <= 0 {
		return fmt.Errorf("usage limits must be positive")
	}
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	if s.usageLimitsSet {
		if s.usageLimits == limits {
			return nil
		}
		return fmt.Errorf("usage limits are already bound")
	}
	s.usageLimits = limits
	s.usageLimitsSet = true
	return nil
}

func (s *Store) usageBinding() (*extensions.Snapshot, kernel.UsageLimits) {
	s.usageMu.RLock()
	defer s.usageMu.RUnlock()
	limits := s.usageLimits
	if !s.usageLimitsSet {
		limits = kernel.DefaultUsageLimits()
	}
	return s.usageCatalog, limits
}

// prepareUsageExtensions performs all count, envelope, reference, byte and
// schema checks before it clones any event-owned raw value bytes.
func (s *Store) prepareUsageExtensions(values []extensions.UsageValue) ([]preparedUsageValue, error) {
	if len(values) == 0 {
		return nil, nil
	}
	snapshot, limits := s.usageBinding()
	if snapshot == nil {
		return nil, fmt.Errorf("usage catalog is not bound")
	}
	if len(values) > limits.MaxValues {
		return nil, fmt.Errorf("usage extension count exceeds the host limit")
	}
	for _, value := range values {
		if int64(len(value.TypeRef.Kind)) > limits.MaxExtensionBytes ||
			int64(len(value.TypeRef.ID)) > limits.MaxExtensionBytes ||
			int64(len(value.Value)) > limits.MaxExtensionBytes {
			return nil, fmt.Errorf("usage extension value exceeds the host limit")
		}
	}

	seen := make(map[extensions.Ref]struct{}, len(values))
	preparedDescriptors := make([]preparedUsageDescriptor, len(values))
	for index, value := range values {
		if _, duplicate := seen[value.TypeRef]; duplicate {
			return nil, fmt.Errorf("duplicate usage extension reference")
		}
		seen[value.TypeRef] = struct{}{}
		if err := snapshot.ValidateUsageValueWithin(value, limits.MaxExtensionBytes); err != nil {
			return nil, fmt.Errorf("usage extension value is invalid")
		}
		publicDescriptor, ok := snapshot.UsageDescriptor(value.TypeRef)
		if !ok {
			return nil, fmt.Errorf("usage extension descriptor is incomplete")
		}
		lock, err := snapshot.LockDependencies([]extensions.Ref{publicDescriptor.SchemaRef})
		if err != nil {
			return nil, fmt.Errorf("usage extension dependencies are unavailable")
		}
		// Persist the immutable data contract. Executable module/build metadata
		// may change while this exact usage type and schema remain unchanged.
		descriptorJSON, err := json.Marshal(publicDescriptor)
		if err != nil {
			return nil, fmt.Errorf("usage extension descriptor cannot be encoded")
		}
		schemaPinsJSON, err := json.Marshal(lock.Schemas)
		if err != nil {
			return nil, fmt.Errorf("usage extension schema pins cannot be encoded")
		}
		schemas := make(map[extensions.Ref]extensions.Schema, len(lock.Schemas))
		for _, pin := range lock.Schemas {
			schema, ok := snapshot.Schema(pin.Ref)
			if !ok || schema.Digest != pin.SHA256 {
				return nil, fmt.Errorf("usage extension schema does not match its dependency lock")
			}
			schemas[pin.Ref] = schema
		}
		preparedDescriptors[index] = preparedUsageDescriptor{
			ref: value.TypeRef, descriptorJSON: descriptorJSON, schemaPinsJSON: schemaPinsJSON,
			publicDescriptor: publicDescriptor, lock: lock, schemas: schemas,
		}
	}

	// OwnedEncoding applies the exact JSON encoder size to the full retained
	// array budget. Its RawMessage result is compact, owned, and precision
	// preserving, so the bytes persisted are the bytes the budget measured.
	prepared := make([]preparedUsageValue, len(values))
	retainedBytes := int64(2) // JSON array brackets
	for index, value := range values {
		separatorBytes := int64(0)
		if index > 0 {
			separatorBytes = 1
		}
		if retainedBytes > limits.MaxExtensionBytes-separatorBytes {
			return nil, fmt.Errorf("usage extension envelope exceeds the host limit")
		}
		remainingBytes := limits.MaxExtensionBytes - retainedBytes - separatorBytes
		owned, encodedBytes, err := value.OwnedEncoding(remainingBytes)
		if err != nil {
			return nil, fmt.Errorf("usage extension envelope exceeds the host limit")
		}
		if err := snapshot.ValidateUsageValueWithin(owned, limits.MaxExtensionBytes); err != nil {
			return nil, fmt.Errorf("usage extension value is invalid")
		}
		retainedBytes += encodedBytes + separatorBytes
		prepared[index] = preparedUsageValue{
			descriptor: preparedDescriptors[index],
			value:      owned.Value,
		}
	}
	return prepared, nil
}

func (s *Store) persistUsageExtensionsTx(ctx context.Context, tx *sql.Tx, eventID int64, values []preparedUsageValue) error {
	for _, item := range values {
		if err := persistUsageDescriptorTx(ctx, tx, item.descriptor); err != nil {
			return err
		}
		ref := item.descriptor.ref
		if _, err := tx.ExecContext(ctx, `INSERT INTO usage_extension_values(event_id,descriptor_kind,descriptor_id,descriptor_version,value_json) VALUES(?,?,?,?,?)`,
			eventID, ref.Kind, ref.ID, ref.ContractVersion, item.value); err != nil {
			return fmt.Errorf("persist usage extension value: %w", err)
		}
	}
	return nil
}

func persistUsageDescriptorTx(ctx context.Context, tx *sql.Tx, descriptor preparedUsageDescriptor) error {
	ref := descriptor.ref
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO usage_extension_descriptors(kind,id,contract_version,descriptor_json,schema_pins_json) VALUES(?,?,?,?,?)`,
		ref.Kind, ref.ID, ref.ContractVersion, descriptor.descriptorJSON, descriptor.schemaPinsJSON); err != nil {
		return fmt.Errorf("persist usage extension descriptor: %w", err)
	}
	var existingDescriptor, existingPins []byte
	if err := tx.QueryRowContext(ctx, `SELECT descriptor_json,schema_pins_json FROM usage_extension_descriptors WHERE kind=? AND id=? AND contract_version=?`,
		ref.Kind, ref.ID, ref.ContractVersion).Scan(&existingDescriptor, &existingPins); err != nil {
		return err
	}
	if !bytes.Equal(existingDescriptor, descriptor.descriptorJSON) {
		return fmt.Errorf("usage extension descriptor identity conflicts with persisted metadata")
	}
	if !bytes.Equal(existingPins, descriptor.schemaPinsJSON) {
		return fmt.Errorf("usage extension schema pins conflict with persisted metadata")
	}

	expectedLinks := make(map[extensions.Ref]extensions.SchemaPin, len(descriptor.lock.Schemas))
	rootRef := descriptor.publicDescriptor.SchemaRef
	for _, pin := range descriptor.lock.Schemas {
		if _, exists := expectedLinks[pin.Ref]; exists {
			return fmt.Errorf("usage extension dependency lock contains duplicate schema references")
		}
		schema, ok := descriptor.schemas[pin.Ref]
		if !ok || schema.Digest != pin.SHA256 {
			return fmt.Errorf("usage extension schema digest does not match its dependency lock")
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO usage_extension_schemas(kind,id,contract_version,digest,document) VALUES(?,?,?,?,?)`,
			schema.Ref.Kind, schema.Ref.ID, schema.Ref.ContractVersion, schema.Digest, []byte(schema.Document)); err != nil {
			return fmt.Errorf("persist usage extension schema: %w", err)
		}
		var existingDigest string
		var existingDocument []byte
		if err := tx.QueryRowContext(ctx, `SELECT digest,document FROM usage_extension_schemas WHERE kind=? AND id=? AND contract_version=?`,
			schema.Ref.Kind, schema.Ref.ID, schema.Ref.ContractVersion).Scan(&existingDigest, &existingDocument); err != nil {
			return err
		}
		if existingDigest != schema.Digest || !bytes.Equal(existingDocument, schema.Document) {
			return fmt.Errorf("usage schema identity conflicts with persisted digest")
		}
		isRoot := schema.Ref == rootRef
		rootFlag := 0
		if isRoot {
			rootFlag = 1
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO usage_extension_descriptor_schemas(descriptor_kind,descriptor_id,descriptor_version,schema_kind,schema_id,schema_version,schema_digest,is_root) VALUES(?,?,?,?,?,?,?,?)`,
			ref.Kind, ref.ID, ref.ContractVersion, schema.Ref.Kind, schema.Ref.ID, schema.Ref.ContractVersion, schema.Digest, rootFlag); err != nil {
			return fmt.Errorf("persist usage extension schema link: %w", err)
		}
		expectedLinks[pin.Ref] = pin
	}
	if _, ok := expectedLinks[rootRef]; !ok {
		return fmt.Errorf("usage extension root schema is absent from its dependency lock")
	}
	return verifyUsageSchemaClosure(ctx, tx, descriptor.publicDescriptor, descriptor.schemaPinsJSON)
}

// The descriptor's frozen pins are the archive's closure manifest. Verify the
// complete set without consulting a live catalog or interpreting schema refs.
func verifyUsageSchemaClosure(ctx context.Context, tx *sql.Tx, descriptor extensions.UsageDescriptorSnapshot, pinsJSON []byte) error {
	var pins []extensions.SchemaPin
	if err := json.Unmarshal(pinsJSON, &pins); err != nil || len(pins) == 0 {
		return fmt.Errorf("persisted usage schema pins are missing or invalid")
	}
	expected := make(map[extensions.Ref]string, len(pins))
	for _, pin := range pins {
		if pin.Ref.Validate() != nil || pin.Ref.Kind != extensions.SchemaKind {
			return fmt.Errorf("persisted usage schema pin reference is invalid")
		}
		digest, err := hex.DecodeString(pin.SHA256)
		if err != nil || len(digest) != sha256.Size {
			return fmt.Errorf("persisted usage schema pin digest is invalid")
		}
		if _, duplicate := expected[pin.Ref]; duplicate {
			return fmt.Errorf("persisted usage schema pins are ambiguous")
		}
		expected[pin.Ref] = pin.SHA256
	}
	if rootDigest, ok := expected[descriptor.SchemaRef]; !ok || rootDigest != descriptor.SchemaDigest {
		return fmt.Errorf("persisted usage root schema pin conflicts with its descriptor")
	}
	ref := descriptor.Ref
	rows, err := tx.QueryContext(ctx, `SELECT ds.schema_kind,ds.schema_id,ds.schema_version,ds.schema_digest,ds.is_root,s.digest,s.document
		FROM usage_extension_descriptor_schemas ds
		LEFT JOIN usage_extension_schemas s ON s.kind=ds.schema_kind AND s.id=ds.schema_id AND s.contract_version=ds.schema_version
		WHERE ds.descriptor_kind=? AND ds.descriptor_id=? AND ds.descriptor_version=?`, ref.Kind, ref.ID, ref.ContractVersion)
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := make(map[extensions.Ref]struct{}, len(expected))
	for rows.Next() {
		var schemaRef extensions.Ref
		var linkDigest string
		var isRoot int
		var storedDigest sql.NullString
		var document []byte
		if err := rows.Scan(&schemaRef.Kind, &schemaRef.ID, &schemaRef.ContractVersion, &linkDigest, &isRoot, &storedDigest, &document); err != nil {
			return err
		}
		if _, duplicate := seen[schemaRef]; duplicate {
			return fmt.Errorf("persisted usage schema links are ambiguous")
		}
		expectedDigest, ok := expected[schemaRef]
		documentDigest := sha256.Sum256(document)
		if !ok || linkDigest != expectedDigest || !storedDigest.Valid || storedDigest.String != expectedDigest || hex.EncodeToString(documentDigest[:]) != expectedDigest {
			return fmt.Errorf("persisted usage schema closure conflicts with its frozen pins")
		}
		wantRoot := 0
		if schemaRef == descriptor.SchemaRef {
			wantRoot = 1
		}
		if isRoot != wantRoot {
			return fmt.Errorf("persisted usage root schema link conflicts")
		}
		seen[schemaRef] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("persisted usage schema closure is incomplete")
	}
	return nil
}

// UsageDetail reads solely from persisted rows; it does not require a live
// catalog or call any extension implementation.
func (s *Store) UsageDetail(ctx context.Context, id int64, options usage.DetailOptions) (UsageDetailRecord, error) {
	if err := ctx.Err(); err != nil {
		return UsageDetailRecord{}, err
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return UsageDetailRecord{}, err
	}
	defer tx.Rollback()
	event, err := scanUsageRecord(tx.QueryRowContext(ctx, usageRecordSelect+` WHERE id=?`, id))
	if err != nil {
		return UsageDetailRecord{}, err
	}
	result := UsageDetailRecord{Event: event}
	var recordedPins [][]byte
	var rows *sql.Rows
	wantExtensions := options.IncludeExtensions || options.IncludePrivate
	if wantExtensions {
		query := `SELECT v.value_json,v.descriptor_kind,v.descriptor_id,v.descriptor_version,d.descriptor_json,d.schema_pins_json
			FROM usage_extension_values v
			LEFT JOIN usage_extension_descriptors d ON d.kind=v.descriptor_kind AND d.id=v.descriptor_id AND d.contract_version=v.descriptor_version
			WHERE v.event_id=?`
		if options.IncludePrivate {
			query += ` AND json_extract(CAST(d.descriptor_json AS TEXT),'$.classification') IN (?,?)`
		} else {
			query += ` AND json_extract(CAST(d.descriptor_json AS TEXT),'$.classification')=?`
		}
		query += ` ORDER BY v.descriptor_kind,v.descriptor_id,v.descriptor_version`
		if options.IncludePrivate {
			rows, err = tx.QueryContext(ctx, query, id,
				string(extensions.UsagePrivate), string(extensions.UsageManagementVisible))
		} else {
			rows, err = tx.QueryContext(ctx, query, id, string(extensions.UsageManagementVisible))
		}
		if err != nil {
			return UsageDetailRecord{}, err
		}
	}
	for wantExtensions && rows.Next() {
		var rawValue, descriptorJSON, pinsJSON []byte
		var valueRef extensions.Ref
		if err := rows.Scan(&rawValue, &valueRef.Kind, &valueRef.ID, &valueRef.ContractVersion, &descriptorJSON, &pinsJSON); err != nil {
			return UsageDetailRecord{}, err
		}
		var descriptor extensions.UsageDescriptorSnapshot
		if err := json.Unmarshal(descriptorJSON, &descriptor); err != nil {
			return UsageDetailRecord{}, fmt.Errorf("persisted usage descriptor is invalid")
		}
		if descriptor.Ref.Validate() != nil || descriptor.Ref.Kind != extensions.UsageKind || descriptor.Ref != valueRef || descriptor.SchemaRef.Validate() != nil || descriptor.SchemaRef.Kind != extensions.SchemaKind {
			return UsageDetailRecord{}, fmt.Errorf("persisted usage descriptor schema link is inconsistent")
		}
		if descriptor.Classification != extensions.UsagePrivate && descriptor.Classification != extensions.UsageManagementVisible {
			return UsageDetailRecord{}, fmt.Errorf("persisted usage descriptor classification is invalid")
		}
		result.Extensions = append(result.Extensions, UsageDetailValue{
			Descriptor: descriptor,
			Value:      append(json.RawMessage(nil), rawValue...),
		})
		recordedPins = append(recordedPins, pinsJSON)
	}
	if wantExtensions {
		if err := rows.Err(); err != nil {
			return UsageDetailRecord{}, err
		}
		if err := rows.Close(); err != nil {
			return UsageDetailRecord{}, err
		}
		for index, value := range result.Extensions {
			if err := verifyUsageSchemaClosure(ctx, tx, value.Descriptor, recordedPins[index]); err != nil {
				return UsageDetailRecord{}, err
			}
		}
	}
	if options.IncludeRaw {
		var raw kernel.RawUsageFragment
		var value []byte
		err := tx.QueryRowContext(ctx, `SELECT provider_kind,provider_id,provider_version,api,value_json FROM usage_raw_fragments WHERE event_id=?`, id).Scan(
			&raw.ProviderDefinition.Kind, &raw.ProviderDefinition.ID, &raw.ProviderDefinition.ContractVersion, &raw.API, &value)
		if err != nil && err != sql.ErrNoRows {
			return UsageDetailRecord{}, err
		}
		if err == nil {
			if raw.ProviderDefinition.Validate() != nil || raw.ProviderDefinition.Kind != "provider-definition" || raw.API == "" || !json.Valid(value) || len(bytes.TrimSpace(value)) == 0 || bytes.TrimSpace(value)[0] != '{' {
				return UsageDetailRecord{}, fmt.Errorf("persisted raw usage fragment is invalid")
			}
			raw.Value = append(json.RawMessage(nil), value...)
			result.RawUsage = &raw
		}
	}
	if err := tx.Commit(); err != nil {
		return UsageDetailRecord{}, err
	}
	return result, nil
}
