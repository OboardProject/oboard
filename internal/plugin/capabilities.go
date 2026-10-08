package plugin

import (
	"sort"
	"strings"
	"time"
)

// Capability names are the only permissions a plugin can declare or receive.
// Each one is a fixed, structured OBoard operation. There is deliberately no
// capability that accepts a command, a binary, a raw Agent task or a raw
// Controller request; see forbiddenCapabilityPrefixes.
const (
	CapServersRead        = "servers.read"
	CapServersHealthRead  = "servers.health.read"
	CapServersMetricsRead = "servers.metrics.read"
	CapNetworkPing        = "network.ping"
	CapNetworkTrace       = "network.trace"
	CapNetworkTCPProbe    = "network.tcp_probe"
	CapNetworkDNSLookup   = "network.dns_lookup"
	CapNetworkHTTPProbe   = "network.http_probe"
	CapHTTPRequest        = "http.request"
	CapStateRead          = "state.read"
	CapStateWrite         = "state.write"
	CapSecretsUse         = "secrets.use"
	CapNotificationsSend  = "notifications.send"
	CapUsersRead          = "users.read"
	CapUsersNotify        = "users.notify"
	CapPlansRead          = "plans.read"
	CapPlansNotify        = "plans.notify"
	CapEventsServerStatus = "events.server_status"
	CapUIPage             = "ui.page"
)

// Resource kinds a grant can scope a capability to.
const (
	ResourceNone                = ""
	ResourceServer              = "server"
	ResourceHTTPHost            = "http_host"
	ResourceNotificationChannel = "notification_channel"
	ResourceUser                = "user"
	ResourcePlan                = "plan"
)

type Risk string

const (
	RiskLow    Risk = "low"
	RiskMedium Risk = "medium"
	RiskHigh   Risk = "high"
)

// CapabilitySpec is the fixed contract for one capability. Limits are host
// ceilings; a run can only lower them through its manifest limits.
type CapabilitySpec struct {
	Name        string        `json:"name"`
	Group       string        `json:"group"`
	Label       string        `json:"label"`
	Description string        `json:"description"`
	Resource    string        `json:"resource,omitempty"`
	Risk        Risk          `json:"risk"`
	AgentTask   bool          `json:"agent_task"`
	Audited     bool          `json:"audited"`
	Methods     []string      `json:"methods"`
	Timeout     time.Duration `json:"-"`
	// RatePerMinute bounds calls per plugin instance across runs.
	RatePerMinute int `json:"rate_per_minute"`
}

var capabilityCatalog = []CapabilitySpec{
	{Name: CapServersRead, Group: "servers", Label: "读取服务器基础信息", Description: "读取已授权服务器的名称、状态、地区与公网地址。", Resource: ResourceServer, Risk: RiskLow, Methods: []string{"servers.get", "servers.list"}, Timeout: 5 * time.Second, RatePerMinute: 240},
	{Name: CapServersHealthRead, Group: "servers", Label: "读取服务器健康状态", Description: "读取已授权服务器的连接与配置同步状态。", Resource: ResourceServer, Risk: RiskLow, Methods: []string{"servers.health"}, Timeout: 5 * time.Second, RatePerMinute: 240},
	{Name: CapServersMetricsRead, Group: "servers", Label: "读取服务器资源指标", Description: "读取已授权服务器最近一次 CPU、内存、磁盘与网络采样。", Resource: ResourceServer, Risk: RiskLow, Methods: []string{"servers.metrics"}, Timeout: 5 * time.Second, RatePerMinute: 240},
	{Name: CapNetworkPing, Group: "network", Label: "在指定服务器执行 Ping", Description: "由指定服务器的 Agent 以原生 ICMP 探测公网目标。", Resource: ResourceServer, Risk: RiskMedium, AgentTask: true, Audited: true, Methods: []string{"network.ping"}, Timeout: 30 * time.Second, RatePerMinute: 20},
	{Name: CapNetworkTrace, Group: "network", Label: "在指定服务器执行 Trace", Description: "由指定服务器的 Agent 以原生 ICMP/UDP/TCP 逐跳追踪公网目标。", Resource: ResourceServer, Risk: RiskMedium, AgentTask: true, Audited: true, Methods: []string{"network.trace"}, Timeout: 45 * time.Second, RatePerMinute: 10},
	{Name: CapNetworkTCPProbe, Group: "network", Label: "在指定服务器执行 TCP 探测", Description: "由指定服务器的 Agent 测试公网目标端口的 TCP 连接。", Resource: ResourceServer, Risk: RiskMedium, AgentTask: true, Audited: true, Methods: []string{"network.tcpProbe"}, Timeout: 20 * time.Second, RatePerMinute: 30},
	{Name: CapNetworkDNSLookup, Group: "network", Label: "在指定服务器执行 DNS 查询", Description: "由指定服务器的 Agent 查询域名的 A/AAAA 记录。", Resource: ResourceServer, Risk: RiskLow, AgentTask: true, Audited: true, Methods: []string{"network.dnsLookup"}, Timeout: 20 * time.Second, RatePerMinute: 30},
	{Name: CapNetworkHTTPProbe, Group: "network", Label: "在指定服务器执行 HTTP 探测", Description: "由指定服务器的 Agent 以 GET/HEAD 探测公网 HTTP(S) 地址，只返回状态与耗时。", Resource: ResourceServer, Risk: RiskMedium, AgentTask: true, Audited: true, Methods: []string{"network.httpProbe"}, Timeout: 30 * time.Second, RatePerMinute: 20},
	{Name: CapHTTPRequest, Group: "http", Label: "访问外部 HTTPS 服务", Description: "由主控受控客户端访问清单声明且已授权的公网 HTTPS 主机。", Resource: ResourceHTTPHost, Risk: RiskHigh, Audited: true, Methods: []string{"http.request"}, Timeout: 35 * time.Second, RatePerMinute: 120},
	{Name: CapStateRead, Group: "state", Label: "读取插件运行状态", Description: "读取本插件实例自己的私有状态。", Risk: RiskLow, Methods: []string{"state.get", "state.list"}, Timeout: 5 * time.Second, RatePerMinute: 600},
	{Name: CapStateWrite, Group: "state", Label: "保存插件运行状态", Description: "写入、删除与条件更新本插件实例自己的私有状态。", Risk: RiskLow, Methods: []string{"state.set", "state.delete", "state.compareAndSwap"}, Timeout: 5 * time.Second, RatePerMinute: 600},
	{Name: CapSecretsUse, Group: "secrets", Label: "使用已配置的密钥", Description: "在 HTTP 认证或 HMAC 签名中引用本实例密钥；插件代码拿不到明文。", Risk: RiskHigh, Audited: true, Methods: []string{"crypto.hmac"}, Timeout: 5 * time.Second, RatePerMinute: 240},
	{Name: CapNotificationsSend, Group: "notifications", Label: "向管理员发送通知", Description: "通过已授权的管理员通知渠道发送文本通知。", Resource: ResourceNotificationChannel, Risk: RiskMedium, Audited: true, Methods: []string{"notifications.send"}, Timeout: 20 * time.Second, RatePerMinute: 10},
	{Name: CapUsersRead, Group: "users", Label: "读取用户列表", Description: "读取已授权用户的名称与状态，不含密码、订阅地址或代理凭证。", Resource: ResourceUser, Risk: RiskLow, Methods: []string{"users.get", "users.list"}, Timeout: 5 * time.Second, RatePerMinute: 120},
	{Name: CapUsersNotify, Group: "users", Label: "向指定用户推送", Description: "向已授权用户发送通知。用户自己启用的 Telegram 或 Bark 渠道都会进入发送队列。", Resource: ResourceUser, Risk: RiskMedium, Audited: true, Methods: []string{"users.notify"}, Timeout: 20 * time.Second, RatePerMinute: 10},
	{Name: CapPlansRead, Group: "plans", Label: "读取套餐与套餐用户", Description: "读取已授权套餐，以及这些套餐当前有效用户的名称与状态。", Resource: ResourcePlan, Risk: RiskLow, Methods: []string{"plans.list", "plans.users"}, Timeout: 5 * time.Second, RatePerMinute: 120},
	{Name: CapPlansNotify, Group: "plans", Label: "向套餐用户推送", Description: "向已授权套餐的当前有效用户发送通知。Telegram 与 Bark 渠道同样可用。", Resource: ResourcePlan, Risk: RiskMedium, Audited: true, Methods: []string{"plans.notify"}, Timeout: 20 * time.Second, RatePerMinute: 5},
	{Name: CapEventsServerStatus, Group: "events", Label: "订阅服务器上下线事件", Description: "已授权服务器上线或离线时触发本插件。", Resource: ResourceServer, Risk: RiskLow, Methods: []string{}, Timeout: 0, RatePerMinute: 0},
	{Name: CapUIPage, Group: "ui", Label: "在插件页展示界面", Description: "在插件实例中展示一份封闭视图。插件只能发布视图文档，不能提供自己的页面脚本。", Risk: RiskLow, Methods: []string{"ui.publish"}, Timeout: 5 * time.Second, RatePerMinute: 60},
}

var (
	capabilityByName   = map[string]CapabilitySpec{}
	capabilityByMethod = map[string]CapabilitySpec{}
)

func init() {
	for _, spec := range capabilityCatalog {
		if forbiddenCapability(spec.Name) {
			panic("forbidden plugin capability in catalog: " + spec.Name)
		}
		capabilityByName[spec.Name] = spec
		for _, method := range spec.Methods {
			if _, duplicate := capabilityByMethod[method]; duplicate {
				panic("duplicate plugin SDK method: " + method)
			}
			capabilityByMethod[method] = spec
		}
	}
}

// forbiddenCapabilityPrefixes can never become plugin permissions. A plugin
// describes which OBoard capability it needs; it never describes a command.
var forbiddenCapabilityPrefixes = []string{
	"shell", "exec", "command", "process", "subprocess", "spawn", "terminal", "pty",
	"remote_exec", "remote_operation", "filesystem", "database", "sql", "agent",
	"controller", "unix_socket", "tasks", "host", "power", "management",
}

func forbiddenCapability(name string) bool {
	head, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(name)), ".")
	for _, prefix := range forbiddenCapabilityPrefixes {
		if head == prefix {
			return true
		}
	}
	return false
}

// ForbiddenCapabilityGroups is shown by the Web permission review under
// "插件不具有".
func ForbiddenCapabilityGroups() []string {
	return []string{"Shell 与命令执行", "主机文件系统", "任意 Agent 任务", "主控数据库与内部 API", "终端 / PTY", "主机电源操作"}
}

func LookupCapability(name string) (CapabilitySpec, bool) {
	spec, ok := capabilityByName[name]
	return spec, ok
}

func CapabilityForMethod(method string) (CapabilitySpec, bool) {
	spec, ok := capabilityByMethod[method]
	return spec, ok
}

func CapabilityCatalog() []CapabilitySpec {
	out := append([]CapabilitySpec(nil), capabilityCatalog...)
	return out
}

func CapabilityNames() []string {
	names := make([]string, 0, len(capabilityCatalog))
	for _, spec := range capabilityCatalog {
		names = append(names, spec.Name)
	}
	sort.Strings(names)
	return names
}
