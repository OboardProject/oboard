package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

type BroadcastRecipient struct {
	UserID   int64
	Bindings []model.TelegramBinding
}

func (s *Store) CreateNotificationBroadcast(ctx context.Context, item *model.NotificationBroadcast, recipients []BroadcastRecipient) (bool, error) {
	if item == nil || item.ActorUserID <= 0 || item.IdempotencyKey == "" {
		return false, errors.New("invalid notification broadcast")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var existingID int64
	if err := tx.QueryRowContext(ctx, `select id from notification_broadcasts where idempotency_key=?`, item.IdempotencyKey).Scan(&existingID); err == nil {
		stored, getErr := getNotificationBroadcastQuery(ctx, tx, existingID)
		if getErr != nil {
			return false, getErr
		}
		*item = *stored
		return false, tx.Commit()
	} else if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	ts := now()
	item.Status = "pending"
	item.CreatedAt = parseTime(ts)
	item.RecipientCount = len(recipients)
	res, err := tx.ExecContext(ctx, `insert into notification_broadcasts(actor_user_id,actor_name,title,body,filter_json,idempotency_key,status,recipient_count,created_at) values(?,?,?,?,?,?,'pending',?,?)`, item.ActorUserID, item.ActorName, item.Title, item.Body, item.FilterJSON, item.IdempotencyKey, item.RecipientCount, ts)
	if err != nil {
		return false, err
	}
	item.ID, _ = res.LastInsertId()
	for _, recipient := range recipients {
		if len(recipient.Bindings) == 0 {
			if _, err := tx.ExecContext(ctx, `insert into notification_broadcast_targets(broadcast_id,user_id,status,attempts,error,next_attempt_at,created_at,updated_at) values(?,?,'failed',3,'telegram_not_bound',?,?,?)`, item.ID, recipient.UserID, ts, ts, ts); err != nil {
				return false, err
			}
			continue
		}
		for _, binding := range recipient.Bindings {
			if _, err := tx.ExecContext(ctx, `insert into notification_broadcast_targets(broadcast_id,user_id,binding_id,channel_id,chat_id,status,next_attempt_at,created_at,updated_at) values(?,?,?,?,?,'pending',?,?,?)`, item.ID, recipient.UserID, binding.ID, binding.ChannelID, binding.ChatID, ts, ts, ts); err != nil {
				return false, err
			}
		}
	}
	if err := refreshNotificationBroadcastCounts(ctx, tx, item.ID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	stored, err := s.GetNotificationBroadcast(ctx, item.ID)
	if err == nil {
		*item = *stored
	}
	return true, err
}

func getNotificationBroadcastQuery(ctx context.Context, q rowQueryer, id int64) (*model.NotificationBroadcast, error) {
	var item model.NotificationBroadcast
	var created string
	var completed sql.NullString
	err := q.QueryRowContext(ctx, `select id,actor_user_id,actor_name,title,body,filter_json,idempotency_key,status,recipient_count,success_count,failure_count,created_at,completed_at from notification_broadcasts where id=?`, id).Scan(&item.ID, &item.ActorUserID, &item.ActorName, &item.Title, &item.Body, &item.FilterJSON, &item.IdempotencyKey, &item.Status, &item.RecipientCount, &item.SuccessCount, &item.FailureCount, &created, &completed)
	if err != nil {
		return nil, err
	}
	item.CreatedAt = parseTime(created)
	item.CompletedAt = parseNullTime(completed)
	return &item, nil
}

type rowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) GetNotificationBroadcast(ctx context.Context, id int64) (*model.NotificationBroadcast, error) {
	return getNotificationBroadcastQuery(ctx, s.db, id)
}

func (s *Store) ListNotificationBroadcasts(ctx context.Context, limit int) ([]model.NotificationBroadcast, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `select id,actor_user_id,actor_name,title,body,filter_json,idempotency_key,status,recipient_count,success_count,failure_count,created_at,completed_at from notification_broadcasts order by id desc limit ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.NotificationBroadcast{}
	for rows.Next() {
		var item model.NotificationBroadcast
		var created string
		var completed sql.NullString
		if err := rows.Scan(&item.ID, &item.ActorUserID, &item.ActorName, &item.Title, &item.Body, &item.FilterJSON, &item.IdempotencyKey, &item.Status, &item.RecipientCount, &item.SuccessCount, &item.FailureCount, &created, &completed); err != nil {
			return nil, err
		}
		item.CreatedAt = parseTime(created)
		item.CompletedAt = parseNullTime(completed)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ListPendingNotificationBroadcastTargets(ctx context.Context, at time.Time, limit int) ([]model.NotificationBroadcastTarget, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `select t.id,t.broadcast_id,t.user_id,t.binding_id,t.channel_id,t.chat_id,t.status,t.attempts,t.error,t.next_attempt_at,t.sent_at,
		b.actor_user_id,b.actor_name,b.title,b.body,b.filter_json,b.idempotency_key,b.status,b.recipient_count,b.success_count,b.failure_count,b.created_at,b.completed_at,
		c.owner_user_id,coalesce(u.username,''),c.name,c.type,c.enabled,c.events,c.config_json,c.templates_json,c.created_at,c.updated_at
		from notification_broadcast_targets t join notification_broadcasts b on b.id=t.broadcast_id join notification_channels c on c.id=t.channel_id left join users u on u.id=c.owner_user_id
		where t.status in ('pending','failed') and t.attempts<3 and t.next_attempt_at<=? and c.enabled=1 order by t.id limit ?`, at.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.NotificationBroadcastTarget{}
	for rows.Next() {
		var item model.NotificationBroadcastTarget
		var bindingID, channelID, chatID sql.NullInt64
		var next, broadcastCreated, channelCreated, channelUpdated string
		var sent, completed sql.NullString
		var enabled int
		if err := rows.Scan(&item.ID, &item.BroadcastID, &item.UserID, &bindingID, &channelID, &chatID, &item.Status, &item.Attempts, &item.Error, &next, &sent,
			&item.Broadcast.ActorUserID, &item.Broadcast.ActorName, &item.Broadcast.Title, &item.Broadcast.Body, &item.Broadcast.FilterJSON, &item.Broadcast.IdempotencyKey, &item.Broadcast.Status, &item.Broadcast.RecipientCount, &item.Broadcast.SuccessCount, &item.Broadcast.FailureCount, &broadcastCreated, &completed,
			&item.Channel.OwnerUserID, &item.Channel.OwnerUsername, &item.Channel.Name, &item.Channel.Type, &enabled, &item.Channel.Events, &item.Channel.ConfigJSON, &item.Channel.TemplatesJSON, &channelCreated, &channelUpdated); err != nil {
			return nil, err
		}
		if bindingID.Valid {
			item.BindingID = &bindingID.Int64
		}
		if channelID.Valid {
			item.ChannelID = &channelID.Int64
			item.Channel.ID = channelID.Int64
		}
		if chatID.Valid {
			item.ChatID = &chatID.Int64
		}
		item.NextAttemptAt = parseTime(next)
		item.SentAt = parseNullTime(sent)
		item.Broadcast.CreatedAt = parseTime(broadcastCreated)
		item.Broadcast.CompletedAt = parseNullTime(completed)
		item.Channel.Enabled = enabled == 1
		item.Channel.CreatedAt = parseTime(channelCreated)
		item.Channel.UpdatedAt = parseTime(channelUpdated)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) CompleteNotificationBroadcastTarget(ctx context.Context, targetID int64, sendErr error, retryAt time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var broadcastID int64
	if err := tx.QueryRowContext(ctx, `select broadcast_id from notification_broadcast_targets where id=?`, targetID).Scan(&broadcastID); err != nil {
		return err
	}
	ts := now()
	if sendErr == nil {
		if _, err := tx.ExecContext(ctx, `update notification_broadcast_targets set status='sent',attempts=attempts+1,error='',sent_at=?,updated_at=? where id=? and status<>'sent'`, ts, ts, targetID); err != nil {
			return err
		}
	} else {
		errorText := sendErr.Error()
		if len(errorText) > 1000 {
			errorText = errorText[:1000]
		}
		if _, err := tx.ExecContext(ctx, `update notification_broadcast_targets set status='failed',attempts=attempts+1,error=?,next_attempt_at=?,updated_at=? where id=? and status<>'sent'`, errorText, retryAt.UTC().Format(time.RFC3339Nano), ts, targetID); err != nil {
			return err
		}
	}
	if err := refreshNotificationBroadcastCounts(ctx, tx, broadcastID); err != nil {
		return err
	}
	return tx.Commit()
}

func refreshNotificationBroadcastCounts(ctx context.Context, tx *sql.Tx, broadcastID int64) error {
	var success, failed, pending int
	if err := tx.QueryRowContext(ctx, `select
		coalesce(sum(case when sent>0 then 1 else 0 end),0),
		coalesce(sum(case when sent=0 and retryable=0 then 1 else 0 end),0),
		coalesce(sum(case when sent=0 and retryable>0 then 1 else 0 end),0)
		from (select user_id,
			sum(case when status='sent' then 1 else 0 end) sent,
			sum(case when status='pending' or (status='failed' and attempts<3) then 1 else 0 end) retryable
			from notification_broadcast_targets where broadcast_id=? group by user_id)`, broadcastID).Scan(&success, &failed, &pending); err != nil {
		return err
	}
	status := "sending"
	completedAt := any(nil)
	if pending == 0 {
		completedAt = now()
		switch {
		case success > 0 && failed == 0:
			status = "completed"
		case success > 0:
			status = "partial"
		default:
			status = "failed"
		}
	}
	_, err := tx.ExecContext(ctx, `update notification_broadcasts set status=?,success_count=?,failure_count=?,completed_at=? where id=?`, status, success, failed, completedAt, broadcastID)
	return err
}
