package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/OboardProject/oboard/internal/model"
)

func (s *Store) CreateAIProvider(ctx context.Context, item *model.AIProvider) error {
	if item == nil {
		return errors.New("AI provider is required")
	}
	ts := now()
	_, err := s.db.ExecContext(ctx, `insert into ai_providers(id,name,base_url,model,credential_encrypted,enabled,allow_raw_audit,daily_token_limit,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?)`, item.ID, item.Name, item.BaseURL, item.Model, item.CredentialEncrypted, boolInt(item.Enabled), boolInt(item.AllowRawAudit), item.DailyTokenLimit, ts, ts)
	if err == nil {
		item.HasCredential = item.CredentialEncrypted != ""
		item.CreatedAt, item.UpdatedAt = parseTime(ts), parseTime(ts)
	}
	return err
}

func (s *Store) ListAIProviders(ctx context.Context) ([]model.AIProvider, error) {
	rows, err := s.db.QueryContext(ctx, `select id,name,base_url,model,credential_encrypted,enabled,allow_raw_audit,daily_token_limit,last_used_at,created_at,updated_at from ai_providers order by created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.AIProvider{}
	for rows.Next() {
		item, err := scanAIProvider(rows)
		if err != nil {
			return nil, err
		}
		item.CredentialEncrypted = ""
		out = append(out, *item)
	}
	return out, rows.Err()
}

func (s *Store) GetAIProvider(ctx context.Context, id string) (*model.AIProvider, error) {
	return scanAIProvider(s.db.QueryRowContext(ctx, `select id,name,base_url,model,credential_encrypted,enabled,allow_raw_audit,daily_token_limit,last_used_at,created_at,updated_at from ai_providers where id=?`, id))
}

func (s *Store) UpdateAIProvider(ctx context.Context, item *model.AIProvider) error {
	result, err := s.db.ExecContext(ctx, `update ai_providers set name=?,base_url=?,model=?,credential_encrypted=?,enabled=?,allow_raw_audit=?,daily_token_limit=?,updated_at=? where id=?`, item.Name, item.BaseURL, item.Model, item.CredentialEncrypted, boolInt(item.Enabled), boolInt(item.AllowRawAudit), item.DailyTokenLimit, now(), item.ID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DeleteAIProvider(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `delete from ai_providers where id=? and not exists(select 1 from ai_audit_reviews where provider_id=?)`, id, id)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func scanAIProvider(scanner interface{ Scan(...any) error }) (*model.AIProvider, error) {
	var item model.AIProvider
	var enabled, raw int
	var last sql.NullString
	var created, updated string
	if err := scanner.Scan(&item.ID, &item.Name, &item.BaseURL, &item.Model, &item.CredentialEncrypted, &enabled, &raw, &item.DailyTokenLimit, &last, &created, &updated); err != nil {
		return nil, err
	}
	item.Enabled, item.AllowRawAudit, item.HasCredential = enabled != 0, raw != 0, item.CredentialEncrypted != ""
	item.LastUsedAt, item.CreatedAt, item.UpdatedAt = nullableTime(last), parseTime(created), parseTime(updated)
	return &item, nil
}
