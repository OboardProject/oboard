package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func (s *Store) GetServerRemoteAccessPolicy(ctx context.Context, serverID int64) (model.ServerRemoteAccessPolicy, error) {
	var item model.ServerRemoteAccessPolicy
	var terminal, mcp int
	var created, updated string
	err := s.db.QueryRowContext(ctx, `select server_id,remote_terminal_enabled,mcp_enabled,created_at,updated_at from server_remote_access_policies where server_id=?`, serverID).Scan(&item.ServerID, &terminal, &mcp, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ServerRemoteAccessPolicy{ServerID: serverID, RemoteTerminalEnabled: true}, nil
	}
	if err != nil {
		return model.ServerRemoteAccessPolicy{}, err
	}
	item.RemoteTerminalEnabled = terminal != 0
	item.MCPEnabled = mcp != 0
	item.CreatedAt = parseTime(created)
	item.UpdatedAt = parseTime(updated)
	return item, nil
}

func (s *Store) UpsertServerRemoteAccessPolicy(ctx context.Context, policy model.ServerRemoteAccessPolicy) (model.ServerRemoteAccessPolicy, error) {
	ts := now()
	_, err := s.db.ExecContext(ctx, `insert into server_remote_access_policies(server_id,remote_terminal_enabled,mcp_enabled,created_at,updated_at) values(?,?,?, ?,?) on conflict(server_id) do update set remote_terminal_enabled=excluded.remote_terminal_enabled,mcp_enabled=excluded.mcp_enabled,updated_at=excluded.updated_at`, policy.ServerID, boolInt(policy.RemoteTerminalEnabled), boolInt(policy.MCPEnabled), ts, ts)
	if err != nil {
		return model.ServerRemoteAccessPolicy{}, err
	}
	return s.GetServerRemoteAccessPolicy(ctx, policy.ServerID)
}

func (s *Store) GetServerRemoteAccessStatus(ctx context.Context, serverID int64) (model.ServerRemoteAccessStatus, error) {
	var item model.ServerRemoteAccessStatus
	var capabilitiesJSON, allowJSON, updated string
	err := s.db.QueryRowContext(ctx, `select server_id,capabilities_json,local_mode,local_allow_json,updated_at from server_remote_access_status where server_id=?`, serverID).Scan(&item.ServerID, &capabilitiesJSON, &item.LocalMode, &allowJSON, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ServerRemoteAccessStatus{ServerID: serverID, Capabilities: []string{}}, nil
	}
	if err != nil {
		return model.ServerRemoteAccessStatus{}, err
	}
	_ = json.Unmarshal([]byte(capabilitiesJSON), &item.Capabilities)
	if item.Capabilities == nil {
		item.Capabilities = []string{}
	}
	_ = json.Unmarshal([]byte(allowJSON), &item.LocalAllow)
	item.UpdatedAt = parseTime(updated)
	return item, nil
}

func (s *Store) UpsertServerRemoteAccessStatus(ctx context.Context, serverID int64, report model.RemoteAccessReport) error {
	if serverID <= 0 {
		return nil
	}
	capabilities, _ := json.Marshal(report.Capabilities)
	allow, _ := json.Marshal(report.LocalAllow)
	mode := strings.TrimSpace(report.LocalMode)
	if mode == "" {
		mode = model.RemoteAccessModeStandard
	}
	ts := now()
	_, err := s.db.ExecContext(ctx, `insert into server_remote_access_status(server_id,capabilities_json,local_mode,local_allow_json,updated_at) values(?,?,?,?,?) on conflict(server_id) do update set capabilities_json=excluded.capabilities_json,local_mode=excluded.local_mode,local_allow_json=excluded.local_allow_json,updated_at=excluded.updated_at`, serverID, string(capabilities), mode, string(allow), ts)
	return err
}

func (s *Store) GetMCPPrivilegedGrantByOAuthGrant(ctx context.Context, oauthGrantID string) (*model.MCPPrivilegedGrant, error) {
	return s.scanMCPPrivilegedGrant(ctx, `select id,oauth_grant_id,oauth_client_id,authorized_user_id,capabilities_json,resource_boundary_json,expires_at,revoked_at,created_by_user_id,created_at,updated_at,last_step_up_at,revision from mcp_privileged_grants where oauth_grant_id=?`, oauthGrantID)
}

func (s *Store) GetMCPPrivilegedGrant(ctx context.Context, id int64) (*model.MCPPrivilegedGrant, error) {
	return s.scanMCPPrivilegedGrant(ctx, `select id,oauth_grant_id,oauth_client_id,authorized_user_id,capabilities_json,resource_boundary_json,expires_at,revoked_at,created_by_user_id,created_at,updated_at,last_step_up_at,revision from mcp_privileged_grants where id=?`, id)
}

func (s *Store) scanMCPPrivilegedGrant(ctx context.Context, query string, args ...any) (*model.MCPPrivilegedGrant, error) {
	var item model.MCPPrivilegedGrant
	var capabilitiesJSON string
	var expires, revoked, lastStepUp sql.NullString
	var created, updated string
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&item.ID, &item.OAuthGrantID, &item.OAuthClientID, &item.AuthorizedUserID, &capabilitiesJSON, &item.ResourceBoundaryJSON, &expires, &revoked, &item.CreatedByUserID, &created, &updated, &lastStepUp, &item.Revision)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(capabilitiesJSON), &item.Capabilities)
	if item.Capabilities == nil {
		item.Capabilities = []string{}
	}
	item.ExpiresAt = parseNullTime(expires)
	item.RevokedAt = parseNullTime(revoked)
	item.LastStepUpAt = parseNullTime(lastStepUp)
	item.CreatedAt = parseTime(created)
	item.UpdatedAt = parseTime(updated)
	return &item, nil
}

func (s *Store) UpsertMCPPrivilegedGrant(ctx context.Context, grant model.MCPPrivilegedGrant) (*model.MCPPrivilegedGrant, error) {
	if grant.OAuthGrantID == "" {
		return nil, errors.New("oauth_grant_id is required")
	}
	capabilities, err := json.Marshal(grant.Capabilities)
	if err != nil {
		return nil, err
	}
	if len(grant.ResourceBoundaryJSON) == 0 {
		grant.ResourceBoundaryJSON = []byte(`{}`)
	}
	ts := now()
	var expires any
	if grant.ExpiresAt != nil && !grant.ExpiresAt.IsZero() {
		expires = grant.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	existing, err := s.GetMCPPrivilegedGrantByOAuthGrant(ctx, grant.OAuthGrantID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if errors.Is(err, sql.ErrNoRows) || existing == nil {
		res, err := s.db.ExecContext(ctx, `insert into mcp_privileged_grants(oauth_grant_id,oauth_client_id,authorized_user_id,capabilities_json,resource_boundary_json,expires_at,created_by_user_id,created_at,updated_at,last_step_up_at,revision) values(?,?,?,?,?,?,?,?,?,?,1)`, grant.OAuthGrantID, grant.OAuthClientID, grant.AuthorizedUserID, string(capabilities), string(grant.ResourceBoundaryJSON), expires, grant.CreatedByUserID, ts, ts, ts)
		if err != nil {
			return nil, err
		}
		id, _ := res.LastInsertId()
		return s.GetMCPPrivilegedGrant(ctx, id)
	}
	_, err = s.db.ExecContext(ctx, `update mcp_privileged_grants set oauth_client_id=?,authorized_user_id=?,capabilities_json=?,resource_boundary_json=?,expires_at=?,revoked_at=null,created_by_user_id=?,updated_at=?,last_step_up_at=?,revision=revision+1 where id=?`, grant.OAuthClientID, grant.AuthorizedUserID, string(capabilities), string(grant.ResourceBoundaryJSON), expires, grant.CreatedByUserID, ts, ts, existing.ID)
	if err != nil {
		return nil, err
	}
	return s.GetMCPPrivilegedGrant(ctx, existing.ID)
}

func (s *Store) RevokeMCPPrivilegedGrant(ctx context.Context, id int64) error {
	ts := now()
	res, err := s.db.ExecContext(ctx, `update mcp_privileged_grants set revoked_at=coalesce(revoked_at,?),updated_at=? where id=?`, ts, ts, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) RevokeMCPPrivilegedGrantsForOAuthGrant(ctx context.Context, oauthGrantID string) error {
	ts := now()
	_, err := s.db.ExecContext(ctx, `update mcp_privileged_grants set revoked_at=coalesce(revoked_at,?),updated_at=? where oauth_grant_id=? and revoked_at is null`, ts, ts, oauthGrantID)
	return err
}

func (s *Store) InsertRemoteAccessAudit(ctx context.Context, event model.RemoteAccessAuditEvent) error {
	if event.StartedAt.IsZero() {
		event.StartedAt = time.Now().UTC()
	}
	if event.MetadataJSON == nil {
		event.MetadataJSON = []byte("{}")
	}
	var actorUserID, serverID any
	if event.ActorUserID != nil {
		actorUserID = *event.ActorUserID
	}
	if event.ServerID != nil {
		serverID = *event.ServerID
	}
	var ended any
	if event.EndedAt != nil && !event.EndedAt.IsZero() {
		ended = event.EndedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, `insert into remote_access_audit(event_type,actor_type,actor_user_id,oauth_client_id,oauth_grant_id,server_id,session_id,request_id,capability,result,started_at,ended_at,duration_ms,source_ip,metadata_json) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, event.EventType, event.ActorType, actorUserID, event.OAuthClientID, event.OAuthGrantID, serverID, event.SessionID, event.RequestID, event.Capability, event.Result, event.StartedAt.UTC().Format(time.RFC3339Nano), ended, event.DurationMS, event.SourceIP, string(event.MetadataJSON))
	return err
}

func (s *Store) ListRemoteAccessAudit(ctx context.Context, limit int) ([]model.RemoteAccessAuditEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `select id,event_type,actor_type,actor_user_id,oauth_client_id,oauth_grant_id,server_id,session_id,request_id,capability,result,started_at,ended_at,duration_ms,source_ip,metadata_json from remote_access_audit order by id desc limit ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.RemoteAccessAuditEvent{}
	for rows.Next() {
		var item model.RemoteAccessAuditEvent
		var actorUserID, serverID sql.NullInt64
		var started string
		var ended sql.NullString
		var metadata string
		if err := rows.Scan(&item.ID, &item.EventType, &item.ActorType, &actorUserID, &item.OAuthClientID, &item.OAuthGrantID, &serverID, &item.SessionID, &item.RequestID, &item.Capability, &item.Result, &started, &ended, &item.DurationMS, &item.SourceIP, &metadata); err != nil {
			return nil, err
		}
		if actorUserID.Valid {
			id := actorUserID.Int64
			item.ActorUserID = &id
		}
		if serverID.Valid {
			id := serverID.Int64
			item.ServerID = &id
		}
		item.StartedAt = parseTime(started)
		item.EndedAt = parseNullTime(ended)
		item.MetadataJSON = []byte(metadata)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) CreateStepUpChallenge(ctx context.Context, item model.StepUpChallenge) error {
	_, _ = s.db.ExecContext(ctx, `delete from step_up_challenges where expires_at<=?`, now())
	webauthn := string(item.WebAuthnSessionJSON)
	if webauthn == "" {
		webauthn = "{}"
	}
	_, err := s.db.ExecContext(ctx, `insert into step_up_challenges(id,user_id,session_id,session_version,purpose,resource_type,resource_id,nonce,webauthn_session_json,expires_at,created_at) values(?,?,?,?,?,?,?,?,?,?,?)`, item.ID, item.UserID, item.SessionID, item.SessionVersion, item.Purpose, item.ResourceType, item.ResourceID, item.Nonce, webauthn, item.ExpiresAt.UTC().Format(time.RFC3339Nano), item.CreatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) GetStepUpChallenge(ctx context.Context, id string) (model.StepUpChallenge, error) {
	var item model.StepUpChallenge
	var consumed sql.NullString
	var expires, created string
	err := s.db.QueryRowContext(ctx, `select id,user_id,session_id,session_version,purpose,resource_type,resource_id,nonce,webauthn_session_json,consumed_at,expires_at,created_at from step_up_challenges where id=?`, id).Scan(&item.ID, &item.UserID, &item.SessionID, &item.SessionVersion, &item.Purpose, &item.ResourceType, &item.ResourceID, &item.Nonce, &item.WebAuthnSessionJSON, &consumed, &expires, &created)
	if err != nil {
		return model.StepUpChallenge{}, err
	}
	item.ConsumedAt = parseNullTime(consumed)
	item.ExpiresAt = parseTime(expires)
	item.CreatedAt = parseTime(created)
	if !item.ExpiresAt.After(time.Now().UTC()) {
		return model.StepUpChallenge{}, sql.ErrNoRows
	}
	return item, nil
}

func (s *Store) ConsumeStepUpChallenge(ctx context.Context, id string) error {
	ts := now()
	res, err := s.db.ExecContext(ctx, `update step_up_challenges set consumed_at=? where id=? and consumed_at is null and expires_at>?`, ts, id, ts)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) ConsumeStepUpToken(ctx context.Context, tokenHash string, expiresAt time.Time) error {
	_, _ = s.db.ExecContext(ctx, `delete from consumed_step_up_tokens where expires_at<=?`, now())
	_, err := s.db.ExecContext(ctx, `insert into consumed_step_up_tokens(token_hash,expires_at) values(?,?)`, tokenHash, expiresAt.UTC().Format(time.RFC3339Nano))
	return err
}
