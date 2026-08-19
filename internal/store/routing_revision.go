package store

import (
	"context"
	"fmt"
	"strings"
)

// routingCacheRevisionTable is a single-row monotonic revision that every
// routing-relevant table mutation bumps through SQL triggers. It lets the
// Controller cache the derived FullRoutingConfigData + effective access
// snapshot and rebuild only when a governing table actually changed, while the
// database stays the source of truth: the revision is written in the same
// transaction as the mutation, so no Go call site can miss an invalidation.
const routingCacheRevisionTable = "routing_cache_revision"
const configurationRevisionTable = "configuration_revision"

// configurationRevisionTables are the tables whose successful management writes
// change the desired runtime state. Draft and pending authorization rows use
// conditional triggers; only active plan/binding/exception transitions enter
// automatic deployment. Probe results, fetched rule-set content, and the
// access-change lifecycle records themselves remain excluded.
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
	"users",
}

// routingRevisionTables are the tables that feed FullRoutingConfigData or the
// effective access snapshot. user_devices is handled separately because its
// per-report activity timestamp writes must not invalidate the cache.
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
	"users",
	"proxy_path_egress_results",
}

func routingRevisionTriggerStatements() []string {
	out := []string{fmt.Sprintf(`insert or ignore into %s(id,revision) values(1,0)`, routingCacheRevisionTable)}
	for _, table := range routingRevisionTables {
		for _, event := range []string{"insert", "update", "delete"} {
			out = append(out, fmt.Sprintf(
				`create trigger if not exists routing_rev_%s_%s after %s on %s begin update %s set revision=revision+1 where id=1; end`,
				table, event, event, table, routingCacheRevisionTable))
		}
	}
	// Device activity timestamp updates are high-frequency writes on the hot
	// audit path; only identity- or access-relevant fields invalidate.
	out = append(out,
		`create trigger if not exists routing_rev_user_devices_insert after insert on user_devices begin update routing_cache_revision set revision=revision+1 where id=1; end`,
		`create trigger if not exists routing_rev_user_devices_update after update on user_devices when old.status<>new.status or old.proxy_access_state<>new.proxy_access_state or old.credential_epoch<>new.credential_epoch or old.user_id<>new.user_id or old.token_hash<>new.token_hash begin update routing_cache_revision set revision=revision+1 where id=1; end`,
		`create trigger if not exists routing_rev_user_devices_delete after delete on user_devices begin update routing_cache_revision set revision=revision+1 where id=1; end`,
	)
	return out
}

func configurationRevisionTriggerStatements() []string {
	out := []string{fmt.Sprintf(`insert or ignore into %s(id,revision) values(1,0)`, configurationRevisionTable)}
	updateWhen := map[string]string{
		"servers":            `old.name<>new.name or old.chain_secret<>new.chain_secret or coalesce(old.entry_address,'')<>coalesce(new.entry_address,'') or old.region_code<>new.region_code or old.region_mode<>new.region_mode or old.entry_ip_mode<>new.entry_ip_mode or coalesce(old.listen_ip,'')<>coalesce(new.listen_ip,'') or old.listen_mode<>new.listen_mode or old.ip_stack<>new.ip_stack or old.udp_inbound_mode<>new.udp_inbound_mode or old.mtu_mode<>new.mtu_mode or old.mtu_value<>new.mtu_value or old.mtu_probe_host<>new.mtu_probe_host or old.mtu_probe_port<>new.mtu_probe_port or old.mtu_overhead_bytes<>new.mtu_overhead_bytes or old.bbr_enabled<>new.bbr_enabled or old.port_range_start<>new.port_range_start or old.port_range_end<>new.port_range_end or old.internal_port_range_start<>new.internal_port_range_start or old.internal_port_range_end<>new.internal_port_range_end or old.port_policy_revision<>new.port_policy_revision or old.connection_audit_enabled<>new.connection_audit_enabled`,
		"inbounds":           `old.server_id<>new.server_id or old.name<>new.name or old.protocol<>new.protocol or old.listen_ip<>new.listen_ip or old.port<>new.port or old.entry_ip_mode<>new.entry_ip_mode or old.external_ip<>new.external_ip or old.dns_sync_enabled<>new.dns_sync_enabled or coalesce(old.dns_credential_id,0)<>coalesce(new.dns_credential_id,0) or old.dns_domain<>new.dns_domain or old.dns_proxy_enabled<>new.dns_proxy_enabled or old.dns_record_types<>new.dns_record_types or old.ddns_enabled<>new.ddns_enabled or old.ddns_interval_seconds<>new.ddns_interval_seconds or old.tls<>new.tls or old.config_json<>new.config_json or old.enabled<>new.enabled`,
		"routing_rule_sets":  `old.name<>new.name or old.url<>new.url or old.format<>new.format or old.mihomo_behavior<>new.mihomo_behavior`,
		"subscription_plans": `old.name<>new.name or old.description<>new.description or old.enabled<>new.enabled or old.revision<>new.revision or coalesce(old.active_revision_id,0)<>coalesce(new.active_revision_id,0) or coalesce(old.draft_revision_id,0)<>coalesce(new.draft_revision_id,0) or coalesce(old.current_revision_id,0)<>coalesce(new.current_revision_id,0) or coalesce(old.latest_revision_id,0)<>coalesce(new.latest_revision_id,0) or coalesce(old.pending_revision_id,0)<>coalesce(new.pending_revision_id,0) or old.lock_version<>new.lock_version`,

		"server_dns_policies": `old.encrypted_list_id<>new.encrypted_list_id or old.bootstrap_list_id<>new.bootstrap_list_id or old.revision<>new.revision or old.strategy<>new.strategy or old.auto_test<>new.auto_test or old.test_interval_seconds<>new.test_interval_seconds`,
		"warp_profiles":       `old.server_id<>new.server_id or old.name<>new.name or old.config_json<>new.config_json or old.mtu<>new.mtu or old.dns_strategy<>new.dns_strategy or old.enabled<>new.enabled`,
		"users":               `old.role<>new.role or old.status<>new.status or old.proxy_uuid<>new.proxy_uuid or old.proxy_password<>new.proxy_password or old.speed_limit_mbps<>new.speed_limit_mbps or old.traffic_limit_bytes<>new.traffic_limit_bytes or old.traffic_reset_mode<>new.traffic_reset_mode or old.traffic_reset_day<>new.traffic_reset_day or old.legacy_proxy_enabled<>new.legacy_proxy_enabled`,
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

func (s *Store) migrateRoutingCacheRevisionTriggers(ctx context.Context) error {
	for _, stmt := range routingRevisionTriggerStatements() {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("install routing cache revision %s: %w", strings.SplitN(stmt, " ", 4)[2], err)
		}
	}
	return nil
}

func (s *Store) migrateConfigurationRevisionTriggers(ctx context.Context) error {
	for _, stmt := range configurationRevisionTriggerStatements() {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("install configuration revision trigger: %w", err)
		}
	}
	// Device activity timestamps are deliberately excluded, while credential,
	// ownership, and access-state changes remain configuration mutations.
	for _, stmt := range []string{
		`create trigger if not exists config_rev_user_devices_insert after insert on user_devices begin update configuration_revision set revision=revision+1 where id=1; end`,
		`create trigger if not exists config_rev_user_devices_update after update on user_devices when old.status<>new.status or old.proxy_access_state<>new.proxy_access_state or old.credential_epoch<>new.credential_epoch or old.user_id<>new.user_id or old.token_hash<>new.token_hash begin update configuration_revision set revision=revision+1 where id=1; end`,
		`create trigger if not exists config_rev_user_devices_delete after delete on user_devices begin update configuration_revision set revision=revision+1 where id=1; end`,
	} {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("install configuration device trigger: %w", err)
		}
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
