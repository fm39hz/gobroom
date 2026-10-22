package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// Fixed statements are compiled by database/sql per physical connection. This
// caches query plans, never credential contents or live authority decisions.
type runtimeStatements struct {
	credentialByID, credentialBinding, usageEvent, usageDaily, operationRuntime *sql.Stmt
}

const credentialByIDSQL = `SELECT c.id,c.credential_type,c.secret_ref,r.revision,cp.revision,p.id,p.definition_id,p.definition_contract_version FROM connections c JOIN connection_credential_revisions r ON r.connection_id=c.id JOIN connection_principals cp ON cp.connection_id=c.id JOIN provider_nodes p ON p.id=c.provider_node_id WHERE c.id=? AND c.enabled=1`

const credentialBindingSQL = `SELECT c.id,c.credential_type,r.revision,cp.revision,p.id,p.definition_id,p.definition_contract_version
FROM connections c
JOIN connection_credential_revisions r ON r.connection_id=c.id
JOIN connection_principals cp ON cp.connection_id=c.id
JOIN provider_nodes p ON p.id=c.provider_node_id
WHERE c.id=? AND c.enabled=1`

const usageEventSQL = `INSERT INTO usage_events(timestamp,logical_model,provider_node_id,external_model,connection_id,status,latency_ms,input_tokens,output_tokens,cached_read_tokens,cached_write_tokens,reasoning_tokens,estimated_cost,accounting_issues_json,request_class,session_id,ttft_ms,output_tokens_per_second,compatibility_fidelity,compatibility_losses_json,compatibility_plan_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
const usageDailySQL = `INSERT INTO usage_daily(date_key,attempts,incomplete_attempts,input_tokens_sum,input_tokens_reported,output_tokens_sum,output_tokens_reported,cached_read_tokens_sum,cached_read_tokens_reported,cached_write_tokens_sum,cached_write_tokens_reported,reasoning_tokens_sum,reasoning_tokens_reported,estimated_cost_sum,estimated_cost_reported,updated_at)
VALUES(?,1,?,COALESCE(?,0),CASE WHEN ? IS NULL THEN 0 ELSE 1 END,COALESCE(?,0),CASE WHEN ? IS NULL THEN 0 ELSE 1 END,COALESCE(?,0),CASE WHEN ? IS NULL THEN 0 ELSE 1 END,COALESCE(?,0),CASE WHEN ? IS NULL THEN 0 ELSE 1 END,COALESCE(?,0),CASE WHEN ? IS NULL THEN 0 ELSE 1 END,COALESCE(?,0),CASE WHEN ? IS NULL THEN 0 ELSE 1 END,CURRENT_TIMESTAMP)
ON CONFLICT(date_key) DO UPDATE SET attempts=attempts+1,incomplete_attempts=incomplete_attempts+excluded.incomplete_attempts,input_tokens_sum=input_tokens_sum+excluded.input_tokens_sum,input_tokens_reported=input_tokens_reported+excluded.input_tokens_reported,output_tokens_sum=output_tokens_sum+excluded.output_tokens_sum,output_tokens_reported=output_tokens_reported+excluded.output_tokens_reported,cached_read_tokens_sum=cached_read_tokens_sum+excluded.cached_read_tokens_sum,cached_read_tokens_reported=cached_read_tokens_reported+excluded.cached_read_tokens_reported,cached_write_tokens_sum=cached_write_tokens_sum+excluded.cached_write_tokens_sum,cached_write_tokens_reported=cached_write_tokens_reported+excluded.cached_write_tokens_reported,reasoning_tokens_sum=reasoning_tokens_sum+excluded.reasoning_tokens_sum,reasoning_tokens_reported=reasoning_tokens_reported+excluded.reasoning_tokens_reported,estimated_cost_sum=estimated_cost_sum+excluded.estimated_cost_sum,estimated_cost_reported=estimated_cost_reported+excluded.estimated_cost_reported,updated_at=CURRENT_TIMESTAMP`
const operationRuntimeSQL = `INSERT INTO operation_runtime(connection_id,model_ref,operation_id,operation_version,status,failures,successes,blocked_until,last_error,cause,evidence,confidence,generation,last_attempt,last_success,observed_at_ns)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(connection_id,model_ref,operation_id,operation_version) DO UPDATE SET status=excluded.status,failures=excluded.failures,successes=excluded.successes,blocked_until=excluded.blocked_until,last_error=excluded.last_error,cause=excluded.cause,evidence=excluded.evidence,confidence=excluded.confidence,generation=excluded.generation,last_attempt=excluded.last_attempt,last_success=excluded.last_success,observed_at_ns=excluded.observed_at_ns
WHERE excluded.observed_at_ns>=operation_runtime.observed_at_ns`

func (s *Store) prepareRuntimeStatements() error {
	for _, item := range []struct {
		query       string
		destination **sql.Stmt
	}{
		{credentialByIDSQL, &s.runtime.credentialByID},
		{credentialBindingSQL, &s.runtime.credentialBinding},
		{usageEventSQL, &s.runtime.usageEvent},
		{usageDailySQL, &s.runtime.usageDaily},
		{operationRuntimeSQL, &s.runtime.operationRuntime},
	} {
		statement, err := s.DB.Prepare(item.query)
		if err != nil {
			return fmt.Errorf("prepare runtime SQL: %w", err)
		}
		*item.destination = statement
	}
	return nil
}

func (s *runtimeStatements) close() error {
	var result error
	for _, statement := range []*sql.Stmt{s.credentialByID, s.credentialBinding, s.usageEvent, s.usageDaily, s.operationRuntime} {
		if statement != nil {
			result = errors.Join(result, statement.Close())
		}
	}
	return result
}
