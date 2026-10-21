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

const usageEventSQL = `INSERT INTO usage_events(timestamp,logical_model,provider_node_id,external_model,connection_id,status,latency_ms,input_tokens,output_tokens,estimated_cost,request_class,session_id,ttft_ms,output_tokens_per_second,compatibility_fidelity,compatibility_losses_json,compatibility_plan_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
const usageDailySQL = `INSERT INTO usage_daily(date_key,requests,prompt_tokens,completion_tokens,estimated_cost,updated_at) VALUES(?,1,?,?,?,CURRENT_TIMESTAMP) ON CONFLICT(date_key) DO UPDATE SET requests=requests+1,prompt_tokens=prompt_tokens+excluded.prompt_tokens,completion_tokens=completion_tokens+excluded.completion_tokens,estimated_cost=estimated_cost+excluded.estimated_cost,updated_at=CURRENT_TIMESTAMP`
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
