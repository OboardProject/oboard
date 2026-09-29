package store

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

var currentPluginTablePattern = regexp.MustCompile(`create table if not exists (\w+)`)

// TestRetiredPluginRuntimeUpgradesToCapabilityModel starts from the plugin
// tables, settings and rows written by 03663a5 and verifies the upgrade drops
// every retired record, disables plugins, and is idempotent.
func TestRetiredPluginRuntimeUpgradesToCapabilityModel(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "oboard.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/plugin_runtime_03663a5.sql")
	if err != nil {
		t.Fatal(err)
	}
	seed := []string{
		`insert into users(id,username,password_hash,role,status,proxy_uuid,proxy_password,created_at,updated_at) values(1,'admin','x','admin','active','uuid-1','pw-1','2026-09-01T00:00:00Z','2026-09-01T00:00:00Z')`,
		`delete from app_settings where key like 'plugins.%'`,
	}
	for _, schema := range pluginSchemaStatements {
		if match := currentPluginTablePattern.FindStringSubmatch(schema); match != nil {
			seed = append(seed, `drop table if exists `+match[1])
		}
	}
	if _, err := s.db.ExecContext(ctx, `pragma foreign_keys=off`); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range seed {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if _, err := s.db.ExecContext(ctx, string(fixture)); err != nil {
		t.Fatalf("apply previous plugin state: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `pragma foreign_keys=on`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	for pass := 0; pass < 2; pass++ {
		s, err = Open(path)
		if err != nil {
			t.Fatalf("pass %d: upgrade from previous plugin state: %v", pass, err)
		}
		for _, table := range retiredPluginTables {
			if table == "plugin_installations" || table == "plugin_grants" || table == "plugin_secrets" || table == "plugin_state" || table == "plugin_runs" || table == "plugin_run_logs" {
				continue
			}
			var n int
			if err := s.db.QueryRowContext(ctx, `select count(*) from sqlite_master where type='table' and name=?`, table).Scan(&n); err != nil || n != 0 {
				t.Fatalf("pass %d: retired table %s still exists (%d, %v)", pass, table, n, err)
			}
		}
		var triggers int
		if err := s.db.QueryRowContext(ctx, `select count(*) from sqlite_master where type='trigger' and name like 'plugin_webhook_%'`).Scan(&triggers); err != nil || triggers != 0 {
			t.Fatalf("pass %d: retired webhook triggers remain (%d, %v)", pass, triggers, err)
		}
		for _, table := range []string{"plugin_installations", "plugin_packages", "plugin_instances", "plugin_grants", "plugin_secrets", "plugin_state", "plugin_runs", "plugin_run_logs"} {
			var n int
			if err := s.db.QueryRowContext(ctx, `select count(*) from `+table).Scan(&n); err != nil || n != 0 {
				t.Fatalf("pass %d: %s must start empty after the upgrade (%d, %v)", pass, table, n, err)
			}
		}
		var columns int
		if err := s.db.QueryRowContext(ctx, `select count(*) from pragma_table_info('plugin_grants') where name in ('revision_id','capabilities_json','grant_json')`).Scan(&columns); err != nil || columns != 1 {
			t.Fatalf("pass %d: plugin_grants kept the retired shape (%d, %v)", pass, columns, err)
		}
		settings := map[string]string{}
		rows, err := s.db.QueryContext(ctx, `select key,value from app_settings where key like 'plugins.%'`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var key, value string
			if err := rows.Scan(&key, &value); err != nil {
				t.Fatal(err)
			}
			settings[key] = value
		}
		rows.Close()
		want := map[string]string{
			model.PluginSettingEnabled:           "false",
			model.PluginSettingModel:             model.PluginModelCurrent,
			model.PluginSettingMaxTimeoutSeconds: pluginSettingDefaults[model.PluginSettingMaxTimeoutSeconds],
			model.PluginSettingLogRetentionDays:  pluginSettingDefaults[model.PluginSettingLogRetentionDays],
		}
		for key, value := range want {
			if settings[key] != value {
				t.Fatalf("pass %d: %s = %q, want %q", pass, key, settings[key], value)
			}
		}
		for _, retired := range []string{"plugins.host_actions_enabled", "plugins.summary_retention_days"} {
			if _, ok := settings[retired]; ok {
				t.Fatalf("pass %d: retired setting %s survived", pass, retired)
			}
		}
		var events []string
		rows, err = s.db.QueryContext(ctx, `select id from event_outbox where id in ('evt-retired','evt-offline') order by id`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id string
			_ = rows.Scan(&id)
			events = append(events, id)
		}
		rows.Close()
		if len(events) != 1 || events[0] != "evt-offline" {
			t.Fatalf("pass %d: only the server status event may survive, got %v", pass, events)
		}
		var status string
		if err := s.db.QueryRowContext(ctx, `select status from automation_changesets where id='cs-plugin'`).Scan(&status); err != nil || status != "expired" {
			t.Fatalf("pass %d: retired plugin changeset status %q (%v)", pass, status, err)
		}
		if pass == 0 {
			// A later administrator decision must survive the next restart.
			if _, err := s.db.ExecContext(ctx, `update app_settings set value='true' where key=?`, model.PluginSettingEnabled); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if pass == 0 {
			s, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			var enabled string
			if err := s.db.QueryRowContext(ctx, `select value from app_settings where key=?`, model.PluginSettingEnabled).Scan(&enabled); err != nil || enabled != "true" {
				t.Fatalf("reopen must not rerun the retired-data cleanup: enabled=%q (%v)", enabled, err)
			}
			if _, err := s.db.ExecContext(ctx, `update app_settings set value='false' where key=?`, model.PluginSettingEnabled); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
}
