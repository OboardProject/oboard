package store

import (
	"context"
	"fmt"
)

func (s *Store) migrateScriptingSchema(ctx context.Context) error {
	stmts := []string{
		`create table if not exists scripts (
			id integer primary key autoincrement,
			name text not null,
			description text not null default '',
			owner_user_id integer not null references users(id) on delete restrict,
			status text not null default 'draft' check(status in ('draft','enabled','disabled','archived')),
			created_at text not null,
			updated_at text not null
		)`,
		`create unique index if not exists idx_scripts_owner_name on scripts(owner_user_id, lower(name))`,
		`create table if not exists script_revisions (
			id integer primary key autoincrement,
			script_id integer not null references scripts(id) on delete cascade,
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
			unique(script_id, revision_number)
		)`,
		`create unique index if not exists idx_script_revisions_one_draft on script_revisions(script_id) where status='draft'`,
		`create table if not exists script_trigger_bindings (
			id integer primary key autoincrement,
			script_id integer not null references scripts(id) on delete cascade,
			revision_id integer not null references script_revisions(id) on delete restrict,
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
		`create unique index if not exists idx_script_triggers_script_name on script_trigger_bindings(script_id, lower(name))`,
		`create table if not exists script_grants (
			id integer primary key autoincrement,
			script_id integer not null references scripts(id) on delete cascade,
			revision_id integer not null references script_revisions(id) on delete restrict,
			binding_id integer references script_trigger_bindings(id) on delete set null,
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
		`create index if not exists idx_script_grants_script on script_grants(script_id, revoked_at, expires_at)`,
		`create table if not exists script_trigger_states (
			binding_id integer primary key references script_trigger_bindings(id) on delete cascade,
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
		`create index if not exists idx_script_trigger_states_due on script_trigger_states(next_due_at) where next_due_at is not null`,
		`create table if not exists script_runs (
			id integer primary key autoincrement,
			uuid text not null unique,
			script_id integer not null references scripts(id) on delete restrict,
			revision_id integer not null references script_revisions(id) on delete restrict,
			binding_id integer references script_trigger_bindings(id) on delete set null,
			grant_id integer references script_grants(id) on delete set null,
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
		`create index if not exists idx_script_runs_queue on script_runs(status, created_at, id)`,
		`create index if not exists idx_script_runs_script on script_runs(script_id, created_at desc)`,
		`create table if not exists script_run_attempts (
			id integer primary key autoincrement,
			run_id integer not null references script_runs(id) on delete cascade,
			generation integer not null,
			worker_id text not null,
			status text not null,
			resource_json text not null default '{}',
			error_code text not null default '',
			started_at text not null,
			finished_at text,
			unique(run_id, generation)
		)`,
		`create table if not exists script_run_actions (
			id integer primary key autoincrement,
			run_id integer not null references script_runs(id) on delete cascade,
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
		`create unique index if not exists idx_script_run_actions_operation on script_run_actions(operation_id) where operation_id<>''`,
		`create table if not exists script_state (
			script_id integer not null references scripts(id) on delete cascade,
			key text not null,
			value_json text not null,
			version integer not null default 1,
			updated_at text not null,
			primary key(script_id, key)
		)`,
		`create table if not exists script_run_logs (
			id integer primary key autoincrement,
			run_id integer not null references script_runs(id) on delete cascade,
			seq integer not null,
			level text not null,
			message text not null,
			fields_json text not null default '{}',
			created_at text not null,
			unique(run_id, seq)
		)`,
		`create index if not exists idx_script_run_logs_run on script_run_logs(run_id, seq)`,
		`create table if not exists script_secrets (
			id integer primary key autoincrement,
			name text not null unique,
			purpose text not null,
			resource_scope_json text not null default '{}',
			value_encrypted text not null,
			revoked_at text,
			created_by_user_id integer not null references users(id) on delete restrict,
			created_at text not null
		)`,
		`create table if not exists server_script_policies (
			server_id integer primary key references servers(id) on delete cascade,
			scripts_enabled integer not null default 0,
			scripts_power_enabled integer not null default 0,
			created_at text not null,
			updated_at text not null
		)`,
		`create index if not exists idx_event_outbox_pending on event_outbox(status, available_at, created_at)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("scripting schema: %w", err)
		}
	}
	defaults := map[string]string{
		"scripts.enabled":                "false",
		"scripts.host_actions_enabled":   "false",
		"scripts.scheduler_paused":       "false",
		"scripts.recovery_generation":    "1",
		"scripts.max_concurrency":        "2",
		"scripts.max_timeout_seconds":    "300",
		"scripts.log_retention_days":     "30",
		"scripts.summary_retention_days": "90",
	}
	ts := now()
	for key, value := range defaults {
		if _, err := s.db.ExecContext(ctx, `insert into app_settings(key,value,updated_at) values(?,?,?) on conflict(key) do nothing`, key, value, ts); err != nil {
			return err
		}
	}
	return nil
}
