package store

import (
	"fmt"
	"strings"
)

// Configuration intent scoping.
//
// Every configuration write records, in its own transaction, which servers have
// to re-converge. A table whose changed row cannot be mapped to servers records
// the `unresolved` scope, which the drain expands to the whole fleet. That is
// correct but expensive: one edited inbound used to make every enrolled server
// recompute its projection.
//
// A mapping states only what the changed row *seeds*: the servers, inbounds,
// proxy paths, or assignable nodes it names. One shared expansion then walks
// from those seeds to the servers that render configuration for them, so each
// table describes its own row and nothing else.
//
// The seeds are the union of before and after: update triggers expand both
// `old` and `new`, so a row that moves between servers re-converges the one it
// left as well as the one it joined. Too *narrow* is the dangerous direction —
// a server that is never marked pending never converges — so a seed set is
// allowed to be a superset, and the periodic sweep re-marks the fleet on a slow
// cadence as the backstop.

// configurationScopeSeeds are the starting points a changed row contributes.
// Each entry is a SELECT; `nodes` entries return (node_type, node_id) and the
// rest return a single id column.
type configurationScopeSeeds struct {
	servers  []string
	inbounds []string
	paths    []string
	nodes    []string
}

func (s configurationScopeSeeds) empty() bool {
	return len(s.servers) == 0 && len(s.inbounds) == 0 && len(s.paths) == 0 && len(s.nodes) == 0
}

// unionOrEmpty joins seed selects, or produces a typed empty relation when the
// table contributes none of that kind.
func unionOrEmpty(selects []string, emptyColumns string) string {
	if len(selects) == 0 {
		return fmt.Sprintf(`select %s where 0`, emptyColumns)
	}
	return strings.Join(selects, " union ")
}

// chainServersOfPaths is every server that renders configuration for a set of
// paths: the servers their steps sit on, the servers of the inbounds those
// steps target, and the server of each path's root inbound. A change anywhere
// on a chain changes what its neighbours listen for and dial.
func chainServersOfPaths(paths string) string {
	return fmt.Sprintf(`select s.server_id as server_id from proxy_path_steps s where s.path_id in (%[1]s)
		union select i.server_id from proxy_path_steps s2 join inbounds i on i.id=s2.inbound_id where s2.path_id in (%[1]s)
		union select ri.server_id from proxy_paths p join inbounds ri on ri.id=p.inbound_id where p.id in (%[1]s)`, paths)
}

// configurationScopeServerSelect expands seeds to the servers that must
// re-converge.
//
// SQLite trigger programs reject a WITH clause, so each seed set is inlined; the
// expansion is shaped to keep the number of copies small. Inbound seeds pull in
// the paths they root or are traversed by, because those chains dial through
// them. Node seeds use the shallower expansion on purpose: an authorization
// change reaches the server of a granted inbound and the chain of a granted
// path, not every unrelated chain that happens to pass through that inbound.
func configurationScopeServerSelect(seeds configurationScopeSeeds) string {
	parts := []string{}
	if len(seeds.servers) > 0 {
		parts = append(parts, unionOrEmpty(seeds.servers, `null as server_id`))
	}
	if len(seeds.inbounds) > 0 {
		inbounds := unionOrEmpty(seeds.inbounds, `null as inbound_id`)
		parts = append(parts,
			fmt.Sprintf(`select server_id from inbounds where id in (%s)`, inbounds),
			chainServersOfPaths(fmt.Sprintf(`select id from proxy_paths where inbound_id in (%[1]s) union select path_id from proxy_path_steps where inbound_id in (%[1]s)`, inbounds)))
	}
	if len(seeds.paths) > 0 {
		parts = append(parts, chainServersOfPaths(unionOrEmpty(seeds.paths, `null as path_id`)))
	}
	if len(seeds.nodes) > 0 {
		nodes := unionOrEmpty(seeds.nodes, `null as node_type, null as node_id`)
		parts = append(parts,
			fmt.Sprintf(`select server_id from inbounds where id in (select n.node_id from (%s) n where n.node_type='inbound')`, nodes),
			chainServersOfPaths(fmt.Sprintf(`select n2.node_id from (%s) n2 where n2.node_type='proxy_path'`, nodes)))
	}
	return strings.Join(parts, " union ")
}

// userAuthorizedNodes are the nodes a user may currently reach: the active
// revision of the plan they are bound to, plus their individual exceptions.
//
// The `users` table itself has no configuration trigger on purpose - identity,
// status and credential changes travel the authorization lease and runtime-user
// lanes instead of the deployment path - so this is used by the tables that do
// carry authorization into configuration.
func userAuthorizedNodes(userExpr string) string {
	return fmt.Sprintf(`select n.node_type as node_type,n.node_id as node_id from subscription_plan_revision_nodes n
		join subscription_plans p on p.active_revision_id=n.revision_id
		join user_plan_bindings b on b.plan_id=p.id
		where b.user_id=%[1]s and b.enabled=1
		union select e.node_type,e.node_id from user_node_exceptions e where e.user_id=%[1]s`, userExpr)
}

// activePlanNodes are the nodes a plan currently grants. Draft revisions are
// deliberately excluded: they are not runtime state until they are activated,
// and the activation itself changes the plan row.
func activePlanNodes(planExpr string) string {
	return fmt.Sprintf(`select n.node_type as node_type,n.node_id as node_id from subscription_plan_revision_nodes n
		join subscription_plans sp on sp.active_revision_id=n.revision_id where sp.id=%s`, planExpr)
}

// revisionPlanNodes are the nodes currently granted by the plan a revision
// belongs to, which is what a change to that revision's contents can affect.
func revisionPlanNodes(revisionExpr string) string {
	return fmt.Sprintf(`select n.node_type as node_type,n.node_id as node_id from subscription_plan_revision_nodes n
		join subscription_plan_revisions rv on rv.id=%s
		join subscription_plans sp on sp.id=rv.plan_id and sp.active_revision_id=n.revision_id`, revisionExpr)
}

// configurationSyncScopeSeeds returns what a changed row of this table seeds,
// or empty when the table has no derivable mapping and must fall back to the
// whole fleet.
func configurationSyncScopeSeeds(table, alias string) configurationScopeSeeds {
	server := func(expr string) string { return fmt.Sprintf(`select %s.%s as server_id`, alias, expr) }
	inbound := func(expr string) string { return fmt.Sprintf(`select %s.%s as inbound_id`, alias, expr) }
	path := func(expr string) string { return fmt.Sprintf(`select %s.%s as path_id`, alias, expr) }
	switch table {
	case "servers":
		return configurationScopeSeeds{servers: []string{server("id")}}
	case "server_dns_policies", "warp_profiles", "snell_listener_runtime", "proxy_path_port_allocations":
		return configurationScopeSeeds{servers: []string{server("server_id")}}
	case "outbounds":
		return configurationScopeSeeds{servers: []string{server("server_id"), server("next_server_id")}}
	case "port_forwards", "tunnels":
		return configurationScopeSeeds{servers: []string{server("source_server_id"), server("target_server_id")}}
	case "inbounds":
		return configurationScopeSeeds{inbounds: []string{inbound("id")}}
	case "proxy_paths":
		return configurationScopeSeeds{paths: []string{path("id")}}
	case "proxy_path_steps":
		return configurationScopeSeeds{
			servers:  []string{server("server_id")},
			inbounds: []string{inbound("inbound_id")},
			paths:    []string{path("path_id")},
		}
	case "routing_rules":
		return configurationScopeSeeds{
			servers: []string{server("server_id"), server("target_server_id")},
			paths:   []string{path("proxy_path_id"), path("target_proxy_path_id")},
		}
	case "routing_rule_sets":
		return configurationScopeSeeds{
			servers: []string{fmt.Sprintf(`select server_id as server_id from routing_rules where rule_set_id=%s.id`, alias)},
			paths:   []string{fmt.Sprintf(`select proxy_path_id as path_id from routing_rules where rule_set_id=%s.id`, alias)},
		}
	case "dns_lists":
		return configurationScopeSeeds{servers: []string{fmt.Sprintf(`select server_id as server_id from server_dns_policies where encrypted_list_id=%[1]s.id or bootstrap_list_id=%[1]s.id`, alias)}}
	case "external_outbounds":
		return configurationScopeSeeds{paths: []string{fmt.Sprintf(`select path_id as path_id from proxy_path_steps where external_outbound_id=%s.id`, alias)}}
	case "family_split_templates":
		return configurationScopeSeeds{paths: []string{fmt.Sprintf(`select id as path_id from proxy_paths where template_id=%s.id`, alias)}}
	case "proxy_credentials":
		return configurationScopeSeeds{
			inbounds: []string{inbound("inbound_id")},
			paths:    []string{path("path_id")},
		}
	case "user_devices":
		return configurationScopeSeeds{nodes: []string{userAuthorizedNodes(alias + ".user_id")}}
	case "user_plan_bindings":
		// The plan on both sides of the change: the nodes it grants are what
		// the binding adds or takes away. The user's other nodes are unchanged.
		return configurationScopeSeeds{nodes: []string{activePlanNodes(alias + ".plan_id")}}
	case "user_node_exceptions":
		return configurationScopeSeeds{nodes: []string{fmt.Sprintf(`select %[1]s.node_type as node_type,%[1]s.node_id as node_id`, alias)}}
	case "subscription_plans":
		return configurationScopeSeeds{nodes: []string{activePlanNodes(alias + ".id")}}
	case "subscription_plan_revisions":
		return configurationScopeSeeds{nodes: []string{
			fmt.Sprintf(`select node_type as node_type,node_id as node_id from subscription_plan_revision_nodes where revision_id=%s.id`, alias),
			activePlanNodes(alias + ".plan_id"),
		}}
	case "subscription_plan_revision_nodes":
		return configurationScopeSeeds{nodes: []string{
			fmt.Sprintf(`select %[1]s.node_type as node_type,%[1]s.node_id as node_id`, alias),
			revisionPlanNodes(alias + ".revision_id"),
		}}
	case "subscription_plan_revision_rules", "subscription_plan_revision_node_exclusions":
		return configurationScopeSeeds{nodes: []string{
			fmt.Sprintf(`select node_type as node_type,node_id as node_id from subscription_plan_revision_nodes where revision_id=%s.revision_id`, alias),
			revisionPlanNodes(alias + ".revision_id"),
		}}
	default:
		// user_groups and user_group_members decide plan membership through
		// rules that are re-evaluated outside SQL, so they stay fleet-wide.
		return configurationScopeSeeds{}
	}
}

// configurationSyncScopeAliases is which row images an event can read. An
// update contributes both, so a row that moves between servers re-converges the
// one it left as well as the one it joined.
func configurationSyncScopeAliases(event string) []string {
	switch event {
	case "insert":
		return []string{"new"}
	case "delete":
		return []string{"old"}
	default:
		return []string{"old", "new"}
	}
}
