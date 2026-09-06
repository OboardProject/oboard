package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

// CompiledRoutePolicy is the shared matching unit for target resolution and routing.
type CompiledRoutePolicy struct {
	ServerID          int64          `json:"server_id"`
	PathID            *int64         `json:"path_id,omitempty"`
	StageKey          string         `json:"stage_key"`
	RuleID            int64          `json:"rule_id"`
	Order             int            `json:"order"`
	Match             map[string]any `json:"match"`
	Resolver          string         `json:"resolver,omitempty"`
	Outbound          string         `json:"outbound"`
	ResolveInSelector bool           `json:"resolve_in_selector"`
}

func compileRoutePolicy(rule model.RoutingRule, inboundTags, authUsers []string, outbound string, dns map[string]any, sets []model.RoutingRuleSet) (*CompiledRoutePolicy, error) {
	match, err := routingMatch(rule)
	if err != nil {
		return nil, err
	}
	policy := &CompiledRoutePolicy{ServerID: rule.ServerID, PathID: rule.ProxyPathID, StageKey: "root", RuleID: rule.ID, Order: rule.SortPosition, Match: match, Outbound: outbound, ResolveInSelector: rule.Action == model.RouteActionFamilySplit}
	if rule.StageStepID != nil {
		policy.StageKey = fmt.Sprint(*rule.StageStepID)
	}
	if len(inboundTags) > 0 {
		scope := map[string]any{"inbound": append([]string(nil), inboundTags...)}
		if len(authUsers) > 0 {
			scope["auth_user"] = append([]string(nil), authUsers...)
		}
		if match["type"] == "logical" || match["invert"] == true {
			policy.Match = map[string]any{"type": "logical", "mode": "and", "rules": []map[string]any{scope, match}}
		} else {
			for key, value := range scope {
				match[key] = value
			}
		}
	}
	if strings.TrimSpace(rule.DNSResolver) != "" && rule.Action != model.RouteActionBlock {
		if err := ValidateRoutingDNSOverride(rule, sets); err != nil {
			return nil, err
		}
		policy.Resolver, err = RequireDNSServerTag(dns, rule.DNSResolver)
		if err != nil {
			return nil, err
		}
	}
	return policy, nil
}

func (p *CompiledRoutePolicy) Rules() []map[string]any {
	rules := make([]map[string]any, 0, 2)
	if p.Resolver != "" && !p.ResolveInSelector {
		resolve := cloneNestedMap(p.Match)
		resolve["action"] = "resolve"
		resolve["server"] = p.Resolver
		resolve["disable_optimistic_cache"] = true
		rules = append(rules, resolve)
	}
	route := cloneNestedMap(p.Match)
	route["action"] = "route"
	route["outbound"] = p.Outbound
	return append(rules, route)
}

func routingMatch(rule model.RoutingRule) (map[string]any, error) {
	if rule.MatchSource == model.RoutingMatchSourceRuleSet {
		if rule.RuleSetID == nil {
			return nil, errors.New("rule_set_id is required")
		}
		return map[string]any{"rule_set": []string{routingRuleSetTag(*rule.RuleSetID)}}, nil
	}
	raw := strings.TrimSpace(rule.MatchJSON)
	if raw == "" {
		raw = "{}"
	}
	if err := ValidateRoutingMatchJSON(raw); err != nil {
		return nil, err
	}
	var match map[string]any
	if err := json.Unmarshal([]byte(raw), &match); err != nil {
		return nil, err
	}
	return match, nil
}

func RequireDNSServerTag(dns map[string]any, wanted string) (string, error) {
	wanted = strings.TrimSpace(wanted)
	for _, server := range dnsServerItems(dns["servers"]) {
		if server["tag"] == wanted {
			return wanted, nil
		}
	}
	return "", markInvalidDesiredState(fmt.Errorf("resolver_not_found: DNS resolver %q is not generated on this server", wanted))
}

func ValidateRoutingDNSOverride(rule model.RoutingRule, sets []model.RoutingRuleSet) error {
	if strings.TrimSpace(rule.DNSResolver) == "" || rule.Action == model.RouteActionBlock {
		return nil
	}
	var match map[string]any
	if rule.MatchSource == model.RoutingMatchSourceRuleSet {
		for _, set := range sets {
			if rule.RuleSetID != nil && set.ID == *rule.RuleSetID {
				var source struct {
					Rules []map[string]any `json:"rules"`
				}
				if set.Revision == "" || json.Unmarshal(set.Content, &source) != nil || len(source.Rules) == 0 {
					break
				}
				for _, item := range source.Rules {
					if err := preResolveMatch(item, 0); err != nil {
						return err
					}
				}
				return nil
			}
		}
		return markInvalidDesiredState(errors.New("dns_override_requires_pre_resolve_match: rule-set snapshot cannot be proven domain-only; use stage DNS policy"))
	}
	var err error
	match, err = routingMatch(rule)
	if err != nil {
		return err
	}
	return preResolveMatch(match, 0)
}

func preResolveMatch(match map[string]any, depth int) error {
	if depth > 16 {
		return errors.New("routing match exceeds maximum nesting depth")
	}
	for key, value := range match {
		switch key {
		case "type", "mode", "invert", "domain", "domain_suffix", "domain_keyword", "domain_regex", "port", "port_range", "source_ip_cidr", "source_ip_is_private", "source_port", "source_port_range", "network", "protocol":
		case "rules":
			children, ok := value.([]any)
			if !ok {
				return errors.New("logical rules must be an array")
			}
			for _, child := range children {
				item, ok := child.(map[string]any)
				if !ok {
					return errors.New("logical rule must be an object")
				}
				if err := preResolveMatch(item, depth+1); err != nil {
					return err
				}
			}
		default:
			return markInvalidDesiredState(fmt.Errorf("dns_override_requires_pre_resolve_match: %s cannot select target DNS before resolution; use stage DNS policy", key))
		}
	}
	return nil
}

func validateProtectedRoutingMatch(match map[string]any, depth int) error {
	if match == nil {
		return errors.New("routing match must be an object")
	}
	if depth > 16 {
		return errors.New("routing match exceeds maximum nesting depth")
	}
	for key, value := range match {
		switch key {
		case "inbound", "auth_user", "action", "outbound", "server", "rule_set", "domain_resolver", "detour", "override_address", "override_port":
			return fmt.Errorf("%s is reserved for Controller routing scope and actions", key)
		case "rules":
			children, ok := value.([]any)
			if !ok || len(children) == 0 {
				return errors.New("logical rules must be a non-empty array")
			}
			for _, child := range children {
				item, ok := child.(map[string]any)
				if !ok {
					return errors.New("logical rule must be an object")
				}
				if err := validateProtectedRoutingMatch(item, depth+1); err != nil {
					return err
				}
			}
		case "port":
			if err := validateRoutingPorts(value); err != nil {
				return err
			}
		case "port_range":
			if err := validateRoutingPortRanges(value); err != nil {
				return err
			}
		}
	}
	return nil
}

func restrictedSSHInboundTags(server model.Server, groups ...[]model.Inbound) []string {
	seen := map[string]bool{}
	var result []string
	for _, group := range groups {
		for _, inbound := range group {
			if inbound.ServerID == server.ID && inbound.Enabled && inbound.Protocol == model.ProtocolSSH {
				name := tag("in", inbound.ID)
				if !seen[name] {
					seen[name] = true
					result = append(result, name)
				}
			}
		}
	}
	sort.Strings(result)
	return result
}

// routePolicyContext carries the per-server inputs every compiled route policy
// needs: the generated DNS servers, the rule-set snapshots, the restricted SSH
// listeners that must only ever receive public answers, and whether the
// active kernel can run the DNS group that enforces that filter.
type routePolicyContext struct {
	server    model.Server
	dns       map[string]any
	sets      []model.RoutingRuleSet
	sshTags   []string
	dnsGroups bool
}

func newRoutePolicyContext(server model.Server, dns map[string]any, sets []model.RoutingRuleSet, inbounds ...[]model.Inbound) *routePolicyContext {
	return &routePolicyContext{server: server, dns: dns, sets: sets, sshTags: restrictedSSHInboundTags(server, inbounds...), dnsGroups: KernelSupportsDNSGroup(server)}
}

func compileRoutePolicies(rule model.RoutingRule, inboundTags, authUsers []string, outbound string, ctx *routePolicyContext) ([]map[string]any, error) {
	policy, err := compileRoutePolicy(rule, inboundTags, authUsers, outbound, ctx.dns, ctx.sets)
	if err != nil {
		return nil, err
	}
	if policy.Resolver == "" || policy.ResolveInSelector {
		return policy.Rules(), nil
	}
	var protectedTags []string
	for _, sshTag := range ctx.sshTags {
		if len(inboundTags) == 0 || stringSliceContains(inboundTags, sshTag) {
			protectedTags = append(protectedTags, sshTag)
		}
	}
	if len(protectedTags) == 0 {
		return policy.Rules(), nil
	}
	// Restricted SSH traffic is re-resolved by the branch resolver, so the
	// answers must pass the same public-address boundary the relay enforces on
	// literal targets. Without the group capability the kernel cannot filter
	// answers, and an unguarded resolve would reopen private-network access.
	if !ctx.dnsGroups {
		return nil, markInvalidDesiredState(fmt.Errorf("kernel_capability_missing: routing rule %s applies DNS %q to restricted SSH traffic on server %s, which requires %s; update Agent/kernel first", rule.Name, rule.DNSResolver, ctx.server.Name, dnsGroupCapability))
	}
	guarded, err := compileRoutePolicy(rule, protectedTags, authUsers, outbound, ctx.dns, ctx.sets)
	if err != nil {
		return nil, err
	}
	guarded.Resolver = restrictedDNSResolver(ctx.dns, policy.Resolver)
	rules := guarded.Rules()
	// A stage whose listeners are all restricted SSH has nothing left for the
	// unfiltered unit; a server-wide rule keeps it for every other listener,
	// and the guarded unit above wins first for SSH because it is ordered
	// before it.
	var remaining []string
	for _, inboundTag := range inboundTags {
		if !stringSliceContains(protectedTags, inboundTag) {
			remaining = append(remaining, inboundTag)
		}
	}
	if len(inboundTags) > 0 && len(remaining) == 0 {
		return rules, nil
	}
	if len(remaining) > 0 {
		if policy, err = compileRoutePolicy(rule, remaining, authUsers, outbound, ctx.dns, ctx.sets); err != nil {
			return nil, err
		}
	}
	return append(rules, policy.Rules()...), nil
}

// restrictedDNSResolver returns the public-answers-only view of a resolver,
// materializing it as a single-member DNS group (or a copy of the resolver's
// own group membership) the first time it is needed.
func restrictedDNSResolver(config map[string]any, resolver string) string {
	guardedTag := "ssh-public-" + resolver
	servers := dnsServerItems(config["servers"])
	for _, server := range servers {
		if server["tag"] == guardedTag {
			return guardedTag
		}
	}
	group := map[string]any{"type": dnsGroupType, "tag": guardedTag, "members": []string{resolver}, "public_answers_only": true}
	for _, server := range servers {
		if server["tag"] == resolver && server["type"] == dnsGroupType {
			for _, key := range []string{"members", "timeout_ms", "max_concurrency"} {
				if value, ok := server[key]; ok {
					group[key] = value
				}
			}
		}
	}
	config["servers"] = append(servers, group)
	return guardedTag
}
