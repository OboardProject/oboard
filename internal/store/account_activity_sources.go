package store

import (
	"context"
	"github.com/OboardProject/oboard/internal/auditrisk"
	"time"
)

func (s *Store) LoadAccountActivitySources(ctx context.Context, userID int64, minute time.Time, sourceVersion string) ([]auditrisk.SourceActivity, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT source,bits,bytes FROM account_activity_v1_source WHERE account=? AND minute=? AND version=? ORDER BY source LIMIT 32`, userID, minute.UTC().Truncate(time.Minute).Unix(), sourceVersion)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []auditrisk.SourceActivity{}
	for rows.Next() {
		var v auditrisk.SourceActivity
		if err := rows.Scan(&v.SourceGroup, &v.Bitmap, &v.Bytes); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
