package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

const ServerOfflineNoticeStatusOffline = "offline"
const ServerOfflineNoticeStatusOnline = "online"

func (s *Store) UpsertServerOfflineNotice(ctx context.Context, serverID int64, status string, sinceAt, notifyAt time.Time, groupKey string) error {
	ts := now()
	_, err := s.db.ExecContext(ctx, `insert into server_offline_notices(server_id,status,since_at,notify_at,group_key,notified,updated_at) values(?,?,?,?,?,0,?)
		on conflict(server_id) do update set status=excluded.status,since_at=excluded.since_at,notify_at=excluded.notify_at,group_key=excluded.group_key,notified=0,updated_at=excluded.updated_at`,
		serverID, status, sinceAt.UTC().Format(time.RFC3339Nano), notifyAt.UTC().Format(time.RFC3339Nano), groupKey, ts)
	return err
}

func (s *Store) ExtendOfflineNoticeGroup(ctx context.Context, groupKey string, notifyAt time.Time) (time.Time, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `select coalesce(max(notify_at),'') from server_offline_notices where status='offline' and notified=0 and group_key=?`, groupKey).Scan(&raw)
	if err != nil {
		return time.Time{}, err
	}
	latest := notifyAt.UTC()
	if raw != "" {
		if existing, parseErr := time.Parse(time.RFC3339Nano, raw); parseErr == nil && existing.After(latest) {
			latest = existing
		}
	}
	ts := latest.Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx, `update server_offline_notices set notify_at=?,updated_at=? where status='offline' and notified=0 and group_key=?`, ts, now(), groupKey); err != nil {
		return time.Time{}, err
	}
	return latest, nil
}

func (s *Store) CancelServerOfflineNotice(ctx context.Context, serverID int64) error {
	_, err := s.db.ExecContext(ctx, `delete from server_offline_notices where server_id=?`, serverID)
	return err
}

func (s *Store) ListDueOfflineNotices(ctx context.Context, at time.Time) ([]model.ServerOfflineNotice, error) {
	return s.listDueNotices(ctx, ServerOfflineNoticeStatusOffline, at)
}

func (s *Store) ListDueOnlineNotices(ctx context.Context, at time.Time) ([]model.ServerOfflineNotice, error) {
	return s.listDueNotices(ctx, ServerOfflineNoticeStatusOnline, at)
}

func (s *Store) listDueNotices(ctx context.Context, status string, at time.Time) ([]model.ServerOfflineNotice, error) {
	rows, err := s.db.QueryContext(ctx, `select n.server_id,n.status,n.since_at,n.notify_at,n.group_key,coalesce(s.name,''),s.last_seen_at
		from server_offline_notices n
		join servers s on s.id=n.server_id
		left join server_telemetry t on t.server_id=n.server_id
		where n.status=? and n.notified=0 and n.notify_at<=? and coalesce(t.offline_notify_enabled,1)!=0 order by n.notify_at`, status, at.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ServerOfflineNotice{}
	for rows.Next() {
		var item model.ServerOfflineNotice
		var sinceRaw, notifyRaw, groupKey string
		var lastSeen sql.NullString
		if err := rows.Scan(&item.ServerID, &item.Status, &sinceRaw, &notifyRaw, &groupKey, &item.ServerName, &lastSeen); err != nil {
			return nil, err
		}
		item.SinceAt = parseTime(sinceRaw)
		item.NotifyAt = parseTime(notifyRaw)
		item.GroupKey = groupKey
		item.LastSeenAt = parseNullTime(lastSeen)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) DeleteServerOfflineNotices(ctx context.Context, serverIDs []int64) error {
	if len(serverIDs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range serverIDs {
		if _, err := tx.ExecContext(ctx, `delete from server_offline_notices where server_id=?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) CountRecentSubscriptionPullAbnormal(ctx context.Context, userID int64, since time.Time) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `select count(*) from subscription_pull_audits where user_id=? and requested_at>=? and outcome in ('rejected_invalid_request','denied_suspended','denied_risk')`,
		userID, since.UTC().Format(time.RFC3339Nano)).Scan(&count)
	return count, err
}
