package store

import (
	"context"
	"time"
)

func (s *Store) deleteLatencyRetentionBatch(ctx context.Context, query, cutoff string, limit int) (int64, []int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback()
	var live, historical int64
	if err := tx.QueryRowContext(ctx, `select live_cursor,backfill_cursor from latency_rollup_state where id=1`).Scan(&live, &historical); err != nil {
		return 0, nil, err
	}
	rows, err := tx.QueryContext(ctx, query+" returning server_id,id", cutoff, limit)
	if err != nil {
		return 0, nil, err
	}
	ids := make(map[int64]bool)
	var count, missing int64
	for rows.Next() {
		var server, id int64
		if err := rows.Scan(&server, &id); err != nil {
			rows.Close()
			return 0, nil, err
		}
		ids[server] = true
		count++
		if live >= 0 && (id > live || id <= historical) {
			missing++
		}
	}
	readErr := rows.Err()
	closeErr := rows.Close()
	if readErr != nil {
		return 0, nil, readErr
	}
	if closeErr != nil {
		return 0, nil, closeErr
	}
	at, err := time.Parse(time.RFC3339Nano, cutoff)
	if err != nil {
		return 0, nil, err
	}
	floor := at.Unix()
	if at.Nanosecond() > 0 {
		floor++
	}
	if _, err := tx.ExecContext(ctx, `update latency_rollup_state set generation=generation+1,expired_unprocessed=expired_unprocessed+?,retention_floor=max(retention_floor,?),updated_at=? where id=1`, missing, floor, now()); err != nil {
		return 0, nil, err
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, err
	}
	servers := make([]int64, 0, len(ids))
	for id := range ids {
		servers = append(servers, id)
	}
	return count, servers, nil
}

func (s *Store) latencyRollupRetention(ctx context.Context, cutoff time.Time) (int64, bool, error) {
	var deleted int64
	floor := cutoff.Unix()
	if cutoff.Nanosecond() > 0 {
		floor++
	}
	for i := 0; i < maintenanceMaxBatches; i++ {
		count, err := s.deleteLatencySummaryBatch(ctx, floor)
		if err != nil {
			return deleted, deleted > 0, err
		}
		deleted += count
		if count < int64(maintenanceBatchSize) {
			return deleted, false, nil
		}
	}
	return deleted, true, nil
}

func (s *Store) deleteLatencySummaryBatch(ctx context.Context, floor int64) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `delete from latency_rollup_buckets where rowid in (select rowid from latency_rollup_buckets where bucket_start<? order by bucket_start limit ?)`, floor, maintenanceBatchSize)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `update latency_rollup_state set generation=generation+1,retention_floor=max(retention_floor,?),updated_at=? where id=1`, floor, now()); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Store) LatencyRollupShouldYield(ctx context.Context) (bool, error) {
	if time.Now().UnixNano() < s.latencyIngestSlowUntil.Load() {
		return true, nil
	}
	var busy bool
	err := s.db.QueryRowContext(ctx, `select exists(select 1 from agent_tasks indexed by idx_agent_tasks_update_active where type in ('apply_deployment','apply_core_config','apply_config') and status='running')`).Scan(&busy)
	return busy, err
}
