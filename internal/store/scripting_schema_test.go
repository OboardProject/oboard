package store

import (
	"context"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestScriptingTablesMigrateFromPreviousSchema(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/previous-scripts.sqlite"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "legacy-script", PasswordHash: "hash", Role: model.RoleAdmin, Status: "active", ProxyUUID: "legacy-script-uuid", ProxyPassword: "legacy-script-pass"}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"drop table if exists script_run_logs",
		"drop table if exists script_run_actions",
		"drop table if exists script_run_attempts",
		"drop table if exists script_runs",
		"drop table if exists script_state",
		"drop table if exists script_secrets",
		"drop table if exists script_grants",
		"drop table if exists script_trigger_states",
		"drop table if exists script_trigger_bindings",
		"drop table if exists script_revisions",
		"drop table if exists scripts",
		"drop table if exists server_script_policies",
		"delete from app_settings where key like 'scripts.%'",
	} {
		if _, err := s.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	settings, err := s.ListSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings[model.ScriptSettingEnabled] != "false" || settings[model.ScriptSettingHostActionsEnabled] != "false" {
		t.Fatalf("upgrade must leave scripts disabled: %#v", settings)
	}
	item := model.Script{Name: "after-upgrade", OwnerUserID: user.ID, Status: model.ScriptStatusDraft}
	if err := s.CreateScript(ctx, &item); err != nil || item.ID <= 0 {
		t.Fatalf("create script after migration: %#v err=%v", item, err)
	}
}
