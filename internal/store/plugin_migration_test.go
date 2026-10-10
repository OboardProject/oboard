package store

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

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

func TestClaimPluginEventsRequeuesExpiredLease(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.db.ExecContext(ctx, `insert into event_outbox(id,topic,aggregate_id,payload_json,status,attempts,available_at,lease_owner,lease_until,created_at) values('expired-plugin','plugin.server.offline','server:1','{}','leased',2,?,?,?,?)`, now(), "old-worker", time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), now()); err != nil {
		t.Fatal(err)
	}
	items, err := db.ClaimPluginEvents(ctx, "new-worker", time.Now().UTC().Add(time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "expired-plugin" || items[0].Attempts != 2 {
		t.Fatalf("expired lease was not reclaimed: %#v", items)
	}
}

func TestRetiredScriptOutboxMigrationRemovesUndeliverableWork(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "oboard.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ id, status string }{{"script-pending", "pending"}, {"script-leased", "leased"}, {"script-complete", "completed"}} {
		if _, err := db.db.ExecContext(ctx, `insert into event_outbox(id,topic,aggregate_id,payload_json,status,available_at,created_at) values(?,?,?,'{}',?,?,?)`, row.id, "script.server.offline", row.id, row.status, now(), now()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.db.ExecContext(ctx, `delete from app_settings where key=?`, retiredScriptOutboxMigrationKey); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var pending, leased, completed int
	if err := db.db.QueryRowContext(ctx, `select count(*) from event_outbox where id='script-pending'`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRowContext(ctx, `select count(*) from event_outbox where id='script-leased'`).Scan(&leased); err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRowContext(ctx, `select count(*) from event_outbox where id='script-complete'`).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if pending != 0 || leased != 0 || completed != 1 {
		t.Fatalf("script outbox migration left pending=%d leased=%d completed=%d", pending, leased, completed)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.db.QueryRowContext(ctx, `select count(*) from event_outbox where topic like 'script.%'`).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if completed != 1 {
		t.Fatalf("script outbox migration is not idempotent, remaining=%d", completed)
	}
}

func TestPluginPageSchemaUpgradesPreviousRuns(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "oboard.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `pragma foreign_keys=off`); err != nil {
		t.Fatal(err)
	}
	oldRuns := strings.Replace(pluginRunsTableSQL, "create table if not exists plugin_runs", "create table plugin_runs", 1)
	oldRuns = strings.Replace(oldRuns, `,'ui','action'`, "", 1)
	for _, stmt := range []string{
		`drop table plugin_runs`,
		oldRuns,
		`insert into plugin_runs(uuid,installation_id,instance_id,package_id,plugin_key,plugin_version,trigger,idempotency_key,status,queued_at,recovery_generation) values('kept-run',1,1,1,'acme.demo','1.0.0','manual','key-1','queued','2026-10-01T00:00:00Z',1)`,
		`drop table plugin_page_snapshots`,
		`pragma foreign_keys=on`,
	} {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		s, err = Open(path)
		if err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
		var ddl, uuid string
		if err := s.db.QueryRowContext(ctx, `select sql from sqlite_master where type='table' and name='plugin_runs'`).Scan(&ddl); err != nil || !strings.Contains(ddl, "'ui'") {
			t.Fatalf("pass %d: trigger check was not widened: %s %v", pass, ddl, err)
		}
		if err := s.db.QueryRowContext(ctx, `select uuid from plugin_runs where trigger='manual'`).Scan(&uuid); err != nil || uuid != "kept-run" {
			t.Fatalf("pass %d: previous run was not preserved: %s %v", pass, uuid, err)
		}
		var snapshots int
		if err := s.db.QueryRowContext(ctx, `select count(*) from sqlite_master where type='table' and name='plugin_page_snapshots'`).Scan(&snapshots); err != nil || snapshots != 1 {
			t.Fatalf("pass %d: snapshot table missing (%d, %v)", pass, snapshots, err)
		}
		if pass == 0 {
			if _, err := s.db.ExecContext(ctx, `insert into plugin_runs(uuid,installation_id,instance_id,package_id,plugin_key,plugin_version,trigger,idempotency_key,status,queued_at,recovery_generation) values('ui-run',1,1,1,'acme.demo','1.0.0','ui','key-2','queued','2026-10-01T00:00:01Z',1)`); err != nil {
				t.Fatalf("ui trigger insert: %v", err)
			}
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
