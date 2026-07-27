package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/OboardProject/oboard/internal/model"
)

var (
	ErrGoogleEABCredentialExists = errors.New("Google EAB Key ID 已保存")
	ErrGoogleEABCredentialInUse  = errors.New("Google EAB 正在被证书使用")
)

func (s *Store) CreateGoogleEABCredential(ctx context.Context, credential *model.GoogleEABCredential) error {
	var exists int
	if err := s.db.QueryRowContext(ctx, `select count(*) from google_eab_credentials where key_id=?`, credential.KeyID).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return ErrGoogleEABCredentialExists
	}
	ts := now()
	result, err := s.db.ExecContext(ctx, `insert into google_eab_credentials(key_id,remark,hmac_key_encrypted,created_at,updated_at) values(?,?,?,?,?)`, credential.KeyID, credential.Remark, credential.HMACKeyEncrypted, ts, ts)
	if err != nil {
		return err
	}
	credential.ID, _ = result.LastInsertId()
	credential.CreatedAt = parseTime(ts)
	credential.UpdatedAt = credential.CreatedAt
	return nil
}

func (s *Store) ListGoogleEABCredentials(ctx context.Context) ([]model.GoogleEABCredential, error) {
	rows, err := s.db.QueryContext(ctx, googleEABCredentialSelect+` group by g.id order by g.created_at desc,g.id desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanGoogleEABCredentials(rows)
}

func (s *Store) GetGoogleEABCredential(ctx context.Context, id int64) (*model.GoogleEABCredential, error) {
	rows, err := s.db.QueryContext(ctx, googleEABCredentialSelect+` where g.id=? group by g.id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanGoogleEABCredentials(rows)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, sql.ErrNoRows
	}
	return &items[0], nil
}

func (s *Store) DeleteGoogleEABCredential(ctx context.Context, id int64) error {
	credential, err := s.GetGoogleEABCredential(ctx, id)
	if err != nil {
		return err
	}
	if credential.UsageCount > 0 {
		return ErrGoogleEABCredentialInUse
	}
	result, err := s.db.ExecContext(ctx, `delete from google_eab_credentials where id=?`, id)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil {
		return err
	} else if count == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// #nosec G101 -- the query text names encrypted columns; it contains no credential material.
const googleEABCredentialSelect = `select g.id,g.key_id,g.remark,g.hmac_key_encrypted,g.created_at,g.updated_at,count(c.id) from google_eab_credentials g left join certificates c on c.google_eab_credential_id=g.id`

func scanGoogleEABCredentials(rows *sql.Rows) ([]model.GoogleEABCredential, error) {
	items := []model.GoogleEABCredential{}
	for rows.Next() {
		var credential model.GoogleEABCredential
		var createdAt, updatedAt string
		if err := rows.Scan(&credential.ID, &credential.KeyID, &credential.Remark, &credential.HMACKeyEncrypted, &createdAt, &updatedAt, &credential.UsageCount); err != nil {
			return nil, err
		}
		credential.CreatedAt = parseTime(createdAt)
		credential.UpdatedAt = parseTime(updatedAt)
		items = append(items, credential)
	}
	return items, rows.Err()
}
