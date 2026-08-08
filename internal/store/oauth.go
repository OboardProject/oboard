package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/OboardProject/oboard/internal/mcpauth"
	"github.com/OboardProject/oboard/internal/model"
)

var ErrOAuthRefreshReuse = errors.New("oauth refresh token reuse detected")

func (s *Store) CreateOAuthClient(ctx context.Context, item *model.OAuthClient) error {
	redirects, err := json.Marshal(item.RedirectURIs)
	if err != nil {
		return err
	}
	ts := now()
	if item.IdentityType == "" {
		item.IdentityType = "preregistered"
	}
	_, err = s.db.ExecContext(ctx, `insert into oauth_clients(id,name,redirect_uris_json,identity_type,metadata_uri,metadata_hash,metadata_etag,metadata_fetched_at,client_metadata_json,enabled,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?)`, item.ID, item.Name, string(redirects), item.IdentityType, item.MetadataURI, item.MetadataHash, item.MetadataETag, timePtrString(item.MetadataFetchedAt), normalizedJSONObject(item.ClientMetadata), boolInt(item.Enabled), ts, ts)
	if err == nil {
		item.CreatedAt, item.UpdatedAt = parseTime(ts), parseTime(ts)
	}
	return err
}

func (s *Store) GetOAuthClient(ctx context.Context, id string) (*model.OAuthClient, error) {
	return scanOAuthClient(s.db.QueryRowContext(ctx, `select id,name,redirect_uris_json,identity_type,coalesce(metadata_uri,''),coalesce(metadata_hash,''),coalesce(metadata_etag,''),metadata_fetched_at,client_metadata_json,enabled,created_at,updated_at from oauth_clients where id=?`, id))
}

func (s *Store) ListOAuthClients(ctx context.Context) ([]model.OAuthClient, error) {
	rows, err := s.db.QueryContext(ctx, `select id,name,redirect_uris_json,identity_type,coalesce(metadata_uri,''),coalesce(metadata_hash,''),coalesce(metadata_etag,''),metadata_fetched_at,client_metadata_json,enabled,created_at,updated_at from oauth_clients order by created_at desc`)
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
	result, err := s.db.ExecContext(ctx, `update oauth_clients set name=?,redirect_uris_json=?,identity_type=?,metadata_uri=?,metadata_hash=?,metadata_etag=?,metadata_fetched_at=?,enabled=?,updated_at=? where id=?`, item.Name, string(redirects), item.IdentityType, item.MetadataURI, item.MetadataHash, item.MetadataETag, timePtrString(item.MetadataFetchedAt), boolInt(item.Enabled), now(), item.ID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DeleteOAuthClient(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `select principal_id,approval_profile_id from oauth_grants where client_id=?`, id)
	if err != nil {
		return err
	}
	var principalIDs, profileIDs []string
	for rows.Next() {
		var principalID, profileID string
		if err := rows.Scan(&principalID, &profileID); err != nil {
			rows.Close()
			return err
		}
		principalIDs = append(principalIDs, principalID)
		profileIDs = append(profileIDs, profileID)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `delete from oauth_clients where id=?`, id)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	for _, principalID := range principalIDs {
		if _, err := tx.ExecContext(ctx, `delete from api_principals where id=? and type=?`, principalID, model.APIPrincipalOAuth); err != nil {
			return err
		}
	}
	for _, profileID := range profileIDs {
		if _, err := tx.ExecContext(ctx, `delete from oauth_approval_profiles where id=?`, profileID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func scanOAuthClient(scanner interface{ Scan(...any) error }) (*model.OAuthClient, error) {
	var item model.OAuthClient
	var redirects, metadata, created, updated string
	var identityType, metadataURI, metadataHash, metadataETag string
	var metadataFetched sql.NullString
	var enabled int
	if err := scanner.Scan(&item.ID, &item.Name, &redirects, &identityType, &metadataURI, &metadataHash, &metadataETag, &metadataFetched, &metadata, &enabled, &created, &updated); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(redirects), &item.RedirectURIs)
	item.IdentityType, item.MetadataURI, item.MetadataHash, item.MetadataETag = identityType, metadataURI, metadataHash, metadataETag
	item.MetadataFetchedAt = nullableTime(metadataFetched)
	item.ClientMetadata = json.RawMessage(metadata)
	item.Enabled = enabled != 0
	item.CreatedAt, item.UpdatedAt = parseTime(created), parseTime(updated)
	return &item, nil
}

// CreateOAuthGrantV2 creates the v2 grant, its audit principal, approval
// profile, and per-capability approval policies in one transaction. Access
// level and the versioned resource boundary are the authorization source.
func (s *Store) CreateOAuthGrantV2(ctx context.Context, grant *model.OAuthGrant, principal *model.APIPrincipal, profile *model.OAuthApprovalProfile, policies []model.ApprovalPolicy) error {
	principalScopes, err := json.Marshal(principal.Scopes)
	if err != nil {
		return err
	}
	cidrs, err := json.Marshal(principal.AllowedCIDRs)
	if err != nil {
		return err
	}
	boundary := normalizedJSONObject(grant.ResourceBoundaryJSON)
	if len(boundary) == 0 || boundary == "{}" {
		boundary = `{"version":1}`
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := now()
	if _, err := tx.ExecContext(ctx, `insert into api_principals(id,owner_user_id,name,type,enabled,scopes_json,resource_filter_json,allowed_cidrs_json,rate_limit_per_minute,max_concurrency,expires_at,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?)`, principal.ID, principal.OwnerUserID, principal.Name, principal.Type, boolInt(principal.Enabled), string(principalScopes), normalizedJSONObject(principal.ResourceFilter), string(cidrs), principal.RateLimitPerMinute, principal.MaxConcurrency, timePtrString(principal.ExpiresAt), ts, ts); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `insert into oauth_approval_profiles(id,name,auto_approve_risk,created_at,updated_at) values(?,?,?,?,?)`, profile.ID, profile.Name, profile.AutoApproveRisk, ts, ts); err != nil {
		return err
	}
	status := string(grant.Status)
	if status == "" {
		status = string(model.OAuthGrantActive)
	}
	if grant.AccessLevel == "" {
		grant.AccessLevel = "read"
	}
	if grant.PolicyVersion == 0 {
		grant.PolicyVersion = 1
	}
	if grant.RoleVersion == 0 {
		grant.RoleVersion = 1
	}
	if grant.ConsentVersion == 0 {
		grant.ConsentVersion = 1
	}
	if _, err := tx.ExecContext(ctx, `insert into oauth_grants(id,client_id,user_id,principal_id,access_level,resource_boundary_v2_json,approval_profile_id,offline_access,policy_version,role_version,consent_version,status,expires_at,revoked_at,revoke_reason,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, grant.ID, grant.ClientID, grant.UserID, principal.ID, grant.AccessLevel, string(boundary), profile.ID, boolInt(grant.OfflineAccess), grant.PolicyVersion, grant.RoleVersion, grant.ConsentVersion, status, timePtrString(grant.ExpiresAt), timePtrString(grant.RevokedAt), grant.RevokeReason, ts); err != nil {
		return err
	}
	for index := range policies {
		policy := &policies[index]
		if _, err := tx.ExecContext(ctx, `insert into approval_policies(id,principal_id,capability,resource_filter_json,mode,allow_risk4,expires_at,created_at,updated_at) values(?,?,?,?,?,?,?,?,?)`, policy.ID, principal.ID, policy.Capability, normalizedJSONObject(policy.ResourceFilter), policy.Mode, boolInt(false), timePtrString(policy.ExpiresAt), ts, ts); err != nil {
			return err
		}
		policy.PrincipalID, policy.CreatedAt, policy.UpdatedAt = principal.ID, parseTime(ts), parseTime(ts)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	principal.CreatedAt, principal.UpdatedAt = parseTime(ts), parseTime(ts)
	profile.CreatedAt, profile.UpdatedAt = parseTime(ts), parseTime(ts)
	grant.PrincipalID, grant.ApprovalProfileID, grant.CreatedAt = principal.ID, profile.ID, parseTime(ts)
	grant.ApprovalProfile = profile
	return nil
}

// CreateOAuthGrant keeps the legacy signature for tests and migration shims;
// it delegates to the v2 writer after normalizing a legacy scope grant.
func (s *Store) CreateOAuthGrant(ctx context.Context, grant *model.OAuthGrant, principal *model.APIPrincipal, profile *model.OAuthApprovalProfile, policies []model.ApprovalPolicy) error {
	if grant.AccessLevel == "" {
		grant.AccessLevel = "read"
	}
	return s.CreateOAuthGrantV2(ctx, grant, principal, profile, policies)
}

func (s *Store) GetOAuthGrant(ctx context.Context, id string) (*model.OAuthGrant, error) {
	return scanOAuthGrant(s.db.QueryRowContext(ctx, `select g.id,g.client_id,c.name,g.user_id,u.username,g.principal_id,g.access_level,g.resource_boundary_v2_json,g.approval_profile_id,g.offline_access,g.policy_version,g.role_version,g.consent_version,g.status,g.expires_at,g.last_used_at,g.revoked_at,g.revoke_reason,g.created_at,p.id,p.name,p.auto_approve_risk,p.created_at,p.updated_at from oauth_grants g join oauth_clients c on c.id=g.client_id join users u on u.id=g.user_id join oauth_approval_profiles p on p.id=g.approval_profile_id where g.id=?`, id))
}

func (s *Store) ListOAuthGrants(ctx context.Context) ([]model.OAuthGrant, error) {
	rows, err := s.db.QueryContext(ctx, `select g.id,g.client_id,c.name,g.user_id,u.username,g.principal_id,g.access_level,g.resource_boundary_v2_json,g.approval_profile_id,g.offline_access,g.policy_version,g.role_version,g.consent_version,g.status,g.expires_at,g.last_used_at,g.revoked_at,g.revoke_reason,g.created_at,p.id,p.name,p.auto_approve_risk,p.created_at,p.updated_at from oauth_grants g join oauth_clients c on c.id=g.client_id join users u on u.id=g.user_id join oauth_approval_profiles p on p.id=g.approval_profile_id order by g.created_at desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.OAuthGrant{}
	for rows.Next() {
		item, err := scanOAuthGrant(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *item)
	}
	return items, rows.Err()
}

func scanOAuthGrant(scanner interface{ Scan(...any) error }) (*model.OAuthGrant, error) {
	var item model.OAuthGrant
	var boundary, created, profileCreated, profileUpdated string
	var expires, lastUsed, revoked sql.NullString
	var offline int
	profile := &model.OAuthApprovalProfile{}
	if err := scanner.Scan(&item.ID, &item.ClientID, &item.ClientName, &item.UserID, &item.Username, &item.PrincipalID, &item.AccessLevel, &boundary, &item.ApprovalProfileID, &offline, &item.PolicyVersion, &item.RoleVersion, &item.ConsentVersion, &item.Status, &expires, &lastUsed, &revoked, &item.RevokeReason, &created, &profile.ID, &profile.Name, &profile.AutoApproveRisk, &profileCreated, &profileUpdated); err != nil {
		return nil, err
	}
	item.ResourceBoundaryJSON = []byte(boundary)
	item.OfflineAccess = offline != 0
	item.ExpiresAt, item.LastUsedAt, item.RevokedAt = nullableTime(expires), nullableTime(lastUsed), nullableTime(revoked)
	item.CreatedAt = parseTime(created)
	profile.CreatedAt, profile.UpdatedAt = parseTime(profileCreated), parseTime(profileUpdated)
	item.ApprovalProfile = profile
	return &item, nil
}

func (s *Store) RevokeOAuthGrant(ctx context.Context, id string, at time.Time) error {
	return s.RevokeOAuthGrantReason(ctx, id, at, "")
}

func (s *Store) RevokeOAuthGrantReason(ctx context.Context, id string, at time.Time, reason string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := at.UTC().Format(time.RFC3339Nano)
	var principalID string
	if err := tx.QueryRowContext(ctx, `select principal_id from oauth_grants where id=?`, id).Scan(&principalID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `update oauth_grants set status=?,revoked_at=coalesce(revoked_at,?),revoke_reason=? where id=? and revoked_at is null`, string(model.OAuthGrantRevoked), ts, reason, id)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	if _, err := tx.ExecContext(ctx, `update api_principals set enabled=0,updated_at=? where id=?`, ts, principalID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update oauth_access_tokens set revoked_at=? where grant_id=? and revoked_at is null`, ts, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update oauth_refresh_tokens set revoked_at=? where grant_id=? and revoked_at is null`, ts, id); err != nil {
		return err
	}
	return tx.Commit()
}

// MarkOAuthGrantNeedsReconsent marks a grant for mandatory re-consent and
// revokes every token and authorization code for the grant.
func (s *Store) MarkOAuthGrantNeedsReconsent(ctx context.Context, id string, at time.Time, reason string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := at.UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `update oauth_grants set status=?,revoke_reason=? where id=? and status='active'`, string(model.OAuthGrantNeedsReconsent), reason, id)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `update oauth_access_tokens set revoked_at=? where grant_id=? and revoked_at is null`, ts, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update oauth_refresh_tokens set revoked_at=? where grant_id=? and revoked_at is null`, ts, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from oauth_authorization_codes where grant_id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateOAuthAuthorizationCode(ctx context.Context, item *model.OAuthAuthorizationCode) error {
	ts := now()
	_, err := s.db.ExecContext(ctx, `insert into oauth_authorization_codes(code_hash,grant_id,client_id,user_id,principal_id,redirect_uri,resource,code_challenge,expires_at,created_at) values(?,?,?,?,?,?,?,?,?,?)`, item.CodeHash, item.GrantID, item.ClientID, item.UserID, item.PrincipalID, item.RedirectURI, item.Resource, item.CodeChallenge, item.ExpiresAt.UTC().Format(time.RFC3339Nano), ts)
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
	var expires, created string
	if err := tx.QueryRowContext(ctx, `select code_hash,grant_id,client_id,user_id,principal_id,redirect_uri,resource,code_challenge,expires_at,created_at from oauth_authorization_codes where code_hash=?`, codeHash).Scan(&item.CodeHash, &item.GrantID, &item.ClientID, &item.UserID, &item.PrincipalID, &item.RedirectURI, &item.Resource, &item.CodeChallenge, &expires, &created); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `delete from oauth_authorization_codes where code_hash=?`, codeHash); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	item.ExpiresAt, item.CreatedAt = parseTime(expires), parseTime(created)
	return &item, nil
}

func (s *Store) CreateOAuthTokens(ctx context.Context, access, refresh *model.OAuthToken) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := now()
	if _, err := tx.ExecContext(ctx, `insert into oauth_access_tokens(token_hash,grant_id,principal_id,client_id,user_id,resource,expires_at,created_at) values(?,?,?,?,?,?,?,?)`, access.TokenHash, access.GrantID, access.PrincipalID, access.ClientID, access.UserID, access.Resource, access.ExpiresAt.UTC().Format(time.RFC3339Nano), ts); err != nil {
		return err
	}
	if refresh != nil {
		if _, err := tx.ExecContext(ctx, `insert into oauth_refresh_tokens(token_hash,family_id,grant_id,parent_token_hash,principal_id,client_id,user_id,resource,expires_at,created_at) values(?,?,?,?,?,?,?,?,?,?)`, refresh.TokenHash, refresh.FamilyID, refresh.GrantID, refresh.ParentTokenHash, refresh.PrincipalID, refresh.ClientID, refresh.UserID, refresh.Resource, refresh.ExpiresAt.UTC().Format(time.RFC3339Nano), ts); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// AuthenticateMCPAccessToken resolves an access token for the /mcp audience,
// returning the live grant policy, the token, the effective user role, and the
// user status. MCP authorization derives everything from the grant policy; the
// API principal row is kept only for audit identity and rate limiting.
func (s *Store) AuthenticateMCPAccessToken(ctx context.Context, tokenHash, resource string, at time.Time) (token *model.OAuthToken, role, userStatus string, userID int64, err error) {
	row := s.db.QueryRowContext(ctx, `select t.token_hash,t.grant_id,t.principal_id,t.client_id,t.user_id,t.resource,t.expires_at,t.revoked_at,t.created_at,u.role,u.status from oauth_access_tokens t join users u on u.id=t.user_id where t.token_hash=?`, tokenHash)
	var item model.OAuthToken
	var expires, created string
	var revoked sql.NullString
	if err = row.Scan(&item.TokenHash, &item.GrantID, &item.PrincipalID, &item.ClientID, &item.UserID, &item.Resource, &expires, &revoked, &created, &role, &userStatus); err != nil {
		return nil, "", "", 0, err
	}
	item.ExpiresAt, item.RevokedAt, item.CreatedAt = parseTime(expires), nullableTime(revoked), parseTime(created)
	if item.RevokedAt != nil || !item.ExpiresAt.After(at) || item.Resource != resource {
		return nil, "", "", 0, sql.ErrNoRows
	}
	return &item, role, userStatus, item.UserID, nil
}

// AuthenticateOAuthAccessToken is the legacy REST-oriented resolver used by the
// generic apiAuth path and older tests. It verifies the token, grant, client,
// and user and returns the API principal with grant-derived scopes. MCP now
// uses AuthenticateMCPAccessToken + ResolveActiveGrant instead.
func (s *Store) AuthenticateOAuthAccessToken(ctx context.Context, tokenHash string, at time.Time) (*model.APIPrincipal, *model.OAuthToken, *model.OAuthGrant, error) {
	row := s.db.QueryRowContext(ctx, `select t.token_hash,t.grant_id,t.principal_id,t.client_id,t.user_id,t.resource,t.expires_at,t.revoked_at,t.created_at,p.id,p.owner_user_id,p.name,p.type,p.enabled,p.scopes_json,p.resource_filter_json,p.allowed_cidrs_json,p.rate_limit_per_minute,p.max_concurrency,p.expires_at,p.last_used_at,p.created_at,p.updated_at from oauth_access_tokens t join api_principals p on p.id=t.principal_id where t.token_hash=?`, tokenHash)
	var token model.OAuthToken
	var tokenExpires, tokenCreated string
	var tokenRevoked sql.NullString
	var principal model.APIPrincipal
	var owner sql.NullInt64
	var enabled int
	var scopesJSON, filterJSON, cidrsJSON string
	var principalExpires, principalLastUsed sql.NullString
	var principalCreated, principalUpdated string
	if err := row.Scan(&token.TokenHash, &token.GrantID, &token.PrincipalID, &token.ClientID, &token.UserID, &token.Resource, &tokenExpires, &tokenRevoked, &tokenCreated, &principal.ID, &owner, &principal.Name, &principal.Type, &enabled, &scopesJSON, &filterJSON, &cidrsJSON, &principal.RateLimitPerMinute, &principal.MaxConcurrency, &principalExpires, &principalLastUsed, &principalCreated, &principalUpdated); err != nil {
		return nil, nil, nil, err
	}
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
		return nil, nil, nil, sql.ErrNoRows
	}
	grant, err := s.GetOAuthGrant(ctx, token.GrantID)
	if err != nil || grant.PrincipalID != principal.ID || grant.ClientID != token.ClientID || grant.UserID != token.UserID || grant.RevokedAt != nil || grant.Status != model.OAuthGrantActive || grant.ExpiresAt != nil && !grant.ExpiresAt.After(at) {
		return nil, nil, nil, sql.ErrNoRows
	}
	// MCP authorization derives everything from the grant; the API principal
	// row keeps only a projection for audit identity and rate limiting.
	level := mcpauth.ParseAccessLevel(grant.AccessLevel)
	principal.Scopes = level.NormalizedScopes(grant.OfflineAccess)
	principal.ResourceFilter = mcpauth.LegacyResourceFilterJSON(mcpauth.ParseBoundary(grant.ResourceBoundaryJSON))
	_, _ = s.db.ExecContext(ctx, `update api_principals set last_used_at=? where id=?`, at.UTC().Format(time.RFC3339Nano), principal.ID)
	_, _ = s.db.ExecContext(ctx, `update oauth_grants set last_used_at=? where id=?`, at.UTC().Format(time.RFC3339Nano), grant.ID)
	grant.LastUsedAt = &at
	return &principal, &token, grant, nil
}

// ResolveActiveGrant returns the grant row for an active grant along with the
// effective role of the owning user. It verifies client and user state so a
// revoked client or disabled user immediately invalidates every token.
func (s *Store) ResolveActiveGrant(ctx context.Context, grantID string, at time.Time) (*model.OAuthGrant, model.Role, bool, error) {
	row := s.db.QueryRowContext(ctx, `select g.id,g.client_id,g.user_id,g.principal_id,g.access_level,g.resource_boundary_v2_json,g.approval_profile_id,p.auto_approve_risk,g.offline_access,g.policy_version,g.role_version,g.consent_version,g.status,g.expires_at,g.revoked_at,g.created_at,c.enabled,u.status from oauth_grants g join oauth_clients c on c.id=g.client_id join users u on u.id=g.user_id join oauth_approval_profiles p on p.id=g.approval_profile_id where g.id=?`, grantID)
	var grant model.OAuthGrant
	var boundary, status, created string
	var expires, revoked sql.NullString
	var autoRisk, offline, clientEnabled int
	var userStatus string
	if err := row.Scan(&grant.ID, &grant.ClientID, &grant.UserID, &grant.PrincipalID, &grant.AccessLevel, &boundary, &grant.ApprovalProfileID, &autoRisk, &offline, &grant.PolicyVersion, &grant.RoleVersion, &grant.ConsentVersion, &status, &expires, &revoked, &created, &clientEnabled, &userStatus); err != nil {
		return nil, "", false, err
	}
	grant.OfflineAccess = offline != 0
	grant.Status = model.OAuthGrantStatus(status)
	grant.ResourceBoundaryJSON = []byte(boundary)
	grant.ApprovalProfile = &model.OAuthApprovalProfile{ID: grant.ApprovalProfileID, AutoApproveRisk: autoRisk}
	grant.ExpiresAt = parseNullTime(expires)
	grant.RevokedAt = parseNullTime(revoked)
	grant.CreatedAt = parseTime(created)
	if grant.RevokedAt != nil || grant.Status != model.OAuthGrantActive || grant.ExpiresAt != nil && !grant.ExpiresAt.After(at) || clientEnabled == 0 || userStatus != "active" {
		return nil, "", false, sql.ErrNoRows
	}
	user := model.User{ID: grant.UserID, Status: userStatus}
	effectiveRole, err := s.EffectiveUserRole(ctx, user)
	if err != nil {
		return nil, "", false, err
	}
	return &grant, effectiveRole, true, nil
}

func (s *Store) ConsumeOAuthRefreshToken(ctx context.Context, tokenHash string, at time.Time) (*model.OAuthToken, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var item model.OAuthToken
	var expires, created string
	var consumed, revoked, reuseDetected sql.NullString
	if err := tx.QueryRowContext(ctx, `select token_hash,family_id,grant_id,parent_token_hash,principal_id,client_id,user_id,resource,expires_at,consumed_at,revoked_at,reuse_detected_at,created_at from oauth_refresh_tokens where token_hash=?`, tokenHash).Scan(&item.TokenHash, &item.FamilyID, &item.GrantID, &item.ParentTokenHash, &item.PrincipalID, &item.ClientID, &item.UserID, &item.Resource, &expires, &consumed, &revoked, &reuseDetected, &created); err != nil {
		return nil, err
	}
	item.ConsumedAt, item.RevokedAt, item.ReuseDetectedAt = nullableTime(consumed), nullableTime(revoked), nullableTime(reuseDetected)
	if item.ConsumedAt != nil {
		ts := at.UTC().Format(time.RFC3339Nano)
		_, _ = tx.ExecContext(ctx, `update oauth_refresh_tokens set revoked_at=coalesce(revoked_at,?),reuse_detected_at=case when token_hash=? then ? else reuse_detected_at end where family_id=?`, ts, tokenHash, ts, item.FamilyID)
		_, _ = tx.ExecContext(ctx, `update oauth_access_tokens set revoked_at=? where grant_id=? and revoked_at is null`, ts, item.GrantID)
		_ = tx.Commit()
		return &item, ErrOAuthRefreshReuse
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
