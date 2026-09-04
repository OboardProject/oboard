package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

const subscriptionRelaySelect = `select id,name,public_url,status,token_hash,signing_secret_encrypted,coalesce(enrollment_hash,''),enrollment_expires_at,version,build,commit_hash,os,arch,service_manager,update_target_version,update_target_build,update_requested_at,last_update_error,last_seen_at,created_at,updated_at from subscription_relays`

const (
	subscriptionRelayURLSetting                = "subscription_relay_url"
	subscriptionControllerDirectEnabledSetting = "subscription_controller_direct_enabled"
)

func (s *Store) CreateSubscriptionRelay(ctx context.Context, relay *model.SubscriptionRelay) error {
	ts := now()
	result, err := s.db.ExecContext(ctx, `insert into subscription_relays(name,public_url,status,enrollment_hash,enrollment_expires_at,created_at,updated_at) values(?,?,?,?,?,?,?)`, relay.Name, relay.PublicURL, relay.Status, nullEmpty(relay.EnrollmentHash), timePtrString(relay.EnrollmentExpiresAt), ts, ts)
	if err != nil {
		return err
	}
	relay.ID, _ = result.LastInsertId()
	relay.CreatedAt, relay.UpdatedAt = parseTime(ts), parseTime(ts)
	return nil
}

func (s *Store) ListSubscriptionRelays(ctx context.Context) ([]model.SubscriptionRelay, error) {
	rows, err := s.db.QueryContext(ctx, subscriptionRelaySelect+` order by created_at,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSubscriptionRelays(rows)
}

func (s *Store) GetSubscriptionRelay(ctx context.Context, id int64) (*model.SubscriptionRelay, error) {
	rows, err := s.db.QueryContext(ctx, subscriptionRelaySelect+` where id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanSubscriptionRelays(rows)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, sql.ErrNoRows
	}
	return &items[0], nil
}

func (s *Store) SubscriptionRelayURLExists(ctx context.Context, publicURL string, excludeID int64) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `select exists(select 1 from subscription_relays where public_url=? and id<>?)`, publicURL, excludeID).Scan(&exists)
	return exists, err
}

func (s *Store) UpdateSubscriptionRelay(ctx context.Context, relay *model.SubscriptionRelay) error {
	result, err := s.db.ExecContext(ctx, `update subscription_relays set name=?,public_url=?,updated_at=? where id=?`, relay.Name, relay.PublicURL, now(), relay.ID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DeleteSubscriptionRelay(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := now()
	if err := restoreControllerDirectForActiveRelay(ctx, tx, id, ts); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `delete from subscription_relays where id=?`, id)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

func (s *Store) ClaimSubscriptionRelayEnrollment(ctx context.Context, enrollmentHash, tokenHash, signingSecretEncrypted string) (*model.SubscriptionRelay, error) {
	if enrollmentHash == "" || tokenHash == "" || signingSecretEncrypted == "" {
		return nil, errors.New("relay enrollment material is required")
	}
	ts := now()
	result, err := s.db.ExecContext(ctx, `update subscription_relays set token_hash=?,signing_secret_encrypted=?,enrollment_hash=NULL,enrollment_expires_at=NULL,status='online',last_seen_at=?,updated_at=? where enrollment_hash=? and enrollment_expires_at is not null and enrollment_expires_at>=?`, tokenHash, signingSecretEncrypted, ts, ts, enrollmentHash, ts)
	if err != nil {
		return nil, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return nil, sql.ErrNoRows
	}
	var id int64
	if err := s.db.QueryRowContext(ctx, `select id from subscription_relays where token_hash=?`, tokenHash).Scan(&id); err != nil {
		return nil, err
	}
	return s.GetSubscriptionRelay(ctx, id)
}

func (s *Store) SetSubscriptionRelayEnrollment(ctx context.Context, id int64, enrollmentHash string, expiresAt time.Time) error {
	result, err := s.db.ExecContext(ctx, `update subscription_relays set enrollment_hash=?,enrollment_expires_at=?,updated_at=? where id=?`, enrollmentHash, expiresAt.UTC().Format(time.RFC3339Nano), now(), id)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) UpdateSubscriptionRelayHeartbeat(ctx context.Context, relay *model.SubscriptionRelay) error {
	result, err := s.db.ExecContext(ctx, `update subscription_relays set status=?,version=?,build=?,commit_hash=?,os=?,arch=?,service_manager=?,last_update_error=?,last_seen_at=?,updated_at=? where id=?`, relay.Status, relay.Version, relay.Build, relay.Commit, relay.OS, relay.Arch, relay.ServiceManager, relay.LastUpdateError, timePtrString(relay.LastSeenAt), now(), relay.ID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) RequestSubscriptionRelayUpdate(ctx context.Context, id int64, targetVersion, targetBuild string) error {
	ts := now()
	result, err := s.db.ExecContext(ctx, `update subscription_relays set status='updating',update_target_version=?,update_target_build=?,update_requested_at=?,last_update_error='',updated_at=? where id=? and token_hash<>''`, targetVersion, targetBuild, ts, ts, id)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) SetSubscriptionRelayActiveIfUnset(ctx context.Context, publicURL string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := now()
	result, err := tx.ExecContext(ctx, `insert into app_settings(key,value,updated_at) values(?,?,?) on conflict(key) do update set value=excluded.value,updated_at=excluded.updated_at where trim(app_settings.value)=''`, subscriptionRelayURLSetting, publicURL, ts)
	if err != nil {
		return err
	}
	activated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if activated > 0 {
		if _, err := tx.ExecContext(ctx, `insert into app_settings(key,value,updated_at) values(?,?,?) on conflict(key) do update set value=excluded.value,updated_at=excluded.updated_at`, subscriptionControllerDirectEnabledSetting, "false", ts); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) CompleteSubscriptionRelayUpdate(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `update subscription_relays set status='online',update_target_version='',update_target_build='',update_requested_at=NULL,last_update_error='',updated_at=? where id=?`, now(), id)
	return err
}

func (s *Store) ClearSubscriptionRelayUpdateRequest(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `update subscription_relays set update_target_version='',update_target_build='',update_requested_at=NULL,updated_at=? where id=?`, now(), id)
	return err
}

func (s *Store) MarkSubscriptionRelayUninstalled(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := now()
	result, err := tx.ExecContext(ctx, `update subscription_relays set status='uninstalled',token_hash='',signing_secret_encrypted='',enrollment_hash=NULL,enrollment_expires_at=NULL,update_target_version='',update_target_build='',update_requested_at=NULL,last_update_error='',last_seen_at=?,updated_at=? where id=?`, ts, ts, id)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return sql.ErrNoRows
	}
	if err := restoreControllerDirectForActiveRelay(ctx, tx, id, ts); err != nil {
		return err
	}
	return tx.Commit()
}

func restoreControllerDirectForActiveRelay(ctx context.Context, tx *sql.Tx, id int64, ts string) error {
	result, err := tx.ExecContext(ctx, `update app_settings set value='',updated_at=? where key=? and rtrim(value,'/')=(select rtrim(public_url,'/') from subscription_relays where id=?)`, ts, subscriptionRelayURLSetting, id)
	if err != nil {
		return err
	}
	cleared, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if cleared == 0 {
		return nil
	}
	_, err = tx.ExecContext(ctx, `insert into app_settings(key,value,updated_at) values(?,?,?) on conflict(key) do update set value=excluded.value,updated_at=excluded.updated_at`, subscriptionControllerDirectEnabledSetting, "true", ts)
	return err
}

func scanSubscriptionRelays(rows *sql.Rows) ([]model.SubscriptionRelay, error) {
	items := []model.SubscriptionRelay{}
	for rows.Next() {
		var relay model.SubscriptionRelay
		var enrollmentExpires, updateRequested, lastSeen sql.NullString
		var createdAt, updatedAt string
		if err := rows.Scan(&relay.ID, &relay.Name, &relay.PublicURL, &relay.Status, &relay.TokenHash, &relay.SigningSecretEncrypted, &relay.EnrollmentHash, &enrollmentExpires, &relay.Version, &relay.Build, &relay.Commit, &relay.OS, &relay.Arch, &relay.ServiceManager, &relay.UpdateTargetVersion, &relay.UpdateTargetBuild, &updateRequested, &relay.LastUpdateError, &lastSeen, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		relay.EnrollmentExpiresAt = nullableTime(enrollmentExpires)
		relay.UpdateRequestedAt = nullableTime(updateRequested)
		relay.LastSeenAt = nullableTime(lastSeen)
		relay.CreatedAt, relay.UpdatedAt = parseTime(createdAt), parseTime(updatedAt)
		items = append(items, relay)
	}
	return items, rows.Err()
}
