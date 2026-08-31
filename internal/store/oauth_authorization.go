package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/mcpauth"
	"github.com/OboardProject/oboard/internal/model"
)

// OAuthAuthorizeRequest carries one browser authorization approval in a single
// atomic store transaction: find-or-reuse the live grant and persist the code.
type OAuthAuthorizeRequest struct {
	ClientID        string
	UserID          int64
	ResourceKey     string
	ResourceURL     string
	RequestedScopes []string
	RequestOffline  bool
	AccessLevel             string
	BoundaryJSON            []byte
	PrincipalResourceFilter []byte
	NewGrant                *model.OAuthGrant
	NewPrincipal    *model.APIPrincipal
	NewProfile      *model.OAuthApprovalProfile
	NewPolicies     []model.ApprovalPolicy
	Code            *model.OAuthAuthorizationCode
}

func isOAuthGrantUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "unique") ||
		strings.Contains(strings.ToLower(err.Error()), "constraint")
}

// AuthorizeOAuthClient atomically reuses or creates the live MCP authorization
// and persists the authorization code. A failed code write never leaves a new grant.
func (s *Store) AuthorizeOAuthClient(ctx context.Context, req OAuthAuthorizeRequest) (*model.OAuthGrant, *model.APIPrincipal, error) {
	if req.ResourceKey == "" {
		req.ResourceKey = model.OAuthResourceKeyMCP
	}
	grant, principal, err := s.authorizeOAuthClientOnce(ctx, req)
	if err == nil {
		return grant, principal, nil
	}
	if !isOAuthGrantUniqueViolation(err) {
		return nil, nil, err
	}
	return s.authorizeOAuthClientOnce(ctx, req)
}

func (s *Store) authorizeOAuthClientOnce(ctx context.Context, req OAuthAuthorizeRequest) (*model.OAuthGrant, *model.APIPrincipal, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	ts := now()
	at := parseTime(ts)
	live, err := s.findLiveOAuthGrantTx(ctx, tx, req.ClientID, req.UserID, req.ResourceKey)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, err
	}

	var grant *model.OAuthGrant
	var principal *model.APIPrincipal

	if live != nil {
		grant = live
		principal, err = s.getAPIPrincipalTx(ctx, tx, live.PrincipalID)
		if err != nil {
			return nil, nil, err
		}
		offline := live.OfflineAccess
		if req.RequestOffline {
			offline = true
		}
		status := live.Status
		if status == model.OAuthGrantNeedsReconsent {
			status = model.OAuthGrantActive
		}
		boundary := normalizedJSONObject(req.BoundaryJSON)
		if len(boundary) == 0 || boundary == "{}" {
			boundary = `{"version":1}`
		}
		principalScopes, marshalErr := json.Marshal(mcpauth.ParseAccessLevel(req.AccessLevel).NormalizedScopes(offline))
		if marshalErr != nil {
			return nil, nil, marshalErr
		}
		if _, err := tx.ExecContext(ctx, `update oauth_grants set access_level=?,resource_boundary_v2_json=?,offline_access=?,status=?,last_authorized_at=? where id=?`,
			req.AccessLevel, boundary, boolInt(offline), string(status), ts, live.ID); err != nil {
			return nil, nil, err
		}
		if _, err := tx.ExecContext(ctx, `update api_principals set enabled=1,scopes_json=?,resource_filter_json=?,updated_at=? where id=?`,
			string(principalScopes), normalizedJSONObject(req.PrincipalResourceFilter), ts, live.PrincipalID); err != nil {
			return nil, nil, err
		}
		grant.AccessLevel = req.AccessLevel
		grant.ResourceBoundaryJSON = req.BoundaryJSON
		grant.OfflineAccess = offline
		grant.Status = status
		grant.LastAuthorizedAt = &at
		principal.Enabled = true
		principal.Scopes = mcpauth.ParseAccessLevel(req.AccessLevel).NormalizedScopes(offline)
	} else {
		if err := s.insertOAuthGrantV2Tx(ctx, tx, req.NewGrant, req.NewPrincipal, req.NewProfile, req.NewPolicies, ts); err != nil {
			return nil, nil, err
		}
		grant = req.NewGrant
		principal = req.NewPrincipal
		grant.LastAuthorizedAt = &at
		if _, err := tx.ExecContext(ctx, `update oauth_grants set last_authorized_at=?,resource_key=? where id=?`, ts, req.ResourceKey, grant.ID); err != nil {
			return nil, nil, err
		}
	}

	code := req.Code
	code.GrantID = grant.ID
	code.PrincipalID = principal.ID
	scopesJSON, err := json.Marshal(code.Scopes)
	if err != nil {
		return nil, nil, err
	}
	if _, err := tx.ExecContext(ctx, `insert into oauth_authorization_codes(code_hash,grant_id,client_id,user_id,principal_id,redirect_uri,resource,code_challenge,requested_scopes_json,expires_at,created_at) values(?,?,?,?,?,?,?,?,?,?,?)`,
		code.CodeHash, code.GrantID, code.ClientID, code.UserID, code.PrincipalID, code.RedirectURI, code.Resource, code.CodeChallenge, string(scopesJSON), code.ExpiresAt.UTC().Format(time.RFC3339Nano), ts); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	code.CreatedAt = at
	return grant, principal, nil
}

func (s *Store) findLiveOAuthGrant(ctx context.Context, clientID string, userID int64, resourceKey string) (*model.OAuthGrant, error) {
	return s.findLiveOAuthGrantTx(ctx, s.db, clientID, userID, resourceKey)
}

func (s *Store) findLiveOAuthGrantTx(ctx context.Context, q querier, clientID string, userID int64, resourceKey string) (*model.OAuthGrant, error) {
	if resourceKey == "" {
		resourceKey = model.OAuthResourceKeyMCP
	}
	row := q.QueryRowContext(ctx, `select g.id,g.client_id,c.name,g.user_id,u.username,g.principal_id,g.access_level,g.resource_boundary_v2_json,g.approval_profile_id,g.offline_access,g.policy_version,g.role_version,g.consent_version,g.status,g.resource_key,g.expires_at,g.last_used_at,g.last_authorized_at,g.revoked_at,g.revoke_reason,g.created_at,p.id,p.name,p.auto_approve_risk,p.created_at,p.updated_at from oauth_grants g join oauth_clients c on c.id=g.client_id join users u on u.id=g.user_id join oauth_approval_profiles p on p.id=g.approval_profile_id where g.client_id=? and g.user_id=? and g.resource_key=? and g.revoked_at is null and g.status in ('active','needs_reconsent') order by g.created_at desc limit 1`, clientID, userID, resourceKey)
	return scanOAuthGrant(row)
}

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *Store) getAPIPrincipalTx(ctx context.Context, q querier, id string) (*model.APIPrincipal, error) {
	var principal model.APIPrincipal
	var owner sql.NullInt64
	var enabled int
	var scopesJSON, filterJSON, cidrsJSON string
	var expires, lastUsed sql.NullString
	var created, updated string
	if err := q.QueryRowContext(ctx, `select id,owner_user_id,name,type,enabled,scopes_json,resource_filter_json,allowed_cidrs_json,rate_limit_per_minute,max_concurrency,expires_at,last_used_at,created_at,updated_at from api_principals where id=?`, id).Scan(
		&principal.ID, &owner, &principal.Name, &principal.Type, &enabled, &scopesJSON, &filterJSON, &cidrsJSON,
		&principal.RateLimitPerMinute, &principal.MaxConcurrency, &expires, &lastUsed, &created, &updated,
	); err != nil {
		return nil, err
	}
	principal.Enabled = enabled != 0
	if owner.Valid {
		principal.OwnerUserID = &owner.Int64
	}
	_ = json.Unmarshal([]byte(scopesJSON), &principal.Scopes)
	_ = json.Unmarshal([]byte(cidrsJSON), &principal.AllowedCIDRs)
	principal.ResourceFilter = json.RawMessage(filterJSON)
	principal.ExpiresAt, principal.LastUsedAt = nullableTime(expires), nullableTime(lastUsed)
	principal.CreatedAt, principal.UpdatedAt = parseTime(created), parseTime(updated)
	return &principal, nil
}

func (s *Store) insertOAuthGrantV2Tx(ctx context.Context, tx *sql.Tx, grant *model.OAuthGrant, principal *model.APIPrincipal, profile *model.OAuthApprovalProfile, policies []model.ApprovalPolicy, ts string) error {
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
	resourceKey := grant.ResourceKey
	if resourceKey == "" {
		resourceKey = model.OAuthResourceKeyMCP
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
	if _, err := tx.ExecContext(ctx, `insert into oauth_grants(id,client_id,user_id,principal_id,access_level,resource_boundary_v2_json,approval_profile_id,offline_access,policy_version,role_version,consent_version,status,resource_key,last_authorized_at,expires_at,revoked_at,revoke_reason,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		grant.ID, grant.ClientID, grant.UserID, principal.ID, grant.AccessLevel, string(boundary), profile.ID, boolInt(grant.OfflineAccess), grant.PolicyVersion, grant.RoleVersion, grant.ConsentVersion, status, resourceKey, ts, timePtrString(grant.ExpiresAt), timePtrString(grant.RevokedAt), grant.RevokeReason, ts); err != nil {
		return err
	}
	for index := range policies {
		policy := &policies[index]
		if _, err := tx.ExecContext(ctx, `insert into approval_policies(id,principal_id,capability,resource_filter_json,mode,allow_risk4,expires_at,created_at,updated_at) values(?,?,?,?,?,?,?,?,?)`, policy.ID, principal.ID, policy.Capability, normalizedJSONObject(policy.ResourceFilter), policy.Mode, boolInt(false), timePtrString(policy.ExpiresAt), ts, ts); err != nil {
			return err
		}
		policy.PrincipalID, policy.CreatedAt, policy.UpdatedAt = principal.ID, parseTime(ts), parseTime(ts)
	}
	principal.CreatedAt, principal.UpdatedAt = parseTime(ts), parseTime(ts)
	profile.CreatedAt, profile.UpdatedAt = parseTime(ts), parseTime(ts)
	grant.PrincipalID, grant.ApprovalProfileID, grant.CreatedAt = principal.ID, profile.ID, parseTime(ts)
	grant.ApprovalProfile = profile
	grant.ResourceKey = resourceKey
	return nil
}

// RevokeOAuthAuthorization revokes every live authorization for the same client,
// user, and resource key as the referenced grant.
func (s *Store) RevokeOAuthAuthorization(ctx context.Context, grantID string, at time.Time) error {
	grant, err := s.GetOAuthGrant(ctx, grantID)
	if err != nil {
		return err
	}
	resourceKey := grant.ResourceKey
	if resourceKey == "" {
		resourceKey = model.OAuthResourceKeyMCP
	}
	rows, err := s.db.QueryContext(ctx, `select id from oauth_grants where client_id=? and user_id=? and resource_key=? and revoked_at is null and status in ('active','needs_reconsent')`, grant.ClientID, grant.UserID, resourceKey)
	if err != nil {
		return err
	}
	var grantIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return errors.Join(err, rows.Close())
		}
		grantIDs = append(grantIDs, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(grantIDs) == 0 {
		return sql.ErrNoRows
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := at.UTC().Format(time.RFC3339Nano)
	for _, id := range grantIDs {
		var principalID string
		if err := tx.QueryRowContext(ctx, `select principal_id from oauth_grants where id=?`, id).Scan(&principalID); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `update oauth_grants set status=?,revoked_at=coalesce(revoked_at,?),revoke_reason=coalesce(nullif(revoke_reason,''),?) where id=? and revoked_at is null`, string(model.OAuthGrantRevoked), ts, "authorization_revoked", id)
		if err != nil {
			return err
		}
		if count, _ := result.RowsAffected(); count == 0 {
			continue
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
		if _, err := tx.ExecContext(ctx, `update mcp_privileged_grants set revoked_at=coalesce(revoked_at,?),updated_at=? where oauth_grant_id=? and revoked_at is null`, ts, ts, id); err != nil {
			return err
		}
		if err := s.invalidateOAuthGrantConsumablesTx(ctx, tx, ts, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RevokeOAuthOfflineAccess disables offline refresh for one authorization without
// revoking active access tokens.
func (s *Store) RevokeOAuthOfflineAccess(ctx context.Context, grantID string, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := at.UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `update oauth_grants set offline_access=0 where id=? and revoked_at is null and status in ('active','needs_reconsent')`, grantID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	if _, err := tx.ExecContext(ctx, `update oauth_refresh_tokens set revoked_at=? where grant_id=? and revoked_at is null`, ts, grantID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) invalidateOAuthGrantConsumablesTx(ctx context.Context, tx *sql.Tx, ts, grantID string) error {
	if _, err := tx.ExecContext(ctx, `delete from oauth_authorization_codes where grant_id=?`, grantID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from mcp_prepared_plans where grant_id=? and consumed_at is null`, grantID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from mcp_task_continuations where grant_id=?`, grantID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update automation_external_actions set consumed_at=? where grant_id=? and consumed_at is null`, ts, grantID); err != nil {
		return err
	}
	return nil
}

// OAuthGrantSessionStats returns active token and refresh-family counts.
func (s *Store) OAuthGrantSessionStats(ctx context.Context, grantID string, at time.Time) (accessTokens, refreshFamilies int, err error) {
	expiresAfter := at.UTC().Format(time.RFC3339Nano)
	if err := s.db.QueryRowContext(ctx, `select count(*) from oauth_access_tokens where grant_id=? and revoked_at is null and expires_at>?`, grantID, expiresAfter).Scan(&accessTokens); err != nil {
		return 0, 0, err
	}
	if err := s.db.QueryRowContext(ctx, `select count(distinct family_id) from oauth_refresh_tokens where grant_id=? and revoked_at is null and expires_at>?`, grantID, expiresAfter).Scan(&refreshFamilies); err != nil {
		return 0, 0, err
	}
	return accessTokens, refreshFamilies, nil
}

func (s *Store) EnrichOAuthGrantStats(ctx context.Context, grants []model.OAuthGrant, at time.Time) error {
	for index := range grants {
		access, families, err := s.OAuthGrantSessionStats(ctx, grants[index].ID, at)
		if err != nil {
			return err
		}
		grants[index].ActiveAccessTokens = access
		grants[index].ActiveRefreshFamilies = families
	}
	return nil
}
