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

func TestMigrateOAuthGrantDedupeIsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "oauth-dedupe.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()

	client := &model.OAuthClient{ID: "oc_dedupe", Name: "Hermes", RedirectURIs: []string{"http://127.0.0.1/callback"}, IdentityType: "preregistered", ClientMetadata: json.RawMessage(`{}`), Enabled: true}
	if err := db.CreateOAuthClient(ctx, client); err != nil {
		t.Fatal(err)
	}
	userID := int64(1)
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := raw.ExecContext(ctx, `insert into users(id,username,password_hash,role,status,proxy_uuid,proxy_password,subscription_token,created_at,updated_at) values(1,'noxsk','hash','admin','active','00000000-0000-4000-8000-000000000001','pw','sub',?,?)`, ts, ts); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `drop index if exists idx_oauth_grants_live_authorization`); err != nil {
		t.Fatal(err)
	}

	createDuplicate := func(id, principalID, profileID, lastUsed string) {
		t.Helper()
		if _, err := raw.ExecContext(ctx, `insert into api_principals(id,owner_user_id,name,type,enabled,scopes_json,resource_filter_json,allowed_cidrs_json,rate_limit_per_minute,max_concurrency,created_at,updated_at) values(?,1,?, 'oauth',1,'[]','{}','[]',120,4,?,?)`, principalID, id, ts, ts); err != nil {
			t.Fatal(err)
		}
		if _, err := raw.ExecContext(ctx, `insert into oauth_approval_profiles(id,name,auto_approve_risk,created_at,updated_at) values(?,? ,3,?,?)`, profileID, id, ts, ts); err != nil {
			t.Fatal(err)
		}
		lastUsedValue := sql.NullString{}
		if lastUsed != "" {
			lastUsedValue = sql.NullString{String: lastUsed, Valid: true}
		}
		if _, err := raw.ExecContext(ctx, `insert into oauth_grants(id,client_id,user_id,principal_id,access_level,resource_boundary_v2_json,approval_profile_id,offline_access,policy_version,role_version,consent_version,status,resource_key,last_used_at,created_at,last_authorized_at) values(?,?,?,?, 'operate','{}',?,1,1,1,2,'active','mcp',?,?,?)`, id, client.ID, userID, principalID, profileID, lastUsedValue, ts, ts); err != nil {
			t.Fatal(err)
		}
	}

	createDuplicate("grt_a", "oauth_a", "oap_a", "")
	createDuplicate("grt_b", "oauth_b", "oap_b", ts)
	createDuplicate("grt_c", "oauth_c", "oap_c", "")

	refreshExpires := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339Nano)
	if _, err := raw.ExecContext(ctx, `insert into oauth_refresh_tokens(token_hash,family_id,grant_id,principal_id,client_id,user_id,resource,expires_at,created_at) values('hash_a','fam_a','grt_a','oauth_a',?,1,'https://panel.example.com/api/v1/mcp',?,?)`, client.ID, refreshExpires, ts); err != nil {
		t.Fatal(err)
	}

	if _, err := raw.ExecContext(ctx, `delete from app_settings where key=?`, oauthGrantDedupeSetting); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateOAuthGrantDedupe(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureOAuthGrantLiveUniqueIndex(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateOAuthGrantDedupe(ctx); err != nil {
		t.Fatal(err)
	}

	var activeCount int
	if err := raw.QueryRowContext(ctx, `select count(*) from oauth_grants where client_id=? and user_id=? and resource_key='mcp' and revoked_at is null and status in ('active','needs_reconsent')`, client.ID, userID).Scan(&activeCount); err != nil {
		t.Fatal(err)
	}
	if activeCount != 1 {
		t.Fatalf("active grants=%d, want 1", activeCount)
	}

	var canonicalID string
	if err := raw.QueryRowContext(ctx, `select id from oauth_grants where client_id=? and user_id=? and revoked_at is null`, client.ID, userID).Scan(&canonicalID); err != nil {
		t.Fatal(err)
	}
	if canonicalID != "grt_b" {
		t.Fatalf("canonical grant=%s, want grt_b", canonicalID)
	}

	var refreshGrantID string
	if err := raw.QueryRowContext(ctx, `select grant_id from oauth_refresh_tokens where token_hash='hash_a'`).Scan(&refreshGrantID); err != nil {
		t.Fatal(err)
	}
	if refreshGrantID != canonicalID {
		t.Fatalf("refresh grant=%s, want %s", refreshGrantID, canonicalID)
	}
}
