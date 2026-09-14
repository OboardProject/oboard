package confighealth

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

func (c *collector) checkRoutingRules() {
	for _, rule := range c.input.RoutingRules {
		c.checkRoutingRuleReferences(rule)
		c.checkRoutingRuleMatch(rule)
	}
}

// checkRoutingRuleReferences reports every stored pointer whose target is gone.
// A dangling target makes the rule unrepresentable: configuration generation
// for the owning server stops, which takes down the whole server rather than
// just this rule.
func (c *collector) checkRoutingRuleReferences(rule model.RoutingRule) {
	report := func(code, title, detail, severity string, remedy Remedy) {
		c.add(Finding{
			Code:         code,
			Severity:     severity,
			Scope:        ScopeRoutingRule,
			ResourceID:   rule.ID,
			ResourceName: rule.Name,
			ServerID:     rule.ServerID,
			Title:        title,
			Detail:       detail,
			Remedy:       remedy,
		})
	}
	deleteRemedy := Remedy{Kind: RemedyDelete, Summary: "删除这条无法生效的分流规则", Destructive: true}
	disableRemedy := Remedy{Kind: RemedyDisable, Summary: "停用这条分流规则"}

	if rule.ServerID > 0 {
		if _, ok := c.serverByID[rule.ServerID]; !ok {
			report("routing_rule.server.missing", "分流规则所属的服务器已不存在",
				fmt.Sprintf("服务器 %d 已被删除", rule.ServerID), SeverityWarning, deleteRemedy)
		}
	}
	if rule.ProxyPathID != nil && *rule.ProxyPathID > 0 {
		if _, ok := c.pathByID[*rule.ProxyPathID]; !ok {
			report("routing_rule.source_path.missing", "分流规则挂载的链路已不存在",
				fmt.Sprintf("链路 %d 已被删除", *rule.ProxyPathID), SeverityBlocking, deleteRemedy)
		}
	}
	if rule.StageStepID != nil && *rule.StageStepID > 0 {
		if _, ok := c.stepByID[*rule.StageStepID]; !ok {
			report("routing_rule.stage_step.missing", "分流规则挂载的链路节点已不存在",
				fmt.Sprintf("链路节点 %d 已被删除，规则失去了生效位置", *rule.StageStepID), SeverityBlocking, deleteRemedy)
		}
	}
	if rule.RuleSetID != nil && *rule.RuleSetID > 0 && !c.ruleSetIDs[*rule.RuleSetID] {
		report("routing_rule.rule_set.missing", "分流规则引用的规则集已不存在",
			fmt.Sprintf("规则集 %d 已被删除，匹配条件无法展开", *rule.RuleSetID), SeverityBlocking, deleteRemedy)
	}
	if rule.OutboundID != nil && *rule.OutboundID > 0 && !c.outboundIDs[*rule.OutboundID] {
		report("routing_rule.outbound.missing", "分流规则指向的出站已不存在",
			fmt.Sprintf("出站 %d 已被删除", *rule.OutboundID), SeverityBlocking, deleteRemedy)
	}
	if rule.ExternalOutboundID != nil && *rule.ExternalOutboundID > 0 && !c.externalIDs[*rule.ExternalOutboundID] {
		report("routing_rule.external_outbound.missing", "分流规则指向的外部节点已不存在",
			fmt.Sprintf("外部节点 %d 已被删除", *rule.ExternalOutboundID), SeverityBlocking, deleteRemedy)
	}
	if rule.TargetServerID != nil && *rule.TargetServerID > 0 {
		if _, ok := c.serverByID[*rule.TargetServerID]; !ok {
			report("routing_rule.target_server.missing", "分流规则指向的目标服务器已不存在",
				fmt.Sprintf("服务器 %d 已被删除", *rule.TargetServerID), SeverityBlocking, deleteRemedy)
		}
	}
	if rule.TargetProxyPathID != nil && *rule.TargetProxyPathID > 0 {
		target, ok := c.pathByID[*rule.TargetProxyPathID]
		switch {
		case !ok:
			report("routing_rule.target_path.missing", "分流规则指向的链路已不存在",
				fmt.Sprintf("链路 %d 已被删除", *rule.TargetProxyPathID), SeverityBlocking, deleteRemedy)
		case rule.Enabled && !target.Enabled:
			report("routing_rule.target_path.disabled", "分流规则指向的链路已停用",
				fmt.Sprintf("链路「%s」(#%d) 已停用，命中这条规则的流量没有出口", target.Name, target.ID), SeverityWarning, disableRemedy)
		}
	}
	if rule.FamilySplitTemplateID != nil && *rule.FamilySplitTemplateID > 0 && !c.input.FamilySplitTemplateIDs[*rule.FamilySplitTemplateID] {
		report("routing_rule.family_template.missing", "分流规则引用的双栈模板已不存在",
			fmt.Sprintf("模板 %d 已被删除，IPv4/IPv6 分流无法展开", *rule.FamilySplitTemplateID), SeverityBlocking, deleteRemedy)
	}
	if rule.Action == model.RouteActionFamilySplit && (rule.FamilySplitTemplateID == nil || *rule.FamilySplitTemplateID <= 0) {
		report("routing_rule.family_template.unset", "双栈分流规则没有选择模板",
			"动作为 IPv4/IPv6 分流，但没有绑定任何模板", SeverityBlocking, deleteRemedy)
	}
}

// checkRoutingRuleMatch reports a stored match document that can no longer be
// decoded. Generation reads the same bytes, so this is always blocking.
func (c *collector) checkRoutingRuleMatch(rule model.RoutingRule) {
	raw := strings.TrimSpace(rule.MatchJSON)
	if raw == "" {
		return
	}
	var match map[string]any
	if err := json.Unmarshal([]byte(raw), &match); err != nil || match == nil {
		detail := "匹配条件不是一个合法的 JSON 对象"
		if err != nil {
			detail = err.Error()
		}
		c.add(Finding{
			Code:         "routing_rule.match.invalid",
			Severity:     SeverityBlocking,
			Scope:        ScopeRoutingRule,
			ResourceID:   rule.ID,
			ResourceName: rule.Name,
			ServerID:     rule.ServerID,
			Title:        "分流规则的匹配条件无法解析",
			Detail:       detail,
			Path:         "match_json",
			Remedy:       Remedy{Kind: RemedyDelete, Summary: "删除这条无法解析的规则", Destructive: true},
		})
	}
}

// checkDNSPolicies reports a per-server policy that binds a resolver list which
// no longer exists. The bootstrap list is mandatory, so losing it stops DNS
// generation for that server; an encrypted list of zero is the valid
// plain-DNS-only policy and is never a finding.
func (c *collector) checkDNSPolicies() {
	for _, policy := range c.input.ServerDNSPolicies {
		if _, ok := c.serverByID[policy.ServerID]; !ok {
			continue
		}
		if policy.BootstrapListID > 0 {
			if _, ok := c.dnsListByID[policy.BootstrapListID]; !ok {
				c.add(Finding{
					Code:       "dns_policy.bootstrap_list.missing",
					Severity:   SeverityBlocking,
					Scope:      ScopeDNSPolicy,
					ResourceID: policy.ServerID,
					ServerID:   policy.ServerID,
					Title:      "服务器绑定的基础解析列表已不存在",
					Detail:     fmt.Sprintf("解析列表 %d 已被删除，该服务器无法生成 DNS 配置", policy.BootstrapListID),
					Remedy:     Remedy{Kind: RemedyNone, Summary: "请为该服务器重新选择基础解析列表"},
				})
			}
		}
		if policy.EncryptedListID > 0 {
			if _, ok := c.dnsListByID[policy.EncryptedListID]; !ok {
				c.add(Finding{
					Code:       "dns_policy.encrypted_list.missing",
					Severity:   SeverityBlocking,
					Scope:      ScopeDNSPolicy,
					ResourceID: policy.ServerID,
					ServerID:   policy.ServerID,
					Title:      "服务器绑定的加密解析列表已不存在",
					Detail:     fmt.Sprintf("解析列表 %d 已被删除，该服务器无法生成 DNS 配置", policy.EncryptedListID),
					Remedy:     Remedy{Kind: RemedyNone, Summary: "请重新选择加密解析列表，或切换为仅明文解析"},
				})
			}
		}
	}
}
