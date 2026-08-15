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

func (s *Store) migrateRoutingCacheRevisionTriggers(ctx context.Context) error {
	create := routingRevisionTriggerStatements()
	for _, stmt := range create {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("install routing cache revision %s: %w", strings.SplitN(stmt, " ", 4)[2], err)
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
