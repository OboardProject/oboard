package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func (s *Store) GetUserAuthentication(ctx context.Context, userID int64) (model.UserAuthentication, error) {
	var item model.UserAuthentication
	var enabled int
	var updated string
	err := s.db.QueryRowContext(ctx, `select user_id,totp_enabled,totp_secret_encrypted,recovery_code_hashes_json,totp_last_used_step,coalesce(webauthn_user_handle,''),updated_at from user_authentication where user_id=?`, userID).Scan(&item.UserID, &enabled, &item.TOTPSecretEncrypted, &item.RecoveryCodeHashesJSON, &item.TOTPLastUsedStep, &item.WebAuthnUserHandle, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		item.UserID = userID
		item.RecoveryCodeHashesJSON = "[]"
		item.TOTPLastUsedStep = -1
		return item, nil
	}
	if err != nil {
		return model.UserAuthentication{}, err
	}
	item.TOTPEnabled = enabled != 0
	item.UpdatedAt = parseTime(updated)
	return item, nil
}

func (s *Store) SetTOTPSetup(ctx context.Context, userID int64, encryptedSecret string) error {
	ts := now()
	_, err := s.db.ExecContext(ctx, `insert into user_authentication(user_id,totp_enabled,totp_secret_encrypted,recovery_code_hashes_json,totp_last_used_step,updated_at) values(?,0,?,'[]',-1,?) on conflict(user_id) do update set totp_enabled=0,totp_secret_encrypted=excluded.totp_secret_encrypted,recovery_code_hashes_json='[]',totp_last_used_step=-1,updated_at=excluded.updated_at`, userID, encryptedSecret, ts)
	return err
}

func (s *Store) EnableTOTP(ctx context.Context, userID int64, encryptedSecret string, recoveryCodeHashes []string, lastUsedStep int64) error {
	encoded, err := json.Marshal(recoveryCodeHashes)
	if err != nil {
		return err
	}
	ts := now()
	_, err = s.db.ExecContext(ctx, `insert into user_authentication(user_id,totp_enabled,totp_secret_encrypted,recovery_code_hashes_json,totp_last_used_step,updated_at) values(?,1,?,?,?,?) on conflict(user_id) do update set totp_enabled=1,totp_secret_encrypted=excluded.totp_secret_encrypted,recovery_code_hashes_json=excluded.recovery_code_hashes_json,totp_last_used_step=excluded.totp_last_used_step,updated_at=excluded.updated_at`, userID, encryptedSecret, string(encoded), lastUsedStep, ts)
	return err
}

func (s *Store) DisableTOTP(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `update user_authentication set totp_enabled=0,totp_secret_encrypted='',recovery_code_hashes_json='[]',totp_last_used_step=-1,updated_at=? where user_id=?`, now(), userID)
	return err
}

func (s *Store) ConsumeTOTPStep(ctx context.Context, userID, step int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `update user_authentication set totp_last_used_step=?,updated_at=? where user_id=? and totp_enabled=1 and totp_last_used_step<?`, step, now(), userID, step)
	if err != nil {
		return false, err
	}
	count, err := res.RowsAffected()
	return count == 1, err
}

func (s *Store) ReplaceTOTPRecoveryCodes(ctx context.Context, userID int64, hashes []string) error {
	encoded, err := json.Marshal(hashes)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `update user_authentication set recovery_code_hashes_json=?,updated_at=? where user_id=? and totp_enabled=1`, string(encoded), now(), userID)
	if err != nil {
		return err
	}
	if count, err := res.RowsAffected(); err != nil {
		return err
	} else if count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) ConsumeTOTPRecoveryCode(ctx context.Context, userID int64, hash string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var encoded string
	if err := tx.QueryRowContext(ctx, `select recovery_code_hashes_json from user_authentication where user_id=? and totp_enabled=1`, userID).Scan(&encoded); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	var hashes []string
	if err := json.Unmarshal([]byte(encoded), &hashes); err != nil {
		return false, err
	}
	match := -1
	for i := range hashes {
		if subtle.ConstantTimeCompare([]byte(hashes[i]), []byte(hash)) == 1 {
			match = i
		}
	}
	if match < 0 {
		return false, nil
	}
	hashes = append(hashes[:match], hashes[match+1:]...)
	next, err := json.Marshal(hashes)
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `update user_authentication set recovery_code_hashes_json=?,updated_at=? where user_id=?`, string(next), now(), userID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) EnsureWebAuthnUserHandle(ctx context.Context, userID int64, proposed string) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var current sql.NullString
	err = tx.QueryRowContext(ctx, `select webauthn_user_handle from user_authentication where user_id=?`, userID).Scan(&current)
	if err == nil && current.Valid && current.String != "" {
		return current.String, tx.Commit()
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	ts := now()
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, `insert into user_authentication(user_id,webauthn_user_handle,updated_at) values(?,?,?)`, userID, proposed, ts)
	} else {
		_, err = tx.ExecContext(ctx, `update user_authentication set webauthn_user_handle=?,updated_at=? where user_id=?`, proposed, ts, userID)
	}
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return proposed, nil
}

func (s *Store) ListPasskeyCredentials(ctx context.Context, userID int64) ([]model.PasskeyCredential, error) {
	rows, err := s.db.QueryContext(ctx, `select id,user_id,name,credential_id,credential_json,created_at,last_used_at from passkey_credentials where user_id=? order by id asc`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.PasskeyCredential, 0)
	for rows.Next() {
		var item model.PasskeyCredential
		var created string
		var lastUsed sql.NullString
		if err := rows.Scan(&item.ID, &item.UserID, &item.Name, &item.CredentialID, &item.CredentialJSON, &created, &lastUsed); err != nil {
			return nil, err
		}
		item.CreatedAt = parseTime(created)
		item.LastUsedAt = parseNullTime(lastUsed)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) CreatePasskeyCredential(ctx context.Context, item *model.PasskeyCredential) error {
	ts := now()
	res, err := s.db.ExecContext(ctx, `insert into passkey_credentials(user_id,name,credential_id,credential_json,created_at) values(?,?,?,?,?)`, item.UserID, item.Name, item.CredentialID, item.CredentialJSON, ts)
	if err != nil {
		return err
	}
	item.ID, err = res.LastInsertId()
	item.CreatedAt = parseTime(ts)
	return err
}

func (s *Store) UpdatePasskeyCredential(ctx context.Context, userID int64, credentialID, credentialJSON string) error {
	ts := now()
	res, err := s.db.ExecContext(ctx, `update passkey_credentials set credential_json=?,last_used_at=? where user_id=? and credential_id=?`, credentialJSON, ts, userID, credentialID)
	if err != nil {
		return err
	}
	if count, err := res.RowsAffected(); err != nil {
		return err
	} else if count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DeletePasskeyCredential(ctx context.Context, userID, id int64) error {
	res, err := s.db.ExecContext(ctx, `delete from passkey_credentials where id=? and user_id=?`, id, userID)
	if err != nil {
		return err
	}
	if count, err := res.RowsAffected(); err != nil {
		return err
	} else if count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) CreateAuthChallenge(ctx context.Context, item model.AuthChallenge) error {
	_, _ = s.db.ExecContext(ctx, `delete from auth_challenges where expires_at<=?`, now())
	created := item.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	var userID any
	if item.UserID > 0 {
		userID = item.UserID
	}
	_, err := s.db.ExecContext(ctx, `insert into auth_challenges(token_hash,kind,user_id,data_encrypted,expires_at,created_at) values(?,?,?,?,?,?)`, item.TokenHash, item.Kind, userID, item.DataEncrypted, item.ExpiresAt.UTC().Format(time.RFC3339Nano), created.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) GetAuthChallenge(ctx context.Context, tokenHash, kind string) (model.AuthChallenge, error) {
	var item model.AuthChallenge
	var expires, created string
	var userID sql.NullInt64
	err := s.db.QueryRowContext(ctx, `select token_hash,kind,user_id,data_encrypted,expires_at,created_at from auth_challenges where token_hash=? and kind=?`, tokenHash, kind).Scan(&item.TokenHash, &item.Kind, &userID, &item.DataEncrypted, &expires, &created)
	if err != nil {
		return model.AuthChallenge{}, err
	}
	if userID.Valid {
		item.UserID = userID.Int64
	}
	item.ExpiresAt = parseTime(expires)
	item.CreatedAt = parseTime(created)
	if !item.ExpiresAt.After(time.Now().UTC()) {
		_, _ = s.db.ExecContext(ctx, `delete from auth_challenges where token_hash=?`, tokenHash)
		return model.AuthChallenge{}, sql.ErrNoRows
	}
	return item, nil
}

func (s *Store) DeleteAuthChallenge(ctx context.Context, tokenHash, kind string) error {
	_, err := s.db.ExecContext(ctx, `delete from auth_challenges where token_hash=? and kind=?`, tokenHash, kind)
	return err
}
