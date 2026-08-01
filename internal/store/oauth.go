package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

var ErrOAuthRefreshReuse = errors.New("oauth refresh token reuse detected")

func (s *Store) CreateOAuthClient(ctx context.Context, item *model.OAuthClient) error {
	redirects, err := json.Marshal(item.RedirectURIs)
	if err != nil {
		return err
	}
	scopes, err := json.Marshal(item.AllowedScopes)
	if err != nil {
		return err
	}
	ts := now()
	_, err = s.db.ExecContext(ctx, `insert into oauth_clients(id,name,redirect_uris_json,allowed_scopes_json,client_metadata_json,enabled,created_at,updated_at) values(?,?,?,?,?,?,?,?)`, item.ID, item.Name, string(redirects), string(scopes), normalizedJSONObject(item.ClientMetadata), boolInt(item.Enabled), ts, ts)
	if err == nil {
		item.CreatedAt, item.UpdatedAt = parseTime(ts), parseTime(ts)
	}
	return err
}

func (s *Store) GetOAuthClient(ctx context.Context, id string) (*model.OAuthClient, error) {
	return scanOAuthClient(s.db.QueryRowContext(ctx, `select id,name,redirect_uris_json,allowed_scopes_json,client_metadata_json,enabled,created_at,updated_at from oauth_clients where id=?`, id))
}

func (s *Store) ListOAuthClients(ctx context.Context) ([]model.OAuthClient, error) {
	rows, err := s.db.QueryContext(ctx, `select id,name,redirect_uris_json,allowed_scopes_json,client_metadata_json,enabled,created_at,updated_at from oauth_clients order by created_at desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.OAuthClient{}
	for rows.Next() {
		item, err := scanOAuthClient(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *item)
	}
	return out, rows.Err()
}

func (s *Store) UpdateOAuthClient(ctx context.Context, item *model.OAuthClient) error {
	redirects, err := json.Marshal(item.RedirectURIs)
	if err != nil {
		return err
	}
	scopes, err := json.Marshal(item.AllowedScopes)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `update oauth_clients set name=?,redirect_uris_json=?,allowed_scopes_json=?,enabled=?,updated_at=? where id=?`, item.Name, string(redirects), string(scopes), boolInt(item.Enabled), now(), item.ID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DeleteOAuthClient(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `delete from oauth_clients where id=?`, id)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func scanOAuthClient(scanner interface{ Scan(...any) error }) (*model.OAuthClient, error) {
	var item model.OAuthClient
	var redirects, scopes, metadata, created, updated string
	var enabled int
	if err := scanner.Scan(&item.ID, &item.Name, &redirects, &scopes, &metadata, &enabled, &created, &updated); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(redirects), &item.RedirectURIs)
	_ = json.Unmarshal([]byte(scopes), &item.AllowedScopes)
	item.ClientMetadata = json.RawMessage(metadata)
	item.Enabled = enabled != 0
	item.CreatedAt, item.UpdatedAt = parseTime(created), parseTime(updated)
	return &item, nil
}

func (s *Store) CreateOAuthAuthorizationCode(ctx context.Context, item *model.OAuthAuthorizationCode) error {
	scopes, err := json.Marshal(item.Scopes)
	if err != nil {
		return err
	}
	ts := now()
	_, err = s.db.ExecContext(ctx, `insert into oauth_authorization_codes(code_hash,client_id,user_id,principal_id,redirect_uri,scopes_json,resource,code_challenge,expires_at,created_at) values(?,?,?,?,?,?,?,?,?,?)`, item.CodeHash, item.ClientID, item.UserID, item.PrincipalID, item.RedirectURI, string(scopes), item.Resource, item.CodeChallenge, item.ExpiresAt.UTC().Format(time.RFC3339Nano), ts)
	if err == nil {
		item.CreatedAt = parseTime(ts)
	}
	return err
}

func (s *Store) ConsumeOAuthAuthorizationCode(ctx context.Context, codeHash string) (*model.OAuthAuthorizationCode, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var item model.OAuthAuthorizationCode
	var scopes, expires, created string
	if err := tx.QueryRowContext(ctx, `select code_hash,client_id,user_id,principal_id,redirect_uri,scopes_json,resource,code_challenge,expires_at,created_at from oauth_authorization_codes where code_hash=?`, codeHash).Scan(&item.CodeHash, &item.ClientID, &item.UserID, &item.PrincipalID, &item.RedirectURI, &scopes, &item.Resource, &item.CodeChallenge, &expires, &created); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `delete from oauth_authorization_codes where code_hash=?`, codeHash); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(scopes), &item.Scopes)
	item.ExpiresAt, item.CreatedAt = parseTime(expires), parseTime(created)
	return &item, nil
}

func (s *Store) CreateOAuthTokens(ctx context.Context, access, refresh *model.OAuthToken) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	accessScopes, _ := json.Marshal(access.Scopes)
	refreshScopes, _ := json.Marshal(refresh.Scopes)
	ts := now()
	if _, err := tx.ExecContext(ctx, `insert into oauth_access_tokens(token_hash,principal_id,client_id,user_id,scopes_json,resource,expires_at,created_at) values(?,?,?,?,?,?,?,?)`, access.TokenHash, access.PrincipalID, access.ClientID, access.UserID, string(accessScopes), access.Resource, access.ExpiresAt.UTC().Format(time.RFC3339Nano), ts); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `insert into oauth_refresh_tokens(token_hash,family_id,principal_id,client_id,user_id,scopes_json,resource,expires_at,created_at) values(?,?,?,?,?,?,?,?,?)`, refresh.TokenHash, refresh.FamilyID, refresh.PrincipalID, refresh.ClientID, refresh.UserID, string(refreshScopes), refresh.Resource, refresh.ExpiresAt.UTC().Format(time.RFC3339Nano), ts); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AuthenticateOAuthAccessToken(ctx context.Context, tokenHash string, at time.Time) (*model.APIPrincipal, *model.OAuthToken, error) {
	row := s.db.QueryRowContext(ctx, `select t.token_hash,t.principal_id,t.client_id,t.user_id,t.scopes_json,t.resource,t.expires_at,t.revoked_at,t.created_at,p.id,p.owner_user_id,p.name,p.type,p.enabled,p.scopes_json,p.resource_filter_json,p.allowed_cidrs_json,p.rate_limit_per_minute,p.max_concurrency,p.expires_at,p.last_used_at,p.created_at,p.updated_at from oauth_access_tokens t join api_principals p on p.id=t.principal_id where t.token_hash=?`, tokenHash)
	var token model.OAuthToken
	var tokenScopes, tokenExpires, tokenCreated string
	var tokenRevoked sql.NullString
	var principal model.APIPrincipal
	var owner sql.NullInt64
	var enabled int
	var scopesJSON, filterJSON, cidrsJSON string
	var principalExpires, principalLastUsed sql.NullString
	var principalCreated, principalUpdated string
	if err := row.Scan(&token.TokenHash, &token.PrincipalID, &token.ClientID, &token.UserID, &tokenScopes, &token.Resource, &tokenExpires, &tokenRevoked, &tokenCreated, &principal.ID, &owner, &principal.Name, &principal.Type, &enabled, &scopesJSON, &filterJSON, &cidrsJSON, &principal.RateLimitPerMinute, &principal.MaxConcurrency, &principalExpires, &principalLastUsed, &principalCreated, &principalUpdated); err != nil {
		return nil, nil, err
	}
	_ = json.Unmarshal([]byte(tokenScopes), &token.Scopes)
	token.ExpiresAt, token.RevokedAt, token.CreatedAt = parseTime(tokenExpires), nullableTime(tokenRevoked), parseTime(tokenCreated)
	principal.Enabled = enabled != 0
	if owner.Valid {
		principal.OwnerUserID = &owner.Int64
	}
	_ = json.Unmarshal([]byte(scopesJSON), &principal.Scopes)
	_ = json.Unmarshal([]byte(cidrsJSON), &principal.AllowedCIDRs)
	principal.ResourceFilter = json.RawMessage(filterJSON)
	principal.ExpiresAt, principal.LastUsedAt = nullableTime(principalExpires), nullableTime(principalLastUsed)
	principal.CreatedAt, principal.UpdatedAt = parseTime(principalCreated), parseTime(principalUpdated)
	if !principal.Enabled || token.RevokedAt != nil || !token.ExpiresAt.After(at) || principal.ExpiresAt != nil && !principal.ExpiresAt.After(at) {
		return nil, nil, sql.ErrNoRows
	}
	_, _ = s.db.ExecContext(ctx, `update api_principals set last_used_at=? where id=?`, at.UTC().Format(time.RFC3339Nano), principal.ID)
	return &principal, &token, nil
}

func (s *Store) ConsumeOAuthRefreshToken(ctx context.Context, tokenHash string, at time.Time) (*model.OAuthToken, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var item model.OAuthToken
	var scopes, expires, created string
	var consumed, revoked sql.NullString
	if err := tx.QueryRowContext(ctx, `select token_hash,family_id,principal_id,client_id,user_id,scopes_json,resource,expires_at,consumed_at,revoked_at,created_at from oauth_refresh_tokens where token_hash=?`, tokenHash).Scan(&item.TokenHash, &item.FamilyID, &item.PrincipalID, &item.ClientID, &item.UserID, &scopes, &item.Resource, &expires, &consumed, &revoked, &created); err != nil {
		return nil, err
	}
	item.ConsumedAt, item.RevokedAt = nullableTime(consumed), nullableTime(revoked)
	if item.ConsumedAt != nil {
		_, _ = tx.ExecContext(ctx, `update oauth_refresh_tokens set revoked_at=? where family_id=? and revoked_at is null`, at.UTC().Format(time.RFC3339Nano), item.FamilyID)
		_ = tx.Commit()
		return nil, ErrOAuthRefreshReuse
	}
	item.ExpiresAt, item.CreatedAt = parseTime(expires), parseTime(created)
	if item.RevokedAt != nil || !item.ExpiresAt.After(at) {
		return nil, sql.ErrNoRows
	}
	if _, err := tx.ExecContext(ctx, `update oauth_refresh_tokens set consumed_at=? where token_hash=? and consumed_at is null`, at.UTC().Format(time.RFC3339Nano), tokenHash); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(scopes), &item.Scopes)
	return &item, nil
}

func (s *Store) RevokeOAuthToken(ctx context.Context, tokenHash string) error {
	ts := now()
	if result, err := s.db.ExecContext(ctx, `update oauth_access_tokens set revoked_at=? where token_hash=? and revoked_at is null`, ts, tokenHash); err == nil {
		if count, _ := result.RowsAffected(); count > 0 {
			return nil
		}
	} else {
		return err
	}
	var family string
	if err := s.db.QueryRowContext(ctx, `select family_id from oauth_refresh_tokens where token_hash=?`, tokenHash).Scan(&family); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `update oauth_refresh_tokens set revoked_at=? where family_id=? and revoked_at is null`, ts, family)
	return err
}
