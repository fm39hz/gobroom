package store

import (
	"fmt"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
)

// AuthRuntime stores authentication health separately from operational route health.
type AuthRuntime struct {
	ProviderNodeID, ConnectionID, ModelRef, RouteID string
	Scope                                           kernel.OutcomeScope
	OperationRef                                    extensions.Ref
	Generation, Status                              string
	CredentialRevision                              string
	Failures, Successes                             int
	BlockedUntil, LastError, Cause, Evidence        string
	Confidence                                      float64
	LastAttempt, LastSuccess                        string
	ObservedAt                                      time.Time
}

func (s *Store) SaveAuthRuntime(item AuthRuntime) error {
	if item.ProviderNodeID == "" || item.Scope == "" || item.Generation == "" {
		return fmt.Errorf("authentication runtime requires scope, provider and generation")
	}
	switch item.Scope {
	case kernel.ScopeProvider:
	case kernel.ScopeConnection:
		if item.ConnectionID == "" || item.CredentialRevision == "" {
			return fmt.Errorf("connection authentication runtime requires connection and credential revision")
		}
	case kernel.ScopeRoute, kernel.ScopeRouteConnection:
		if item.ConnectionID == "" || item.CredentialRevision == "" || item.ModelRef == "" || item.RouteID == "" {
			return fmt.Errorf("route authentication runtime requires connection, credential revision, model and route")
		}
	case kernel.ScopeOperation:
		if item.ConnectionID == "" || item.CredentialRevision == "" || item.ModelRef == "" || item.RouteID == "" || item.OperationRef.Kind != "operation" || item.OperationRef.Validate() != nil {
			return fmt.Errorf("operation authentication runtime requires connection, credential revision, model, route and exact operation")
		}
	default:
		return fmt.Errorf("unsupported authentication runtime scope %q", item.Scope)
	}
	if item.Status == "" {
		return fmt.Errorf("authentication runtime status is required")
	}
	if item.ObservedAt.IsZero() {
		item.ObservedAt = time.Now()
	}
	guard := `EXISTS(SELECT 1 FROM provider_nodes WHERE id=?)`
	args := []any{item.Scope, item.ProviderNodeID, item.ConnectionID, item.ModelRef, item.RouteID, item.OperationRef.ID, item.OperationRef.ContractVersion, item.Generation, item.CredentialRevision, item.Status, item.Failures, item.Successes, item.BlockedUntil, item.LastError, item.Cause, item.Evidence, item.Confidence, item.LastAttempt, item.LastSuccess, item.ObservedAt.UnixNano()}
	if item.Scope != kernel.ScopeProvider {
		guard = `EXISTS(SELECT 1 FROM connections c JOIN connection_credential_revisions r ON r.connection_id=c.id WHERE c.id=? AND c.provider_node_id=? AND r.revision=?)`
		args = append(args, item.ConnectionID, item.ProviderNodeID, item.CredentialRevision)
	} else {
		args = append(args, item.ProviderNodeID)
	}
	_, err := s.DB.Exec(`INSERT INTO auth_runtime(scope,provider_node_id,connection_id,model_ref,route_id,operation_id,operation_version,generation,credential_revision,status,failures,successes,blocked_until,last_error,cause,evidence,confidence,last_attempt,last_success,observed_at_ns)
SELECT ?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,? WHERE `+guard+`
ON CONFLICT(scope,provider_node_id,connection_id,route_id,model_ref,operation_id,operation_version) DO UPDATE SET generation=excluded.generation,credential_revision=excluded.credential_revision,status=excluded.status,failures=excluded.failures,successes=excluded.successes,blocked_until=excluded.blocked_until,last_error=excluded.last_error,cause=excluded.cause,evidence=excluded.evidence,confidence=excluded.confidence,last_attempt=excluded.last_attempt,last_success=excluded.last_success,observed_at_ns=excluded.observed_at_ns
WHERE excluded.observed_at_ns>=auth_runtime.observed_at_ns`, args...)
	return err
}

func (s *Store) AuthRuntimes() ([]AuthRuntime, error) {
	rows, err := s.DB.Query(`SELECT scope,provider_node_id,connection_id,route_id,model_ref,operation_id,operation_version,generation,credential_revision,status,failures,successes,blocked_until,last_error,cause,evidence,confidence,last_attempt,last_success,observed_at_ns FROM auth_runtime ORDER BY scope,provider_node_id,connection_id,route_id,model_ref,operation_id,operation_version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]AuthRuntime, 0)
	for rows.Next() {
		var item AuthRuntime
		var scope string
		var observed int64
		if err := rows.Scan(&scope, &item.ProviderNodeID, &item.ConnectionID, &item.RouteID, &item.ModelRef, &item.OperationRef.ID, &item.OperationRef.ContractVersion, &item.Generation, &item.CredentialRevision, &item.Status, &item.Failures, &item.Successes, &item.BlockedUntil, &item.LastError, &item.Cause, &item.Evidence, &item.Confidence, &item.LastAttempt, &item.LastSuccess, &observed); err != nil {
			return nil, err
		}
		item.Scope = kernel.OutcomeScope(scope)
		if item.OperationRef.ID != "" || item.OperationRef.ContractVersion != 0 {
			item.OperationRef.Kind = "operation"
		}
		item.ObservedAt = time.Unix(0, observed)
		result = append(result, item)
	}
	return result, rows.Err()
}
