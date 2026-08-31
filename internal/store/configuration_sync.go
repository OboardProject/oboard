package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const (
	// ConfigurationSyncTriggerRevision is a management write that advanced the
	// desired-state watermark. Unchanged servers stay failed or no-op; they
	// must not allocate a new config_version.
	ConfigurationSyncTriggerRevision = "revision"
	// ConfigurationSyncTriggerOperatorRetry is an explicit retry of a failed
	// sync. The same payload may be requeued at the existing config_version.
	ConfigurationSyncTriggerOperatorRetry = "operator_retry"
	// ConfigurationSyncTriggerAgentDrift means a previously synced Agent no
	// longer matches the last successful payload and must be redeployed.
	ConfigurationSyncTriggerAgentDrift = "agent_drift"
	// ConfigurationSyncMaxPreparationRetries bounds automatic retries of
	// prepare-time failures that never queued an Agent task. A task that
	// already ran and failed stays failed until the operator retries or the
	// desired projection actually changes.
	ConfigurationSyncMaxPreparationRetries = 6
)

// ConfigurationSyncState is the durable bridge between a saved desired state
// and the Agent task queue. The database remains authoritative across process
// restarts; the Controller worker only provides prompt reconciliation.
type ConfigurationSyncState struct {
	ServerID          int64
	WantedRevision    uint64
	WantedDigest      string
	State             string
	LastConfigVersion int64
	LastTaskID        int64
	RetryCount        int
	NextRetryAt       *time.Time
	LastError         string
	TriggerReason     string
	SyncStrategy      string
	ChangedAt         time.Time
	UpdatedAt         time.Time
}

// MarkConfigurationSyncPending records that servers should converge to revision.
// A same-revision call leaves an existing synced/failed/queued state unchanged so
// runtime traffic or quota edits cannot reopen a fleet deployment.
func (s *Store) MarkConfigurationSyncPending(ctx context.Context, revision uint64, serverIDs []int64) ([]int64, error) {
	if revision == 0 {
		return nil, fmt.Errorf("configuration revision must be positive")
	}
	if len(serverIDs) == 0 {
		rows, err := s.db.QueryContext(ctx, `select id from servers where coalesce(agent_id,'')<>'' order by id`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			serverIDs = append(serverIDs, id)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	if len(serverIDs) == 0 {
		return nil, nil
	}
	unique := make(map[int64]struct{}, len(serverIDs))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	digest := fmt.Sprintf("routing:%d", revision)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	for _, serverID := range serverIDs {
		if serverID <= 0 {
			continue
		}
		unique[serverID] = struct{}{}
		_, err := tx.ExecContext(ctx, `
			insert into configuration_sync_states(server_id,wanted_revision,wanted_digest,state,last_config_version,last_task_id,retry_count,next_retry_at,last_error,trigger_reason,sync_strategy,changed_at,updated_at)
			values(?,?,?,'pending',0,0,0,null,'',?,'',?,?)
			on conflict(server_id) do update set
				wanted_revision=case when excluded.wanted_revision>configuration_sync_states.wanted_revision then excluded.wanted_revision else configuration_sync_states.wanted_revision end,
				wanted_digest=case when excluded.wanted_revision>configuration_sync_states.wanted_revision then excluded.wanted_digest else configuration_sync_states.wanted_digest end,
				state=case when excluded.wanted_revision>configuration_sync_states.wanted_revision then 'pending' else configuration_sync_states.state end,
				retry_count=case when excluded.wanted_revision>configuration_sync_states.wanted_revision then 0 else configuration_sync_states.retry_count end,
				next_retry_at=case when excluded.wanted_revision>configuration_sync_states.wanted_revision then null else configuration_sync_states.next_retry_at end,
				last_error=case when excluded.wanted_revision>configuration_sync_states.wanted_revision then '' else configuration_sync_states.last_error end,
				trigger_reason=case when excluded.wanted_revision>configuration_sync_states.wanted_revision then excluded.trigger_reason else configuration_sync_states.trigger_reason end,
				sync_strategy=case when excluded.wanted_revision>configuration_sync_states.wanted_revision then '' else configuration_sync_states.sync_strategy end,
				changed_at=case when excluded.wanted_revision>configuration_sync_states.wanted_revision then excluded.changed_at else configuration_sync_states.changed_at end,
				updated_at=case when excluded.wanted_revision>configuration_sync_states.wanted_revision then excluded.updated_at else configuration_sync_states.updated_at end`, serverID, revision, digest, ConfigurationSyncTriggerRevision, now, now)
		if err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	out := make([]int64, 0, len(unique))
	for id := range unique {
		out = append(out, id)
	}
	return out, nil
}

// EnsureConfigurationSyncRevision repairs the post-commit handoff gap during
// startup. An equal current revision is left untouched, matching
// MarkConfigurationSyncPending, so a Controller restart or a same-revision
// mark (for example a user quota PATCH) does not redeploy synced servers.
func (s *Store) EnsureConfigurationSyncRevision(ctx context.Context, serverID int64, revision uint64) (bool, error) {
	if serverID <= 0 || revision == 0 {
		return false, fmt.Errorf("server id and configuration revision must be positive")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `
		insert into configuration_sync_states(server_id,wanted_revision,wanted_digest,state,last_config_version,last_task_id,retry_count,next_retry_at,last_error,trigger_reason,sync_strategy,changed_at,updated_at)
		values(?,?,?,'pending',0,0,0,null,'',?,'',?,?)
		on conflict(server_id) do update set
			wanted_revision=excluded.wanted_revision,
			wanted_digest=excluded.wanted_digest,
			state='pending',
			retry_count=0,
			next_retry_at=null,
			last_error='',
			trigger_reason=excluded.trigger_reason,
			changed_at=excluded.changed_at,
			updated_at=excluded.updated_at
		where configuration_sync_states.wanted_revision < excluded.wanted_revision`,
		serverID, revision, fmt.Sprintf("routing:%d", revision), ConfigurationSyncTriggerRevision, now, now)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count > 0, err
}

func (s *Store) MarkConfigurationSyncDrift(ctx context.Context, serverID int64, revision uint64) error {
	if serverID <= 0 || revision == 0 {
		return fmt.Errorf("server id and configuration revision must be positive")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `update configuration_sync_states set wanted_revision=case when ?>wanted_revision then ? else wanted_revision end,wanted_digest=case when ?>wanted_revision then ? else wanted_digest end,state='pending',trigger_reason=?,next_retry_at=null,last_error='',changed_at=?,updated_at=? where server_id=? and state!='failed'`, revision, revision, revision, fmt.Sprintf("routing:%d", revision), ConfigurationSyncTriggerAgentDrift, now, now, serverID)
	return err
}

// MarkConfigurationSyncWaiting returns a claimed desired state to the pending
// queue without counting a retry or presenting it as a failure. It is used for
// prerequisites that are already progressing elsewhere, such as managed
// certificate issuance.
func (s *Store) MarkConfigurationSyncWaiting(ctx context.Context, serverID int64, revision uint64, retryAt time.Time, reason string) error {
	if len(reason) > 2000 {
		reason = reason[:2000]
	}
	_, err := s.db.ExecContext(ctx, `update configuration_sync_states set state='pending',next_retry_at=?,last_error=?,updated_at=? where server_id=? and wanted_revision=? and state='preparing'`, retryAt.UTC().Format(time.RFC3339Nano), strings.TrimSpace(reason), time.Now().UTC().Format(time.RFC3339Nano), serverID, revision)
	return err
}

func (s *Store) ListConfigurationSyncStates(ctx context.Context, now time.Time) ([]ConfigurationSyncState, error) {
	formattedNow := now.UTC().Format(time.RFC3339Nano)
	query := fmt.Sprintf(`select server_id,wanted_revision,wanted_digest,state,last_config_version,last_task_id,retry_count,next_retry_at,last_error,ifnull(trigger_reason,''),ifnull(sync_strategy,''),changed_at,updated_at from configuration_sync_states where (state='pending' and (next_retry_at is null or next_retry_at<=?)) or (state='failed' and last_config_version=0 and retry_count<%d and (next_retry_at is null or next_retry_at<=?)) order by wanted_revision,server_id`, ConfigurationSyncMaxPreparationRetries)
	rows, err := s.db.QueryContext(ctx, query, formattedNow, formattedNow)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ConfigurationSyncState{}
	for rows.Next() {
		item, err := scanConfigurationSyncState(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// MarkConfigurationSyncNoop records that an automatic reconciler compared
// DeploymentProjectionDigest and found no data-plane change. The wanted
// revision is accepted without creating an Agent task.
func (s *Store) MarkConfigurationSyncNoop(ctx context.Context, serverID int64, revision uint64, digest string) error {
	if serverID <= 0 || revision == 0 {
		return fmt.Errorf("server id and configuration revision must be positive")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `update configuration_sync_states set state='synced',wanted_digest=?,sync_strategy='semantic_noop',last_error='',next_retry_at=null,updated_at=? where server_id=? and wanted_revision=?`, strings.TrimSpace(digest), now, serverID, revision)
	return err
}

func (s *Store) UpdateConfigurationSyncWantedDigest(ctx context.Context, serverID int64, digest string) error {
	if serverID <= 0 {
		return fmt.Errorf("server id must be positive")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `update configuration_sync_states set wanted_digest=?,updated_at=? where server_id=? and state='synced'`, strings.TrimSpace(digest), now, serverID)
	return err
}

func (s *Store) RecoverConfigurationSyncStates(ctx context.Context) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx, `update configuration_sync_states set state='failed',next_retry_at=null,updated_at=? where state in ('preparing','queued') and last_config_version>0 and exists (select 1 from agent_tasks where agent_tasks.server_id=configuration_sync_states.server_id and agent_tasks.type in ('apply_deployment','apply_core_config') and agent_tasks.config_version=configuration_sync_states.last_config_version and agent_tasks.status='failed') and not exists (select 1 from agent_tasks where agent_tasks.server_id=configuration_sync_states.server_id and agent_tasks.type in ('apply_deployment','apply_core_config') and agent_tasks.config_version=configuration_sync_states.last_config_version and agent_tasks.status in ('pending','running'))`, now); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `update configuration_sync_states set state='pending',next_retry_at=null,updated_at=? where state in ('preparing','queued') and not exists (select 1 from agent_tasks where agent_tasks.server_id=configuration_sync_states.server_id and agent_tasks.type in ('apply_deployment','apply_core_config') and agent_tasks.config_version=configuration_sync_states.last_config_version and agent_tasks.status in ('pending','running','failed'))`, now)
	return err
}

func (s *Store) ClaimConfigurationSync(ctx context.Context, serverID int64, revision uint64) (bool, error) {
	result, err := s.db.ExecContext(ctx, `update configuration_sync_states set state='preparing',updated_at=? where server_id=? and wanted_revision=? and state in ('pending','failed')`, time.Now().UTC().Format(time.RFC3339Nano), serverID, revision)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (s *Store) MarkConfigurationSyncPreparationFailure(ctx context.Context, serverID int64, revision uint64, resultError string) error {
	var retryCount int
	if err := s.db.QueryRowContext(ctx, `select retry_count from configuration_sync_states where server_id=? and wanted_revision=?`, serverID, revision).Scan(&retryCount); err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}
	retryCount++
	backoff := time.Second * time.Duration(1<<minInt(retryCount-1, 5))
	if backoff > 30*time.Second {
		backoff = 30 * time.Second
	}
	if len(resultError) > 2000 {
		resultError = resultError[:2000]
	}
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `update configuration_sync_states set state='failed',retry_count=?,next_retry_at=?,last_error=?,updated_at=? where server_id=? and wanted_revision=?`, retryCount, now.Add(backoff).Format(time.RFC3339Nano), strings.TrimSpace(resultError), now.Format(time.RFC3339Nano), serverID, revision)
	return err
}

func (s *Store) MarkConfigurationSyncQueued(ctx context.Context, serverID int64, revision uint64, configVersion, taskID int64, payloadDigest string) error {
	_, err := s.db.ExecContext(ctx, `update configuration_sync_states set state='queued',wanted_digest=?,last_config_version=?,last_task_id=?,next_retry_at=null,last_error='',updated_at=? where server_id=? and wanted_revision=?`, strings.TrimSpace(payloadDigest), configVersion, taskID, time.Now().UTC().Format(time.RFC3339Nano), serverID, revision)
	return err
}

// MarkConfigurationSyncUnchangedFailure restores a claimed row to failed when
// the current projection matches the last failed apply_deployment. It does not
// increment retry_count or allocate a later retry, so the operator-facing
// blocked state stays stable until an explicit retry or a real change.
func (s *Store) MarkConfigurationSyncUnchangedFailure(ctx context.Context, serverID int64, revision uint64, resultError string) error {
	if len(resultError) > 2000 {
		resultError = resultError[:2000]
	}
	resultError = strings.TrimSpace(resultError)
	_, err := s.db.ExecContext(ctx, `update configuration_sync_states set state='failed',next_retry_at=null,last_error=case when ?='' then last_error else ? end,updated_at=? where server_id=? and wanted_revision=? and state='preparing'`, resultError, resultError, time.Now().UTC().Format(time.RFC3339Nano), serverID, revision)
	return err
}

func (s *Store) MarkConfigurationSyncResult(ctx context.Context, serverID, configVersion int64, succeeded bool, resultError string) error {
	now := time.Now().UTC()
	if succeeded {
		_, err := s.db.ExecContext(ctx, `update configuration_sync_states set state='synced',last_error='',next_retry_at=null,updated_at=? where server_id=? and last_config_version=? and state in ('queued','running')`, now.Format(time.RFC3339Nano), serverID, configVersion)
		return err
	}
	var retryCount int
	if err := s.db.QueryRowContext(ctx, `select retry_count from configuration_sync_states where server_id=? and last_config_version=? and state in ('queued','running')`, serverID, configVersion).Scan(&retryCount); err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}
	retryCount++
	backoff := time.Second * time.Duration(1<<minInt(retryCount-1, 5))
	if backoff > 30*time.Second {
		backoff = 30 * time.Second
	}
	if len(resultError) > 2000 {
		resultError = resultError[:2000]
	}
	_, err := s.db.ExecContext(ctx, `update configuration_sync_states set state='failed',retry_count=?,next_retry_at=?,last_error=?,updated_at=? where server_id=? and last_config_version=? and state in ('queued','running')`, retryCount, now.Add(backoff).Format(time.RFC3339Nano), strings.TrimSpace(resultError), now.Format(time.RFC3339Nano), serverID, configVersion)
	return err
}

func (s *Store) RetryFailedConfigurationSync(ctx context.Context, serverIDs []int64) (int64, error) {
	args := []any{ConfigurationSyncTriggerOperatorRetry, time.Now().UTC().Format(time.RFC3339Nano)}
	query := `update configuration_sync_states set state='pending',retry_count=0,next_retry_at=null,last_error='',trigger_reason=?,updated_at=? where state='failed'`
	if len(serverIDs) > 0 {
		placeholders := make([]string, 0, len(serverIDs))
		for _, id := range serverIDs {
			if id <= 0 {
				continue
			}
			placeholders = append(placeholders, "?")
			args = append(args, id)
		}
		if len(placeholders) == 0 {
			return 0, nil
		}
		query += ` and server_id in (` + strings.Join(placeholders, ",") + `)`
	}
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) ListAllConfigurationSyncStates(ctx context.Context) ([]ConfigurationSyncState, error) {
	rows, err := s.db.QueryContext(ctx, `select server_id,wanted_revision,wanted_digest,state,last_config_version,last_task_id,retry_count,next_retry_at,last_error,ifnull(trigger_reason,''),ifnull(sync_strategy,''),changed_at,updated_at from configuration_sync_states order by server_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ConfigurationSyncState{}
	for rows.Next() {
		item, err := scanConfigurationSyncState(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ConfigurationSyncState(ctx context.Context, serverID int64) (ConfigurationSyncState, error) {
	row := s.db.QueryRowContext(ctx, `select server_id,wanted_revision,wanted_digest,state,last_config_version,last_task_id,retry_count,next_retry_at,last_error,ifnull(trigger_reason,''),ifnull(sync_strategy,''),changed_at,updated_at from configuration_sync_states where server_id=?`, serverID)
	return scanConfigurationSyncState(row)
}

type configurationSyncScanner interface {
	Scan(dest ...any) error
}

func scanConfigurationSyncState(scanner configurationSyncScanner) (ConfigurationSyncState, error) {
	var item ConfigurationSyncState
	var nextRetry, changedAt, updatedAt sql.NullString
	if err := scanner.Scan(&item.ServerID, &item.WantedRevision, &item.WantedDigest, &item.State, &item.LastConfigVersion, &item.LastTaskID, &item.RetryCount, &nextRetry, &item.LastError, &item.TriggerReason, &item.SyncStrategy, &changedAt, &updatedAt); err != nil {
		return ConfigurationSyncState{}, err
	}
	var err error
	if nextRetry.Valid && strings.TrimSpace(nextRetry.String) != "" {
		parsed, parseErr := parseConfigurationSyncTime(nextRetry.String)
		if parseErr != nil {
			return ConfigurationSyncState{}, parseErr
		}
		item.NextRetryAt = &parsed
	}
	item.ChangedAt, err = parseConfigurationSyncTime(changedAt.String)
	if err != nil {
		return ConfigurationSyncState{}, err
	}
	item.UpdatedAt, err = parseConfigurationSyncTime(updatedAt.String)

	return item, err
}

func parseConfigurationSyncTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid configuration sync timestamp: %w", err)
	}
	return parsed, nil
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
