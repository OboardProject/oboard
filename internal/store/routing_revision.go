package store

import (
	"context"
	"fmt"
)

// routingCacheRevisionTable is a single-row monotonic revision that every
// routing-relevant table mutation bumps through SQL triggers. It lets the
// Controller cache the derived FullRoutingConfigData + effective access
// snapshot and rebuild only when a governing table actually changed, while the
// database stays the source of truth: the revision is written in the same
// transaction as the mutation, so no Go call site can miss an invalidation.
const routingCacheRevisionTable = "routing_cache_revision"
const configurationRevisionTable = "configuration_revision"
const trafficPolicyRevisionTable = "traffic_policy_revision"

// configurationRevisionTables are the tables whose successful management writes
// change the desired runtime state. Draft and pending authorization rows use
// conditional triggers; only active plan/binding/exception transitions enter
// automatic deployment. Probe results, fetched rule-set content, and the
// access-change lifecycle records themselves remain excluded.
//
// Traffic accounting (users.traffic_used_bytes) and user runtime quota fields
// are not configuration desired-state. User identity changes also stay off this
// watermark: they invalidate the routing snapshot and queue targeted
// apply_core_config. Quota fields advance traffic_policy_revision and use
// traffic reports / apply_traffic_policy.
var configurationRevisionTables = []string{
	"servers",
	"inbounds",
	"outbounds",
	"external_outbounds",
	"proxy_paths",
	"proxy_path_steps",
	"proxy_path_port_allocations",
	"routing_rules",
	"routing_rule_sets",
	"dns_lists",
	"server_dns_policies",
	"warp_profiles",
	"port_forwards",
	"tunnels",
	"user_groups",
	"user_group_members",
	"subscription_plans",
	"subscription_plan_revisions",
	"subscription_plan_revision_nodes",
	"subscription_plan_revision_rules",
	"subscription_plan_revision_node_exclusions",
	"user_plan_bindings",
	"user_node_exceptions",
}

// routingRevisionTables are the tables that feed FullRoutingConfigData or the
// effective access snapshot. `users` and `user_devices` are handled separately
// because high-frequency accounting and activity timestamp writes must not
// invalidate the cache.
var routingRevisionTables = []string{
	"servers",
	"inbounds",
	"outbounds",
	"external_outbounds",
	"proxy_paths",
	"proxy_path_steps",
	"proxy_path_port_allocations",
	"routing_rules",
	"routing_rule_sets",
	"dns_lists",
	"server_dns_policies",
	"warp_profiles",
	"port_forwards",
	"tunnels",
	"user_groups",
	"user_group_members",
	"subscription_plans",
	"subscription_plan_revisions",
	"subscription_plan_revision_nodes",
	"subscription_plan_revision_rules",
	"subscription_plan_revision_node_exclusions",
	"user_plan_bindings",
	"user_node_exceptions",
	"proxy_path_egress_results",
}

// usersRoutingUpdateWhen lists user fields that change identity, access, or
// protocol projection. Traffic accounting and runtime quota fields are
// excluded: they are materialized for the panel and overlaid as Traffic
// Runtime Policy at generate time, so they must not rebuild the routing
// snapshot on every report.
const usersRoutingUpdateWhen = `old.username<>new.username or old.role<>new.role or old.status<>new.status or old.proxy_uuid<>new.proxy_uuid or old.proxy_password<>new.proxy_password or old.legacy_proxy_enabled<>new.legacy_proxy_enabled or old.device_limit<>new.device_limit`

const userDevicesUpdateWhen = `old.status<>new.status or old.proxy_access_state<>new.proxy_access_state or old.credential_epoch<>new.credential_epoch or old.user_id<>new.user_id or old.token_hash<>new.token_hash`

func routingRevisionTriggerStatements() []string {
	out := []string{fmt.Sprintf(`insert or ignore into %s(id,revision) values(1,0)`, routingCacheRevisionTable)}
	for _, table := range routingRevisionTables {
		for _, event := range []string{"insert", "update", "delete"} {
			out = append(out,
				fmt.Sprintf(`drop trigger if exists routing_rev_%s_%s`, table, event),
				fmt.Sprintf(`create trigger routing_rev_%s_%s after %s on %s begin update %s set revision=revision+1 where id=1; end`, table, event, event, table, routingCacheRevisionTable),
			)
		}
	}
	out = append(out,
		`drop trigger if exists routing_rev_users_insert`,
		`drop trigger if exists routing_rev_users_update`,
		`drop trigger if exists routing_rev_users_delete`,
		`create trigger routing_rev_users_insert after insert on users begin update routing_cache_revision set revision=revision+1 where id=1; end`,
		`create trigger routing_rev_users_delete after delete on users begin update routing_cache_revision set revision=revision+1 where id=1; end`,
		`create trigger routing_rev_users_update after update on users when `+usersRoutingUpdateWhen+` begin update routing_cache_revision set revision=revision+1 where id=1; end`,
		`drop trigger if exists routing_rev_user_devices_insert`,
		`drop trigger if exists routing_rev_user_devices_update`,
		`drop trigger if exists routing_rev_user_devices_delete`,
		`create trigger routing_rev_user_devices_insert after insert on user_devices begin update routing_cache_revision set revision=revision+1 where id=1; end`,
		`create trigger routing_rev_user_devices_update after update on user_devices when `+userDevicesUpdateWhen+` begin update routing_cache_revision set revision=revision+1 where id=1; end`,
		`create trigger routing_rev_user_devices_delete after delete on user_devices begin update routing_cache_revision set revision=revision+1 where id=1; end`,
	)
	return out
}

func configurationRevisionTriggerStatements() []string {
	out := []string{fmt.Sprintf(`insert or ignore into %s(id,revision) values(1,0)`, configurationRevisionTable)}
	updateWhen := map[string]string{
		"servers":            `old.name<>new.name or old.chain_secret<>new.chain_secret or coalesce(old.entry_address,'')<>coalesce(new.entry_address,'') or old.region_code<>new.region_code or old.region_mode<>new.region_mode or old.entry_ip_mode<>new.entry_ip_mode or coalesce(old.listen_ip,'')<>coalesce(new.listen_ip,'') or old.listen_mode<>new.listen_mode or old.ip_stack<>new.ip_stack or old.udp_inbound_mode<>new.udp_inbound_mode or old.mtu_mode<>new.mtu_mode or old.mtu_value<>new.mtu_value or old.mtu_probe_host<>new.mtu_probe_host or old.mtu_probe_port<>new.mtu_probe_port or old.mtu_overhead_bytes<>new.mtu_overhead_bytes or old.bbr_enabled<>new.bbr_enabled or old.port_range_start<>new.port_range_start or old.port_range_end<>new.port_range_end or old.internal_port_range_start<>new.internal_port_range_start or old.internal_port_range_end<>new.internal_port_range_end or old.port_policy_revision<>new.port_policy_revision or old.connection_audit_enabled<>new.connection_audit_enabled`,
		"inbounds":           `old.server_id<>new.server_id or old.name<>new.name or old.protocol<>new.protocol or old.listen_ip<>new.listen_ip or old.port<>new.port or coalesce(old.advertise_port,0)<>coalesce(new.advertise_port,0) or old.entry_ip_mode<>new.entry_ip_mode or old.external_ip<>new.external_ip or old.dns_sync_enabled<>new.dns_sync_enabled or coalesce(old.dns_credential_id,0)<>coalesce(new.dns_credential_id,0) or old.dns_domain<>new.dns_domain or old.dns_proxy_enabled<>new.dns_proxy_enabled or old.dns_record_types<>new.dns_record_types or old.ddns_enabled<>new.ddns_enabled or old.ddns_interval_seconds<>new.ddns_interval_seconds or old.tls<>new.tls or old.config_json<>new.config_json or old.enabled<>new.enabled`,
		"routing_rule_sets":  `old.name<>new.name or old.url<>new.url or old.format<>new.format or old.mihomo_behavior<>new.mihomo_behavior`,
		"subscription_plans": `old.enabled<>new.enabled or coalesce(old.active_revision_id,0)<>coalesce(new.active_revision_id,0) or coalesce(old.current_revision_id,0)<>coalesce(new.current_revision_id,0)`,
		"server_dns_policies": `coalesce(old.encrypted_list_id,0)<>coalesce(new.encrypted_list_id,0) or old.bootstrap_list_id<>new.bootstrap_list_id or old.revision<>new.revision or old.strategy<>new.strategy or old.auto_test<>new.auto_test or old.test_interval_seconds<>new.test_interval_seconds`,
		"warp_profiles":       `old.server_id<>new.server_id or old.name<>new.name or old.config_json<>new.config_json or old.mtu<>new.mtu or old.dns_strategy<>new.dns_strategy or old.enabled<>new.enabled`,
	}
	for _, table := range configurationRevisionTables {
		insertCondition, updateCondition, deleteCondition := configurationRevisionConditions(table, updateWhen[table])
		out = append(out,
			fmt.Sprintf(`drop trigger if exists config_rev_%s_insert`, table),
			fmt.Sprintf(`drop trigger if exists config_rev_%s_update`, table),
			fmt.Sprintf(`drop trigger if exists config_rev_%s_delete`, table),
			fmt.Sprintf(`create trigger config_rev_%s_insert after insert on %s when %s begin update %s set revision=revision+1 where id=1; end`, table, table, insertCondition, configurationRevisionTable),
			fmt.Sprintf(`create trigger config_rev_%s_delete after delete on %s when %s begin update %s set revision=revision+1 where id=1; end`, table, table, deleteCondition, configurationRevisionTable),
			fmt.Sprintf(`create trigger config_rev_%s_update after update on %s when %s begin update %s set revision=revision+1 where id=1; end`, table, table, updateCondition, configurationRevisionTable),
		)
	}
	out = append(out,
		`drop trigger if exists config_rev_users_insert`,
		`drop trigger if exists config_rev_users_update`,
		`drop trigger if exists config_rev_users_delete`,
		`drop trigger if exists config_rev_user_devices_insert`,
		`drop trigger if exists config_rev_user_devices_update`,
		`drop trigger if exists config_rev_user_devices_delete`,
		`create trigger config_rev_user_devices_insert after insert on user_devices begin update configuration_revision set revision=revision+1 where id=1; end`,
		`create trigger config_rev_user_devices_update after update on user_devices when `+userDevicesUpdateWhen+` begin update configuration_revision set revision=revision+1 where id=1; end`,
		`create trigger config_rev_user_devices_delete after delete on user_devices begin update configuration_revision set revision=revision+1 where id=1; end`,
	)
	return out
}

func configurationRevisionConditions(table, updateCondition string) (string, string, string) {
	if updateCondition == "" {
		updateCondition = "1"
	}
	switch table {
	case "proxy_paths":
		return "new.enabled=1", "old.enabled=1 or new.enabled=1", "old.enabled=1"
	case "proxy_path_steps":
		return "exists(select 1 from proxy_paths where id=new.path_id and enabled=1)", "exists(select 1 from proxy_paths where id=old.path_id and enabled=1) or exists(select 1 from proxy_paths where id=new.path_id and enabled=1)", "exists(select 1 from proxy_paths where id=old.path_id and enabled=1)"
	case "subscription_plan_revisions":
		return "new.status='active'", "old.status='active' or new.status='active'", "old.status='active'"
	case "subscription_plan_revision_nodes", "subscription_plan_revision_rules", "subscription_plan_revision_node_exclusions":
		return "exists(select 1 from subscription_plan_revisions where id=new.revision_id and status='active')", "exists(select 1 from subscription_plan_revisions where id=old.revision_id and status='active') or exists(select 1 from subscription_plan_revisions where id=new.revision_id and status='active')", "exists(select 1 from subscription_plan_revisions where id=old.revision_id and status='active')"
	case "user_plan_bindings":
		return "new.status='active' and new.enabled=1", "(old.status='active' or new.status='active') and (old.enabled<>new.enabled or old.status<>new.status or old.user_id<>new.user_id or old.plan_id<>new.plan_id or coalesce(old.starts_at,'')<>coalesce(new.starts_at,'') or coalesce(old.expires_at,'')<>coalesce(new.expires_at,''))", "old.status='active' and old.enabled=1"
	case "user_node_exceptions":
		return "new.status='active'", "old.status='active' or new.status='active'", "old.status='active'"
	default:
		return "1", updateCondition, "1"
	}
}

func (s *Store) dropTriggersReferencingTable(ctx context.Context, table string) error {
	rows, err := s.db.QueryContext(ctx, `select name from sqlite_master where type='trigger' and (tbl_name=? or instr(coalesce(sql,''), ?) > 0)`, table, table)
	if err != nil {
		return err
	}
	defer rows.Close()
	names := make([]string, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, name := range names {
		if _, err := s.db.ExecContext(ctx, `drop trigger if exists `+name); err != nil {
			return fmt.Errorf("drop trigger %s referencing %s: %w", name, table, err)
		}
	}
	return nil
}

func (s *Store) dropManagedRevisionTriggers(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `select name from sqlite_master where type='trigger' and (name like 'routing_rev_%' or name like 'config_rev_%')`)
	if err != nil {
		return err
	}
	defer rows.Close()
	names := make([]string, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, name := range names {
		if _, err := s.db.ExecContext(ctx, `drop trigger if exists `+name); err != nil {
			return fmt.Errorf("drop managed revision trigger %s: %w", name, err)
		}
	}
	return nil
}

func (s *Store) migrateManagedRevisionTriggers(ctx context.Context) error {
	if err := s.dropManagedRevisionTriggers(ctx); err != nil {
		return err
	}
	for _, stmt := range routingRevisionTriggerStatements() {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("install routing cache revision trigger: %w", err)
		}
	}
	for _, stmt := range configurationRevisionTriggerStatements() {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("install configuration revision trigger: %w", err)
		}
	}
	return nil
}

func (s *Store) migrateRoutingCacheRevisionTriggers(ctx context.Context) error {
	return nil
}

func (s *Store) migrateConfigurationRevisionTriggers(ctx context.Context) error {
	return s.migrateManagedRevisionTriggers(ctx)
}

func (s *Store) migrateTrafficPolicyRevision(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `create table if not exists traffic_policy_revision (id integer primary key check(id=1), revision integer not null default 0)`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `insert or ignore into traffic_policy_revision(id,revision) values(1,0)`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "configuration_sync_states", "trigger_reason", `alter table configuration_sync_states add column trigger_reason text not null default ''`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "configuration_sync_states", "sync_strategy", `alter table configuration_sync_states add column sync_strategy text not null default ''`); err != nil {
		return err
	}
	return nil
}

// RoutingCacheRevision returns the current routing-table mutation revision.
// The Controller snapshot cache rebuilds whenever this value changes.
func (s *Store) RoutingCacheRevision(ctx context.Context) (uint64, error) {
	var revision uint64
	if err := s.db.QueryRowContext(ctx, `select revision from `+routingCacheRevisionTable+` where id=1`).Scan(&revision); err != nil {
		return 0, err
	}
	return revision, nil
}

// ConfigurationRevision is the crash-safe watermark for management writes that
// must converge to Agents. It is separate from RoutingCacheRevision because
// the latter also advances for operational results and other derived caches.
func (s *Store) ConfigurationRevision(ctx context.Context) (uint64, error) {
	var revision uint64
	if err := s.db.QueryRowContext(ctx, `select revision from `+configurationRevisionTable+` where id=1`).Scan(&revision); err != nil {
		return 0, err
	}
	return revision, nil
}

// TrafficPolicyRevision is the monotonic watermark for runtime quota, speed
// limit, lease, and period policy. It must never wake the configuration
// reconciler or increment config_version.
func (s *Store) TrafficPolicyRevision(ctx context.Context) (uint64, error) {
	var revision uint64
	if err := s.db.QueryRowContext(ctx, `select revision from `+trafficPolicyRevisionTable+` where id=1`).Scan(&revision); err != nil {
		return 0, err
	}
	return revision, nil
}

func bumpTrafficPolicyRevisionTx(tx trafficTx, ctx context.Context) error {
	_, err := tx.ExecContext(ctx, `update traffic_policy_revision set revision=revision+1 where id=1`)
	return err
}

// BumpTrafficPolicyRevision advances the runtime traffic-policy watermark.
// Callers must not treat this as a configuration mutation.
func (s *Store) BumpTrafficPolicyRevision(ctx context.Context) (uint64, error) {
	if _, err := s.db.ExecContext(ctx, `update traffic_policy_revision set revision=revision+1 where id=1`); err != nil {
		return 0, err
	}
	return s.TrafficPolicyRevision(ctx)
}

func (s *Store) revisionTriggerSQL(ctx context.Context, name string) (string, error) {
	var sqlText string
	err := s.db.QueryRowContext(ctx, `select sql from sqlite_master where type='trigger' and name=?`, name).Scan(&sqlText)
	return sqlText, err
}

// PendingTaskServerIDs returns the distinct servers that have at least one
// queued pending task. The Controller recovery scan uses it to re-wake agents
// after a lost wake notification; SQLite remains the task source of truth.
func (s *Store) PendingTaskServerIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `select distinct server_id from agent_tasks where status='pending' order by server_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var serverID int64
		if err := rows.Scan(&serverID); err != nil {
			return nil, err
		}
		out = append(out, serverID)
	}
	return out, rows.Err()
}
