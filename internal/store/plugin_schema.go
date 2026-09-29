package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

// retiredPluginTables belong to the retired plugin runtime (revisions,
// bindings, webhooks and management grants). They are dropped, never read.
// Order respects foreign keys.
var retiredPluginTables = []string{
	"plugin_legacy_records",
	"plugin_webhook_deliveries",
	"plugin_webhooks",
	"plugin_installation_secrets",
	"plugin_package_versions",
	"plugin_installations",
	"plugin_run_logs",
	"plugin_run_actions",
	"plugin_run_attempts",
	"plugin_runs",
	"plugin_trigger_states",
	"plugin_grants",
	"plugin_trigger_bindings",
	"plugin_state",
	"plugin_secrets",
	"server_plugin_policies",
	"plugin_revisions",
	"plugins",
}

var pluginSchemaStatements = []string{
	`create table if not exists plugin_installations (
		id integer primary key autoincrement,
		plugin_key text not null unique,
		name text not null,
		description text not null default '',
		active_package_id integer not null default 0,
		publisher_identity text not null,
		enabled integer not null default 0 check(enabled in (0,1)),
		draft_manifest_json text not null default '',
		draft_source text not null default '',
		draft_updated_at text,
		created_at text not null,
		updated_at text not null
	)`,
	`create table if not exists plugin_packages (
		id integer primary key autoincrement,
		installation_id integer not null references plugin_installations(id) on delete cascade,
		plugin_key text not null,
		version text not null,
		sha256 text not null,
		manifest_json text not null,
		source text not null,
		icon_png blob,
		readme text not null default '',
		license text not null default '',
		publisher_identity text not null,
		publisher_name text not null default '',
		signature_state text not null check(signature_state in ('verified','unsigned')),
		source_kind text not null check(source_kind in ('upload','github','editor')),
		source_repository text not null default '',
		source_commit text not null default '',
		created_by_user_id integer not null default 0,
		created_at text not null,
		unique(installation_id, version),
		unique(installation_id, sha256)
	)`,
	`create table if not exists plugin_instances (
		id integer primary key autoincrement,
		installation_id integer not null references plugin_installations(id) on delete cascade,
		name text not null,
		enabled integer not null default 0 check(enabled in (0,1)),
		auto_paused integer not null default 0 check(auto_paused in (0,1)),
		permission_review_required integer not null default 0 check(permission_review_required in (0,1)),
		values_json text not null default '{}',
		custom_json text not null default '[]',
		revision integer not null default 1,
		failure_streak integer not null default 0,
		last_run_at text,
		last_success_at text,
		last_error_code text not null default '',
		created_at text not null,
		updated_at text not null
	)`,
	`create unique index if not exists idx_plugin_instances_name on plugin_instances(installation_id, lower(name))`,
	`create table if not exists plugin_grants (
		instance_id integer primary key references plugin_instances(id) on delete cascade,
		package_id integer not null references plugin_packages(id) on delete cascade,
		grant_json text not null,
		revision integer not null default 1,
		approved_by_user_id integer not null default 0,
		approved_at text not null
	)`,
	`create table if not exists plugin_secrets (
		instance_id integer not null references plugin_instances(id) on delete cascade,
		name text not null,
		value_encrypted text not null,
		updated_at text not null,
		primary key(instance_id, name)
	)`,
	`create table if not exists plugin_state (
		instance_id integer not null references plugin_instances(id) on delete cascade,
		key text not null,
		value_json text not null,
		version integer not null default 1,
		updated_at text not null,
		primary key(instance_id, key)
	)`,
	`create table if not exists plugin_schedules (
		id integer primary key autoincrement,
		instance_id integer not null references plugin_instances(id) on delete cascade,
		kind text not null check(kind in ('interval','cron','event')),
		interval text not null default '',
		cron text not null default '',
		timezone text not null default '',
		event text not null default '',
		enabled integer not null default 0 check(enabled in (0,1)),
		next_due_at text,
		last_fired_at text,
		last_skipped_at text,
		last_skip_reason text not null default '',
		created_at text not null,
		updated_at text not null
	)`,
	`create index if not exists idx_plugin_schedules_due on plugin_schedules(enabled, next_due_at)`,
	`create index if not exists idx_plugin_schedules_instance on plugin_schedules(instance_id)`,
	`create table if not exists plugin_runs (
		id integer primary key autoincrement,
		uuid text not null unique,
		installation_id integer not null,
		instance_id integer not null,
		package_id integer not null,
		plugin_key text not null,
		plugin_version text not null,
		trigger text not null check(trigger in ('manual','interval','cron','event')),
		trigger_json text not null default '{}',
		caller_principal text not null default '',
		idempotency_key text not null unique,
		status text not null check(status in ('queued','running','succeeded','failed','timeout','cancelled','permission_denied','resource_limit')),
		error_code text not null default '',
		error_message text not null default '',
		cancel_requested integer not null default 0,
		queued_at text not null,
		started_at text,
		finished_at text,
		capability_call_count integer not null default 0,
		http_call_count integer not null default 0,
		agent_operation_count integer not null default 0,
		result_json text not null default '',
		lease_owner text not null default '',
		lease_generation integer not null default 0,
		lease_until text,
		recovery_generation integer not null default 1
	)`,
	`create index if not exists idx_plugin_runs_queue on plugin_runs(status, queued_at, id)`,
	`create index if not exists idx_plugin_runs_instance on plugin_runs(instance_id, queued_at desc)`,
	`create index if not exists idx_plugin_runs_installation on plugin_runs(installation_id, queued_at desc)`,
	`create table if not exists plugin_run_logs (
		run_id integer not null references plugin_runs(id) on delete cascade,
		seq integer not null,
		level text not null,
		message text not null,
		created_at text not null,
		primary key(run_id, seq)
	)`,
	`create table if not exists plugin_audit_events (
		id integer primary key autoincrement,
		created_at text not null,
		installation_id integer not null,
		instance_id integer not null,
		plugin_key text not null,
		plugin_version text not null,
		run_uuid text not null default '',
		capability text not null,
		resource text not null default '',
		result text not null,
		error_code text not null default '',
		duration_ms integer not null default 0,
		detail_json text not null default '{}'
	)`,
	`create index if not exists idx_plugin_audit_instance on plugin_audit_events(instance_id, created_at desc)`,
	`create index if not exists idx_plugin_audit_created on plugin_audit_events(created_at)`,
	`create index if not exists idx_event_outbox_pending on event_outbox(status, available_at, created_at)`,
}

var pluginSettingDefaults = map[string]string{
	model.PluginSettingEnabled:            "false",
	model.PluginSettingSchedulerPaused:    "false",
	model.PluginSettingRecoveryGeneration: "1",
	model.PluginSettingMaxConcurrency:     "2",
	model.PluginSettingMaxTimeoutSeconds:  "60",
	model.PluginSettingLogRetentionDays:   "14",
	model.PluginSettingRunRetentionDays:   "30",
}

// migratePluginSchema moves any database into the capability-based plugin
// model. Data of the retired plugin runtime (code, grants, secrets, triggers,
// state, runs and webhooks) is dropped; it can never execute again.
func (s *Store) migratePluginSchema(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var marker string
	err = tx.QueryRowContext(ctx, `select value from app_settings where key=?`, model.PluginSettingModel).Scan(&marker)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("plugin schema: %w", err)
	}
	if marker != model.PluginModelCurrent {
		if err := dropRetiredPluginRuntime(ctx, tx); err != nil {
			return err
		}
	}
	for _, stmt := range pluginSchemaStatements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("plugin schema: %w", err)
		}
	}
	ts := now()
	for key, value := range pluginSettingDefaults {
		if _, err := tx.ExecContext(ctx, `insert into app_settings(key,value,updated_at) values(?,?,?) on conflict(key) do nothing`, key, value, ts); err != nil {
			return err
		}
	}
	if marker != model.PluginModelCurrent {
		if _, err := tx.ExecContext(ctx, `insert into app_settings(key,value,updated_at) values(?,?,?) on conflict(key) do update set value=excluded.value,updated_at=excluded.updated_at`, model.PluginSettingModel, model.PluginModelCurrent, ts); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func dropRetiredPluginRuntime(ctx context.Context, tx *sql.Tx) error {
	for _, table := range retiredPluginTables {
		if _, err := tx.ExecContext(ctx, `drop table if exists `+table); err != nil {
			return fmt.Errorf("plugin schema: drop %s: %w", table, err)
		}
	}
	// Retired runtime policy (timeouts, retention, host actions) never carries
	// over; the current defaults are inserted after the new tables exist.
	if _, err := tx.ExecContext(ctx, `delete from app_settings where key like 'plugins.%'`); err != nil {
		return err
	}
	ts := now()
	// Old grants never carry over: plugin execution starts disabled and every
	// newly installed plugin needs a fresh administrator grant.
	if _, err := tx.ExecContext(ctx, `insert into app_settings(key,value,updated_at) values(?, 'false', ?) on conflict(key) do update set value='false',updated_at=excluded.updated_at`, model.PluginSettingEnabled, ts); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from event_outbox where topic like 'plugin.%' and topic not in ('plugin.server.offline','plugin.server.recovered')`); err != nil && !strings.Contains(err.Error(), "no such table") {
		return err
	}
	// Management changesets proposed by retired plugins can never apply.
	if _, err := tx.ExecContext(ctx, `update automation_changesets set status='expired',updated_at=? where principal_id like 'plugin:%' and status in ('draft','validated','awaiting_approval','approved')`, ts); err != nil && !strings.Contains(err.Error(), "no such table") {
		return err
	}
	return nil
}
