package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

const (
	PlanRuleEntryInbound     = "entry_inbound"
	PlanRuleEntryServer      = "entry_server"
	PlanRulePathServer       = "path_server"
	PlanRuleExitServer       = "exit_server"
	PlanRuleExitRegion       = "exit_region"
	PlanRuleExternalOutbound = "external_outbound"
)

type PlanMembershipResolution struct {
	Nodes       []model.SubscriptionPlanNode
	MatchedBy   map[string][]int64
	Warnings    []string
	AddedKeys   []string
	RemovedKeys []string
	Digest      string
}

func NormalizePlanMembershipRules(rules []model.PlanMembershipRule) ([]model.PlanMembershipRule, error) {
	out := append([]model.PlanMembershipRule(nil), rules...)
	seen := map[int64]bool{}
	for i := range out {
		out[i].Kind = strings.TrimSpace(out[i].Kind)
		out[i].ScopeKey = strings.TrimSpace(out[i].ScopeKey)
		if out[i].RuleID <= 0 || seen[out[i].RuleID] {
			return nil, fmt.Errorf("rule_id must be positive and unique")
		}
		seen[out[i].RuleID] = true
		switch out[i].Kind {
		case PlanRuleEntryInbound:
			t, id, ok := ParseNodeKey(out[i].ScopeKey)
			if !ok || t != model.AssignableNodeInbound || id <= 0 {
				return nil, fmt.Errorf("invalid entry scope %q", out[i].ScopeKey)
			}
		case PlanRuleEntryServer, PlanRulePathServer, PlanRuleExitServer, PlanRuleExternalOutbound:
			id, err := strconv.ParseInt(out[i].ScopeKey, 10, 64)
			if err != nil || id <= 0 {
				return nil, fmt.Errorf("invalid %s scope %q", out[i].Kind, out[i].ScopeKey)
			}
		case PlanRuleExitRegion:
			out[i].ScopeKey = NormalizeRegionCode(out[i].ScopeKey)
			if out[i].ScopeKey == "" {
				return nil, fmt.Errorf("invalid exit region scope")
			}
		default:
			return nil, fmt.Errorf("invalid membership rule kind %q", out[i].Kind)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RuleID < out[j].RuleID })
	return out, nil
}

func ResolvePlanMembershipRules(base []model.SubscriptionPlanNode, rules []model.PlanMembershipRule, exclusions []model.PlanNodeExclusion, catalog []AssignableNode) (PlanMembershipResolution, error) {
	rules, err := NormalizePlanMembershipRules(rules)
	if err != nil {
		return PlanMembershipResolution{}, err
	}
	baseByKey := map[string]model.SubscriptionPlanNode{}
	explicit := map[string]model.SubscriptionPlanNode{}
	for _, node := range base {
		key := NodeKeyOf(node.NodeType, node.NodeID)
		baseByKey[key] = node
		if node.SourceType != model.PlanNodeSourceRule {
			node.SourceType = model.PlanNodeSourceExplicit
			node.SourceRuleID = 0
			explicit[key] = node
		}
	}
	excluded := map[string]bool{}
	for _, item := range exclusions {
		excluded[NodeKeyOf(item.NodeType, item.NodeID)] = true
	}
	matchedBy := map[string][]int64{}
	matchedRule := map[int64]int{}
	for _, rule := range rules {
		for _, node := range catalog {
			if !node.Enabled || !planMembershipRuleMatches(rule, node) {
				continue
			}
			matchedBy[node.Key] = append(matchedBy[node.Key], rule.RuleID)
			matchedRule[rule.RuleID]++
		}
	}
	resultByKey := map[string]model.SubscriptionPlanNode{}
	for key, node := range explicit {
		if !excluded[key] {
			resultByKey[key] = node
		}
	}
	for _, node := range catalog {
		ids := matchedBy[node.Key]
		if len(ids) == 0 || excluded[node.Key] {
			continue
		}
		if _, ok := resultByKey[node.Key]; ok {
			continue
		}
		pn, ok := baseByKey[node.Key]
		if !ok {
			pn = model.SubscriptionPlanNode{NodeType: node.Type, NodeID: node.ID, Enabled: true}
		}
		pn.SourceType = model.PlanNodeSourceRule
		pn.SourceRuleID = ids[0]
		resultByKey[node.Key] = pn
	}
	out := make([]model.SubscriptionPlanNode, 0, len(resultByKey))
	for _, node := range resultByKey {
		out = append(out, node)
	}
	sort.Slice(out, func(i, j int) bool {
		return NodeKeyOf(out[i].NodeType, out[i].NodeID) < NodeKeyOf(out[j].NodeType, out[j].NodeID)
	})
	baseKeys, resultKeys := map[string]bool{}, map[string]bool{}
	for key := range baseByKey {
		baseKeys[key] = true
	}
	for key := range resultByKey {
		resultKeys[key] = true
	}
	added, removed := []string{}, []string{}
	for key := range resultKeys {
		if !baseKeys[key] {
			added = append(added, key)
		}
	}
	for key := range baseKeys {
		if !resultKeys[key] {
			removed = append(removed, key)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	warnings := []string{}
	for _, rule := range rules {
		if matchedRule[rule.RuleID] == 0 {
			warnings = append(warnings, fmt.Sprintf("规则 %d 当前没有匹配节点", rule.RuleID))
		}
	}
	digestRaw, _ := json.Marshal(map[string]any{"rules": rules, "exclusions": exclusions, "nodes": sortedNodeKeys(out)})
	sum := sha256.Sum256(digestRaw)
	return PlanMembershipResolution{Nodes: out, MatchedBy: matchedBy, Warnings: warnings, AddedKeys: added, RemovedKeys: removed, Digest: hex.EncodeToString(sum[:])}, nil
}

func planMembershipRuleMatches(rule model.PlanMembershipRule, node AssignableNode) bool {
	switch rule.Kind {
	case PlanRuleEntryInbound:
		return node.EntryKey == rule.ScopeKey
	case PlanRuleEntryServer:
		return strconv.FormatInt(node.EntryServerID, 10) == rule.ScopeKey && node.EntryServerID > 0
	case PlanRulePathServer:
		for _, item := range node.PathServers {
			if strconv.FormatInt(item.ServerID, 10) == rule.ScopeKey {
				return true
			}
		}
	case PlanRuleExitServer:
		return strconv.FormatInt(node.ExitServerID, 10) == rule.ScopeKey && node.ExitServerID > 0
	case PlanRuleExitRegion:
		return NormalizeRegionCode(node.ExitRegion) == rule.ScopeKey && rule.ScopeKey != ""
	case PlanRuleExternalOutbound:
		return strconv.FormatInt(node.ExitExternalOutboundID, 10) == rule.ScopeKey && node.ExitExternalOutboundID > 0
	}
	return false
}

func sortedNodeKeys(nodes []model.SubscriptionPlanNode) []string {
	out := make([]string, 0, len(nodes))
	for _, node := range nodes {
		out = append(out, NodeKeyOf(node.NodeType, node.NodeID))
	}
	sort.Strings(out)
	return out
}
