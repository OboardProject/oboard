package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// oauthV2MigrationSetting marks whether the OAuth v2 re-consent migration ran.
const oauthV2MigrationSetting = "system.oauth_v2_migration_done"

// MigrateOAuthV2 migrates legacy grants to the v2 model exactly once:
//
//   - Grants with a fine-grained write scope are suggested operate; grants with
//     only read/plan scopes are suggested read.
//   - Every legacy grant is marked needs_reconsent so the user must choose
//     read or operate again on next connection.
//   - All access tokens, refresh token families, and authorization codes for
//     those grants are revoked, so old tokens can never bypass the new model.
//
// The migration never widens a grant: legacy fine-grained write scopes are not
// auto-expanded into an operate grant. It is idempotent and transactional per
// grant batch, and a failure leaves earlier batches fully migrated.
func (s *Store) MigrateOAuthV2(ctx context.Context) error {
	var done string
	if err := s.db.QueryRowContext(ctx, `select value from app_settings where key=?`, oauthV2MigrationSetting).Scan(&done); err == nil && strings.TrimSpace(done) == "1" {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `select id,coalesce(scopes_json,'[]'),coalesce(access_level,'') from oauth_grants`)
	if err != nil {
		return err
	}
	type legacyGrant struct {
		id          string
		scopesJSON  string
		accessLevel string
	}
	var grants []legacyGrant
	for rows.Next() {
		var item legacyGrant
		if err := rows.Scan(&item.id, &item.scopesJSON, &item.accessLevel); err != nil {
			rows.Close()
			return err
		}
		grants = append(grants, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	at := time.Now().UTC()
	migrated := 0
	for _, item := range grants {
		scopes := []string{}
		_ = json.Unmarshal([]byte(item.scopesJSON), &scopes)
		suggested := "read"
		if grantHasWriteScope(scopes) {
			suggested = "operate"
		}
		// Only touch grants that are still active and still on the legacy
		// consent version. New v2 grants already carry an access level and
		// consent_version 2.
		status, version, err := s.grantConsentState(ctx, item.id)
		if err != nil || version >= 2 || status != "active" {
			continue
		}
		if err := s.markGrantMigrated(ctx, item.id, suggested, at); err != nil {
			return err
		}
		migrated++
	}
	if _, err := s.db.ExecContext(ctx, `insert into app_settings(key,value,updated_at) values(?,?,?) on conflict(key) do update set value=excluded.value, updated_at=excluded.updated_at`, oauthV2MigrationSetting, "1", at.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	_ = migrated
	return nil
}

func grantHasWriteScope(scopes []string) bool {
	writeScopes := map[string]bool{
		"servers:onboard": true, "subscriptions:resume": true, "subscriptions:manage": true,
		"topology:write": true, "deployments:apply": true, "servers:plan": false,
		"deployments:validate": false, "proxy_paths:plan": false,
	}
	for _, scope := range scopes {
		if writeScopes[scope] {
			return true
		}
	}
	return false
}

func (s *Store) grantConsentState(ctx context.Context, id string) (string, int, error) {
	var status string
	var version int
	if err := s.db.QueryRowContext(ctx, `select coalesce(status,'active'),consent_version from oauth_grants where id=?`, id).Scan(&status, &version); err != nil {
		return "", 0, err
	}
	return status, version, nil
}

// markGrantMigrated marks one grant needs_reconsent and revokes every token and
// authorization code for it in a single transaction.
func (s *Store) markGrantMigrated(ctx context.Context, id, suggested string, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := at.UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `update oauth_grants set access_level=?,status=?,consent_version=1,revoke_reason=? where id=? and revoked_at is null`, suggested, string(model.OAuthGrantNeedsReconsent), "OAuth v2 migration requires re-consent", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update oauth_access_tokens set revoked_at=coalesce(revoked_at,?) where grant_id=?`, ts, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update oauth_refresh_tokens set revoked_at=coalesce(revoked_at,?) where grant_id=?`, ts, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from oauth_authorization_codes where grant_id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}
