-- Plugin schema, settings and representative rows of the retired plugin runtime at 03663a5.
-- Requires users(id=1) to exist. Generated from a database opened by that commit's store.
CREATE TABLE plugins (
			id integer primary key autoincrement,
			name text not null,
			description text not null default '',
			owner_user_id integer not null references users(id) on delete restrict,
			status text not null default 'draft' check(status in ('draft','enabled','disabled','archived')),
			created_at text not null,
			updated_at text not null
		);
CREATE TABLE plugin_revisions (
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
		);
CREATE TABLE plugin_trigger_bindings (
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
		);
CREATE TABLE plugin_grants (
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
		);
CREATE TABLE plugin_trigger_states (
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
		);
CREATE TABLE plugin_runs (
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
		);
CREATE TABLE plugin_run_attempts (
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
		);
CREATE TABLE plugin_run_actions (
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
		);
CREATE TABLE plugin_state (
			plugin_id integer not null references plugins(id) on delete cascade,
			key text not null,
			value_json text not null,
			version integer not null default 1,
			updated_at text not null,
			primary key(plugin_id, key)
		);
CREATE TABLE plugin_run_logs (
			id integer primary key autoincrement,
			run_id integer not null references plugin_runs(id) on delete cascade,
			seq integer not null,
			level text not null,
			message text not null,
			fields_json text not null default '{}',
			created_at text not null,
			unique(run_id, seq)
		);
CREATE TABLE plugin_secrets (
			id integer primary key autoincrement,
			name text not null unique,
			purpose text not null,
			resource_scope_json text not null default '{}',
			value_encrypted text not null,
			revoked_at text,
			created_by_user_id integer not null references users(id) on delete restrict,
			created_at text not null
		);
CREATE TABLE server_plugin_policies (
			server_id integer primary key references servers(id) on delete cascade,
			plugins_enabled integer not null default 0,
			plugins_power_enabled integer not null default 0,
			created_at text not null,
			updated_at text not null
		);
CREATE TABLE plugin_installations (
 plugin_id integer primary key references plugins(id) on delete restrict,
 package_id text not null unique,
 active_revision_id integer references plugin_revisions(id) on delete restrict,
 installed integer not null default 1 check(installed in (0,1)),
 config_json text not null default '{}', updated_at text not null
 );
CREATE TABLE plugin_package_versions (
 revision_id integer primary key references plugin_revisions(id) on delete restrict,
 plugin_id integer not null references plugin_installations(plugin_id) on delete restrict,
 version text not null, sha256 text not null, manifest_json text not null,
 ui_json text not null default 'null', source_kind text not null,
 source_repository text not null default '', source_commit text not null default '', created_at text not null,
 unique(plugin_id,version), unique(plugin_id,sha256)
 );
CREATE TABLE plugin_installation_secrets (
 plugin_id integer not null references plugin_installations(plugin_id) on delete restrict,
 name text not null, value_encrypted text not null, updated_at text not null,
 primary key(plugin_id,name)
 );
CREATE TABLE plugin_webhooks (
 id text primary key,
 plugin_id integer not null references plugins(id) on delete cascade,
 binding_id integer not null unique references plugin_trigger_bindings(id) on delete cascade,
 binding_revision integer not null,
 revision_id integer not null references plugin_revisions(id) on delete cascade,
 grant_id integer not null references plugin_grants(id) on delete cascade,
 enabled integer not null default 0,
 generation integer not null default 1,
 secret_encrypted text not null,
 created_by_user_id integer not null references users(id) on delete restrict,
 rate_window integer not null default 0,
 rate_count integer not null default 0,
 created_at text not null,
 updated_at text not null
 );
CREATE TABLE plugin_webhook_deliveries (
 webhook_id text not null references plugin_webhooks(id) on delete cascade,
 nonce text not null,
 generation integer not null,
 received_at integer not null,
 expires_at integer not null,
 status text not null default 'received',
 run_id integer references plugin_runs(id) on delete set null,
 primary key(webhook_id, nonce)
 );
CREATE UNIQUE INDEX idx_plugins_owner_name on plugins(owner_user_id, lower(name));
CREATE UNIQUE INDEX idx_plugin_revisions_one_draft on plugin_revisions(plugin_id) where status='draft';
CREATE UNIQUE INDEX idx_plugin_triggers_plugin_name on plugin_trigger_bindings(plugin_id, lower(name));
CREATE INDEX idx_plugin_grants_plugin on plugin_grants(plugin_id, revoked_at, expires_at);
CREATE INDEX idx_plugin_trigger_states_due on plugin_trigger_states(next_due_at) where next_due_at is not null;
CREATE INDEX idx_plugin_runs_queue on plugin_runs(status, created_at, id);
CREATE INDEX idx_plugin_runs_plugin on plugin_runs(plugin_id, created_at desc);
CREATE UNIQUE INDEX idx_plugin_run_actions_operation on plugin_run_actions(operation_id) where operation_id<>'';
CREATE INDEX idx_plugin_run_logs_run on plugin_run_logs(run_id, seq);
CREATE INDEX idx_plugin_webhooks_plugin on plugin_webhooks(plugin_id);
CREATE INDEX idx_plugin_webhook_deliveries_expiry on plugin_webhook_deliveries(expires_at);
CREATE TRIGGER plugin_webhook_run_guard before insert on plugin_runs
 when new.trigger_kind='plugin.webhook'
 begin
 select case when not exists (
 select 1 from plugin_webhooks w
 join plugin_trigger_bindings b on b.id=w.binding_id
 join plugin_grants g on g.id=w.grant_id
 join plugin_webhook_deliveries d on d.webhook_id=w.id
 where w.id=json_extract(new.snapshot_json,'$.webhook_id')
 and w.generation=json_extract(new.snapshot_json,'$.webhook_generation')
 and d.nonce=json_extract(new.snapshot_json,'$.webhook_nonce') and d.generation=w.generation and d.status='received'
 and w.enabled=1 and b.enabled=1 and b.binding_revision=w.binding_revision
 and b.revision_id=w.revision_id and b.plugin_id=w.plugin_id
 and new.plugin_id=w.plugin_id and new.revision_id=w.revision_id
 and new.binding_id=w.binding_id and new.grant_id=w.grant_id
 and g.binding_id=w.binding_id and g.revision_id=w.revision_id and g.plugin_id=w.plugin_id
 and g.revoked_at is null and (g.expires_at is null or g.expires_at>strftime('%Y-%m-%dT%H:%M:%SZ','now'))
 ) then raise(abort,'plugin webhook authorization changed') end;
 end;
CREATE TRIGGER plugin_webhook_run_cancel after update of generation on plugin_webhooks
 begin
 update plugin_runs set status='cancelled',error_code='cancelled',finished_at=new.updated_at,
 lease_generation=lease_generation+1,lease_owner='',lease_until=null
 where trigger_kind='plugin.webhook' and binding_id=new.binding_id and status in ('queued','running');
 end;
insert into plugins(id,name,description,owner_user_id,status,created_at,updated_at) values(1,'restart-watchdog','',1,'enabled','2026-09-01T00:00:00Z','2026-09-01T00:00:00Z');
insert into plugin_revisions(id,plugin_id,revision_number,status,schema_version,runtime,sdk_version,source,source_digest,manifest_json,author_user_id,published_at,published_by_user_id,created_at) values(1,1,1,'published',1,'oboard-js-v1','oboard-sdk-v1','function main(){ return oboard.services.restart({server_id:1}); }','digest-1','{"plugin_id":"acme.watchdog","capabilities":["services.restart","host.reboot"]}',1,'2026-09-01T00:00:00Z',1,'2026-09-01T00:00:00Z');
insert into plugin_trigger_bindings(id,plugin_id,revision_id,name,enabled,kind,spec_json,params_json,env_json,binding_revision,created_by_user_id,created_at,updated_at) values(1,1,1,'every-minute',1,'interval','{"seconds":60}','{}','{"TOKEN":"plain"}',1,1,'2026-09-01T00:00:00Z','2026-09-01T00:00:00Z');
insert into plugin_grants(id,plugin_id,revision_id,binding_id,grant_revision,capabilities_json,resource_scope_json,constraints_json,source_digest,binding_digest,approved_by_user_id,created_at) values(1,1,1,1,1,'["services.restart","host.reboot"]','{"servers":"*"}','{}','digest-1','',1,'2026-09-01T00:00:00Z');
insert into plugin_secrets(id,name,purpose,resource_scope_json,value_encrypted,created_by_user_id,created_at) values(1,'api-token','webhook','{}','ciphertext',1,'2026-09-01T00:00:00Z');
insert into plugin_state(plugin_id,key,value_json,version,updated_at) values(1,'last_restart','"2026-09-01T00:00:00Z"',3,'2026-09-01T00:00:00Z');
insert into plugin_runs(id,uuid,plugin_id,revision_id,binding_id,grant_id,trigger_kind,idempotency_key,status,mode,snapshot_json,created_at) values(1,'run-queued',1,1,1,1,'plugin.interval','interval:1:1','queued','apply','{}','2026-09-01T00:00:00Z');
insert into plugin_installations(plugin_id,package_id,active_revision_id,installed,config_json,updated_at) values(1,'acme.watchdog',1,1,'{}','2026-09-01T00:00:00Z');
insert into app_settings(key,value,updated_at) values('plugins.enabled','true','2026-09-01T00:00:00Z');
insert into app_settings(key,value,updated_at) values('plugins.host_actions_enabled','true','2026-09-01T00:00:00Z');
insert into app_settings(key,value,updated_at) values('plugins.scheduler_paused','false','2026-09-01T00:00:00Z');
insert into app_settings(key,value,updated_at) values('plugins.recovery_generation','4','2026-09-01T00:00:00Z');
insert into app_settings(key,value,updated_at) values('plugins.max_concurrency','2','2026-09-01T00:00:00Z');
insert into app_settings(key,value,updated_at) values('plugins.max_timeout_seconds','300','2026-09-01T00:00:00Z');
insert into app_settings(key,value,updated_at) values('plugins.log_retention_days','30','2026-09-01T00:00:00Z');
insert into app_settings(key,value,updated_at) values('plugins.summary_retention_days','90','2026-09-01T00:00:00Z');
insert into event_outbox(id,topic,aggregate_id,payload_json,status,available_at,created_at) values('evt-retired','plugin.run.completed','run-queued','{}','pending','2026-09-01T00:00:00Z','2026-09-01T00:00:00Z');
insert into event_outbox(id,topic,aggregate_id,payload_json,status,available_at,created_at) values('evt-offline','plugin.server.offline','1','{"server_id":1}','pending','2026-09-01T00:00:00Z','2026-09-01T00:00:00Z');
insert into automation_changesets(id,principal_id,status,idempotency_key,expires_at,created_at,updated_at) values('cs-plugin','plugin:1','awaiting_approval','k1','2099-01-01T00:00:00Z','2026-09-01T00:00:00Z','2026-09-01T00:00:00Z');
