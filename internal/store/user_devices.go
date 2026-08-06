package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

var ErrDeviceLimitReached = errors.New("user device limit reached")

const userDeviceSelect = `select id,device_id_hash,user_id,name,token_hash,token_prefix,credential_epoch,status,subscription_suspended,proxy_access_state,last_subscription_at,last_proxy_activity_at,revoked_at,subscription_suspended_at,created_at,updated_at from user_devices`

func (s *Store) CreateUserDevice(ctx context.Context, device *model.UserDevice) error {
	if device == nil || strings.TrimSpace(device.ID) == "" || strings.TrimSpace(device.DeviceIDHash) == "" || device.UserID <= 0 || strings.TrimSpace(device.Name) == "" || strings.TrimSpace(device.TokenHash) == "" {
		return errors.New("invalid user device")
	}
	if device.CredentialEpoch <= 0 {
		device.CredentialEpoch = 1
	}
	device.Status = "active"
	device.ProxyAccessState = "active"
	ts := now()
	res, err := s.db.ExecContext(ctx, `insert into user_devices(id,device_id_hash,user_id,name,token_hash,token_prefix,credential_epoch,status,subscription_suspended,proxy_access_state,created_at,updated_at)
		select ?,?,u.id,?,?,?,?,?,0,?,?,? from users u where u.id=? and u.status='active' and (coalesce(u.device_limit,0)<=0 or (select count(*) from user_devices d where d.user_id=u.id and d.status='active')<u.device_limit)`,
		device.ID, device.DeviceIDHash, strings.TrimSpace(device.Name), device.TokenHash, device.TokenPrefix, device.CredentialEpoch, device.Status, device.ProxyAccessState, ts, ts, device.UserID)
	if err != nil {
		return err
	}
	count, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		var exists int
		if err := s.db.QueryRowContext(ctx, `select count(*) from users where id=? and status='active'`, device.UserID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return sql.ErrNoRows
		}
		return ErrDeviceLimitReached
	}
	device.CreatedAt = parseTime(ts)
	device.UpdatedAt = device.CreatedAt
	return nil
}

func (s *Store) ListUserDevices(ctx context.Context, userID int64) ([]model.UserDevice, error) {
	rows, err := s.db.QueryContext(ctx, userDeviceSelect+` where user_id=? order by created_at,id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUserDevices(rows)
}

func (s *Store) ListActiveUserDevices(ctx context.Context) ([]model.UserDevice, error) {
	rows, err := s.db.QueryContext(ctx, userDeviceSelect+` where status='active' order by user_id,created_at,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUserDevices(rows)
}

func (s *Store) GetUserDevice(ctx context.Context, userID int64, deviceID string) (*model.UserDevice, error) {
	return scanUserDevice(s.db.QueryRowContext(ctx, userDeviceSelect+` where user_id=? and id=?`, userID, deviceID))
}

func (s *Store) GetUserDeviceByTokenHash(ctx context.Context, tokenHash string) (*model.UserDevice, error) {
	return scanUserDevice(s.db.QueryRowContext(ctx, userDeviceSelect+` where token_hash=? and status='active'`, tokenHash))
}

func (s *Store) GetUserDeviceByHash(ctx context.Context, userID int64, deviceIDHash string) (*model.UserDevice, error) {
	return scanUserDevice(s.db.QueryRowContext(ctx, userDeviceSelect+` where user_id=? and device_id_hash=?`, userID, strings.TrimSpace(deviceIDHash)))
}

func (s *Store) RenameUserDevice(ctx context.Context, userID int64, deviceID, name string) (*model.UserDevice, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 80 {
		return nil, errors.New("device name must be between 1 and 80 characters")
	}
	res, err := s.db.ExecContext(ctx, `update user_devices set name=?,updated_at=? where user_id=? and id=?`, name, now(), userID, deviceID)
	if err != nil {
		return nil, err
	}
	if count, err := res.RowsAffected(); err != nil || count != 1 {
		if err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	return s.GetUserDevice(ctx, userID, deviceID)
}

func (s *Store) RotateUserDevice(ctx context.Context, userID int64, deviceID, tokenHash, tokenPrefix string) (*model.UserDevice, error) {
	if strings.TrimSpace(tokenHash) == "" {
		return nil, errors.New("device token hash is required")
	}
	res, err := s.db.ExecContext(ctx, `update user_devices set token_hash=?,token_prefix=?,credential_epoch=credential_epoch+1,subscription_suspended=0,proxy_access_state='active',subscription_suspended_at=null,updated_at=? where user_id=? and id=? and status='active'`, tokenHash, tokenPrefix, now(), userID, deviceID)
	if err != nil {
		return nil, err
	}
	if count, err := res.RowsAffected(); err != nil || count != 1 {
		if err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	return s.GetUserDevice(ctx, userID, deviceID)
}

func (s *Store) RevokeUserDevice(ctx context.Context, userID int64, deviceID string) (*model.UserDevice, error) {
	ts := now()
	res, err := s.db.ExecContext(ctx, `update user_devices set status='revoked',subscription_suspended=1,proxy_access_state='reject_new',revoked_at=?,subscription_suspended_at=coalesce(subscription_suspended_at,?),updated_at=? where user_id=? and id=?`, ts, ts, ts, userID, deviceID)
	if err != nil {
		return nil, err
	}
	if count, err := res.RowsAffected(); err != nil || count != 1 {
		if err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	return s.GetUserDevice(ctx, userID, deviceID)
}

func (s *Store) SetUserDeviceSubscriptionSuspended(ctx context.Context, userID int64, deviceID string, suspended bool) (*model.UserDevice, error) {
	var at any
	if suspended {
		at = now()
	}
	res, err := s.db.ExecContext(ctx, `update user_devices set subscription_suspended=?,subscription_suspended_at=?,updated_at=? where user_id=? and id=? and status='active'`, boolInt(suspended), at, now(), userID, deviceID)
	if err != nil {
		return nil, err
	}
	if count, err := res.RowsAffected(); err != nil || count != 1 {
		if err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	return s.GetUserDevice(ctx, userID, deviceID)
}

func (s *Store) SetUserDeviceProxyAccessState(ctx context.Context, userID int64, deviceID, state string) (*model.UserDevice, error) {
	state = strings.ToLower(strings.TrimSpace(state))
	if state != "active" && state != "reject_new" {
		return nil, errors.New("invalid device proxy access state")
	}
	res, err := s.db.ExecContext(ctx, `update user_devices set proxy_access_state=?,updated_at=? where user_id=? and id=? and status='active'`, state, now(), userID, deviceID)
	if err != nil {
		return nil, err
	}
	if count, err := res.RowsAffected(); err != nil || count != 1 {
		if err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	return s.GetUserDevice(ctx, userID, deviceID)
}

func (s *Store) MarkUserDeviceSubscriptionActivity(ctx context.Context, userID int64, deviceID string, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `update user_devices set last_subscription_at=?,updated_at=? where user_id=? and id=?`, at.UTC().Format(time.RFC3339Nano), now(), userID, deviceID)
	return err
}

func (s *Store) MarkUserDeviceProxyActivity(ctx context.Context, deviceIDHash string, at time.Time) error {
	if strings.TrimSpace(deviceIDHash) == "" {
		return nil
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `update user_devices set last_proxy_activity_at=?,updated_at=? where device_id_hash=?`, at.UTC().Format(time.RFC3339Nano), now(), deviceIDHash)
	return err
}

type userDeviceScanner interface {
	Scan(...any) error
}

func scanUserDevice(scanner userDeviceScanner) (*model.UserDevice, error) {
	var item model.UserDevice
	var suspended int
	var lastSubscription, lastProxy, revoked, suspendedAt sql.NullString
	var created, updated string
	if err := scanner.Scan(&item.ID, &item.DeviceIDHash, &item.UserID, &item.Name, &item.TokenHash, &item.TokenPrefix, &item.CredentialEpoch, &item.Status, &suspended, &item.ProxyAccessState, &lastSubscription, &lastProxy, &revoked, &suspendedAt, &created, &updated); err != nil {
		return nil, err
	}
	item.SubscriptionSuspended = suspended != 0
	item.LastSubscriptionAt = parseNullTime(lastSubscription)
	item.LastProxyActivityAt = parseNullTime(lastProxy)
	item.RevokedAt = parseNullTime(revoked)
	item.SubscriptionSuspendedAt = parseNullTime(suspendedAt)
	item.CreatedAt = parseTime(created)
	item.UpdatedAt = parseTime(updated)
	return &item, nil
}

func scanUserDevices(rows *sql.Rows) ([]model.UserDevice, error) {
	out := []model.UserDevice{}
	for rows.Next() {
		item, err := scanUserDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *item)
	}
	return out, rows.Err()
}
