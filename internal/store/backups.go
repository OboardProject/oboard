package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

const controllerBackupSecretsSetting = "controller_backup_secret_config"

func (s *Store) CreateControllerBackup(ctx context.Context, v *model.ControllerBackup) error {
	ts := now()
	if v.ID == "" {
		return errors.New("backup id is required")
	}
	_, err := s.db.ExecContext(ctx, `insert into controller_backups(id,name,origin,local_path,local_status,remote_key,remote_target,remote_status,remote_error,size_bytes,source_version,format_version,protected,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, v.ID, v.Name, v.Origin, v.LocalPath, v.LocalStatus, v.RemoteKey, v.RemoteTarget, v.RemoteStatus, v.RemoteError, v.SizeBytes, v.SourceVersion, v.FormatVersion, boolInt(v.Protected), ts, ts)
	if err != nil {
		return err
	}
	v.CreatedAt, v.UpdatedAt = parseTime(ts), parseTime(ts)
	return nil
}

func (s *Store) ListControllerBackups(ctx context.Context) ([]model.ControllerBackup, error) {
	rows, err := s.db.QueryContext(ctx, `select id,name,origin,local_path,local_status,remote_key,remote_target,remote_status,remote_error,size_bytes,source_version,format_version,protected,created_at,updated_at from controller_backups order by created_at desc,id desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.ControllerBackup{}
	for rows.Next() {
		var v model.ControllerBackup
		var protected int
		var createdAt, updatedAt string
		if err := rows.Scan(&v.ID, &v.Name, &v.Origin, &v.LocalPath, &v.LocalStatus, &v.RemoteKey, &v.RemoteTarget, &v.RemoteStatus, &v.RemoteError, &v.SizeBytes, &v.SourceVersion, &v.FormatVersion, &protected, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		v.Protected = protected == 1
		v.CreatedAt, v.UpdatedAt = parseTime(createdAt), parseTime(updatedAt)
		items = append(items, v)
	}
	return items, rows.Err()
}

func (s *Store) GetControllerBackup(ctx context.Context, id string) (*model.ControllerBackup, error) {
	var v model.ControllerBackup
	var protected int
	var createdAt, updatedAt string
	err := s.db.QueryRowContext(ctx, `select id,name,origin,local_path,local_status,remote_key,remote_target,remote_status,remote_error,size_bytes,source_version,format_version,protected,created_at,updated_at from controller_backups where id=?`, id).Scan(&v.ID, &v.Name, &v.Origin, &v.LocalPath, &v.LocalStatus, &v.RemoteKey, &v.RemoteTarget, &v.RemoteStatus, &v.RemoteError, &v.SizeBytes, &v.SourceVersion, &v.FormatVersion, &protected, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	v.Protected = protected == 1
	v.CreatedAt, v.UpdatedAt = parseTime(createdAt), parseTime(updatedAt)
	return &v, nil
}

func (s *Store) UpdateControllerBackupRemote(ctx context.Context, id, key, target, status, message string) error {
	_, err := s.db.ExecContext(ctx, `update controller_backups set remote_key=?,remote_target=?,remote_status=?,remote_error=?,updated_at=? where id=?`, key, target, status, message, now(), id)
	return err
}

func (s *Store) UpdateControllerBackupLocal(ctx context.Context, id, localPath, status string, size int64) error {
	_, err := s.db.ExecContext(ctx, `update controller_backups set local_path=?,local_status=?,size_bytes=?,updated_at=? where id=?`, localPath, status, size, now(), id)
	return err
}

func (s *Store) CompleteControllerBackup(ctx context.Context, id, name, localPath string, size int64, sourceVersion string, formatVersion int) error {
	_, err := s.db.ExecContext(ctx, `update controller_backups set name=?,local_path=?,local_status=?,size_bytes=?,source_version=?,format_version=?,updated_at=? where id=?`, name, localPath, "available", size, sourceVersion, formatVersion, now(), id)
	return err
}

func (s *Store) ExpireControllerBackupLocal(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `update controller_backups set local_path='',local_status='expired',updated_at=? where id=?`, now(), id)
	return err
}

func (s *Store) ExpireControllerBackupRemote(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `update controller_backups set remote_status='expired',remote_error='',updated_at=? where id=?`, now(), id)
	return err
}

func (s *Store) DeleteControllerBackup(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `delete from controller_backups where id=?`, id)
	return err
}

func (s *Store) RewrapEncryptedSecrets(ctx context.Context, sourceSecret, targetSecret string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := rewrapColumn(ctx, tx, `select id,config_encrypted from dns_credentials where config_encrypted<>''`, `update dns_credentials set config_encrypted=?,updated_at=? where id=?`, sourceSecret, targetSecret, "dns-credential"); err != nil {
		return err
	}
	if err := rewrapColumn(ctx, tx, `select id,private_key_encrypted from certificates where private_key_encrypted<>''`, `update certificates set private_key_encrypted=?,updated_at=? where id=?`, sourceSecret, targetSecret, "certificate-private-key"); err != nil {
		return err
	}
	if err := rewrapColumn(ctx, tx, `select id,eab_hmac_key_encrypted from certificates where eab_hmac_key_encrypted<>''`, `update certificates set eab_hmac_key_encrypted=?,updated_at=? where id=?`, sourceSecret, targetSecret, "certificate-eab-hmac-key"); err != nil {
		return err
	}
	if err := rewrapColumn(ctx, tx, `select id,hmac_key_encrypted from google_eab_credentials where hmac_key_encrypted<>''`, `update google_eab_credentials set hmac_key_encrypted=?,updated_at=? where id=?`, sourceSecret, targetSecret, "google-eab-hmac-key"); err != nil {
		return err
	}
	if err := rewrapColumn(ctx, tx, `select id,signing_secret_encrypted from subscription_relays where signing_secret_encrypted<>''`, `update subscription_relays set signing_secret_encrypted=?,updated_at=? where id=?`, sourceSecret, targetSecret, "subscription-relay-signing-secret"); err != nil {
		return err
	}
	var backupSecret string
	err = tx.QueryRowContext(ctx, `select value from app_settings where key=?`, controllerBackupSecretsSetting).Scan(&backupSecret)
	if err == nil && backupSecret != "" {
		plain, decryptErr := security.DecryptSecret(sourceSecret, controllerBackupSecretsSetting, backupSecret)
		if decryptErr != nil {
			return fmt.Errorf("restore backup settings: %w", decryptErr)
		}
		wrapped, encryptErr := security.EncryptSecret(targetSecret, controllerBackupSecretsSetting, plain)
		if encryptErr != nil {
			return encryptErr
		}
		if _, err := tx.ExecContext(ctx, `update app_settings set value=?,updated_at=? where key=?`, wrapped, now(), controllerBackupSecretsSetting); err != nil {
			return err
		}
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update users set session_version=session_version+1`); err != nil {
		return err
	}
	return tx.Commit()
}

func rewrapColumn(ctx context.Context, tx *sql.Tx, selectSQL, updateSQL, sourceSecret, targetSecret, purpose string) error {
	rows, err := tx.QueryContext(ctx, selectSQL)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var value string
		if err := rows.Scan(&id, &value); err != nil {
			return err
		}
		plain, err := security.DecryptSecret(sourceSecret, purpose, value)
		if err != nil {
			return fmt.Errorf("restore protected data: %w", err)
		}
		wrapped, err := security.EncryptSecret(targetSecret, purpose, plain)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, updateSQL, wrapped, now(), id); err != nil {
			return err
		}
	}
	return rows.Err()
}
