package store

import (
	"fmt"

	"github.com/fm39hz/gobroom/internal/quota"
)

func (s *Store) saveOperationQuotaSnapshot(snapshot quota.Snapshot, metadata []byte) error {
	if snapshot.ProviderNodeID == "" || snapshot.WindowName == "" {
		return fmt.Errorf("operation quota requires provider and window identity")
	}
	_, err := s.DB.Exec(`INSERT INTO operation_quota_snapshots(provider_node_id,connection_id,model_ref,operation_id,operation_version,window_name,generation,authority,observed_at_ns,used,limit_value,remaining,reset_at,source,metadata_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(provider_node_id,connection_id,model_ref,operation_id,operation_version,window_name) DO UPDATE SET generation=excluded.generation,authority=excluded.authority,observed_at_ns=excluded.observed_at_ns,used=excluded.used,limit_value=COALESCE(excluded.limit_value,operation_quota_snapshots.limit_value),remaining=COALESCE(excluded.remaining,operation_quota_snapshots.remaining),reset_at=COALESCE(excluded.reset_at,operation_quota_snapshots.reset_at),source=excluded.source,metadata_json=excluded.metadata_json
WHERE excluded.observed_at_ns>operation_quota_snapshots.observed_at_ns OR (excluded.observed_at_ns=operation_quota_snapshots.observed_at_ns AND (excluded.authority>operation_quota_snapshots.authority OR (excluded.authority=operation_quota_snapshots.authority AND excluded.source>=operation_quota_snapshots.source)))`, snapshot.ProviderNodeID, snapshot.ConnectionID, snapshot.ModelRef, snapshot.OperationRef.ID, snapshot.OperationRef.ContractVersion, snapshot.WindowName, snapshot.Generation, int(snapshot.Authority), snapshot.ObservedAt.UTC().UnixNano(), snapshot.Used, snapshot.Limit, snapshot.Remaining, timeString(snapshot.ResetAt), snapshot.Source, string(metadata))
	return err
}
