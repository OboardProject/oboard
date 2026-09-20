package store

import (
	"context"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

func TestPluginRestoreDisablesExecutionAndInvalidatesAuthorization(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir() + "/plugins.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	u := &model.User{Username: "plugin-restore", PasswordHash: "hash", Role: model.RoleAdmin, Status: "active", ProxyUUID: "restore-uuid", ProxyPassword: "restore-password"}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	p := model.Plugin{Name: "Restore", OwnerUserID: u.ID, Status: model.PluginStatusEnabled}
	if err := s.CreatePlugin(ctx, &p); err != nil {
		t.Fatal(err)
	}
	ts := now()
	result, err := s.db.ExecContext(ctx, `insert into plugin_revisions(plugin_id,revision_number,status,schema_version,runtime,sdk_version,source,source_digest,manifest_json,author_user_id,created_at) values(?,1,'published',1,'oboard-js-v1','oboard-sdk-v1','function main(){}','digest','{}',?,?)`, p.ID, u.ID, ts)
	if err != nil {
		t.Fatal(err)
	}
	revision, _ := result.LastInsertId()
	_, err = s.db.ExecContext(ctx, `insert into plugin_grants(plugin_id,revision_id,capabilities_json,resource_scope_json,source_digest,approved_by_user_id,created_at) values(?,?,'[]','{}','digest',?,?)`, p.ID, revision, u.ID, ts)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"queued", "running"} {
		_, err = s.db.ExecContext(ctx, `insert into plugin_runs(uuid,plugin_id,revision_id,trigger_kind,idempotency_key,status,mode,snapshot_json,lease_generation,lease_owner,created_at) values(?,?,?,'manual',?,?,'live','{}',3,'worker',?)`, status, p.ID, revision, status, status, ts)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `insert into plugin_installations(plugin_id,package_id,active_revision_id,updated_at) values(?,'test.restore',?,?)`, p.ID, revision, ts); err != nil {
		t.Fatal(err)
	}
	encrypted, err := security.EncryptSecret("source-test-secret", "plugin-secret", "private-value")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `insert into plugin_installation_secrets(plugin_id,name,value_encrypted,updated_at) values(?,'token',?,?)`, p.ID, encrypted, ts); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := rewrapPluginSecrets(ctx, tx, "source-test-secret", "target-test-secret"); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var wrapped string
	if err := s.db.QueryRowContext(ctx, `select value_encrypted from plugin_installation_secrets where plugin_id=? and name='token'`, p.ID).Scan(&wrapped); err != nil {
		t.Fatal(err)
	}
	plain, err := security.DecryptSecret("target-test-secret", "plugin-secret", wrapped)
	if err != nil || plain != "private-value" {
		t.Fatal("plugin secret did not survive re-encryption")
	}
	if _, err := security.DecryptSecret("source-test-secret", "plugin-secret", wrapped); err == nil {
		t.Fatal("old key still decrypts restored secret")
	}
	if err := s.SetSettings(ctx, map[string]string{"plugins.enabled": "true", "plugins.recovery_generation": "7"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PausePluginSchedulerAfterRestore(ctx); err != nil {
		t.Fatal(err)
	}
	settings, err := s.ListSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings["plugins.enabled"] != "false" || settings["plugins.scheduler_paused"] != "true" || settings["plugins.recovery_generation"] != "8" {
		t.Fatalf("unsafe restore settings: %v", settings["plugins.recovery_generation"])
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `select count(*) from plugin_grants where revoked_at is null`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("live grants: %d %v", count, err)
	}
	if err := s.db.QueryRowContext(ctx, `select count(*) from plugin_runs where status='cancelled' and lease_generation=4 and lease_owner=''`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("stale runs survived: %d %v", count, err)
	}
}
