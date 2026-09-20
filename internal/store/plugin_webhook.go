package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

var (
	ErrPluginWebhookReplay  = errors.New("webhook nonce already consumed")
	ErrPluginWebhookChanged = errors.New("webhook is disabled or changed")
	ErrPluginWebhookRate    = errors.New("webhook rate limit exceeded")
)

const pluginWebhookSelect = `select id,plugin_id,binding_id,binding_revision,revision_id,grant_id,enabled,generation,secret_encrypted,created_by_user_id,created_at,updated_at from plugin_webhooks`

func scanPluginWebhook(row interface{ Scan(...any) error }) (model.PluginWebhook, error) {
	var item model.PluginWebhook
	var created, updated string
	err := row.Scan(&item.ID, &item.PluginID, &item.BindingID, &item.BindingRevision, &item.RevisionID, &item.GrantID, &item.Enabled, &item.Generation, &item.SecretEncrypted, &item.CreatedByUserID, &created, &updated)
	item.CreatedAt, item.UpdatedAt = parseTime(created), parseTime(updated)
	return item, err
}
func (s *Store) GetPluginWebhook(ctx context.Context, id string) (model.PluginWebhook, error) {
	return scanPluginWebhook(s.db.QueryRowContext(ctx, pluginWebhookSelect+` where id=?`, id))
}
func (s *Store) ListPluginWebhooks(ctx context.Context, pluginID int64) ([]model.PluginWebhook, error) {
	rows, err := s.db.QueryContext(ctx, pluginWebhookSelect+` where plugin_id=? order by created_at desc limit 100`, pluginID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.PluginWebhook{}
	for rows.Next() {
		item, err := scanPluginWebhook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
func (s *Store) CreatePluginWebhook(ctx context.Context, item *model.PluginWebhook) error {
	ts := now()
	res, err := s.db.ExecContext(ctx, `insert into plugin_webhooks(id,plugin_id,binding_id,binding_revision,revision_id,grant_id,secret_encrypted,created_by_user_id,created_at,updated_at) select ?,?,?,?,?,?,?,?,?,? where exists(select 1 from plugin_trigger_bindings b join plugin_grants g on g.binding_id=b.id where b.id=? and b.plugin_id=? and b.binding_revision=? and b.revision_id=? and g.id=? and g.plugin_id=b.plugin_id and g.revision_id=b.revision_id and g.revoked_at is null)`, item.ID, item.PluginID, item.BindingID, item.BindingRevision, item.RevisionID, item.GrantID, item.SecretEncrypted, item.CreatedByUserID, ts, ts, item.BindingID, item.PluginID, item.BindingRevision, item.RevisionID, item.GrantID)
	if err = pluginWebhookChanged(res, err); err != nil {
		return err
	}
	item.Enabled = false
	item.Generation = 1
	item.CreatedAt = parseTime(ts)
	item.UpdatedAt = item.CreatedAt
	return nil
}
func pluginWebhookChanged(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrPluginWebhookChanged
	}
	return nil
}

// ChangePluginWebhook uses CAS for both rotation and enablement. Rotation never
// silently enables an endpoint. Every change invalidates in-flight credentials.
func (s *Store) ChangePluginWebhook(ctx context.Context, item model.PluginWebhook, enabled bool, encrypted string) error {
	if encrypted == "" {
		encrypted = item.SecretEncrypted
	}
	res, err := s.db.ExecContext(ctx, `update plugin_webhooks set enabled=?,secret_encrypted=?,generation=generation+1,updated_at=? where id=? and generation=?`, enabled, encrypted, now(), item.ID, item.Generation)
	return pluginWebhookChanged(res, err)
}

// AllowPluginWebhookAttempt bounds even invalid signatures by endpoint, without
// allocating a map entry for attacker-chosen endpoint IDs.
func (s *Store) AllowPluginWebhookAttempt(ctx context.Context, id string, at time.Time) error {
	window := at.Unix() / 60
	res, err := s.db.ExecContext(ctx, `update plugin_webhooks set rate_count=case when rate_window=? then rate_count+1 else 1 end,rate_window=? where id=? and enabled=1 and (rate_window<>? or rate_count<60)`, window, window, id, window)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrPluginWebhookRate
	}
	return nil
}

// ClaimPluginWebhookDelivery persists the nonce before queueing. An interrupted
// request is fail-closed: it may consume a delivery, but can never queue it twice.
func (s *Store) ClaimPluginWebhookDelivery(ctx context.Context, item model.PluginWebhook, nonce string, at, expires time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `delete from plugin_webhook_deliveries where rowid in (select rowid from plugin_webhook_deliveries where expires_at<? limit 256)`, at.Unix()); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `insert into plugin_webhook_deliveries(webhook_id,nonce,generation,received_at,expires_at) select ?,?,?,?,? where exists(select 1 from plugin_webhooks w join plugin_trigger_bindings b on b.id=w.binding_id where w.id=? and w.enabled=1 and w.generation=? and b.enabled=1 and b.binding_revision=w.binding_revision and b.revision_id=w.revision_id and b.plugin_id=w.plugin_id)`, item.ID, nonce, item.Generation, at.Unix(), expires.Unix(), item.ID, item.Generation)
	if isUniqueConstraint(err) {
		return ErrPluginWebhookReplay
	}
	if err = pluginWebhookChanged(res, err); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) FinishPluginWebhookDelivery(ctx context.Context, id, nonce string, runID int64) error {
	status := "rejected"
	var run any
	if runID > 0 {
		status = "queued"
		run = runID
	}
	_, err := s.db.ExecContext(ctx, `update plugin_webhook_deliveries set status=?,run_id=? where webhook_id=? and nonce=? and status='received'`, status, run, id, nonce)
	return err
}

// RestorePluginWebhooks rewraps all endpoint keys and pauses endpoints atomically.
// Call this on the staged restored database, before it can serve traffic.
func (s *Store) RestorePluginWebhooks(ctx context.Context, sourceSecret, targetSecret string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `select id,secret_encrypted from plugin_webhooks`)
	if err != nil {
		return err
	}
	type rewrapped struct{ id, value string }
	var items []rewrapped
	for rows.Next() {
		var id, cipher string
		if err = rows.Scan(&id, &cipher); err != nil {
			rows.Close()
			return err
		}
		plain, err := security.DecryptSecret(sourceSecret, model.PluginWebhookSecretPurpose(id), cipher)
		if err != nil {
			rows.Close()
			return errors.New("cannot decrypt plugin webhook secret")
		}
		value, err := security.EncryptSecret(targetSecret, model.PluginWebhookSecretPurpose(id), plain)
		if err != nil {
			rows.Close()
			return errors.New("cannot encrypt plugin webhook secret")
		}
		items = append(items, rewrapped{id, value})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range items {
		if _, err = tx.ExecContext(ctx, `update plugin_webhooks set enabled=0,generation=generation+1,secret_encrypted=?,updated_at=? where id=?`, item.value, now(), item.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
