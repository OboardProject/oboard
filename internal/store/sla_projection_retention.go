package store

import (
	"context"
	"time"
)

func slaRetentionFloor(at time.Time) int64 {
	floor := at.UTC().Truncate(5 * time.Minute)
	if floor.Before(at) {
		floor = floor.Add(5 * time.Minute)
	}
	return floor.Unix()
}

func (s *Store) deleteSLAEventRetentionBatch(ctx context.Context, query, cutoff string, limit int) (int64, []int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, query+" returning server_id", cutoff, limit)
	if err != nil {
		return 0, nil, err
	}
	ids := map[int64]bool{}
	var count int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, nil, err
		}
		ids[id] = true
		count++
	}
	readErr, closeErr := rows.Err(), rows.Close()
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
	if _, err := tx.ExecContext(ctx, `update sla_projection_state set generation=generation+1,retention_floor=max(retention_floor,?) where id=1`, slaRetentionFloor(at)); err != nil {
		return 0, nil, err
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, err
	}
	result := make([]int64, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	return count, result, nil
}

func (s *Store) purgeSLAProjection(ctx context.Context, cutoff time.Time) (int64, bool, error) {
	var total int64
	for i := 0; i < maintenanceMaxBatches; i++ {
		count, err := s.purgeSLAProjectionBatch(ctx, slaRetentionFloor(cutoff))
		if err != nil {
			return total, total > 0, err
		}
		total += count
		if count < int64(maintenanceBatchSize) {
			return total, false, nil
		}
	}
	return total, true, nil
}

func (s *Store) purgeSLAProjectionBatch(ctx context.Context, floor int64) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `delete from sla_projection_buckets where rowid in (select rowid from sla_projection_buckets where bucket_start<? order by bucket_start limit ?)`, floor, maintenanceBatchSize)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `update sla_projection_state set generation=generation+1,retention_floor=max(retention_floor,?) where id=1`, floor); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}
