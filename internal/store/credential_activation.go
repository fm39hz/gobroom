package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
)

const (
	ActivationPending      = "pending"
	ActivationActive       = "active"
	ActivationExhausted    = "exhausted"
	ActivationSuperseded   = "superseded"
	defaultActivationLimit = 128
	maxActivationLimit     = 1024
)

var activationErrorCodePattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{0,64}$`)

type CredentialCommit struct {
	ConnectionID   string
	CredentialType string
	Revision       string
	Owner          CredentialOwner `json:"-"`
}

func (c CredentialCommit) Validate() error {
	if c.ConnectionID == "" || c.CredentialType == "" || c.Revision == "" {
		return fmt.Errorf("credential commit requires connection, credential type, and revision")
	}
	return c.Owner.Validate()
}

type CredentialActivationProgress struct {
	RoutingReset     bool   `json:"routingReset"`
	ControlVersion   uint64 `json:"controlVersion"`
	ControlPublished bool   `json:"controlPublished"`
	VersionPersisted bool   `json:"versionPersisted"`
	ServingVersion   uint64 `json:"servingVersion"`
	ServingPublished bool   `json:"servingPublished"`
}

type CredentialActivation struct {
	Commit      CredentialCommit             `json:"-"`
	Status      string                       `json:"status"`
	Attempts    int                          `json:"attempts"`
	NextAttempt time.Time                    `json:"nextAttempt"`
	ErrorCode   string                       `json:"errorCode"`
	Progress    CredentialActivationProgress `json:"progress"`
}

func (a CredentialActivation) Validate() error {
	if err := a.Commit.Validate(); err != nil {
		return err
	}
	switch a.Status {
	case ActivationPending, ActivationActive, ActivationExhausted, ActivationSuperseded:
	default:
		return fmt.Errorf("invalid credential activation status")
	}
	if a.Attempts < 0 || !activationErrorCodePattern.MatchString(a.ErrorCode) {
		return fmt.Errorf("invalid credential activation metadata")
	}
	if a.Status == ActivationActive && (!a.Progress.RoutingReset || !a.Progress.ControlPublished || !a.Progress.VersionPersisted || !a.Progress.ServingPublished || a.Progress.ControlVersion == 0 || a.Progress.ControlVersion != a.Progress.ServingVersion || a.ErrorCode != "") {
		return fmt.Errorf("active credential activation requires complete publication evidence")
	}
	return nil
}

// CompareAndSwapConnectionCredential stores the new credential and its durable
// activation work as one transaction. The returned commit contains no secret.
func (s *Store) CompareAndSwapConnectionCredential(ctx context.Context, expected Credential, secret string) (CredentialCommit, error) {
	if expected.ID == "" || expected.Type == "" || expected.Revision == "" || secret == "" {
		return CredentialCommit{}, fmt.Errorf("connection credential revision and replacement secret are required")
	}
	if err := expected.Owner.Validate(); err != nil {
		return CredentialCommit{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return CredentialCommit{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE connections SET secret_ref=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND credential_type=? AND enabled=1 AND provider_node_id=? AND EXISTS(SELECT 1 FROM provider_nodes p WHERE p.id=connections.provider_node_id AND p.definition_id=? AND p.definition_contract_version=?) AND EXISTS(SELECT 1 FROM connection_credential_revisions r WHERE r.connection_id=connections.id AND r.revision=?)`, secret, expected.ID, expected.Type, expected.Owner.ProviderNodeID, expected.Owner.Definition.ID, expected.Owner.Definition.ContractVersion, expected.Revision)
	if err != nil {
		return CredentialCommit{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return CredentialCommit{}, err
	}
	if changed == 0 {
		return CredentialCommit{}, fmt.Errorf("connection %q credential or auth owner changed, was disabled or removed during authorization", expected.ID)
	}
	updated, ok := scanCredential(tx.QueryRowContext(ctx, `SELECT c.id,c.credential_type,c.secret_ref,r.revision,p.id,p.definition_id,p.definition_contract_version FROM connections c JOIN connection_credential_revisions r ON r.connection_id=c.id JOIN provider_nodes p ON p.id=c.provider_node_id WHERE c.id=? AND c.enabled=1`, expected.ID))
	if !ok {
		return CredentialCommit{}, fmt.Errorf("read updated credential commit")
	}
	commit := CredentialCommit{ConnectionID: updated.ID, CredentialType: updated.Type, Revision: updated.Revision, Owner: updated.Owner}
	if err := commit.Validate(); err != nil {
		return CredentialCommit{}, err
	}
	progress, err := json.Marshal(CredentialActivationProgress{})
	if err != nil {
		return CredentialCommit{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO credential_activations(connection_id,provider_node_id,definition_id,definition_contract_version,credential_type,revision,status,attempts,next_attempt_ns,error_code,progress_json,updated_at) VALUES(?,?,?,?,?,?,?,0,?,'',?,CURRENT_TIMESTAMP) ON CONFLICT(connection_id) DO UPDATE SET provider_node_id=excluded.provider_node_id,definition_id=excluded.definition_id,definition_contract_version=excluded.definition_contract_version,credential_type=excluded.credential_type,revision=excluded.revision,status=excluded.status,attempts=0,next_attempt_ns=excluded.next_attempt_ns,error_code='',progress_json=excluded.progress_json,updated_at=CURRENT_TIMESTAMP`, commit.ConnectionID, commit.Owner.ProviderNodeID, commit.Owner.Definition.ID, commit.Owner.Definition.ContractVersion, commit.CredentialType, commit.Revision, ActivationPending, activationTimeValue(time.Now().UTC()), string(progress))
	if err != nil {
		return CredentialCommit{}, err
	}
	if err := tx.Commit(); err != nil {
		return CredentialCommit{}, err
	}
	return commit, nil
}

func (s *Store) CredentialActivation(ctx context.Context, id string) (CredentialActivation, bool, error) {
	return scanCredentialActivation(s.DB.QueryRowContext(ctx, activationSelect+` WHERE a.connection_id=?`, id))
}

func (s *Store) CredentialActivations(ctx context.Context, limit int) ([]CredentialActivation, error) {
	limit = boundedActivationLimit(limit)
	rows, err := s.DB.QueryContext(ctx, activationSelect+` ORDER BY CASE WHEN a.status IN ('pending','exhausted') THEN 0 ELSE 1 END,a.next_attempt_ns,a.connection_id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return readCredentialActivations(rows)
}

func (s *Store) PendingCredentialActivations(ctx context.Context, now time.Time, limit int) ([]CredentialActivation, error) {
	limit = boundedActivationLimit(limit)
	rows, err := s.DB.QueryContext(ctx, activationSelect+` WHERE a.status=? AND a.next_attempt_ns<=? ORDER BY a.next_attempt_ns,a.connection_id LIMIT ?`, ActivationPending, activationTimeValue(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return readCredentialActivations(rows)
}

func (s *Store) NextCredentialActivation(ctx context.Context) (time.Time, bool, error) {
	var raw int64
	err := s.DB.QueryRowContext(ctx, `SELECT next_attempt_ns FROM credential_activations WHERE status=? ORDER BY next_attempt_ns,connection_id LIMIT 1`, ActivationPending).Scan(&raw)
	if err == sql.ErrNoRows {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return activationTimeFromValue(raw), true, nil
}

// SaveCredentialActivation fences every write by captured row key. Only a
// superseded status may update that row after the live credential has rotated.
func (s *Store) SaveCredentialActivation(ctx context.Context, a CredentialActivation) (bool, error) {
	if err := a.Validate(); err != nil {
		return false, err
	}
	progress, err := json.Marshal(a.Progress)
	if err != nil {
		return false, err
	}
	query := `UPDATE credential_activations SET status=?,attempts=?,next_attempt_ns=?,error_code=?,progress_json=?,updated_at=CURRENT_TIMESTAMP WHERE connection_id=? AND provider_node_id=? AND definition_id=? AND definition_contract_version=? AND credential_type=? AND revision=?`
	args := []any{a.Status, a.Attempts, activationTimeValue(a.NextAttempt), a.ErrorCode, string(progress), a.Commit.ConnectionID, a.Commit.Owner.ProviderNodeID, a.Commit.Owner.Definition.ID, a.Commit.Owner.Definition.ContractVersion, a.Commit.CredentialType, a.Commit.Revision}
	if a.Status != ActivationSuperseded {
		query += ` AND EXISTS(SELECT 1 FROM connections c JOIN connection_credential_revisions r ON r.connection_id=c.id JOIN provider_nodes p ON p.id=c.provider_node_id WHERE c.id=credential_activations.connection_id AND c.enabled=1 AND c.credential_type=? AND c.provider_node_id=? AND p.definition_id=? AND p.definition_contract_version=? AND r.revision=?)`
		args = append(args, a.Commit.CredentialType, a.Commit.Owner.ProviderNodeID, a.Commit.Owner.Definition.ID, a.Commit.Owner.Definition.ContractVersion, a.Commit.Revision)
	}
	result, err := s.DB.ExecContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	return changed == 1, err
}

func (s *Store) ResetCredentialRoutingState(ctx context.Context, commit CredentialCommit) (bool, error) {
	if err := commit.Validate(); err != nil {
		return false, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var found int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM connections c JOIN connection_credential_revisions r ON r.connection_id=c.id JOIN provider_nodes p ON p.id=c.provider_node_id WHERE c.id=? AND c.enabled=1 AND c.credential_type=? AND c.provider_node_id=? AND p.definition_id=? AND p.definition_contract_version=? AND r.revision=?`, commit.ConnectionID, commit.CredentialType, commit.Owner.ProviderNodeID, commit.Owner.Definition.ID, commit.Owner.Definition.ContractVersion, commit.Revision).Scan(&found)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := resetConnectionRoutingStateTx(tx, commit.ConnectionID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

const activationSelect = `SELECT a.provider_node_id,a.definition_id,a.definition_contract_version,a.connection_id,a.credential_type,a.revision,a.status,a.attempts,a.next_attempt_ns,a.error_code,a.progress_json FROM credential_activations a`

func scanCredentialActivation(row *sql.Row) (CredentialActivation, bool, error) {
	var a CredentialActivation
	var definitionID, progress string
	var nextAttempt int64
	var contractVersion uint64
	if err := row.Scan(&a.Commit.Owner.ProviderNodeID, &definitionID, &contractVersion, &a.Commit.ConnectionID, &a.Commit.CredentialType, &a.Commit.Revision, &a.Status, &a.Attempts, &nextAttempt, &a.ErrorCode, &progress); err != nil {
		if err == sql.ErrNoRows {
			return CredentialActivation{}, false, nil
		}
		return CredentialActivation{}, false, err
	}
	a.Commit.Owner.Definition = extensionsRef(definitionID, contractVersion)
	a.NextAttempt = activationTimeFromValue(nextAttempt)
	if err := json.Unmarshal([]byte(progress), &a.Progress); err != nil {
		return CredentialActivation{}, false, err
	}
	return a, true, a.Validate()
}

func readCredentialActivations(rows *sql.Rows) ([]CredentialActivation, error) {
	items := make([]CredentialActivation, 0)
	for rows.Next() {
		var a CredentialActivation
		var definitionID, progress string
		var nextAttempt int64
		var contractVersion uint64
		if err := rows.Scan(&a.Commit.Owner.ProviderNodeID, &definitionID, &contractVersion, &a.Commit.ConnectionID, &a.Commit.CredentialType, &a.Commit.Revision, &a.Status, &a.Attempts, &nextAttempt, &a.ErrorCode, &progress); err != nil {
			return nil, err
		}
		a.Commit.Owner.Definition = extensionsRef(definitionID, contractVersion)
		a.NextAttempt = activationTimeFromValue(nextAttempt)
		if err := json.Unmarshal([]byte(progress), &a.Progress); err != nil {
			return nil, err
		}
		if err := a.Validate(); err != nil {
			return nil, err
		}
		items = append(items, a)
	}
	return items, rows.Err()
}

func activationTimeValue(at time.Time) int64 {
	if at.IsZero() {
		return 0
	}
	return at.UnixNano()
}

func activationTimeFromValue(value int64) time.Time {
	if value == 0 {
		return time.Time{}
	}
	return time.Unix(0, value).UTC()
}

func boundedActivationLimit(limit int) int {
	if limit <= 0 {
		return defaultActivationLimit
	}
	if limit > maxActivationLimit {
		return maxActivationLimit
	}
	return limit
}

func extensionsRef(id string, version uint64) extensions.Ref {
	return extensions.Ref{Kind: "provider-definition", ID: id, ContractVersion: version}
}
