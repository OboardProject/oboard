package store

import (
	"context"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestPluginTablesMigrateFromPreviousSchema(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/previous-plugins.sqlite"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "legacy-plugin", PasswordHash: "hash", Role: model.RoleAdmin, Status: "active", ProxyUUID: "legacy-plugin-uuid", ProxyPassword: "legacy-plugin-pass"}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"drop table if exists plugin_webhook_deliveries",
		"drop table if exists plugin_webhooks",
		"drop table if exists plugin_installation_secrets",
		"drop table if exists plugin_package_versions",
		"drop table if exists plugin_installations",
		"drop table if exists plugin_run_logs",
		"drop table if exists plugin_run_actions",
		"drop table if exists plugin_run_attempts",
		"drop table if exists plugin_runs",
		"drop table if exists plugin_state",
		"drop table if exists plugin_secrets",
		"drop table if exists plugin_grants",
		"drop table if exists plugin_trigger_states",
		"drop table if exists plugin_trigger_bindings",
		"drop table if exists plugin_revisions",
		"drop table if exists plugins",
		"drop table if exists server_plugin_policies",
		"delete from app_settings where key like 'plugins.%'",
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
	if settings[model.PluginSettingEnabled] != "false" || settings[model.PluginSettingHostActionsEnabled] != "false" {
		t.Fatalf("upgrade must leave plugins disabled: %#v", settings)
	}
	item := model.Plugin{Name: "after-upgrade", OwnerUserID: user.ID, Status: model.PluginStatusDraft}
	if err := s.CreatePlugin(ctx, &item); err != nil || item.ID <= 0 {
		t.Fatalf("create plugin after migration: %#v err=%v", item, err)
	}
}
