package store

import "context"

// MigratePluginWebhookSchema is called after the plugin tables are initialized.
func (s *Store) MigratePluginWebhookSchema(ctx context.Context) error {
	for _, statement := range []string{
		`create table if not exists plugin_webhooks (
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
 )`,
		`create index if not exists idx_plugin_webhooks_plugin on plugin_webhooks(plugin_id)`,
		`create table if not exists plugin_webhook_deliveries (
 webhook_id text not null references plugin_webhooks(id) on delete cascade,
 nonce text not null,
 generation integer not null,
 received_at integer not null,
 expires_at integer not null,
 status text not null default 'received',
 run_id integer references plugin_runs(id) on delete set null,
 primary key(webhook_id, nonce)
 )`,
		`create index if not exists idx_plugin_webhook_deliveries_expiry on plugin_webhook_deliveries(expires_at)`,
		`create trigger if not exists plugin_webhook_run_guard before insert on plugin_runs
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
 end`,
		`create trigger if not exists plugin_webhook_run_cancel after update of generation on plugin_webhooks
 begin
 update plugin_runs set status='cancelled',error_code='cancelled',finished_at=new.updated_at,
 lease_generation=lease_generation+1,lease_owner='',lease_until=null
 where trigger_kind='plugin.webhook' and binding_id=new.binding_id and status in ('queued','running');
 end`,
	} {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
