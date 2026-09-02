package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
)

const mcpRecipeVersion = "1"

type mcpRecipe struct {
	ID      string
	Version string
	Aliases []string
	Verbs   []string
	Nouns   []string
	Prepare func(context.Context, application.Principal, mcpTaskInput) (*mcpPreparedRecipe, error)
}

type mcpPreparedRecipe struct {
	Status       string
	Intent       string
	Questions    []map[string]any
	Field        string
	Candidates   []MCPResourceRef
	Operations   []mcpOperationRef
	Summary      map[string]any
	Verification map[string]any
	Fallback     []string
	DirectResult any
}

func (s *Server) mcpRecipes() []mcpRecipe {
	return []mcpRecipe{
		{ID: "server.onboard", Version: mcpRecipeVersion, Aliases: []string{"server.onboard", "add server", "create server", "onboard server", "reissue enrollment", "新增服务器", "添加服务器", "接入服务器", "新增节点服务器", "重签发", "重新签发", "重签发接入令牌"}, Verbs: []string{"add", "create", "onboard", "enroll", "reissue", "新增", "添加", "接入", "重签发", "重新签发"}, Nouns: []string{"server", "agent", "服务器", "节点服务器", "接入令牌"}, Prepare: s.prepareServerOnboardRecipe},
		{ID: "user.manage", Version: mcpRecipeVersion, Aliases: []string{"user.manage", "manage user", "create user", "update user", "delete user", "用户管理", "新建用户", "创建用户", "修改用户", "删除用户"}, Verbs: []string{"create", "add", "update", "change", "delete", "remove", "disable", "enable", "吊销", "创建", "新建", "添加", "修改", "删除", "停用", "启用"}, Nouns: []string{"user", "account", "用户", "账号", "账户"}, Prepare: s.prepareUserManageRecipe},
		{ID: "user.traffic.ledger", Version: mcpRecipeVersion, Aliases: []string{"user.traffic.ledger", "traffic ledger", "user traffic ledger", "流量账本", "用户流量账本", "为什么流量不对", "流量看起来不对", "流量对账"}, Verbs: []string{"view", "read", "query", "show", "check", "diagnose", "查看", "查询", "读取", "检查", "对账"}, Nouns: []string{"traffic ledger", "user traffic", "reconciliation", "流量账本", "用户流量", "对账"}, Prepare: s.prepareUserTrafficLedgerRecipe},
		{ID: "user_group.manage", Version: mcpRecipeVersion, Aliases: []string{"user_group.manage", "manage user group", "user group", "用户分组", "分组管理", "用户组"}, Verbs: []string{"create", "update", "delete", "创建", "新增", "修改", "删除"}, Nouns: []string{"user group", "group", "分组", "用户组", "群组"}, Prepare: s.prepareUserGroupRecipe},
		{ID: "user_device.manage", Version: mcpRecipeVersion, Aliases: []string{"user_device.manage", "manage device", "rename device", "revoke device", "设备管理", "重命名设备", "吊销设备"}, Verbs: []string{"rename", "revoke", "重命名", "吊销", "删除"}, Nouns: []string{"device", "设备"}, Prepare: s.prepareUserDeviceRecipe},
		{ID: "server.manage", Version: mcpRecipeVersion, Aliases: []string{"server.manage", "update server", "server settings", "delete server", "修改服务器", "服务器设置", "删除服务器", "清零已用流量", "已用流量清零", "清零服务器流量"}, Verbs: []string{"update", "change", "set", "modify", "delete", "remove", "reset", "clear", "修改", "设置", "调整", "开启", "关闭", "删除", "清零", "重置"}, Nouns: []string{"server", "服务器", "节点", "已用流量", "used traffic"}, Prepare: s.prepareServerManageRecipe},
		{ID: "server.metrics.query", Version: mcpRecipeVersion, Aliases: []string{"server.metrics.query", "server query", "server metrics", "服务器指标", "看流量", "查看流量", "查询流量", "连接数", "延迟", "负载"}, Verbs: []string{"view", "read", "query", "show", "check", "查看", "看", "查询", "读取", "检查"}, Nouns: []string{"server metrics", "server traffic", "traffic", "latency", "connection count", "resource metrics", "指标", "流量", "延迟", "连接数", "负载", "资源"}, Prepare: s.prepareServerMetricsQueryRecipe},
		{ID: "inbound.create", Version: mcpRecipeVersion, Aliases: []string{"inbound.create", "create inbound", "add inbound", "创建入口", "新增入口", "添加入口", "创建入站", "新增入站"}, Verbs: []string{"create", "add", "新增", "添加", "创建"}, Nouns: []string{"inbound", "入口", "入站"}, Prepare: s.prepareInboundCreateRecipe},
		{ID: "subscription_plan.nodes.manage", Version: mcpRecipeVersion, Aliases: []string{"subscription_plan.nodes.manage", "plan node assignment", "套餐节点", "套餐节点分配", "订阅套餐节点"}, Verbs: []string{"add", "remove", "replace", "assign", "添加", "加入", "移除", "替换", "分配"}, Nouns: []string{"subscription plan", "plan node", "套餐", "套餐节点", "订阅套餐"}, Prepare: s.prepareSubscriptionPlanNodesRecipe},
		{ID: "user_node_authorization.manage", Version: mcpRecipeVersion, Aliases: []string{"user_node_authorization.manage", "node user authorization", "节点用户授权", "授权用户", "用户节点授权"}, Verbs: []string{"allow", "deny", "authorize", "revoke", "remove", "允许", "拒绝", "授权", "撤销", "移除"}, Nouns: []string{"node authorization", "user authorization", "节点授权", "用户授权", "授权用户"}, Prepare: s.prepareUserNodeAuthorizationRecipe},
		{ID: "subscription_plan.delete", Version: mcpRecipeVersion, Aliases: []string{"subscription_plan.delete", "delete subscription plan", "删除套餐", "删除订阅套餐"}, Verbs: []string{"delete", "remove", "删除"}, Nouns: []string{"subscription plan", "套餐", "订阅套餐"}, Prepare: s.prepareSubscriptionPlanDeleteRecipe},
		{ID: "proxy_path.manage", Version: mcpRecipeVersion, Aliases: []string{"proxy_path.manage", "proxy path", "proxy chain", "代理链", "代理路径", "链路", "direct branch"}, Verbs: []string{"create", "add", "connect", "route", "创建", "增加", "连接", "经过", "通过"}, Nouns: []string{"proxy path", "chain", "branch", "代理链", "链路", "路径", "wireguard", "ssh"}, Prepare: s.prepareProxyPathRecipe},
		{ID: "deployment.apply", Version: mcpRecipeVersion, Aliases: []string{"deployment.apply", "deploy all", "apply deployment", "部署全部", "部署所有", "下发修改", "重新应用配置"}, Verbs: []string{"deploy", "apply", "redeploy", "部署", "下发", "应用"}, Nouns: []string{"deployment", "configuration", "changes", "部署", "配置", "修改"}, Prepare: s.prepareDeploymentRecipe},
		{ID: "outbound.manage", Version: mcpRecipeVersion, Aliases: []string{"outbound.manage", "manage outbound", "出口管理", "服务器出口"}, Verbs: []string{"create", "add", "update", "change", "delete", "remove", "创建", "新增", "添加", "修改", "删除"}, Nouns: []string{"outbound", "出口", "下一跳"}, Prepare: s.prepareOutboundRecipe},
		{ID: "routing_rule_set.manage", Version: mcpRecipeVersion, Aliases: []string{"routing_rule_set.manage", "routing rule set", "routing ruleset", "rule set", "分流规则集", "路由规则集", "远程规则集"}, Verbs: []string{"create", "add", "update", "change", "delete", "remove", "refresh", "reload", "创建", "新增", "添加", "修改", "删除", "刷新", "重新拉取"}, Nouns: []string{"routing rule set", "routing ruleset", "rule set", "分流规则集", "路由规则集", "远程规则集"}, Prepare: s.prepareRoutingRuleSetRecipe},
		{ID: "family_split_template.manage", Version: mcpRecipeVersion, Aliases: []string{"family_split_template.manage", "dual stack template", "family split template", "双栈模板", "IPv4/IPv6 模板"}, Verbs: []string{"create", "add", "update", "rename", "delete", "remove", "创建", "新增", "添加", "修改", "重命名", "删除"}, Nouns: []string{"family split template", "dual stack template", "双栈模板"}, Prepare: s.prepareFamilySplitTemplateRecipe},
		{ID: "routing.manage", Version: mcpRecipeVersion, Aliases: []string{"routing.manage", "routing rule", "分流规则", "路由规则"}, Verbs: []string{"create", "add", "update", "change", "delete", "remove", "创建", "新增", "添加", "修改", "删除"}, Nouns: []string{"routing rule", "routing rules", "分流", "分流规则", "路由规则"}, Prepare: s.prepareRoutingRuleRecipe},
		{ID: "external_outbound.import", Version: mcpRecipeVersion, Aliases: []string{"external_outbound.import", "import node", "导入节点", "导入第三方节点"}, Verbs: []string{"import", "导入"}, Nouns: []string{"node", "节点", "external outbound", "导入节点"}, Prepare: s.prepareExternalOutboundImportRecipe},
		{ID: "dns_policy.manage", Version: mcpRecipeVersion, Aliases: []string{"dns_policy.manage", "dns policy", "dns 策略", "DNS 策略"}, Verbs: []string{"set", "update", "change", "test", "设置", "修改", "测试", "检查"}, Nouns: []string{"dns policy", "dns 策略", "dns 设置", "DNS 策略", "解析服务"}, Prepare: s.prepareDNSPolicyRecipe},
		{ID: "dns_record.manage", Version: mcpRecipeVersion, Aliases: []string{"dns_record.manage", "dns record", "解析记录", "dns 记录"}, Verbs: []string{"create", "add", "update", "change", "delete", "upsert", "创建", "新增", "添加", "修改", "删除"}, Nouns: []string{"dns record", "dns records", "解析记录", "dns 记录"}, Prepare: s.prepareDNSRecordRecipe},
		{ID: "port_forward.manage", Version: mcpRecipeVersion, Aliases: []string{"port_forward.manage", "port forward", "端口转发"}, Verbs: []string{"create", "add", "update", "change", "delete", "创建", "新增", "添加", "修改", "删除"}, Nouns: []string{"port forward", "port forwards", "端口转发"}, Prepare: s.preparePortForwardRecipe},
		{ID: "tunnel.manage", Version: mcpRecipeVersion, Aliases: []string{"tunnel.manage", "tunnel", "隧道", "wireguard 隧道", "ssh 隧道"}, Verbs: []string{"create", "add", "update", "change", "delete", "创建", "新增", "添加", "修改", "删除"}, Nouns: []string{"tunnel", "tunnels", "隧道", "wireguard", "ssh 隧道"}, Prepare: s.prepareTunnelRecipe},
		{ID: "host_ops.manage", Version: mcpRecipeVersion, Aliases: []string{"host_ops.manage", "diagnose", "agent update", "uninstall agent", "collect logs", "manage logs", "network interfaces", "诊断", "升级 agent", "升级服务器", "卸载 agent", "拉取日志", "管理日志", "读取网卡"}, Verbs: []string{"diagnose", "update", "uninstall", "collect", "rotate", "clear", "detect", "list", "诊断", "升级", "卸载", "拉取", "轮转", "清空", "检测", "读取", "列出"}, Nouns: []string{"agent", "log", "logs", "network interface", "network interfaces", "日志", "mtu", "网卡", "网络接口"}, Prepare: s.prepareHostOpsRecipe},
		{ID: "controller_update.manage", Version: mcpRecipeVersion, Aliases: []string{"controller_update.manage", "controller update", "controller update channel", "主控更新", "主控升级", "主控更新通道"}, Verbs: []string{"check", "set", "switch", "install", "update", "cancel", "检查", "切换", "安装", "更新", "升级", "取消"}, Nouns: []string{"controller update", "controller channel", "主控更新", "主控升级", "更新通道"}, Prepare: s.prepareControllerUpdateRecipe},
		{ID: "notification.manage", Version: mcpRecipeVersion, Aliases: []string{"notification.manage", "notification channel", "通知频道", "通知设置"}, Verbs: []string{"create", "add", "update", "change", "delete", "test", "创建", "新增", "添加", "修改", "删除", "测试", "发布"}, Nouns: []string{"notification", "通知", "频道", "公告"}, Prepare: s.prepareNotificationRecipe},
		{ID: "certificate.manage", Version: mcpRecipeVersion, Aliases: []string{"certificate.manage", "certificate", "证书", "ssl 证书", "tls 证书"}, Verbs: []string{"issue", "renew", "delete", "签发", "续期", "删除", "申请"}, Nouns: []string{"certificate", "证书"}, Prepare: s.prepareCertificateRecipe},
		{ID: "settings.manage", Version: mcpRecipeVersion, Aliases: []string{"settings.manage", "global settings", "全局设置", "面板设置", "面板路径"}, Verbs: []string{"update", "change", "set", "retry", "revoke", "force", "修改", "设置", "开启", "关闭", "重试", "撤销", "强制"}, Nouns: []string{"global settings", "全局设置", "面板设置", "审计开关", "面板路径", "base path"}, Prepare: s.prepareSettingsRecipe},
	}
}

func (s *Server) mcpRecipeByID(id string) (mcpRecipe, bool) {
	for _, recipe := range s.mcpRecipes() {
		if recipe.ID == strings.TrimSpace(id) {
			return recipe, true
		}
	}
	return mcpRecipe{}, false
}

func (s *Server) matchMCPRecipe(input mcpTaskInput) (mcpRecipe, []MCPResourceRef, bool) {
	if input.Intent != "" {
		recipe, ok := s.mcpRecipeByID(input.Intent)
		return recipe, nil, ok
	}
	if isSubscriptionPlanDeleteGoal(input.Goal) {
		recipe, ok := s.mcpRecipeByID("subscription_plan.delete")
		return recipe, nil, ok
	}
	if recipe, ok := s.matchDistinctiveRecipeGoal(input.Goal); ok {
		return recipe, nil, true
	}
	refTypes := map[string]bool{}
	for _, ref := range input.TargetRefs {
		resourceType, _, found := strings.Cut(strings.ToLower(strings.TrimSpace(ref)), ":")
		if found {
			refTypes[resourceType] = true
		}
	}
	if (refTypes["inbound"] || refTypes["proxy_path"] || refTypes["external_outbound"]) && (refTypes["user"] || hasUserNodeAuthorizationParams(input.Params) || containsAnyFold(input.Goal, "用户授权", "授权用户", "节点授权", "node authorization")) {
		recipe, _ := s.mcpRecipeByID("user_node_authorization.manage")
		return recipe, nil, true
	}
	if refTypes["inbound"] || refTypes["proxy_path"] || refTypes["external_outbound"] {
		if refTypes["subscription_plan"] || hasSubscriptionPlanParams(input.Params) || containsAnyFold(input.Goal, "套餐", "subscription plan", "plan node") {
			recipe, _ := s.mcpRecipeByID("subscription_plan.nodes.manage")
			return recipe, nil, true
		}
		recipe, _ := s.mcpRecipeByID("proxy_path.manage")
		return recipe, nil, true
	}
	if refTypes["subscription_plan"] {
		recipe, _ := s.mcpRecipeByID("subscription_plan.nodes.manage")
		return recipe, nil, true
	}
	if refTypes["server"] && hasInboundCreateParams(input.Params) {
		recipe, _ := s.mcpRecipeByID("inbound.create")
		return recipe, nil, true
	}
	if refTypes["server"] && hasServerManageParams(input.Params) {
		recipe, _ := s.mcpRecipeByID("server.manage")
		return recipe, nil, true
	}
	goal := strings.ToLower(strings.TrimSpace(input.Goal))
	if containsAnyFold(goal, "add", "create", "onboard", "enroll", "新增", "添加", "接入") && containsAnyFold(goal, "server", "agent", "服务器", "节点服务器") {
		recipe, _ := s.mcpRecipeByID("server.onboard")
		return recipe, nil, true
	}
	type scored struct {
		recipe mcpRecipe
		score  int
	}
	scores := []scored{}
	for _, recipe := range s.mcpRecipes() {
		score := 0
		for _, alias := range recipe.Aliases {
			if strings.Contains(goal, strings.ToLower(alias)) {
				score += 6
			}
		}
		verb, noun := false, false
		for _, value := range recipe.Verbs {
			verb = verb || strings.Contains(goal, strings.ToLower(value))
		}
		for _, value := range recipe.Nouns {
			noun = noun || strings.Contains(goal, strings.ToLower(value))
		}
		if verb {
			score += 2
		}
		if noun {
			score += 2
		}
		if verb && noun {
			score += 2
		}
		if score > 0 {
			scores = append(scores, scored{recipe, score})
		}
	}
	if len(scores) == 0 {
		if refTypes["server"] {
			return mcpRecipe{}, []MCPResourceRef{
				{Type: "recipe", Name: "deployment.apply", Ref: "deployment.apply", Label: "deployment.apply"},
				{Type: "recipe", Name: "server.manage", Ref: "server.manage", Label: "server.manage"},
			}, false
		}
		return mcpRecipe{}, nil, false
	}
	sort.Slice(scores, func(i, j int) bool {
		if scores[i].score == scores[j].score {
			return scores[i].recipe.ID < scores[j].recipe.ID
		}
		return scores[i].score > scores[j].score
	})
	if len(scores) > 1 && scores[0].score == scores[1].score {
		candidates := []MCPResourceRef{}
		for _, item := range scores {
			if item.score != scores[0].score {
				break
			}
			candidates = append(candidates, MCPResourceRef{Type: "recipe", Name: item.recipe.ID, Ref: item.recipe.ID, Label: item.recipe.ID})
		}
		return mcpRecipe{}, candidates, false
	}
	return scores[0].recipe, nil, true
}

// matchDistinctiveRecipeGoal routes goals that carry a uniquely distinctive
// domain noun before generic param-based checks can hijack them. It returns the
// bound recipe, including its Prepare function, rather than an ID-only stub.
func (s *Server) matchDistinctiveRecipeGoal(goal string) (mcpRecipe, bool) {
	distinctive := []struct {
		recipeID string
		tokens   []string
	}{
		{"user.traffic.ledger", []string{"流量账本", "用户流量账本", "为什么流量不对", "流量看起来不对", "流量对账", "traffic ledger", "user traffic ledger"}},
		{"server.manage", []string{"清零已用流量", "已用流量清零", "清零服务器流量", "清零这台服务器的流量", "重置服务器已用流量", "清零流量", "reset used traffic", "reset server traffic", "zero used traffic", "clear used traffic", "clear server traffic"}},
		{"routing_rule_set.manage", []string{"分流规则集", "路由规则集", "远程规则集", "routing rule set", "routing ruleset", "rule set", "规则集"}},
		{"family_split_template.manage", []string{"双栈模板", "family split template", "dual stack template", "IPv4/IPv6 模板"}},
		{"routing.manage", []string{"分流", "routing rule", "routing rules", "路由规则"}},
		{"external_outbound.import", []string{"导入节点", "import node", "导入第三方节点"}},
		{"dns_record.manage", []string{"解析记录", "dns record", "dns records", "dns 记录"}},
		{"dns_policy.manage", []string{"dns 策略", "dns policy", "解析服务"}},
		{"port_forward.manage", []string{"端口转发", "port forward", "port forwards"}},
		{"tunnel.manage", []string{"隧道", "tunnel"}},
		{"certificate.manage", []string{"证书", "certificate"}},
		{"notification.manage", []string{"通知频道", "notification channel", "公告"}},
		{"settings.manage", []string{"全局设置", "global settings", "面板设置", "面板路径", "base path"}},
		{"controller_update.manage", []string{"主控更新", "主控升级", "主控更新通道", "controller update", "controller channel"}},
		{"host_ops.manage", []string{"诊断", "diagnose", "日志", "logs", "升级 agent", "卸载 agent", "uninstall agent", "拉取", "网卡", "网络接口", "network interface"}},
		{"outbound.manage", []string{"出口", "outbound"}},
		{"subscription_plan.nodes.manage", []string{"套餐节点", "订阅套餐节点", "plan node", "plan nodes"}},
		{"user_node_authorization.manage", []string{"节点用户授权", "用户节点授权", "授权用户", "用户授权", "node user authorization", "node authorization"}},
	}
	type scored struct {
		recipe mcpRecipe
		score  int
	}
	scores := []scored{}
	for _, item := range distinctive {
		recipe, ok := s.mcpRecipeByID(item.recipeID)
		if !ok {
			continue
		}
		score := 0
		for _, token := range item.tokens {
			if containsAnyFold(goal, token) {
				score += 6
			}
		}
		if score > 0 {
			scores = append(scores, scored{recipe, score})
		}
	}
	if len(scores) == 0 {
		return mcpRecipe{}, false
	}
	sort.Slice(scores, func(i, j int) bool { return scores[i].score > scores[j].score })
	if len(scores) > 1 && scores[0].score == scores[1].score {
		return mcpRecipe{}, false
	}
	return scores[0].recipe, true
}

func hasInboundCreateParams(params map[string]any) bool {
	for _, key := range []string{"inbound", "protocol", "inbound.protocol", "port", "inbound.port", "config_json", "inbound.config_json"} {
		if _, ok := params[key]; ok {
			return true
		}
	}
	return false
}

func hasServerManageParams(params map[string]any) bool {
	for _, key := range []string{
		"changes", "name", "server.name", "ip_stack", "server.ip_stack", "listen_ip", "server.listen_ip", "listen_mode", "udp_inbound_mode",
		"mtu_mode", "mtu_value", "mtu_probe_host", "server.mtu_probe_host", "mtu_probe_port", "server.mtu_probe_port", "mtu_overhead_bytes", "server.mtu_overhead_bytes", "bbr_enabled", "server.bbr_enabled", "time_correction_mode", "server.time_correction_mode", "entry_address", "server.entry_address", "entry_ip_mode", "server.entry_ip_mode",
		"region_code", "server.region_code", "region_mode", "server.region_mode", "port_range_start", "server.port_range_start", "port_range_end", "server.port_range_end", "internal_port_range_start", "server.internal_port_range_start",
		"internal_port_range_end", "server.internal_port_range_end", "connection_audit_enabled", "server.connection_audit_enabled", "resource_history_enabled", "server.resource_history_enabled",
		"offline_notify_enabled", "server.offline_notify_enabled", "offline_after_seconds", "server.offline_after_seconds", "expires_at", "server.expires_at", "clear_expires_at", "server.clear_expires_at",
		"auto_renew_enabled", "server.auto_renew_enabled", "renewal_cycle", "server.renewal_cycle", "expiry_notify_enabled", "server.expiry_notify_enabled",
		"latency_probe_enabled", "latency_probe_mode", "latency_probe_public_target", "latency_probe_interval_seconds", "latency_probe_sample_count", "latency_probe_regions", "latency_probe_max_targets",
		"delete", "confirm", "reset_traffic",
	} {
		if _, ok := params[key]; ok {
			return true
		}
	}
	return false
}

func hasUserNodeAuthorizationParams(params map[string]any) bool {
	for _, key := range []string{"user_id", "user_ids", "users", "effect", "revoke", "authorization_ids"} {
		if _, ok := params[key]; ok {
			return true
		}
	}
	return false
}

func hasSubscriptionPlanParams(params map[string]any) bool {
	for _, key := range []string{"subscription_plan", "target_plan", "plan", "plan_id", "nodes", "display_group"} {
		if _, ok := params[key]; ok {
			return true
		}
	}
	return false
}

func isSubscriptionPlanDeleteGoal(goal string) bool {
	if containsAnyFold(goal, "节点", "node", "nodes") {
		return false
	}
	return containsAnyFold(goal, "删除套餐", "删除订阅套餐", "delete subscription plan", "remove subscription plan")
}

func (s *Server) prepareServerOnboardRecipe(ctx context.Context, principal application.Principal, input mcpTaskInput) (*mcpPreparedRecipe, error) {
	name := taskStringParam(input.Params, "server.name", "name")
	if name == "" {
		name = inferredServerName(input.Goal)
	}
	if name == "" {
		return &mcpPreparedRecipe{Status: "needs_input", Intent: "server.onboard", Questions: []map[string]any{{"field": "server.name", "type": "string", "reason": "服务器名称尚未指定。请提供唯一名称，例如 SJC；同名已存在时将询问是否重签发接入令牌"}}}, nil
	}
	existing, err := s.authorizedServersNamed(ctx, principal, name)
	if err != nil {
		return nil, err
	}
	if len(existing) > 0 {
		return s.prepareExistingServerEnrollmentRecipe(ctx, principal, input, existing)
	}
	ipStack := taskStringParam(input.Params, "server.ip_stack", "ip_stack")
	if ipStack == "" {
		ipStack = inferredIPStack(input.Goal)
	}
	if ipStack == "" {
		ipStack = string(model.IPStackAuto)
	}
	region := taskStringParam(input.Params, "server.region_code", "region_code")
	if region == "" {
		region = inferredRegionCode(input.Goal)
	}
	server := map[string]any{"name": name, "ip_stack": ipStack}
	onboardMapping := map[string]string{
		"name":                                  "name",
		"server.name":                           "name",
		"region_code":                           "region_code",
		"server.region_code":                    "region_code",
		"region_mode":                           "region_mode",
		"server.region_mode":                    "region_mode",
		"ip_stack":                              "ip_stack",
		"server.ip_stack":                       "ip_stack",
		"listen_ip":                             "listen_ip",
		"server.listen_ip":                      "listen_ip",
		"listen_mode":                           "listen_mode",
		"server.listen_mode":                    "listen_mode",
		"entry_address":                         "entry_address",
		"server.entry_address":                  "entry_address",
		"entry_ip_mode":                         "entry_ip_mode",
		"server.entry_ip_mode":                  "entry_ip_mode",
		"port_range_start":                      "port_range_start",
		"server.port_range_start":               "port_range_start",
		"port_range_end":                        "port_range_end",
		"server.port_range_end":                 "port_range_end",
		"internal_port_range_start":             "internal_port_range_start",
		"server.internal_port_range_start":      "internal_port_range_start",
		"internal_port_range_end":               "internal_port_range_end",
		"server.internal_port_range_end":        "internal_port_range_end",
		"udp_inbound_mode":                      "udp_inbound_mode",
		"server.udp_inbound_mode":               "udp_inbound_mode",
		"mtu_mode":                              "mtu_mode",
		"server.mtu_mode":                       "mtu_mode",
		"mtu_value":                             "mtu_value",
		"server.mtu_value":                      "mtu_value",
		"mtu_probe_host":                        "mtu_probe_host",
		"server.mtu_probe_host":                 "mtu_probe_host",
		"mtu_probe_port":                        "mtu_probe_port",
		"server.mtu_probe_port":                 "mtu_probe_port",
		"mtu_overhead_bytes":                    "mtu_overhead_bytes",
		"server.mtu_overhead_bytes":             "mtu_overhead_bytes",
		"bbr_enabled":                           "bbr_enabled",
		"server.bbr_enabled":                    "bbr_enabled",
		"connection_audit_enabled":              "connection_audit_enabled",
		"server.connection_audit_enabled":       "connection_audit_enabled",
		"time_correction_mode":                  "time_correction_mode",
		"server.time_correction_mode":           "time_correction_mode",
		"offline_notify_enabled":                "offline_notify_enabled",
		"server.offline_notify_enabled":         "offline_notify_enabled",
		"offline_after_seconds":                 "offline_after_seconds",
		"server.offline_after_seconds":          "offline_after_seconds",
		"expires_at":                            "expires_at",
		"server.expires_at":                     "expires_at",
		"auto_renew_enabled":                    "auto_renew_enabled",
		"server.auto_renew_enabled":             "auto_renew_enabled",
		"renewal_cycle":                         "renewal_cycle",
		"server.renewal_cycle":                  "renewal_cycle",
		"expiry_notify_enabled":                 "expiry_notify_enabled",
		"server.expiry_notify_enabled":          "expiry_notify_enabled",
		"server.resource_history_enabled":       "resource_history_enabled",
		"resource_history_enabled":              "resource_history_enabled",
		"server.latency_probe_enabled":          "latency_probe_enabled",
		"latency_probe_enabled":                 "latency_probe_enabled",
		"server.latency_probe_mode":             "latency_probe_mode",
		"latency_probe_mode":                    "latency_probe_mode",
		"server.latency_probe_public_target":    "latency_probe_public_target",
		"latency_probe_public_target":           "latency_probe_public_target",
		"server.latency_probe_interval_seconds": "latency_probe_interval_seconds",
		"latency_probe_interval_seconds":        "latency_probe_interval_seconds",
		"server.latency_probe_sample_count":     "latency_probe_sample_count",
		"latency_probe_sample_count":            "latency_probe_sample_count",
		"server.latency_probe_regions":          "latency_probe_regions",
		"latency_probe_regions":                 "latency_probe_regions",
		"server.latency_probe_max_targets":      "latency_probe_max_targets",
		"latency_probe_max_targets":             "latency_probe_max_targets",
		"monitoring_mode":                       "monitoring_mode",
		"server.monitoring_mode":                "monitoring_mode",
		"service_start_at":                      "service_start_at",
		"server.service_start_at":               "service_start_at",
		"traffic_reset_mode":                    "traffic_reset_mode",
		"server.traffic_reset_mode":             "traffic_reset_mode",
		"traffic_reset_day":                     "traffic_reset_day",
		"server.traffic_reset_day":              "traffic_reset_day",
		"traffic_limit_bytes":                   "traffic_limit_bytes",
		"server.traffic_limit_bytes":            "traffic_limit_bytes",
		"traffic_used_bytes":                    "traffic_used_bytes",
		"server.traffic_used_bytes":             "traffic_used_bytes",
		"display_tags":                          "display_tags",
		"server.display_tags":                   "display_tags",
	}
	copyTaskParams(server, input.Params, onboardMapping)
	if nested, ok := input.Params["server"].(map[string]any); ok {
		copyTaskParams(server, nested, onboardMapping)
	}
	if region != "" {
		server["region_code"] = region
	}
	if containsAnyFold(input.Goal, "关闭 bbr", "禁用 bbr", "不启用 bbr", "disable bbr", "without bbr", "no bbr") {
		server["bbr_enabled"] = false
	} else if containsAnyFold(input.Goal, "开启 bbr", "打开 bbr", "启用 bbr", "enable bbr", "with bbr") {
		server["bbr_enabled"] = true
	}
	defaults, err := s.panelServerFormDefaults(ctx)
	if err != nil {
		return nil, err
	}
	fillServerMapDefaults(server, defaults)
	issueToken := taskBoolParam(input.Params, defaults.IssueEnrollmentToken, "issue_enrollment_token")
	bbrEnabled, _ := server["bbr_enabled"].(bool)
	operation := mcpOperationRef{Capability: "servers.onboard", Input: map[string]any{"server": server, "issue_enrollment_token": issueToken}}
	return &mcpPreparedRecipe{Status: "ready", Intent: "server.onboard", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "onboard_server", "server_name": name, "region_code": region, "ip_stack": ipStack, "bbr_enabled": bbrEnabled, "requires_external_install": true}, Verification: map[string]any{"after_commit": []string{"external_action_redeemed", "agent_connected", "workflow_terminal"}}}, nil
}

func (s *Server) prepareExistingServerEnrollmentRecipe(ctx context.Context, principal application.Principal, input mcpTaskInput, existing []model.Server) (*mcpPreparedRecipe, error) {
	candidates := make([]MCPResourceRef, 0, len(existing))
	labels := make([]string, 0, len(existing))
	for _, item := range existing {
		ref := serverMCPResourceRef(item)
		ref.Label = serverDisplayLabel(item)
		candidates = append(candidates, ref)
		labels = append(labels, ref.Label)
	}
	target := firstTaskRef(input, "server", "target_server", "server")
	if target != "" {
		resolved, err := s.resolveServerRef(ctx, principal, target)
		if err != nil {
			return nil, fmt.Errorf("target server: %w", err)
		}
		if len(resolved.Candidates) > 0 {
			return &mcpPreparedRecipe{Status: "choose_candidate", Intent: "server.onboard", Field: "target_server", Candidates: resolved.Candidates}, nil
		}
		return s.enrollmentIssueRecipe(resolved.Value.ID, resolved.Value.Name), nil
	}
	if taskBoolParam(input.Params, false, "confirm_reissue") && len(existing) == 1 {
		return s.enrollmentIssueRecipe(existing[0].ID, existing[0].Name), nil
	}
	if len(existing) > 1 {
		return &mcpPreparedRecipe{Status: "choose_candidate", Intent: "server.onboard", Field: "target_server", Candidates: candidates}, nil
	}
	return &mcpPreparedRecipe{Status: "needs_input", Intent: "server.onboard", Questions: []map[string]any{{"field": "confirm_reissue", "type": "boolean", "reason": fmt.Sprintf("已存在 %s，是否为该记录重签发接入令牌？确认后不会创建新服务器", strings.Join(labels, ", "))}}}, nil
}

func (s *Server) enrollmentIssueRecipe(id int64, name string) *mcpPreparedRecipe {
	label := fmt.Sprintf("%s#%d", name, id)
	operation := mcpOperationRef{Capability: "servers.enrollment.issue", Input: map[string]any{"server_id": id}}
	return &mcpPreparedRecipe{
		Status: "ready", Intent: "server.onboard", Operations: []mcpOperationRef{operation},
		Summary:      map[string]any{"action": "reissue_enrollment", "server_id": id, "server_name": name, "server": label, "requires_external_install": true},
		Verification: map[string]any{"after_commit": []string{"external_action_redeemed", "agent_connected", "workflow_terminal"}},
	}
}

func (s *Server) prepareServerManageRecipe(ctx context.Context, principal application.Principal, input mcpTaskInput) (*mcpPreparedRecipe, error) {
	deleting := containsAnyFold(input.Goal, "删除服务器", "delete server", "remove server", "archive server", "删除这台服务器", "删除节点服务器") || taskBoolParam(input.Params, false, "delete")
	target := firstTaskRef(input, "server", "target_server", "server")
	if target == "" {
		matches := s.inferServerCandidatesFromGoal(ctx, principal, input.Goal, 0)
		if len(matches) > 1 {
			return &mcpPreparedRecipe{Status: "choose_candidate", Intent: "server.manage", Field: "target_server", Candidates: matches}, nil
		}
		if len(matches) == 1 {
			target = matches[0].Ref
		}
	}
	if target == "" {
		reason := "需要指定要修改的服务器"
		if deleting {
			reason = "需要指定要删除的服务器"
		}
		return &mcpPreparedRecipe{Status: "needs_input", Intent: "server.manage", Questions: []map[string]any{{"field": "target_server", "type": "resource_ref", "reason": reason}}}, nil
	}
	resolved, err := s.resolveServerRef(ctx, principal, target)
	if err != nil {
		return nil, fmt.Errorf("target server: %w", err)
	}
	if len(resolved.Candidates) > 0 {
		return &mcpPreparedRecipe{Status: "choose_candidate", Intent: "server.manage", Field: "target_server", Candidates: resolved.Candidates}, nil
	}
	if deleting {
		if !taskBoolParam(input.Params, false, "confirm") {
			return &mcpPreparedRecipe{Status: "needs_input", Intent: "server.manage", Questions: []map[string]any{{"field": "confirm", "type": "boolean", "reason": fmt.Sprintf("将删除 %s 及其关联入口、路径与遥测。确认后不可恢复", serverDisplayLabel(model.Server{ID: resolved.Value.ID, Name: resolved.Value.Name}))}}}, nil
		}
		operation := mcpOperationRef{Capability: "servers.delete", Input: map[string]any{"server_id": resolved.Value.ID, "confirm": true}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "server.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "delete_server", "server": resolved.Value.Label, "server_ref": resolved.Value.Ref, "server_id": resolved.Value.ID}, Verification: map[string]any{"after_commit": []string{"workflow_terminal", "server_absent"}}}, nil
	}
	if serverTrafficResetRequested(input) {
		if containsAnyFold(input.Goal, "用户流量", "user traffic", "用户账本", "流量账本") {
			return &mcpPreparedRecipe{Status: "needs_input", Intent: "server.manage", Questions: []map[string]any{{"field": "reset_traffic", "type": "boolean", "reason": "用户流量账本不能改写。若要清零服务器当前周期面板统计，请确认目标服务器并设置 reset_traffic=true"}}}, nil
		}
		operation := mcpOperationRef{Capability: "servers.reset_traffic", Input: map[string]any{"server_id": resolved.Value.ID}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "server.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "reset_server_traffic", "server": resolved.Value.Label, "server_ref": resolved.Value.Ref, "server_id": resolved.Value.ID}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
	}
	changes := map[string]any{}
	if nested, ok := input.Params["changes"].(map[string]any); ok {
		for key, value := range nested {
			changes[key] = value
		}
	}
	copyTaskParams(changes, input.Params, map[string]string{"name": "name", "server.name": "name", "ip_stack": "ip_stack", "server.ip_stack": "ip_stack", "listen_ip": "listen_ip", "server.listen_ip": "listen_ip", "listen_mode": "listen_mode", "server.listen_mode": "listen_mode", "udp_inbound_mode": "udp_inbound_mode", "mtu_mode": "mtu_mode", "mtu_value": "mtu_value", "mtu_probe_host": "mtu_probe_host", "server.mtu_probe_host": "mtu_probe_host", "mtu_probe_port": "mtu_probe_port", "server.mtu_probe_port": "mtu_probe_port", "mtu_overhead_bytes": "mtu_overhead_bytes", "server.mtu_overhead_bytes": "mtu_overhead_bytes", "bbr_enabled": "bbr_enabled", "server.bbr_enabled": "bbr_enabled", "time_correction_mode": "time_correction_mode", "server.time_correction_mode": "time_correction_mode", "entry_address": "entry_address", "server.entry_address": "entry_address", "entry_ip_mode": "entry_ip_mode", "server.entry_ip_mode": "entry_ip_mode", "region_code": "region_code", "server.region_code": "region_code", "region_mode": "region_mode", "server.region_mode": "region_mode", "port_range_start": "port_range_start", "server.port_range_start": "port_range_start", "port_range_end": "port_range_end", "server.port_range_end": "port_range_end", "internal_port_range_start": "internal_port_range_start", "server.internal_port_range_start": "internal_port_range_start", "internal_port_range_end": "internal_port_range_end", "server.internal_port_range_end": "internal_port_range_end", "connection_audit_enabled": "connection_audit_enabled", "server.connection_audit_enabled": "connection_audit_enabled", "resource_history_enabled": "resource_history_enabled", "server.resource_history_enabled": "resource_history_enabled", "offline_notify_enabled": "offline_notify_enabled", "server.offline_notify_enabled": "offline_notify_enabled", "offline_after_seconds": "offline_after_seconds", "server.offline_after_seconds": "offline_after_seconds", "expires_at": "expires_at", "server.expires_at": "expires_at", "clear_expires_at": "clear_expires_at", "server.clear_expires_at": "clear_expires_at", "auto_renew_enabled": "auto_renew_enabled", "server.auto_renew_enabled": "auto_renew_enabled", "renewal_cycle": "renewal_cycle", "server.renewal_cycle": "renewal_cycle", "expiry_notify_enabled": "expiry_notify_enabled", "server.expiry_notify_enabled": "expiry_notify_enabled", "latency_probe_enabled": "latency_probe_enabled", "latency_probe_mode": "latency_probe_mode", "latency_probe_public_target": "latency_probe_public_target", "latency_probe_interval_seconds": "latency_probe_interval_seconds", "latency_probe_sample_count": "latency_probe_sample_count", "latency_probe_regions": "latency_probe_regions", "latency_probe_max_targets": "latency_probe_max_targets", "display_tags": "display_tags", "server.display_tags": "display_tags"})
	if _, ok := changes["ip_stack"]; !ok {
		if value := inferredIPStack(input.Goal); value != "" {
			changes["ip_stack"] = value
		}
	}
	if containsAnyFold(input.Goal, "开启 bbr", "打开 bbr", "enable bbr") {
		changes["bbr_enabled"] = true
	}
	if containsAnyFold(input.Goal, "关闭 bbr", "disable bbr") {
		changes["bbr_enabled"] = false
	}
	if len(changes) == 0 {
		return &mcpPreparedRecipe{Status: "needs_input", Intent: "server.manage", Questions: []map[string]any{{"field": "changes", "type": "object", "reason": "未识别到要修改的服务器设置"}}}, nil
	}
	operation := mcpOperationRef{Capability: "servers.update", Input: map[string]any{"server_id": resolved.Value.ID, "changes": changes}}
	return &mcpPreparedRecipe{Status: "ready", Intent: "server.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "update_server", "server": resolved.Value.Label, "server_ref": resolved.Value.Ref, "changes": changes}, Verification: map[string]any{"after_commit": []string{"workflow_terminal", "server_revision_changed"}}}, nil
}

func (s *Server) prepareInboundCreateRecipe(ctx context.Context, principal application.Principal, input mcpTaskInput) (*mcpPreparedRecipe, error) {
	target := firstTaskRef(input, "server", "target_server", "server")
	if target == "" {
		matches := s.inferServerCandidatesFromGoal(ctx, principal, input.Goal, 0)
		if len(matches) > 1 {
			return &mcpPreparedRecipe{Status: "choose_candidate", Intent: "inbound.create", Field: "target_server", Candidates: matches}, nil
		}
		if len(matches) == 1 {
			target = matches[0].Ref
		}
	}
	if target == "" {
		return &mcpPreparedRecipe{Status: "needs_input", Intent: "inbound.create", Questions: []map[string]any{{"field": "target_server", "type": "resource_ref", "reason": "需要指定入口所在的服务器"}}}, nil
	}
	resolved, err := s.resolveServerRef(ctx, principal, target)
	if err != nil {
		return nil, fmt.Errorf("target server: %w", err)
	}
	if len(resolved.Candidates) > 0 {
		return &mcpPreparedRecipe{Status: "choose_candidate", Intent: "inbound.create", Field: "target_server", Candidates: resolved.Candidates}, nil
	}
	values := map[string]any{}
	if nested, ok := input.Params["inbound"].(map[string]any); ok {
		for key, value := range nested {
			values[key] = value
		}
	}
	copyTaskParams(values, input.Params, map[string]string{ // #nosec G101 -- this map contains parameter names only; credential values come from validated task input.
		"name": "name", "inbound.name": "name", "kind": "kind", "inbound.kind": "kind", "protocol": "protocol", "inbound.protocol": "protocol",
		"port": "port", "inbound.port": "port", "listen_ip": "listen_ip", "inbound.listen_ip": "listen_ip",
		"entry_ip_mode": "entry_ip_mode", "external_ip": "external_ip", "dns_sync_enabled": "dns_sync_enabled",
		"dns_credential_id": "dns_credential_id", "dns_domain": "dns_domain", "dns_proxy_enabled": "dns_proxy_enabled",
		"dns_record_types": "dns_record_types", "ddns_enabled": "ddns_enabled", "ddns_interval_seconds": "ddns_interval_seconds",
		"tls": "tls", "certificate_mode": "certificate_mode", "certificate_id": "certificate_id",
		"certificate_domain": "certificate_domain", "config_json": "config_json", "inbound.config_json": "config_json",
		"reality": "reality", "rotate_reality_key": "rotate_reality_key", "enabled": "enabled",
	})
	kind := strings.ToLower(strings.TrimSpace(fmt.Sprint(values["kind"])))
	protocol := strings.ToLower(strings.TrimSpace(fmt.Sprint(values["protocol"])))
	if (protocol == "" || protocol == "<nil>") && kind != "" && kind != "<nil>" {
		if inferred, ok := inboundKindProtocols[kind]; ok {
			protocol = string(inferred)
		}
	}
	if protocol == "" || protocol == "<nil>" {
		protocol = inferredInboundProtocol(input.Goal)
	}
	if protocol == "" {
		return &mcpPreparedRecipe{Status: "needs_input", Intent: "inbound.create", Questions: []map[string]any{{"field": "protocol", "type": "string", "reason": "需要指定入口协议"}}}, nil
	}
	port := taskIntParam(values, "port")
	if port <= 0 {
		return &mcpPreparedRecipe{Status: "needs_input", Intent: "inbound.create", Questions: []map[string]any{{"field": "port", "type": "integer", "reason": "需要指定入口监听端口"}}}, nil
	}
	name := strings.TrimSpace(fmt.Sprint(values["name"]))
	if name == "" || name == "<nil>" {
		name = strings.ToUpper(protocol)
	}
	values["server_id"] = resolved.Value.ID
	values["name"] = name
	values["protocol"] = protocol
	if kind == "" || kind == "<nil>" {
		kind = defaultInboundKind(protocol)
	}
	values["kind"] = kind
	values["port"] = port
	if _, exists := values["listen_ip"]; !exists {
		values["listen_ip"] = "0.0.0.0"
	}
	if _, exists := values["config_json"]; !exists {
		if kind == "vless-reality" {
			values["config_json"] = `{}`
			if _, exists := values["reality"]; !exists {
				values["reality"] = map[string]any{"handshake_server": defaultVLESSRealityServerName, "handshake_port": 443}
			}
		} else {
			values["config_json"] = defaultInboundKindConfig(kind, protocol)
		}
	}
	if _, exists := values["certificate_mode"]; !exists {
		// VLESS Reality carries its own TLS and must use an external
		// certificate mode; a managed certificate would require a valid SNI
		// domain and blocks Reality creation. Match the panel's vless-reality
		// preset (tls=false, certificate_mode=external).
		if kind == "vless-reality" {
			values["certificate_mode"] = "external"
			if _, exists := values["tls"]; !exists {
				values["tls"] = false
			}
		} else if inboundKindUsesManagedCertificate(kind) {
			values["certificate_mode"] = "auto"
			if _, exists := values["tls"]; !exists {
				values["tls"] = true
			}
		}
	}
	if _, exists := values["enabled"]; !exists {
		values["enabled"] = true
	}
	if blocked, err := s.applyInboundRecipeManagedCertificate(ctx, input, values, resolved.Value.ID); err != nil {
		return nil, err
	} else if blocked != nil {
		return blocked, nil
	}
	operation := mcpOperationRef{Capability: "inbounds.create", Input: map[string]any{"inbound": values}}
	return &mcpPreparedRecipe{
		Status: "ready", Intent: "inbound.create", Operations: []mcpOperationRef{operation},
		Summary:      map[string]any{"action": "create_inbound", "server": resolved.Value.Label, "server_ref": resolved.Value.Ref, "name": name, "protocol": protocol, "port": port},
		Verification: map[string]any{"after_commit": []string{"workflow_terminal", "inbound_present", "deployment_required"}},
	}, nil
}

// defaultInboundPresetConfig mirrors the panel's default inbound presets
// (vless-reality, hy2-tls / hy2-salamander, anytls-basic balanced padding, ss-2022-128,
// mieru-basic, ssh-restricted). Credentials, Reality keypairs, and Salamander
// obfs passwords are left empty so the Controller generates them on save,
// exactly like the panel flow.
func defaultInboundPresetConfig(protocol string) string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "vless":
		return `{"flow":"xtls-rprx-vision","tls":{"enabled":true,"server_name":"gateway.icloud.com","reality":{"enabled":true,"handshake":{"server":"gateway.icloud.com","server_port":443}}}}`
	case "hysteria2", "hy2":
		return `{"tls":{"enabled":true},"up_mbps":1000,"down_mbps":500}`
	case "anytls":
		encoded, _ := json.Marshal(map[string]any{"tls": map[string]any{"enabled": true}})
		return string(encoded)
	case "shadowsocks":
		return `{"method":"2022-blake3-aes-128-gcm"}`
	case "mieru":
		return `{"transport":"TCP","multiplexing":"MULTIPLEXING_DEFAULT","user_hint_is_mandatory":true}`
	case "ssh":
		return `{"access_mode":"restricted_proxy","exposure_confirmed":false,"exposure_confirmation_version":"ssh-inbound-v1"}`
	default:
		return `{}`
	}
}

func defaultInboundKindConfig(kind, protocol string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "vless-ws":
		return `{"tls":{"enabled":true},"transport":{"type":"ws","path":"/vless","headers":{}}}`
	case "vless-tcp":
		return `{}`
	case "hy2-salamander":
		return `{"tls":{"enabled":true},"up_mbps":1000,"down_mbps":500,"obfs":{"type":"salamander"}}`
	default:
		return defaultInboundPresetConfig(protocol)
	}
}

func defaultInboundKind(protocol string) string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "vless":
		return "vless-reality"
	case "hysteria2", "hy2":
		return "hy2-tls"
	case "anytls":
		return "anytls-basic"
	case "shadowsocks":
		return "ss-2022-128"
	case "mieru":
		return "mieru-basic"
	case "snell":
		return "snell-v4"
	case "socks":
		return "socks5-auth"
	case "ssh":
		return "ssh-restricted"
	default:
		return ""
	}
}

func inboundRecipeCertificateMode(values map[string]any) string {
	return strings.ToLower(strings.TrimSpace(taskStringParam(values, "certificate_mode")))
}

func inboundRecipeWantsDNSSync(input mcpTaskInput, values map[string]any) bool {
	if _, exists := values["dns_sync_enabled"]; exists {
		return taskBoolParam(values, false, "dns_sync_enabled")
	}
	return containsAnyFold(input.Goal, "同步 dns", "同步dns", "sync dns", "dns sync")
}

func inboundRecipeDNSDomain(input mcpTaskInput, values map[string]any) string {
	for _, params := range []map[string]any{values, input.Params} {
		for _, key := range []string{"dns_domain", "certificate_domain", "domain", "sni"} {
			if domain := taskStringParam(params, key); isDNSDomainName(domain) {
				return normalizeDomainName(domain)
			}
		}
	}
	if domain := inferredInboundDNSDomain(input.Goal); isDNSDomainName(domain) {
		return domain
	}
	return ""
}

func inferredInboundDNSDomain(goal string) string {
	matches := make([]string, 0, 2)
	seen := map[string]bool{}
	for _, token := range strings.FieldsFunc(goal, func(r rune) bool {
		return r == ',' || r == ';' || r == '，' || r == '、' || r <= ' '
	}) {
		token = strings.Trim(token, "。．.\"'`")
		if !isDNSDomainName(token) {
			continue
		}
		domain := normalizeDomainName(token)
		if seen[domain] {
			continue
		}
		seen[domain] = true
		matches = append(matches, domain)
	}
	if len(matches) == 1 {
		return matches[0]
	}
	return ""
}

// applyInboundRecipeManagedCertificate copies dns_domain into the SNI field,
// enables DNS sync for managed TLS kinds, and attaches a DNS credential so
// Controller can issue the certificate during deployment. dns_sync_enabled=true
// is validated as strictly as inbounds.update: a credential is required before
// ready. Create itself does not wait for a ready certificate.
func (s *Server) applyInboundRecipeManagedCertificate(ctx context.Context, input mcpTaskInput, values map[string]any, serverID int64) (*mcpPreparedRecipe, error) {
	kind := strings.ToLower(strings.TrimSpace(fmt.Sprint(values["kind"])))
	mode := inboundRecipeCertificateMode(values)
	managed := mode == model.CertificateModeAuto || mode == model.CertificateModeExact || mode == model.CertificateModeWildcard || (mode == "" && inboundKindUsesManagedCertificate(kind))
	dnsDomain := inboundRecipeDNSDomain(input, values)
	if dnsDomain != "" {
		values["dns_domain"] = dnsDomain
	}
	if inboundKindUsesManagedCertificate(kind) && mode != model.CertificateModeExternal && mode != model.CertificateModeExplicit {
		values["dns_sync_enabled"] = true
	} else if inboundRecipeWantsDNSSync(input, values) {
		values["dns_sync_enabled"] = true
	} else if parsed, ok := coerceTaskBool(values["dns_sync_enabled"]); ok {
		values["dns_sync_enabled"] = parsed
	}
	certificateDomain := taskStringParam(values, "certificate_domain")
	if certificateDomain == "" && dnsDomain != "" {
		certificateDomain = dnsDomain
	}
	if managed && certificateDomain != "" {
		values["certificate_domain"] = certificateDomain
	}
	syncEnabled := taskBoolParam(values, false, "dns_sync_enabled")
	if syncEnabled && !isDNSDomainName(dnsDomain) {
		return &mcpPreparedRecipe{Status: "needs_input", Intent: "inbound.create", Questions: []map[string]any{{"field": "dns_domain", "type": "string", "reason": "启用 DNS 同步时需要有效的解析域名；提交后由主控写入解析并申请证书，不必等证书就绪"}}}, nil
	}
	if managed && !isDNSDomainName(certificateDomain) {
		return &mcpPreparedRecipe{Status: "needs_input", Intent: "inbound.create", Questions: []map[string]any{{"field": "dns_domain", "type": "string", "reason": "托管证书入口需要有效的解析/SNI 域名。把域名交给主控即可，主控在部署时匹配或申请证书，创建不必等待证书签发完成，也不必改走面板"}}}, nil
	}
	needsCredential := syncEnabled || (managed && isDNSDomainName(certificateDomain))
	if !needsCredential {
		return nil, nil
	}
	credentials, err := s.store.ListDNSCredentials(ctx)
	if err != nil {
		return nil, err
	}
	id, available, ok := pickInboundDNSCredential(credentials, firstNonEmptyString(dnsDomain, certificateDomain), serverID, int64(taskIntParam(values, "dns_credential_id")))
	if !ok {
		return nil, missingDNSCredentialError{Available: available}
	}
	values["dns_credential_id"] = id
	return nil, nil
}

func inferredInboundProtocol(goal string) string {
	goal = strings.ToLower(goal)
	for _, candidate := range []string{"hysteria2", "anytls", "shadowsocks", "mieru", "vless", "ssh"} {
		if strings.Contains(goal, candidate) {
			return candidate
		}
	}
	if strings.Contains(goal, "hy2") {
		return "hysteria2"
	}
	if strings.Contains(goal, "ss 入口") || strings.Contains(goal, "ss inbound") {
		return "shadowsocks"
	}
	return ""
}

func (s *Server) prepareDeploymentRecipe(ctx context.Context, principal application.Principal, input mcpTaskInput) (*mcpPreparedRecipe, error) {
	refs := taskRefsByType(input.TargetRefs, "server")
	if raw, ok := input.Params["server_ids"].([]any); ok {
		for _, value := range raw {
			refs = append(refs, "server:"+strings.TrimSpace(fmt.Sprint(value)))
		}
	}
	targets := []MCPResourceRef{}
	seenTargets := map[int64]bool{}
	if len(refs) == 0 {
		items, err := s.store.ListServers(ctx)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if principal.AllowsInt64("server_ids", item.ID) {
				targets = append(targets, serverMCPResourceRef(item))
				seenTargets[item.ID] = true
			}
		}
	} else {
		for _, ref := range refs {
			resolved, err := s.resolveServerRef(ctx, principal, ref)
			if err != nil {
				return nil, err
			}
			if len(resolved.Candidates) > 0 {
				return &mcpPreparedRecipe{Status: "choose_candidate", Intent: "deployment.apply", Field: "target_server", Candidates: resolved.Candidates}, nil
			}
			if !seenTargets[resolved.Value.ID] {
				targets = append(targets, *resolved.Value)
				seenTargets[resolved.Value.ID] = true
			}
		}
	}
	if len(targets) == 0 {
		return nil, errors.New("no authorized deployment targets are available")
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].ID < targets[j].ID })
	serverIDs := make([]int64, 0, len(targets))
	labels := make([]string, 0, len(targets))
	for _, target := range targets {
		serverIDs = append(serverIDs, target.ID)
		labels = append(labels, target.Label)
	}
	reason := taskStringParam(input.Params, "reason")
	if reason == "" {
		reason = strings.TrimSpace(input.Goal)
	}
	operation := mcpOperationRef{Capability: "deployments.apply", Input: map[string]any{"server_ids": serverIDs, "reason": reason}}
	return &mcpPreparedRecipe{Status: "ready", Intent: "deployment.apply", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "apply_deployment", "targets": labels, "server_count": len(serverIDs)}, Verification: map[string]any{"after_commit": []string{"workflow_terminal", "deployment_tasks_terminal"}}}, nil
}

func (s *Server) prepareProxyPathRecipe(ctx context.Context, principal application.Principal, input mcpTaskInput) (*mcpPreparedRecipe, error) {
	if containsAnyFold(input.Goal, "增加一跳", "add hop", "modify transport", "修改 transport", "修改传输") || firstTaskRef(input, "proxy_path", "target_proxy_path", "proxy_path") != "" {
		return &mcpPreparedRecipe{Status: "fallback_required", Intent: "proxy_path.manage", Fallback: []string{"oboard_discover", "oboard_get_capability_schema", "oboard_plan_desired_state", "oboard_validate_desired_state", "oboard_submit_changeset"}}, nil
	}
	directBranch := containsAnyFold(input.Goal, "direct branch", "直接分支", "直出分支") || taskBoolParam(input.Params, false, "direct_branch")
	if directBranch && taskIntParam(input.Params, "source_step_id") > 0 {
		return &mcpPreparedRecipe{Status: "fallback_required", Intent: "proxy_path.manage", Fallback: []string{"oboard_discover", "oboard_get_capability_schema", "oboard_plan_desired_state", "oboard_validate_desired_state", "oboard_submit_changeset"}}, nil
	}
	inboundRef := firstTaskRef(input, "inbound", "entry_inbound", "inbound")
	if inboundRef == "" {
		matches, err := s.inferInboundCandidates(ctx, principal, input.Goal)
		if err != nil {
			return nil, err
		}
		if len(matches) == 1 {
			inboundRef = matches[0].Ref
		} else if len(matches) > 1 {
			return &mcpPreparedRecipe{Status: "choose_candidate", Intent: "proxy_path.manage", Field: "entry_inbound", Candidates: matches}, nil
		}
	}
	if inboundRef == "" {
		return &mcpPreparedRecipe{Status: "needs_input", Intent: "proxy_path.manage", Questions: []map[string]any{{"field": "entry_inbound", "type": "resource_ref", "reason": "需要指定代理路径的入口"}}}, nil
	}
	inboundResolution, err := s.resolveInboundRef(ctx, principal, inboundRef)
	if err != nil {
		return nil, err
	}
	if len(inboundResolution.Candidates) > 0 {
		return &mcpPreparedRecipe{Status: "choose_candidate", Intent: "proxy_path.manage", Field: "entry_inbound", Candidates: inboundResolution.Candidates}, nil
	}
	inbound, err := s.store.GetInbound(ctx, inboundResolution.Value.ID)
	if err != nil {
		return nil, err
	}
	pathName := taskStringParam(input.Params, "path.name", "name")
	path := map[string]any{"kind": "chain", "name_mode": "auto", "name_template": []any{}, "inbound_id": inbound.ID, "exit_region_mode": "auto", "enabled": true}
	if pathName != "" {
		path["name_mode"] = "custom"
		path["name_template"] = []any{map[string]any{"kind": "literal", "value": pathName}}
	}
	if directBranch {
		path["kind"] = "direct"
		operation := mcpOperationRef{Capability: "topology.write", Input: map[string]any{"path": path, "steps": []any{}}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "proxy_path.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "create_direct_branch", "entry": inboundResolution.Value.Label, "hops": []string{}, "transport": "direct", "will_deploy": false}, Verification: map[string]any{"after_commit": []string{"workflow_terminal", "proxy_path_present"}}}, nil
	}
	externalRef := firstTaskRef(input, "external_outbound", "external_outbound", "target_external_outbound")
	if externalRef == "" {
		matches := s.inferExternalOutboundCandidatesFromGoal(ctx, principal, input.Goal)
		if len(matches) == 1 {
			externalRef = matches[0].Ref
		} else if len(matches) > 1 {
			return &mcpPreparedRecipe{Status: "choose_candidate", Intent: "proxy_path.manage", Field: "external_outbound", Candidates: matches}, nil
		}
	}
	serverRefs := taskRefsByType(input.TargetRefs, "server")
	if raw, ok := input.Params["servers"].([]any); ok {
		for _, value := range raw {
			serverRefs = append(serverRefs, fmt.Sprint(value))
		}
	}
	if len(serverRefs) == 0 && externalRef == "" {
		if matches := s.inferAmbiguousServerCandidatesFromGoal(ctx, principal, input.Goal, inbound.ServerID); len(matches) > 1 {
			return &mcpPreparedRecipe{Status: "choose_candidate", Intent: "proxy_path.manage", Field: "servers", Candidates: matches}, nil
		}
		serverRefs = s.inferOrderedServerRefs(ctx, principal, input.Goal, inbound.ServerID)
	}
	if len(serverRefs) == 0 && externalRef == "" {
		return &mcpPreparedRecipe{Status: "needs_input", Intent: "proxy_path.manage", Questions: []map[string]any{{"field": "servers", "type": "resource_ref_array", "reason": "需要指定有序的代理链服务器"}}}, nil
	}
	steps := []any{}
	hops := []string{}
	transport := strings.ToLower(taskStringParam(input.Params, "transport", "transport_mode", "tunnel_type"))
	if transport == "" && containsAnyFold(input.Goal, "wireguard", "wire guard") {
		transport = "wireguard"
	}
	if transport == "" && containsAnyFold(input.Goal, "ssh tunnel", "ssh 隧道") {
		transport = "ssh"
	}
	if transport == "ssh" && taskIntParam(input.Params, "ssh_port") == 0 {
		return &mcpPreparedRecipe{Status: "needs_input", Intent: "proxy_path.manage", Questions: []map[string]any{{"field": "ssh_port", "type": "integer", "reason": "SSH tunnel 需要目标 Agent 的独立 ssh_port"}}}, nil
	}
	for _, ref := range serverRefs {
		resolved, err := s.resolveServerRef(ctx, principal, ref)
		if err != nil {
			return nil, err
		}
		if len(resolved.Candidates) > 0 {
			return &mcpPreparedRecipe{Status: "choose_candidate", Intent: "proxy_path.manage", Field: "servers", Candidates: resolved.Candidates}, nil
		}
		if resolved.Value.ID == inbound.ServerID {
			continue
		}
		step := map[string]any{"node_type": "server_inbound", "server_id": resolved.Value.ID, "transport_mode": "singbox"}
		switch transport {
		case "wireguard", "wire_guard":
			step["transport_mode"] = "tunnel"
			step["tunnel_type"] = "wireguard"
		case "ssh":
			step["transport_mode"] = "tunnel"
			step["tunnel_type"] = "ssh"
			if port := taskIntParam(input.Params, "ssh_port"); port > 0 {
				step["ssh_port"] = port
			}
		case "port_forward", "transparent":
			step["transport_mode"] = "port_forward"
		}
		steps = append(steps, step)
		hops = append(hops, resolved.Value.Label)
	}
	if externalRef != "" {
		resolved, err := s.resolveExternalOutboundRef(ctx, principal, externalRef)
		if err != nil {
			return nil, err
		}
		if len(resolved.Candidates) > 0 {
			return &mcpPreparedRecipe{Status: "choose_candidate", Intent: "proxy_path.manage", Field: "external_outbound", Candidates: resolved.Candidates}, nil
		}
		steps = append(steps, map[string]any{"node_type": "imported", "external_outbound_id": resolved.Value.ID, "transport_mode": "singbox"})
		hops = append(hops, resolved.Value.Label)
	}
	if len(steps) == 0 {
		return nil, errors.New("proxy path must include at least one server after the entry server")
	}
	operation := mcpOperationRef{Capability: "topology.write", Input: map[string]any{"path": path, "steps": steps}}
	return &mcpPreparedRecipe{Status: "ready", Intent: "proxy_path.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "create_proxy_path", "entry": inboundResolution.Value.Label, "hops": hops, "transport": firstNonEmptyString(transport, "singbox"), "will_deploy": false}, Verification: map[string]any{"after_commit": []string{"workflow_terminal", "proxy_path_present"}}}, nil
}

func taskStringParam(params map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := params[key]; ok {
			if text := strings.TrimSpace(fmt.Sprint(value)); text != "" && text != "<nil>" {
				return text
			}
		}
	}
	return ""
}
func taskBoolParam(params map[string]any, fallback bool, keys ...string) bool {
	for _, key := range keys {
		if value, ok := params[key]; ok {
			if parsed, ok := coerceTaskBool(value); ok {
				return parsed
			}
		}
	}
	return fallback
}

func coerceTaskBool(value any) (bool, bool) {
	switch v := value.(type) {
	case bool:
		return v, true
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(v))
		return parsed, err == nil
	case int:
		return v != 0, true
	case int64:
		return v != 0, true
	case float64:
		return v != 0, true
	default:
		return false, false
	}
}
func taskIntParam(params map[string]any, keys ...string) int {
	for _, key := range keys {
		if value, ok := params[key]; ok {
			n, _ := strconv.Atoi(fmt.Sprint(value))
			return n
		}
	}
	return 0
}
func copyTaskParams(dst, src map[string]any, mapping map[string]string) {
	for from, to := range mapping {
		if value, ok := src[from]; ok {
			dst[to] = value
		}
	}
}
func containsAnyFold(value string, values ...string) bool {
	value = strings.ToLower(value)
	for _, candidate := range values {
		if strings.Contains(value, strings.ToLower(candidate)) {
			return true
		}
	}
	return false
}

func serverTrafficResetRequested(input mcpTaskInput) bool {
	if taskBoolParam(input.Params, false, "reset_traffic") {
		return true
	}
	return containsAnyFold(input.Goal,
		"清零已用流量", "已用流量清零", "清零服务器流量", "清零这台服务器的流量",
		"重置服务器已用流量", "清零流量",
		"reset used traffic", "reset server traffic", "zero used traffic",
		"clear used traffic", "clear server traffic",
	)
}
func inferredIPStack(goal string) string {
	switch {
	case containsAnyFold(goal, "ipv6 优先", "ipv6优先", "prefer ipv6"):
		return string(model.IPStackPreferIPv6)
	case containsAnyFold(goal, "ipv4 优先", "ipv4优先", "prefer ipv4"):
		return string(model.IPStackPreferIPv4)
	case containsAnyFold(goal, "仅 ipv6", "ipv6 only"):
		return string(model.IPStackIPv6Only)
	case containsAnyFold(goal, "仅 ipv4", "ipv4 only"):
		return string(model.IPStackIPv4Only)
	case containsAnyFold(goal, "双栈", "dual stack"):
		return string(model.IPStackDualStack)
	default:
		return ""
	}
}
func inferredRegionCode(goal string) string {
	regions := []struct {
		tokens []string
		code   string
	}{{[]string{"东京", "tokyo"}, "JP"}, {[]string{"香港", "hong kong", "hongkong"}, "HK"}, {[]string{"新加坡", "singapore"}, "SG"}, {[]string{"洛杉矶", "los angeles"}, "US"}, {[]string{"日本", "japan"}, "JP"}, {[]string{"美国", "united states"}, "US"}}
	for _, region := range regions {
		if containsAnyFold(goal, region.tokens...) {
			return region.code
		}
	}
	return ""
}
func inferredServerName(goal string) string {
	candidates := []struct {
		tokens []string
		name   string
	}{{[]string{"tokyo", "东京"}, "Tokyo"}, {[]string{"hong kong", "香港"}, "Hong Kong"}, {[]string{"singapore", "新加坡"}, "Singapore"}, {[]string{"los angeles", "洛杉矶"}, "Los Angeles"}}
	for _, item := range candidates {
		if containsAnyFold(goal, item.tokens...) {
			return item.name
		}
	}
	return ""
}
func firstTaskRef(input mcpTaskInput, resourceType string, keys ...string) string {
	for _, ref := range input.TargetRefs {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(ref)), resourceType+":") {
			return ref
		}
	}
	return taskStringParam(input.Params, keys...)
}
func taskRefsByType(refs []string, resourceType string) []string {
	out := []string{}
	for _, ref := range refs {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(ref)), resourceType+":") {
			out = append(out, ref)
		}
	}
	return out
}

func (s *Server) inferOrderedServerRefs(ctx context.Context, principal application.Principal, goal string, exclude int64) []string {
	items, err := s.store.ListServers(ctx)
	if err != nil {
		return nil
	}
	type match struct {
		pos int
		ref string
	}
	matches := []match{}
	lower := strings.ToLower(goal)
	for _, item := range items {
		if item.ID == exclude || !principal.AllowsInt64("server_ids", item.ID) {
			continue
		}
		pos := strings.Index(lower, strings.ToLower(item.Name))
		if pos < 0 {
			code := strings.ToUpper(item.RegionCode)
			aliases := map[string][]string{"HK": {"香港", "hong kong"}, "SG": {"新加坡", "singapore"}, "JP": {"东京", "日本", "tokyo", "japan"}, "US": {"洛杉矶", "美国", "los angeles"}}[code]
			for _, alias := range aliases {
				if p := strings.Index(lower, strings.ToLower(alias)); p >= 0 {
					pos = p
					break
				}
			}
		}
		if pos >= 0 {
			matches = append(matches, match{pos, "server:" + strconv.FormatInt(item.ID, 10)})
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].pos < matches[j].pos })
	out := []string{}
	for _, item := range matches {
		out = append(out, item.ref)
	}
	return out
}

func (s *Server) inferServerCandidatesFromGoal(ctx context.Context, principal application.Principal, goal string, exclude int64) []MCPResourceRef {
	items, err := s.store.ListServers(ctx)
	if err != nil {
		return nil
	}
	lower := strings.ToLower(goal)
	exact := []MCPResourceRef{}
	for _, item := range items {
		if item.ID != exclude && principal.AllowsInt64("server_ids", item.ID) && strings.Contains(lower, strings.ToLower(item.Name)) {
			exact = append(exact, serverMCPResourceRef(item))
		}
	}
	if len(exact) > 0 {
		return exact
	}
	regionMatches := []MCPResourceRef{}
	for _, region := range mcpRegionAliases() {
		code, aliases := region.code, region.aliases
		if !containsAnyFold(goal, aliases...) {
			continue
		}
		matches := []MCPResourceRef{}
		for _, item := range items {
			if item.ID != exclude && principal.AllowsInt64("server_ids", item.ID) && strings.EqualFold(item.RegionCode, code) {
				matches = append(matches, serverMCPResourceRef(item))
			}
		}
		if len(matches) > 1 {
			return matches
		}
		regionMatches = append(regionMatches, matches...)
	}
	return regionMatches
}

func (s *Server) inferAmbiguousServerCandidatesFromGoal(ctx context.Context, principal application.Principal, goal string, exclude int64) []MCPResourceRef {
	items, err := s.store.ListServers(ctx)
	if err != nil {
		return nil
	}
	for _, region := range mcpRegionAliases() {
		if !containsAnyFold(goal, region.aliases...) {
			continue
		}
		matches := []MCPResourceRef{}
		for _, item := range items {
			if item.ID != exclude && principal.AllowsInt64("server_ids", item.ID) && strings.EqualFold(item.RegionCode, region.code) && !containsAnyFold(goal, item.Name) {
				matches = append(matches, serverMCPResourceRef(item))
			}
		}
		if len(matches) > 1 {
			return matches
		}
	}
	return nil
}

func mcpRegionAliases() []struct {
	code    string
	aliases []string
} {
	return []struct {
		code    string
		aliases []string
	}{
		{"HK", []string{"香港", "hong kong"}},
		{"SG", []string{"新加坡", "singapore"}},
		{"JP", []string{"东京", "日本", "tokyo", "japan"}},
		{"US", []string{"洛杉矶", "美国", "los angeles"}},
	}
}
func (s *Server) inferInboundCandidates(ctx context.Context, principal application.Principal, goal string) ([]MCPResourceRef, error) {
	inbounds, err := s.store.ListInbounds(ctx)
	if err != nil {
		return nil, err
	}
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return nil, err
	}
	names := map[int64]string{}
	for _, server := range servers {
		names[server.ID] = server.Name
	}
	protocol := ""
	for _, value := range []string{"vless", "hysteria2", "hy2", "anytls", "shadowsocks", "mieru", "socks", "socks5"} {
		if containsAnyFold(goal, value) {
			protocol = value
			break
		}
	}
	matches := []MCPResourceRef{}
	for _, inbound := range inbounds {
		if !principal.AllowsInt64("server_ids", inbound.ServerID) {
			continue
		}
		serverMentioned := containsAnyFold(goal, names[inbound.ServerID])
		if !serverMentioned {
			code := ""
			for _, server := range servers {
				if server.ID == inbound.ServerID {
					code = server.RegionCode
					break
				}
			}
			aliases := map[string][]string{"HK": {"香港", "hong kong"}, "SG": {"新加坡", "singapore"}, "JP": {"东京", "日本", "tokyo", "japan"}, "US": {"洛杉矶", "美国", "los angeles"}}[strings.ToUpper(code)]
			serverMentioned = containsAnyFold(goal, aliases...)
		}
		protocolMatch := protocol == "" || strings.EqualFold(protocol, string(inbound.Protocol)) || protocol == "hy2" && inbound.Protocol == model.ProtocolHY2
		if serverMentioned && protocolMatch {
			matches = append(matches, inboundMCPResourceRef(inbound, names[inbound.ServerID]))
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
	return matches, nil
}

func (s *Server) inferExternalOutboundCandidatesFromGoal(ctx context.Context, principal application.Principal, goal string) []MCPResourceRef {
	items, err := s.store.ListExternalOutbounds(ctx)
	if err != nil {
		return nil
	}
	lower := strings.ToLower(goal)
	matches := []MCPResourceRef{}
	for _, item := range items {
		if item.ServerID != nil && !principal.AllowsInt64("server_ids", *item.ServerID) {
			continue
		}
		if strings.Contains(lower, strings.ToLower(item.Name)) {
			matches = append(matches, MCPResourceRef{Type: "external_outbound", ID: item.ID, Name: item.Name, Ref: "external_outbound:" + strconv.FormatInt(item.ID, 10), Label: item.Name})
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
	return matches
}

func mcpOperationRequests(operations []mcpOperationRef) ([]automation.OperationRequest, error) {
	out := make([]automation.OperationRequest, 0, len(operations))
	for _, operation := range operations {
		raw, err := json.Marshal(operation.Input)
		if err != nil {
			return nil, err
		}
		out = append(out, automation.OperationRequest{Capability: operation.Capability, Input: raw})
	}
	return out, nil
}

// prepareUserManageRecipe routes create / update / delete / session-revoke
// requests for panel users onto the users.* executable capabilities.
func (s *Server) prepareUserManageRecipe(ctx context.Context, principal application.Principal, input mcpTaskInput) (*mcpPreparedRecipe, error) {
	goal := strings.ToLower(strings.TrimSpace(input.Goal))
	deleting := containsAnyFold(goal, "删除", "delete", "remove")
	revoking := containsAnyFold(goal, "吊销会话", "revoke session", "踢下线", "强制下线")
	userID := int64(0)
	if target := firstTaskRef(input, "user", "target_user", "user"); target != "" {
		resolved, err := s.resolveUserRef(ctx, principal, target)
		if err != nil {
			return nil, err
		}
		if len(resolved.Candidates) > 0 {
			return &mcpPreparedRecipe{Status: "choose_candidate", Intent: "user.manage", Field: "user", Candidates: resolved.Candidates}, nil
		}
		userID = resolved.Value.ID
	} else if matches := s.inferUserCandidatesFromGoal(ctx, principal, input.Goal); len(matches) == 1 {
		userID = matches[0].ID
	} else if len(matches) > 1 {
		return &mcpPreparedRecipe{Status: "choose_candidate", Intent: "user.manage", Field: "user", Candidates: matches}, nil
	}
	if userID <= 0 && !deleting && !revoking {
		if nested, ok := input.Params["user"].(map[string]any); ok {
			create := map[string]any{}
			for key, value := range nested {
				create[key] = value
			}
			operation := mcpOperationRef{Capability: "users.create", Input: map[string]any{"user": create}}
			return &mcpPreparedRecipe{Status: "ready", Intent: "user.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "create_user", "username": taskStringParam(input.Params, "user.username", "username")}, Verification: map[string]any{"after_commit": []string{"workflow_terminal", "user_present"}}}, nil
		}
		username := taskStringParam(input.Params, "user.username", "username")
		if username == "" {
			username = inferredUserName(input.Goal)
		}
		if username == "" {
			return &mcpPreparedRecipe{Status: "needs_input", Intent: "user.manage", Questions: []map[string]any{{"field": "user", "type": "object", "reason": "需要指定要创建的用户（至少包含 username）"}}}, nil
		}
		user := map[string]any{"username": username}
		if nickname := inferredUserNickname(input.Goal); nickname != "" {
			user["nickname"] = nickname
		}
		for _, key := range []string{"role", "status", "password", "speed_limit_mbps", "traffic_limit_bytes", "device_limit"} {
			if value, ok := input.Params[key]; ok {
				user[key] = value
			}
		}
		operation := mcpOperationRef{Capability: "users.create", Input: map[string]any{"user": user}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "user.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "create_user", "username": username}, Verification: map[string]any{"after_commit": []string{"workflow_terminal", "user_present"}}}, nil
	}
	if userID <= 0 {
		return &mcpPreparedRecipe{Status: "needs_input", Intent: "user.manage", Questions: []map[string]any{{"field": "user", "type": "resource_ref", "reason": "需要指定要操作的用户"}}}, nil
	}
	if deleting {
		operation := mcpOperationRef{Capability: "users.delete", Input: map[string]any{"user_id": userID, "confirm": true}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "user.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "delete_user", "user_id": userID}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
	}
	if revoking {
		operation := mcpOperationRef{Capability: "users.session_revoke", Input: map[string]any{"user_id": userID}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "user.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "revoke_user_sessions", "user_id": userID}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
	}
	changes := map[string]any{}
	if nested, ok := input.Params["changes"].(map[string]any); ok {
		for key, value := range nested {
			changes[key] = value
		}
	}
	for _, key := range []string{"nickname", "role", "status", "password", "speed_limit_mbps", "traffic_limit_bytes", "traffic_reset_mode", "traffic_reset_day", "device_limit", "legacy_proxy_enabled"} {
		if value, ok := input.Params[key]; ok {
			changes[key] = value
		}
	}
	if len(changes) == 0 {
		return &mcpPreparedRecipe{Status: "needs_input", Intent: "user.manage", Questions: []map[string]any{{"field": "changes", "type": "object", "reason": "未识别到要修改的用户设置"}}}, nil
	}
	operation := mcpOperationRef{Capability: "users.update", Input: map[string]any{"user_id": userID, "changes": changes}}
	return &mcpPreparedRecipe{Status: "ready", Intent: "user.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "update_user", "user_id": userID, "changes": changes}, Verification: map[string]any{"after_commit": []string{"workflow_terminal", "user_revision_changed"}}}, nil
}

// prepareUserGroupRecipe routes create / update / delete for user groups.
func (s *Server) prepareUserGroupRecipe(ctx context.Context, principal application.Principal, input mcpTaskInput) (*mcpPreparedRecipe, error) {
	goal := strings.ToLower(strings.TrimSpace(input.Goal))
	deleting := containsAnyFold(goal, "删除", "delete", "remove")
	groupID := int64(0)
	if value := taskIntParam(input.Params, "group_id"); value > 0 {
		groupID = int64(value)
	} else if target := firstTaskRef(input, "user_group", "target_group", "group"); target != "" {
		resolved, err := s.resolveUserGroupRef(ctx, principal, target)
		if err != nil {
			return nil, err
		}
		if len(resolved.Candidates) > 0 {
			return &mcpPreparedRecipe{Status: "choose_candidate", Intent: "user_group.manage", Field: "group", Candidates: resolved.Candidates}, nil
		}
		groupID = resolved.Value.ID
	}
	if groupID <= 0 && !deleting {
		group := map[string]any{}
		if nested, ok := input.Params["user_group"].(map[string]any); ok {
			for key, value := range nested {
				group[key] = value
			}
		}
		if name := taskStringParam(input.Params, "user_group.name", "name"); name == "" && len(group) == 0 {
			return &mcpPreparedRecipe{Status: "needs_input", Intent: "user_group.manage", Questions: []map[string]any{{"field": "user_group", "type": "object", "reason": "需要指定用户分组的名称"}}}, nil
		}
		if name := taskStringParam(input.Params, "user_group.name", "name"); name != "" && group["name"] == nil {
			group["name"] = name
		}
		operation := mcpOperationRef{Capability: "user_groups.create", Input: map[string]any{"user_group": group}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "user_group.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "create_user_group", "name": group["name"]}, Verification: map[string]any{"after_commit": []string{"workflow_terminal", "user_group_present"}}}, nil
	}
	if groupID <= 0 {
		return &mcpPreparedRecipe{Status: "needs_input", Intent: "user_group.manage", Questions: []map[string]any{{"field": "group_id", "type": "integer", "reason": "需要指定要操作的用户分组 ID"}}}, nil
	}
	if deleting {
		operation := mcpOperationRef{Capability: "user_groups.delete", Input: map[string]any{"group_id": groupID, "confirm": true}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "user_group.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "delete_user_group", "group_id": groupID}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
	}
	changes := map[string]any{}
	if nested, ok := input.Params["changes"].(map[string]any); ok {
		for key, value := range nested {
			changes[key] = value
		}
	}
	for _, key := range []string{"name", "description", "role", "enabled", "subscription_custom_path_policy"} {
		if value, ok := input.Params[key]; ok {
			changes[key] = value
		}
	}
	if len(changes) == 0 {
		return &mcpPreparedRecipe{Status: "needs_input", Intent: "user_group.manage", Questions: []map[string]any{{"field": "changes", "type": "object", "reason": "未识别到要修改的分组设置"}}}, nil
	}
	operation := mcpOperationRef{Capability: "user_groups.update", Input: map[string]any{"group_id": groupID, "changes": changes}}
	return &mcpPreparedRecipe{Status: "ready", Intent: "user_group.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "update_user_group", "group_id": groupID, "changes": changes}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
}

// prepareUserDeviceRecipe routes device rename / revoke operations.
func (s *Server) prepareUserDeviceRecipe(ctx context.Context, principal application.Principal, input mcpTaskInput) (*mcpPreparedRecipe, error) {
	userID := int64(0)
	if value := taskIntParam(input.Params, "user_id"); value > 0 {
		userID = int64(value)
	} else if target := firstTaskRef(input, "user", "target_user", "user"); target != "" {
		resolved, err := s.resolveUserRef(ctx, principal, target)
		if err != nil {
			return nil, err
		}
		if len(resolved.Candidates) > 0 {
			return &mcpPreparedRecipe{Status: "choose_candidate", Intent: "user_device.manage", Field: "user", Candidates: resolved.Candidates}, nil
		}
		userID = resolved.Value.ID
	}
	if userID <= 0 {
		return &mcpPreparedRecipe{Status: "needs_input", Intent: "user_device.manage", Questions: []map[string]any{{"field": "user_id", "type": "integer", "reason": "需要指定设备所属用户"}}}, nil
	}
	deviceID := taskStringParam(input.Params, "device_id")
	if deviceID == "" {
		deviceID = strings.TrimPrefix(firstTaskRef(input, "device", "target_device", "device"), "device:")
	}
	if deviceID == "" {
		return &mcpPreparedRecipe{Status: "needs_input", Intent: "user_device.manage", Questions: []map[string]any{{"field": "device_id", "type": "string", "reason": "需要指定设备 ID"}}}, nil
	}
	if containsAnyFold(input.Goal, "吊销", "revoke", "删除") {
		operation := mcpOperationRef{Capability: "user_devices.revoke", Input: map[string]any{"user_id": userID, "device_id": deviceID, "revoked": true}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "user_device.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "revoke_device", "user_id": userID, "device_id": deviceID}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
	}
	name := taskStringParam(input.Params, "name")
	if name == "" {
		return &mcpPreparedRecipe{Status: "needs_input", Intent: "user_device.manage", Questions: []map[string]any{{"field": "name", "type": "string", "reason": "需要指定新的设备名称"}}}, nil
	}
	operation := mcpOperationRef{Capability: "user_devices.update", Input: map[string]any{"user_id": userID, "device_id": deviceID, "name": name}}
	return &mcpPreparedRecipe{Status: "ready", Intent: "user_device.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "rename_device", "user_id": userID, "device_id": deviceID, "name": name}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
}

func inferredUserName(goal string) string {
	lower := strings.ToLower(goal)
	for _, candidate := range []struct {
		tokens []string
		name   string
	}{{[]string{"管理员", "admin"}, "admin"}, {[]string{"操作员", "operator"}, "operator"}} {
		for _, token := range candidate.tokens {
			if strings.Contains(lower, token) {
				return candidate.name
			}
		}
	}
	return ""
}

func inferredUserNickname(goal string) string {
	for _, candidate := range []struct {
		tokens []string
		name   string
	}{{[]string{"管理员", "admin"}, "管理员"}, {[]string{"操作员", "operator"}, "操作员"}} {
		for _, token := range candidate.tokens {
			if strings.Contains(strings.ToLower(goal), strings.ToLower(token)) {
				return candidate.name
			}
		}
	}
	return ""
}

func (s *Server) inferUserCandidatesFromGoal(ctx context.Context, principal application.Principal, goal string) []MCPResourceRef {
	items, err := s.store.ListUsers(ctx)
	if err != nil {
		return nil
	}
	lower := strings.ToLower(goal)
	matches := []MCPResourceRef{}
	for _, item := range items {
		if !principal.AllowsInt64("user_ids", item.ID) {
			continue
		}
		if strings.Contains(lower, strings.ToLower(item.Username)) || (item.Nickname != "" && strings.Contains(lower, strings.ToLower(item.Nickname))) {
			matches = append(matches, userMCPResourceRef(item))
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
	return matches
}
