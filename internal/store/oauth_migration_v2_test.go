package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/OboardProject/oboard/internal/model"
)

// legacyOAuthSchema creates the pre-v2 OAuth table layout (fine-grained
// scopes_json, resource_filter_json, consent_version, no v2 columns) so the
// upgrade path is exercised against real legacy rows.
func legacyOAuthSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	statements := []string{
		`create table users (id integer primary key autoincrement, username text not null unique, nickname text not null default '', password_hash text not null, session_version integer not null default 0, role text not null, status text not null, proxy_uuid text not null, proxy_password text not null, speed_limit_mbps integer not null default 0, traffic_limit_bytes integer not null default 0, traffic_used_bytes integer not null default 0, traffic_reset_mode text not null default 'monthly', traffic_reset_day integer not null default 1, subscription_token text unique, device_limit integer not null default 0, legacy_proxy_enabled integer not null default 1, created_at text not null, updated_at text not null)`,
		`create table api_principals (id text primary key, owner_user_id integer references users(id) on delete set null, name text not null, type text not null, enabled integer not null default 1, scopes_json text not null default '[]', resource_filter_json text not null default '{}', allowed_cidrs_json text not null default '[]', rate_limit_per_minute integer not null default 60, max_concurrency integer not null default 4, expires_at text, last_used_at text, created_at text not null, updated_at text not null)`,
		`create table oauth_clients (id text primary key, name text not null, redirect_uris_json text not null default '[]', allowed_scopes_json text not null default '[]', client_metadata_json text not null default '{}', enabled integer not null default 1, created_at text not null, updated_at text not null)`,
		`create table oauth_approval_profiles (id text primary key, name text not null, auto_approve_risk integer not null default 0, created_at text not null, updated_at text not null)`,
		`create table oauth_grants (id text primary key, client_id text not null references oauth_clients(id) on delete cascade, user_id integer not null references users(id) on delete cascade, principal_id text not null unique references api_principals(id) on delete cascade, scopes_json text not null, resource_filter_json text not null default '{}', approval_profile_id text not null references oauth_approval_profiles(id) on delete restrict, offline_access integer not null default 0, consent_version integer not null default 1, expires_at text, last_used_at text, revoked_at text, created_at text not null)`,
		`create table oauth_authorization_codes (code_hash text primary key, grant_id text not null references oauth_grants(id) on delete cascade, client_id text not null references oauth_clients(id) on delete cascade, user_id integer not null references users(id) on delete cascade, principal_id text not null references api_principals(id) on delete cascade, redirect_uri text not null, scopes_json text not null, resource text not null, code_challenge text not null, expires_at text not null, created_at text not null)`,
		`create table oauth_access_tokens (token_hash text primary key, grant_id text not null references oauth_grants(id) on delete cascade, principal_id text not null references api_principals(id) on delete cascade, client_id text not null references oauth_clients(id) on delete cascade, user_id integer not null references users(id) on delete cascade, scopes_json text not null, resource text not null, expires_at text not null, revoked_at text, created_at text not null)`,
		`create table oauth_refresh_tokens (token_hash text primary key, family_id text not null, grant_id text not null references oauth_grants(id) on delete cascade, parent_token_hash text not null default '', principal_id text not null references api_principals(id) on delete cascade, client_id text not null references oauth_clients(id) on delete cascade, user_id integer not null references users(id) on delete cascade, scopes_json text not null, resource text not null, expires_at text not null, consumed_at text, revoked_at text, reuse_detected_at text, created_at text not null)`,
		`create table app_settings (key text primary key, value text not null, updated_at text not null)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("legacy schema %q: %v", statement, err)
		}
	}
}

func seedLegacyUser(t *testing.T, db *sql.DB, id int64) {
	t.Helper()
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`insert into users(id,username,nickname,password_hash,session_version,role,status,proxy_uuid,proxy_password,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?)`, id, "legacy-user", "", "hash", 0, string(model.RoleAdmin), "active", "uuid", "pw", ts, ts); err != nil {
		t.Fatal(err)
	}
}

func seedLegacyClient(t *testing.T, db *sql.DB, id string, userID int64) {
	t.Helper()
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`insert into oauth_clients(id,name,redirect_uris_json,allowed_scopes_json,client_metadata_json,enabled,created_at,updated_at) values(?,?,?,?,?,?,?,?)`, id, id, `["https://client.example/callback"]`, `[]`, `{}`, 1, ts, ts); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into api_principals(id,owner_user_id,name,type,enabled,scopes_json,resource_filter_json,allowed_cidrs_json,rate_limit_per_minute,max_concurrency,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?)`, "oauth_"+id, userID, "legacy", string(model.APIPrincipalOAuth), 1, `[]`, `{}`, `[]`, 60, 4, ts, ts); err != nil {
		t.Fatal(err)
	}
}

func seedLegacyGrant(t *testing.T, db *sql.DB, clientID string, userID int64, scopes []string) string {
	t.Helper()
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	grantID := "grt_legacy_" + clientID
	if _, err := db.Exec(`insert into oauth_approval_profiles(id,name,auto_approve_risk,created_at,updated_at) values(?,?,?,?,?)`, "oap_legacy_"+clientID, "legacy", 0, ts, ts); err != nil {
		t.Fatal(err)
	}
	scopesJSON, _ := json.Marshal(scopes)
	if _, err := db.Exec(`insert into oauth_grants(id,client_id,user_id,principal_id,scopes_json,resource_filter_json,approval_profile_id,offline_access,consent_version,expires_at,last_used_at,revoked_at,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?)`, grantID, clientID, userID, "oauth_"+clientID, string(scopesJSON), `{}`, "oap_legacy_"+clientID, 0, 1, nil, nil, nil, ts); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into oauth_access_tokens(token_hash,grant_id,principal_id,client_id,user_id,scopes_json,resource,expires_at,revoked_at,created_at) values(?,?,?,?,?,?,?,?,?,?)`, "hash_legacy_"+clientID, grantID, "oauth_"+clientID, clientID, userID, string(scopesJSON), "https://panel.example.com/mcp", time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano), nil, ts); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into oauth_authorization_codes(code_hash,grant_id,client_id,user_id,principal_id,redirect_uri,scopes_json,resource,code_challenge,expires_at,created_at) values(?,?,?,?,?,?,?,?,?,?,?)`, "code_hash_"+clientID, grantID, clientID, userID, "oauth_"+clientID, "https://client.example/callback", string(scopesJSON), "https://panel.example.com/mcp", "challenge", time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano), ts); err != nil {
		t.Fatal(err)
	}
	return grantID
}

func TestMigrateOAuthV2MarksLegacyGrantsNeedsReconsent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "oboard.sqlite")

	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	legacyOAuthSchema(t, legacy)
	seedLegacyUser(t, legacy, 1)
	seedLegacyClient(t, legacy, "legacy_write", 1)
	seedLegacyClient(t, legacy, "legacy_read", 1)
	writeGrant := seedLegacyGrant(t, legacy, "legacy_write", 1, []string{"inventory:read", "servers:onboard"})
	readGrant := seedLegacyGrant(t, legacy, "legacy_read", 1, []string{"inventory:read", "servers:read"})
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	// Open runs the v2 migration and the legacy-column rebuild.
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

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

	// The legacy columns must be gone after the rebuild.
	for _, table := range []string{"oauth_grants", "oauth_access_tokens", "oauth_refresh_tokens", "oauth_authorization_codes"} {
		has, err := db.tableHasColumn(context.Background(), table, "scopes_json")
		if err != nil {
			t.Fatal(err)
		}
		if has {
			t.Fatalf("table %s still has scopes_json", table)
		}
	}
	hasResourceFilter, err := db.tableHasColumn(context.Background(), "oauth_grants", "resource_filter_json")
	if err != nil {
		t.Fatal(err)
	}
	if hasResourceFilter {
		t.Fatal("oauth_grants still has resource_filter_json")
	}

	// The migration must be idempotent.
	if err := db.MigrateOAuthV2(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := db.DropLegacyOAuthScopeColumns(context.Background()); err != nil {
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
