package store

import (
	"context"
	"database/sql"
)

func discardChannelNotifications(ctx context.Context, tx *sql.Tx, channelID int64) error {
	ts := now()
	if _, err := tx.ExecContext(ctx, `update notification_deliveries set status='cancelled',error='notification_channel_disabled',updated_at=? where channel_id=? and status in ('pending','failed')`, ts, channelID); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `update notification_broadcast_targets set status='failed',attempts=3,error='notification_channel_disabled',updated_at=? where channel_id=? and status in ('pending','failed') returning broadcast_id`, ts, channelID)
	if err != nil {
		return err
	}
	ids := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids[id] = true
	}
	err = rows.Err()
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	for id := range ids {
		if err := refreshNotificationBroadcastCounts(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}
