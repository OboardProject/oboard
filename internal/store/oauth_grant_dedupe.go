package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

const oauthGrantDedupeSetting = "system.oauth_grant_dedupe_done"

// MigrateOAuthGrantDedupe merges legacy duplicate live MCP authorizations into
// one canonical grant per client/user/resource_key. It is idempotent.
func (s *Store) MigrateOAuthGrantDedupe(ctx context.Context) error {
	var done string
	if err := s.db.QueryRowContext(ctx, `select value from app_settings where key=?`, oauthGrantDedupeSetting).Scan(&done); err == nil && strings.TrimSpace(done) == "1" {
		return nil
	}
	groups, err := s.listDuplicateOAuthGrantGroups(ctx)
	if err != nil {
		return err
	}
	at := time.Now().UTC()
	for _, group := range groups {
		if err := s.mergeDuplicateOAuthGrantGroup(ctx, group, at); err != nil {
			return err
		}
	}
	return s.setOAuthGrantDedupeDone(ctx)
}

func (s *Store) setOAuthGrantDedupeDone(ctx context.Context) error {
	at := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `insert into app_settings(key,value,updated_at) values(?,?,?) on conflict(key) do update set value=excluded.value, updated_at=excluded.updated_at`, oauthGrantDedupeSetting, "1", at.UTC().Format(time.RFC3339Nano))
	return err
}

type oauthGrantDuplicateGroup struct {
	clientID    string
	userID      int64
	resourceKey string
	grantIDs    []string
}

func (s *Store) listDuplicateOAuthGrantGroups(ctx context.Context) ([]oauthGrantDuplicateGroup, error) {
	rows, err := s.db.QueryContext(ctx, `select client_id,user_id,resource_key,group_concat(id) from oauth_grants where revoked_at is null and status in ('active','needs_reconsent') group by client_id,user_id,resource_key having count(*)>1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var groups []oauthGrantDuplicateGroup
	for rows.Next() {
		var item oauthGrantDuplicateGroup
		var ids string
		if err := rows.Scan(&item.clientID, &item.userID, &item.resourceKey, &ids); err != nil {
			return nil, err
		}
		item.grantIDs = strings.Split(ids, ",")
		groups = append(groups, item)
	}
	return groups, rows.Err()
}

func (s *Store) mergeDuplicateOAuthGrantGroup(ctx context.Context, group oauthGrantDuplicateGroup, at time.Time) error {
	candidates, err := s.loadOAuthGrantMergeCandidates(ctx, group.grantIDs)
	if err != nil {
		return err
	}
	if len(candidates) < 2 {
		return nil
	}
	canonical := pickCanonicalOAuthGrant(candidates)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := at.UTC().Format(time.RFC3339Nano)
	canonicalOffline := canonical.offline
	for _, item := range candidates {
		if item.id == canonical.id {
			continue
		}
		if item.offline {
			canonicalOffline = true
		}
		reason := fmt.Sprintf("legacy_duplicate_merged_into:%s", canonical.id)
		if _, err := tx.ExecContext(ctx, `update oauth_grants set status=?,revoked_at=coalesce(revoked_at,?),revoke_reason=? where id=? and revoked_at is null`, string(model.OAuthGrantRevoked), ts, reason, item.id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `update oauth_access_tokens set grant_id=?,principal_id=? where grant_id=?`, canonical.id, canonical.principalID, item.id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `update oauth_refresh_tokens set grant_id=?,principal_id=? where grant_id=?`, canonical.id, canonical.principalID, item.id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `update mcp_privileged_grants set revoked_at=coalesce(revoked_at,?),updated_at=? where oauth_grant_id=? and revoked_at is null`, ts, ts, item.id); err != nil {
			return err
		}
		if err := s.invalidateOAuthGrantConsumablesTx(ctx, tx, ts, item.id); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `update oauth_grants set offline_access=? where id=?`, boolInt(canonicalOffline), canonical.id); err != nil {
		return err
	}
	var activeRefresh int
	if err := tx.QueryRowContext(ctx, `select count(*) from oauth_refresh_tokens where grant_id=? and revoked_at is null and expires_at>?`, canonical.id, ts).Scan(&activeRefresh); err != nil {
		return err
	}
	if activeRefresh > 0 && !canonicalOffline {
		if _, err := tx.ExecContext(ctx, `update oauth_grants set offline_access=1 where id=?`, canonical.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type oauthGrantMergeCandidate struct {
	id          string
	principalID string
	offline     bool
	lastUsed    sql.NullString
	created     string
}

func (s *Store) loadOAuthGrantMergeCandidates(ctx context.Context, grantIDs []string) ([]oauthGrantMergeCandidate, error) {
	if len(grantIDs) == 0 {
		return nil, nil
	}
	placeholders := strings.Repeat("?,", len(grantIDs)-1) + "?"
	args := make([]any, len(grantIDs))
	for i, id := range grantIDs {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, `select id,principal_id,offline_access,last_used_at,created_at from oauth_grants where id in (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []oauthGrantMergeCandidate
	for rows.Next() {
		var item oauthGrantMergeCandidate
		var offline int
		if err := rows.Scan(&item.id, &item.principalID, &offline, &item.lastUsed, &item.created); err != nil {
			return nil, err
		}
		item.offline = offline != 0
		items = append(items, item)
	}
	return items, rows.Err()
}

func pickCanonicalOAuthGrant(items []oauthGrantMergeCandidate) oauthGrantMergeCandidate {
	best := items[0]
	for _, item := range items[1:] {
		if oauthGrantCandidateRank(item) > oauthGrantCandidateRank(best) {
			best = item
		}
	}
	return best
}

func oauthGrantCandidateRank(item oauthGrantMergeCandidate) int {
	rank := 0
	if item.lastUsed.Valid && strings.TrimSpace(item.lastUsed.String) != "" {
		rank += 1_000_000
		rank += int(parseTime(item.lastUsed.String).Unix())
	}
	rank += int(parseTime(item.created).Unix())
	return rank
}

// EnsureOAuthGrantLiveUniqueIndex creates the partial unique index after legacy
// duplicates are merged.
func (s *Store) EnsureOAuthGrantLiveUniqueIndex(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `create unique index if not exists idx_oauth_grants_live_authorization on oauth_grants(client_id,user_id,resource_key) where revoked_at is null and status in ('active','needs_reconsent')`)
	return err
}

// BackfillOAuthGrantAuthorizationFields sets resource_key and last_authorized_at
// on existing rows before dedupe and the unique index.
func (s *Store) BackfillOAuthGrantAuthorizationFields(ctx context.Context) error {
	ts := now()
	if _, err := s.db.ExecContext(ctx, `update oauth_grants set resource_key='mcp' where coalesce(resource_key,'')=''`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `update oauth_grants set last_authorized_at=created_at where last_authorized_at is null or last_authorized_at=''`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `update oauth_authorization_codes set requested_scopes_json='[]' where coalesce(requested_scopes_json,'')=''`); err != nil && !errors.Is(err, sql.ErrNoRows) {
		if !strings.Contains(strings.ToLower(err.Error()), "no such column") {
			return err
		}
	}
	_ = ts
	return nil
}
