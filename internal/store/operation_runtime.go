package store

import (
	"fmt"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
)

// OperationRuntime is independent of model/account/provider suppression. Its
// exact operation ref is part of the storage identity, never a model suffix.
type OperationRuntime struct {
	ConnectionID, ModelRef                                       string
	OperationRef                                                 extensions.Ref
	Status, BlockedUntil, LastError, Cause, Evidence, Generation string
	Failures, Successes                                          int
	Confidence                                                   float64
	LastAttempt, LastSuccess                                     string
	ObservedAt                                                   time.Time
}

func (s *Store) SaveOperationRuntime(item OperationRuntime) error {
	if item.ConnectionID == "" || item.ModelRef == "" || item.OperationRef.Kind != "operation" || item.OperationRef.Validate() != nil || item.Generation == "" {
		return fmt.Errorf("operation runtime requires a connection, model, exact operation and generation")
	}
	if item.ObservedAt.IsZero() {
		item.ObservedAt = time.Now()
	}
	_, err := s.DB.Exec(`INSERT INTO operation_runtime(connection_id,model_ref,operation_id,operation_version,status,failures,successes,blocked_until,last_error,cause,evidence,confidence,generation,last_attempt,last_success,observed_at_ns)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(connection_id,model_ref,operation_id,operation_version) DO UPDATE SET status=excluded.status,failures=excluded.failures,successes=excluded.successes,blocked_until=excluded.blocked_until,last_error=excluded.last_error,cause=excluded.cause,evidence=excluded.evidence,confidence=excluded.confidence,generation=excluded.generation,last_attempt=excluded.last_attempt,last_success=excluded.last_success,observed_at_ns=excluded.observed_at_ns
WHERE excluded.observed_at_ns>=operation_runtime.observed_at_ns`, item.ConnectionID, item.ModelRef, item.OperationRef.ID, item.OperationRef.ContractVersion, item.Status, item.Failures, item.Successes, item.BlockedUntil, item.LastError, item.Cause, item.Evidence, item.Confidence, item.Generation, item.LastAttempt, item.LastSuccess, item.ObservedAt.UnixNano())
	return err
}

func (s *Store) OperationRuntimes() ([]OperationRuntime, error) {
	rows, err := s.DB.Query(`SELECT connection_id,model_ref,operation_id,operation_version,status,failures,successes,blocked_until,last_error,cause,evidence,confidence,generation,last_attempt,last_success,observed_at_ns FROM operation_runtime ORDER BY connection_id,model_ref,operation_id,operation_version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []OperationRuntime
	for rows.Next() {
		var item OperationRuntime
		var observed int64
		item.OperationRef.Kind = "operation"
		if err := rows.Scan(&item.ConnectionID, &item.ModelRef, &item.OperationRef.ID, &item.OperationRef.ContractVersion, &item.Status, &item.Failures, &item.Successes, &item.BlockedUntil, &item.LastError, &item.Cause, &item.Evidence, &item.Confidence, &item.Generation, &item.LastAttempt, &item.LastSuccess, &observed); err != nil {
			return nil, err
		}
		item.ObservedAt = time.Unix(0, observed)
		result = append(result, item)
	}
	return result, rows.Err()
}
