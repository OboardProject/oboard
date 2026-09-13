package store

import "fmt"

// Configuration intent scoping.
//
// Every configuration write records, in its own transaction, which servers have
// to re-converge. A table whose changed row cannot be mapped to servers records
// the `unresolved` scope, which the drain expands to the whole fleet. That is
// correct but expensive: one edited inbound used to make every enrolled server
// recompute its projection.
//
// The mappings below are the union of the servers the row belonged to before
// the change and the ones it belongs to after it, which is why update triggers
// emit both `old` and `new`. Getting a mapping *too narrow* is the dangerous
// direction - a server that is never marked pending never converges - so only
// relationships that are fully derivable in SQL are mapped here, and the
// periodic sweep re-marks the fleet on a slow cadence as the backstop.

// proxyPathIDsFor is the set of paths an inbound participates in: the paths it
// roots, and the paths that traverse it as a hop.
func proxyPathIDsFor(inboundExpr string) string {
	return fmt.Sprintf(`select id from proxy_paths where inbound_id=%s union select path_id from proxy_path_steps where inbound_id=%s`, inboundExpr, inboundExpr)
}

// serversOfProxyPaths is every server that renders configuration for a set of
// paths: the servers the steps sit on, the servers of the inbounds those steps
// target, and the server of each path's root inbound. A change anywhere on a
// chain changes what its neighbours have to listen for and dial.
func serversOfProxyPaths(pathIDs string) string {
	return fmt.Sprintf(`select s.server_id as server_id from proxy_path_steps s where s.path_id in (%[1]s)
		union select i.server_id from proxy_path_steps s2 join inbounds i on i.id=s2.inbound_id where s2.path_id in (%[1]s)
		union select ri.server_id from proxy_paths p join inbounds ri on ri.id=p.inbound_id where p.id in (%[1]s)`, pathIDs)
}

// configurationSyncScopeSelect returns a select producing the server IDs a
// change to this table touches, or an empty string when the table has no
// derivable mapping and must fall back to the whole fleet.
func configurationSyncScopeSelect(table, alias string) string {
	switch table {
	case "servers":
		return fmt.Sprintf(`select %s.id as server_id`, alias)
	case "server_dns_policies", "warp_profiles", "snell_listener_runtime", "proxy_path_port_allocations":
		return fmt.Sprintf(`select %s.server_id as server_id`, alias)
	case "outbounds":
		return fmt.Sprintf(`select %[1]s.server_id as server_id union select %[1]s.next_server_id`, alias)
	case "port_forwards", "tunnels":
		return fmt.Sprintf(`select %[1]s.source_server_id as server_id union select %[1]s.target_server_id`, alias)
	case "inbounds":
		return fmt.Sprintf(`select %s.server_id as server_id union %s`, alias, serversOfProxyPaths(proxyPathIDsFor(alias+".id")))
	case "proxy_paths":
		return serversOfProxyPaths(fmt.Sprintf(`select %s.id`, alias))
	case "proxy_path_steps":
		return fmt.Sprintf(`select %[1]s.server_id as server_id
			union select i.server_id from inbounds i where i.id=%[1]s.inbound_id
			union %[2]s`, alias, serversOfProxyPaths(fmt.Sprintf(`select %s.path_id`, alias)))
	case "dns_lists":
		return fmt.Sprintf(`select p.server_id as server_id from server_dns_policies p where p.encrypted_list_id=%[1]s.id or p.bootstrap_list_id=%[1]s.id`, alias)
	case "external_outbounds":
		return serversOfProxyPaths(fmt.Sprintf(`select path_id from proxy_path_steps where external_outbound_id=%s.id`, alias))
	case "family_split_templates":
		return serversOfProxyPaths(fmt.Sprintf(`select id from proxy_paths where template_id=%s.id`, alias))
	case "routing_rule_sets":
		return fmt.Sprintf(`select r.server_id as server_id from routing_rules r where r.rule_set_id=%[1]s.id
			union %[2]s`, alias, serversOfProxyPaths(fmt.Sprintf(`select proxy_path_id from routing_rules where rule_set_id=%s.id`, alias)))
	case "routing_rules":
		return fmt.Sprintf(`select %[1]s.server_id as server_id
			union select %[1]s.target_server_id
			union %[2]s
			union %[3]s`, alias,
			serversOfProxyPaths(fmt.Sprintf(`select %s.proxy_path_id`, alias)),
			serversOfProxyPaths(fmt.Sprintf(`select %s.target_proxy_path_id`, alias)))
	default:
		return ""
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
