package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/quota"
)

const (
	spoolHealthKind = "health"
	spoolQuotaKind  = "quota"
)

var ErrPersistenceSpoolFull = errors.New("runtime persistence spool capacity exceeded")

type SpoolHealth struct {
	RouteID       string
	NodeID        string
	CredentialID  string
	ExternalModel string
	Generation    kernel.RouteHealthGeneration
	State         RouteHealth
}

type SpoolRecord struct {
	Key           string
	Kind          string
	Generation    string
	ObservedAt    time.Time
	NextAttemptAt time.Time
	Attempts      int
	DeadLetter    bool
	LastError     string
	Health        *SpoolHealth
	Quota         *quota.Snapshot
}

type PersistenceSpoolStatus struct {
	Enabled     bool   `json:"enabled"`
	Path        string `json:"path,omitempty"`
	Pending     int    `json:"pending"`
	DeadLetters int    `json:"deadLetters"`
	Capacity    int    `json:"capacity"`
	MaxBytes    int64  `json:"maxBytes"`
}

type DurablePersistenceSpool struct {
	mu          sync.Mutex
	path        string
	maxBytes    int64
	maxRecords  int
	maxAttempts int
	records     map[string]SpoolRecord
}

func OpenDurablePersistenceSpool(path string, maxBytes int64, maxRecords, maxAttempts int) (*DurablePersistenceSpool, error) {
	if path == "" {
		return nil, errors.New("runtime persistence spool path is required")
	}
	if maxBytes <= 0 {
		maxBytes = 32 << 20
	}
	if maxRecords <= 0 {
		maxRecords = 8192
	}
	if maxAttempts <= 0 {
		maxAttempts = 12
	}
	spool := &DurablePersistenceSpool{path: path, maxBytes: maxBytes, maxRecords: maxRecords, maxAttempts: maxAttempts, records: map[string]SpoolRecord{}}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		if int64(len(data)) > maxBytes {
			return nil, ErrPersistenceSpoolFull
		}
		var records []SpoolRecord
		if err := json.Unmarshal(data, &records); err != nil {
			return nil, fmt.Errorf("decode runtime persistence spool: %w", err)
		}
		for _, record := range records {
			if err := validateSpoolRecord(record); err != nil {
				return nil, err
			}
			spool.records[record.Key] = record
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return nil, err
		}
		if len(spool.records) > maxRecords {
			return nil, ErrPersistenceSpoolFull
		}
	}
	return spool, nil
}

func (s *DurablePersistenceSpool) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *DurablePersistenceSpool) DeferHealth(observation HealthObservation, attempts int, cause error) (bool, error) {
	observedAt := observation.State.LastAttempt
	if observedAt.IsZero() {
		key := HealthSpoolKey(observation)
		if previous, exists := s.Record(key); exists && previous.Generation == observation.State.Generation {
			observedAt = previous.ObservedAt
		} else {
			observedAt = time.Now()
		}
	}
	record := SpoolRecord{
		Key:        HealthSpoolKey(observation),
		Kind:       spoolHealthKind,
		Generation: observation.State.Generation,
		ObservedAt: observedAt,
		Health: &SpoolHealth{
			RouteID: observation.Route.ID, NodeID: observation.Route.NodeID,
			CredentialID: observation.Route.CredentialID, ExternalModel: observation.Route.ExternalModel,
			Generation: observation.Route.HealthGeneration, State: observation.State,
		},
	}
	return s.deferRecord(record, attempts, cause)
}

func (s *DurablePersistenceSpool) DeferQuota(snapshot quota.Snapshot, attempts int, cause error) (bool, error) {
	copy := snapshot
	return s.deferRecord(SpoolRecord{Key: QuotaSpoolKey(snapshot), Kind: spoolQuotaKind, Generation: snapshot.Generation, ObservedAt: snapshot.ObservedAt, Quota: &copy}, attempts, cause)
}

func (s *DurablePersistenceSpool) deferRecord(record SpoolRecord, attempts int, cause error) (bool, error) {
	if s == nil {
		return false, errors.New("runtime persistence spool is unavailable")
	}
	if err := validateSpoolRecord(record); err != nil {
		return false, err
	}
	if attempts < 1 {
		attempts = 1
	}
	if cause != nil {
		record.LastError = truncateSpoolError(cause.Error(), 512)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, exists := s.records[record.Key]
	if exists && previous.Generation == record.Generation && previous.ObservedAt.Equal(record.ObservedAt) {
		record.Attempts = previous.Attempts + attempts
	} else {
		record.Attempts = attempts
	}
	if exists && previous.DeadLetter && !record.ObservedAt.After(previous.ObservedAt) && previous.Generation == record.Generation {
		record.Attempts = previous.Attempts
		record.DeadLetter = true
	} else {
		record.DeadLetter = record.Attempts >= s.maxAttempts
	}
	if !exists && len(s.records) >= s.maxRecords {
		return false, ErrPersistenceSpoolFull
	}
	if !record.DeadLetter {
		record.NextAttemptAt = time.Now().Add(spoolRetryDelay(record.Attempts))
	}
	next := cloneSpoolRecords(s.records)
	next[record.Key] = record
	if err := s.writeLocked(next); err != nil {
		return false, err
	}
	s.records = next
	return record.DeadLetter, nil
}

func spoolRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	shift := min(attempt-1, 8)
	delay := time.Second * time.Duration(1<<shift)
	return min(delay, 5*time.Minute)
}

func (s *DurablePersistenceSpool) Acknowledge(key string) error {
	if s == nil || key == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.records[key]; !exists {
		return nil
	}
	next := cloneSpoolRecords(s.records)
	delete(next, key)
	if err := s.writeLocked(next); err != nil {
		return err
	}
	s.records = next
	return nil
}

func (s *DurablePersistenceSpool) Record(key string) (SpoolRecord, bool) {
	if s == nil || key == "" {
		return SpoolRecord{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, exists := s.records[key]
	return record, exists
}

func (s *DurablePersistenceSpool) Pending() []SpoolRecord {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	records := make([]SpoolRecord, 0, len(s.records))
	for _, record := range s.records {
		if !record.DeadLetter {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Key < records[j].Key })
	return records
}

func (s *DurablePersistenceSpool) Due(now time.Time) []SpoolRecord {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	records := make([]SpoolRecord, 0, len(s.records))
	for _, record := range s.records {
		if !record.DeadLetter && (record.NextAttemptAt.IsZero() || !record.NextAttemptAt.After(now)) {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Key < records[j].Key })
	return records
}

func (s *DurablePersistenceSpool) DeadLetterCount() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, record := range s.records {
		if record.DeadLetter {
			count++
		}
	}
	return count
}

func (s *DurablePersistenceSpool) Status() PersistenceSpoolStatus {
	if s == nil {
		return PersistenceSpoolStatus{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	status := PersistenceSpoolStatus{Enabled: true, Path: s.path, Capacity: s.maxRecords, MaxBytes: s.maxBytes}
	for _, record := range s.records {
		if record.DeadLetter {
			status.DeadLetters++
		} else {
			status.Pending++
		}
	}
	return status
}

func (s *DurablePersistenceSpool) RequeueDeadLetters() (int, error) {
	if s == nil {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneSpoolRecords(s.records)
	count := 0
	for key, record := range next {
		if !record.DeadLetter {
			continue
		}
		record.DeadLetter = false
		record.Attempts = 0
		record.NextAttemptAt = time.Time{}
		record.LastError = ""
		next[key] = record
		count++
	}
	if count == 0 {
		return 0, nil
	}
	if err := s.writeLocked(next); err != nil {
		return 0, err
	}
	s.records = next
	return count, nil
}

func (s *DurablePersistenceSpool) writeLocked(records map[string]SpoolRecord) error {
	ordered := make([]SpoolRecord, 0, len(records))
	for _, record := range records {
		ordered = append(ordered, record)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Key < ordered[j].Key })
	data, err := json.Marshal(ordered)
	if err != nil {
		return err
	}
	if int64(len(data)) > s.maxBytes {
		return ErrPersistenceSpoolFull
	}
	directory := filepath.Dir(s.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".gobroom-runtime-spool-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return err
	}
	directoryFile, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer directoryFile.Close()
	return directoryFile.Sync()
}

func validateSpoolRecord(record SpoolRecord) error {
	if record.Key == "" {
		return errors.New("runtime persistence spool record has no key")
	}
	switch record.Kind {
	case spoolHealthKind:
		if record.Health == nil {
			return fmt.Errorf("health spool record %q has no payload", record.Key)
		}
		if record.Health.State.LastScope == kernel.ScopeOperation && (record.Health.State.OperationRef.Kind != "operation" || record.Health.State.OperationRef.Validate() != nil) {
			return fmt.Errorf("operation health spool requires an exact operation ref")
		}
	case spoolQuotaKind:
		if record.Quota == nil {
			return fmt.Errorf("quota spool record %q has no payload", record.Key)
		}
		if err := record.Quota.ValidateOperation(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("runtime persistence spool record %q has unknown kind %q", record.Key, record.Kind)
	}
	return nil
}

func cloneSpoolRecords(source map[string]SpoolRecord) map[string]SpoolRecord {
	copy := make(map[string]SpoolRecord, len(source)+1)
	for key, record := range source {
		copy[key] = record
	}
	return copy
}

func HealthSpoolKey(observation HealthObservation) string {
	identity := healthObservationIdentity(observation)
	key, _ := json.Marshal(struct {
		Scope      string         `json:"scope"`
		Provider   string         `json:"provider"`
		Connection string         `json:"connection"`
		Model      string         `json:"model"`
		Route      string         `json:"route"`
		Operation  extensions.Ref `json:"operation,omitzero"`
	}{identity.scope, identity.provider, identity.connection, identity.model, identity.route, identity.operation})
	return spoolHealthKind + ":" + string(key)
}

func QuotaSpoolKey(snapshot quota.Snapshot) string {
	key, _ := json.Marshal(struct {
		Provider   string         `json:"provider"`
		Connection string         `json:"connection"`
		Model      string         `json:"model"`
		Window     string         `json:"window"`
		Operation  extensions.Ref `json:"operation,omitzero"`
	}{snapshot.ProviderNodeID, snapshot.ConnectionID, snapshot.ModelRef, snapshot.WindowName, snapshot.OperationRef})
	return spoolQuotaKind + ":" + string(key)
}

func truncateSpoolError(message string, limit int) string {
	message = strings.TrimSpace(message)
	if len(message) > limit {
		return message[:limit]
	}
	return message
}

func (r SpoolRecord) HealthObservation() (HealthObservation, bool) {
	if r.Kind != spoolHealthKind || r.Health == nil {
		return HealthObservation{}, false
	}
	payload := r.Health
	return HealthObservation{
		Route: kernel.Route{ID: payload.RouteID, NodeID: payload.NodeID, CredentialID: payload.CredentialID, ExternalModel: payload.ExternalModel, HealthGeneration: payload.Generation, SessionOperationRef: payload.State.OperationRef},
		State: func() RouteHealth { state := payload.State; state.Generation = r.Generation; return state }(),
	}, true
}

func (r SpoolRecord) QuotaSnapshot() (quota.Snapshot, bool) {
	if r.Kind != spoolQuotaKind || r.Quota == nil {
		return quota.Snapshot{}, false
	}
	snapshot := *r.Quota
	snapshot.Generation = r.Generation
	return snapshot, true
}
