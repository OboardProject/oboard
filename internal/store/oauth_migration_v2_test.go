package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// seedLegacyGrant writes a legacy-format grant row (fine-grained scopes,
// consent_version 1) plus an active access token.
func seedLegacyGrant(t *testing.T, db *Store, clientID string, principalID string, userID int64, scopes []string) string {
	t.Helper()
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	grantID := "grt_legacy_" + clientID
	if _, err := db.db.ExecContext(context.Background(), `insert into oauth_approval_profiles(id,name,auto_approve_risk,created_at,updated_at) values(?,?,?,?,?)`, "oap_legacy_"+clientID, "legacy", 0, ts, ts); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(context.Background(), `insert into oauth_grants(id,client_id,user_id,principal_id,access_level,resource_boundary_v2_json,scopes_json,resource_filter_json,approval_profile_id,offline_access,policy_version,role_version,consent_version,status,expires_at,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, grantID, clientID, userID, principalID, "", `{"version":1}`, scopesJSON(scopes), `{}`, "oap_legacy_"+clientID, 0, 1, 1, 1, string(model.OAuthGrantActive), nil, ts); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(context.Background(), `insert into oauth_access_tokens(token_hash,grant_id,principal_id,client_id,user_id,scopes_json,resource,expires_at,created_at) values(?,?,?,?,?,?,?,?,?)`, "hash_legacy_"+clientID, grantID, principalID, clientID, userID, scopesJSON(scopes), "https://panel.example.com/mcp", time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano), ts); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(context.Background(), `insert into oauth_authorization_codes(code_hash,grant_id,client_id,user_id,principal_id,redirect_uri,scopes_json,resource,code_challenge,expires_at,created_at) values(?,?,?,?,?,?,?,?,?,?,?)`, "code_hash_"+clientID, grantID, clientID, userID, principalID, "https://client.example/callback", scopesJSON(scopes), "https://panel.example.com/mcp", "challenge", time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano), ts); err != nil {
		t.Fatal(err)
	}
	return grantID
}

func scopesJSON(scopes []string) string {
	encoded, _ := json.Marshal(scopes)
	return string(encoded)
}

func TestMigrateOAuthV2MarksLegacyGrantsNeedsReconsent(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Bootstrap users and clients so FK constraints hold.
	seedLegacyUser(t, db, 1)
	seedLegacyClient(t, db, "legacy_write", 1)
	seedLegacyClient(t, db, "legacy_read", 1)

	writeGrant := seedLegacyGrant(t, db, "legacy_write", "oauth_legacy_write", 1, []string{"inventory:read", "servers:onboard"})
	readGrant := seedLegacyGrant(t, db, "legacy_read", "oauth_legacy_read", 1, []string{"inventory:read", "servers:read"})

	// The auto-migration already ran on the fresh database; clear the marker so
	// the migration is exercised against the seeded legacy rows.
	if err := db.SetSetting(context.Background(), oauthV2MigrationSetting, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateOAuthV2(context.Background()); err != nil {
		t.Fatal(err)
	}

	writeState, err := db.GetOAuthGrant(context.Background(), writeGrant)
	if err != nil {
		t.Fatal(err)
	}
	if writeState.Status != model.OAuthGrantNeedsReconsent || writeState.AccessLevel != "operate" {
		t.Fatalf("legacy write grant = status %s access_level %s, want needs_reconsent/operate", writeState.Status, writeState.AccessLevel)
	}
	readState, err := db.GetOAuthGrant(context.Background(), readGrant)
	if err != nil {
		t.Fatal(err)
	}
	if readState.Status != model.OAuthGrantNeedsReconsent || readState.AccessLevel != "read" {
		t.Fatalf("legacy read grant = status %s access_level %s, want needs_reconsent/read", readState.Status, readState.AccessLevel)
	}

	// Tokens and codes must be revoked/invalidated.
	var revokedAt sql.NullString
	if err := db.db.QueryRowContext(context.Background(), `select revoked_at from oauth_access_tokens where grant_id=?`, writeGrant).Scan(&revokedAt); err != nil {
		t.Fatal(err)
	}
	if !revokedAt.Valid {
		t.Fatal("legacy access token was not revoked")
	}
	var codeCount int
	if err := db.db.QueryRowContext(context.Background(), `select count(*) from oauth_authorization_codes where grant_id=?`, writeGrant).Scan(&codeCount); err != nil {
		t.Fatal(err)
	}
	if codeCount != 0 {
		t.Fatalf("legacy authorization codes were not invalidated: %d", codeCount)
	}

	// The migration must be idempotent.
	if err := db.MigrateOAuthV2(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, err := db.GetOAuthGrant(context.Background(), writeGrant)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != model.OAuthGrantNeedsReconsent {
		t.Fatalf("idempotent re-run changed grant status to %s", after.Status)
	}
}

func seedLegacyUser(t *testing.T, db *Store, id int64) {
	t.Helper()
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.db.ExecContext(context.Background(), `insert into users(id,username,nickname,password_hash,session_version,role,status,proxy_uuid,proxy_password,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?)`, id, "legacy-user", "", "hash", 0, string(model.RoleAdmin), "active", "uuid", "pw", ts, ts); err != nil {
		t.Fatal(err)
	}
}

func seedLegacyClient(t *testing.T, db *Store, id string, userID int64) {
	t.Helper()
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.db.ExecContext(context.Background(), `insert into oauth_clients(id,name,redirect_uris_json,identity_type,client_metadata_json,enabled,created_at,updated_at) values(?,?,?,?,?,?,?,?)`, id, id, `["https://client.example/callback"]`, "preregistered", `{}`, 1, ts, ts); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(context.Background(), `insert into api_principals(id,owner_user_id,name,type,enabled,scopes_json,resource_filter_json,allowed_cidrs_json,rate_limit_per_minute,max_concurrency,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?)`, "oauth_"+id, userID, "legacy", string(model.APIPrincipalOAuth), 1, `[]`, `{}`, `[]`, 60, 4, ts, ts); err != nil {
		t.Fatal(err)
	}
}
