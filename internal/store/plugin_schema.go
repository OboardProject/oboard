package store

import (
	"context"
	"fmt"
)

func (s *Store) migratePluginSchema(ctx context.Context) error {
	stmts := []string{
		`create table if not exists plugins (
			id integer primary key autoincrement,
			name text not null,
			description text not null default '',
			owner_user_id integer not null references users(id) on delete restrict,
			status text not null default 'draft' check(status in ('draft','enabled','disabled','archived')),
			created_at text not null,
			updated_at text not null
		)`,
		`create unique index if not exists idx_plugins_owner_name on plugins(owner_user_id, lower(name))`,
		`create table if not exists plugin_revisions (
			id integer primary key autoincrement,
			plugin_id integer not null references plugins(id) on delete cascade,
			revision_number integer not null,
			status text not null check(status in ('draft','published','superseded')),
			schema_version integer not null,
			runtime text not null,
			sdk_version text not null,
			source text not null,
			source_digest text not null,
			manifest_json text not null,
			author_user_id integer not null references users(id) on delete restrict,
			published_at text,
			published_by_user_id integer references users(id) on delete set null,
			created_at text not null,
			unique(plugin_id, revision_number)
		)`,
		`create unique index if not exists idx_plugin_revisions_one_draft on plugin_revisions(plugin_id) where status='draft'`,
		`create table if not exists plugin_trigger_bindings (
			id integer primary key autoincrement,
			plugin_id integer not null references plugins(id) on delete cascade,
			revision_id integer not null references plugin_revisions(id) on delete restrict,
			name text not null,
			enabled integer not null default 0,
			kind text not null check(kind in ('once','interval','cron','event')),
			spec_json text not null,
			params_json text not null default '{}',
			env_json text not null default '{}',
			binding_revision integer not null default 1,
			created_by_user_id integer not null references users(id) on delete restrict,
			created_at text not null,
			updated_at text not null
		)`,
		`create unique index if not exists idx_plugin_triggers_plugin_name on plugin_trigger_bindings(plugin_id, lower(name))`,
		`create table if not exists plugin_grants (
			id integer primary key autoincrement,
			plugin_id integer not null references plugins(id) on delete cascade,
			revision_id integer not null references plugin_revisions(id) on delete restrict,
			binding_id integer references plugin_trigger_bindings(id) on delete set null,
			grant_revision integer not null default 1,
			capabilities_json text not null,
			resource_scope_json text not null,
			constraints_json text not null default '{}',
			source_digest text not null,
			binding_digest text not null default '',
			expires_at text,
			revoked_at text,
			approved_by_user_id integer not null references users(id) on delete restrict,
			created_at text not null
		)`,
		`create index if not exists idx_plugin_grants_plugin on plugin_grants(plugin_id, revoked_at, expires_at)`,
		`create table if not exists plugin_trigger_states (
			binding_id integer primary key references plugin_trigger_bindings(id) on delete cascade,
			armed integer not null default 1,
			condition_json text not null default '{}',
			current_cycle_key text not null default '',
			last_fired_at text,
			last_skipped_at text,
			last_skip_reason text not null default '',
			next_due_at text,
			hold_until text,
			updated_at text not null
		)`,
		`create index if not exists idx_plugin_trigger_states_due on plugin_trigger_states(next_due_at) where next_due_at is not null`,
		`create table if not exists plugin_runs (
			id integer primary key autoincrement,
			uuid text not null unique,
			plugin_id integer not null references plugins(id) on delete restrict,
			revision_id integer not null references plugin_revisions(id) on delete restrict,
			binding_id integer references plugin_trigger_bindings(id) on delete set null,
			grant_id integer references plugin_grants(id) on delete set null,
			caller_principal text not null default '',
			trigger_kind text not null,
			idempotency_key text not null,
			status text not null,
			mode text not null,
			snapshot_json text not null,
			result_json text not null default '{}',
			error_code text not null default '',
			skip_reason text not null default '',
			lease_owner text not null default '',
			lease_generation integer not null default 0,
			lease_until text,
			recovery_generation integer not null default 1,
			created_at text not null,
			started_at text,
			finished_at text,
			unique(idempotency_key)
		)`,
		`create index if not exists idx_plugin_runs_queue on plugin_runs(status, created_at, id)`,
		`create index if not exists idx_plugin_runs_plugin on plugin_runs(plugin_id, created_at desc)`,
		`create table if not exists plugin_run_attempts (
			id integer primary key autoincrement,
			run_id integer not null references plugin_runs(id) on delete cascade,
			generation integer not null,
			worker_id text not null,
			status text not null,
			resource_json text not null default '{}',
			error_code text not null default '',
			started_at text not null,
			finished_at text,
			unique(run_id, generation)
		)`,
		`create table if not exists plugin_run_actions (
			id integer primary key autoincrement,
			run_id integer not null references plugin_runs(id) on delete cascade,
			action_key text not null,
			capability text not null,
			target_json text not null,
			payload_digest text not null,
			status text not null,
			operation_id text not null default '',
			changeset_id text not null default '',
			task_id integer,
			result_json text not null default '{}',
			error_code text not null default '',
			lease_generation integer not null default 0,
			created_at text not null,
			updated_at text not null,
			unique(run_id, action_key)
		)`,
		`create unique index if not exists idx_plugin_run_actions_operation on plugin_run_actions(operation_id) where operation_id<>''`,
		`create table if not exists plugin_state (
			plugin_id integer not null references plugins(id) on delete cascade,
			key text not null,
			value_json text not null,
			version integer not null default 1,
			updated_at text not null,
			primary key(plugin_id, key)
		)`,
		`create table if not exists plugin_run_logs (
			id integer primary key autoincrement,
			run_id integer not null references plugin_runs(id) on delete cascade,
			seq integer not null,
			level text not null,
			message text not null,
			fields_json text not null default '{}',
			created_at text not null,
			unique(run_id, seq)
		)`,
		`create index if not exists idx_plugin_run_logs_run on plugin_run_logs(run_id, seq)`,
		`create table if not exists plugin_secrets (
			id integer primary key autoincrement,
			name text not null unique,
			purpose text not null,
			resource_scope_json text not null default '{}',
			value_encrypted text not null,
			revoked_at text,
			created_by_user_id integer not null references users(id) on delete restrict,
			created_at text not null
		)`,
		`create table if not exists server_plugin_policies (
			server_id integer primary key references servers(id) on delete cascade,
			plugins_enabled integer not null default 0,
			plugins_power_enabled integer not null default 0,
			created_at text not null,
			updated_at text not null
		)`,
		`create index if not exists idx_event_outbox_pending on event_outbox(status, available_at, created_at)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("plugin schema: %w", err)
		}
	}
	defaults := map[string]string{
		"plugins.enabled":                "false",
		"plugins.host_actions_enabled":   "false",
		"plugins.scheduler_paused":       "false",
		"plugins.recovery_generation":    "1",
		"plugins.max_concurrency":        "2",
		"plugins.max_timeout_seconds":    "300",
		"plugins.log_retention_days":     "30",
		"plugins.summary_retention_days": "90",
	}
	ts := now()
	for key, value := range defaults {
		if _, err := s.db.ExecContext(ctx, `insert into app_settings(key,value,updated_at) values(?,?,?) on conflict(key) do nothing`, key, value, ts); err != nil {
			return err
		}
	}
	return nil
}
