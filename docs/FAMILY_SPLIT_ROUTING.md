# IPv4 / IPv6 家族分流技术契约

## 管理面

家族分流是 `routing_rules` 的 `family_split` 动作，只用于 `path_stage` 规则。它不再指向两条同入口兄弟分支，而是引用一份可复用的双栈模板：

```json
{
  "action": "family_split",
  "family_split_template_id": 9,
  "family_dns_strategy": "auto",
  "enabled": true
}
```

模板表为 `family_split_templates`（名称去空白后大小写不敏感唯一）。每份模板带两条 `proxy_paths.kind=family_branch` 路径（`family=ipv4|ipv6`，`inbound_id` 可空，`template_id` 外键）。分支跳数走现有 `/api/v1/proxy-path-steps`。通用 `POST/PATCH/DELETE /proxy-paths` 拒绝 `kind=family_branch`。

`family_dns_strategy` 只接受 `auto`、`prefer_ipv4`、`prefer_ipv6`，写在规则上而不是模板上。`auto` 继承决策服务器当前 DNS 策略；该策略再按有效 IP 栈解析，在两族同等可用且未显式偏好时保持 IPv4 优先。MCP 的 `routing_rules.create` / `routing_rules.update` / `routing.manage` 以及 `family_split_templates.list/create/update/delete` 必须经过与 REST 相同的 Changeset、审批与领域校验。

模板在未被任何启用的 `family_split` 规则引用时保持停用；Controller 按启用引用计数打开或关闭两条 `family_branch`。同一模板可接到多条链路、同一路径的多个阶段。v1 模板内禁止再嵌套分流出口。第一跳必须是 sing-box（或接到规则后与决策服务器相同而折叠成本机继续）；禁止透明端口转发；模板 position 1 禁止隧道。折叠后剩余第一跳可以是隧道、WARP 或导入节点。最后一跳可写互斥的 `interface_name` / `source_prefix`。

无效启用规则不能保存或部署；停用规则保留结构字段，但把在线状态、入口可达性和内核能力校验推迟到再次启用时执行。旧的兄弟分支 `ipv4_target_proxy_path_id` / `ipv6_target_proxy_path_id` 在升级时复制后缀跳数到新模板，原路径不自动删除。

## DNS 与订阅

域名目标保留有界 A 与 AAAA 候选；IPv4/IPv6 字面量严格使用对应分支，不进行 NAT64/NAT46 或跨家族改写。逻辑入站仍只生成一个订阅节点。`kind=family_branch` 路径即使被授予也不会作为额外订阅节点输出。

入口 DNS 只发布监听实际接受的家族：`A` 取检测到的公网 IPv4，`AAAA` 取公网 IPv6，并可按入口规则回退到全局网卡 IPv6。单栈监听不得发布另一家族。自定义入口域名不能通过 CNAME 绕过此检查，因为 Controller 无法证明其 A/AAAA 与监听家族一致；启用 DNS 同步时必须使用受控服务器地址或自定义 IP，否则同步以可操作错误失败。

## Controller 到 kernel 的期望状态

决策服务器必须先通过健康报告上报 `family_selector_v1`。Controller 在接枝服务器上生成 OBoard 专用 outbound；模板第一跳若与决策服务器相同则折叠，克隆出站落在决策服务器上：

```json
{
  "type": "family-selector",
  "tag": "routing-rule-7-family",
  "ipv4_outbound": "routing-rule-7-ipv4-path-50-step-1",
  "ipv6_outbound": "routing-rule-7-ipv6-path-51-step-1",
  "strategy": "prefer_ipv4",
  "fallback": true,
  "domain_resolver": {
    "server": "bootstrap-primary",
    "strategy": "prefer_ipv4"
  }
}
```

Controller 分别强制两条分支的首个目标入口地址为 IPv4 或 IPv6，即使两个目标服务器都是双栈，也不能让 `auto` 把两条分支解析成同一家族。配置编译失败时不会排队任何服务器任务；一次完整部署为所有相关服务器分配同一单调配置版本。

kernel 对一个域名只做一次解析并把每族候选去重、限制为最多 8 个。TCP 先串行尝试首选家族候选，全部失败后最多切换到另一家族一次；UDP 在关联建立时选择一个有记录的家族，关联存续期间不切换。成功连接会把真实子 outbound 的稳定 tag/type 写入连接审计，而不是记录 `family-selector` 包装器。

Agent 使用现有持久版本门处理该 JSON：旧版本拒绝；同版本同内容幂等；同版本不同内容拒绝。`config_changed` 仍只是提示，缺失或漂移的本地配置必须修复。

## 迁移与回归边界

SQLite 迁移及移除门槛登记在 `docs/UPDATE_MIGRATIONS.md` 的 `controller-db-20260830-family-split-templates`。回归测试必须覆盖旧兄弟分支回填、内核能力持久化、单栈/双栈矩阵、CNAME 家族防泄漏、REST/MCP 模板与规则、DNS 策略、单节点订阅、部署原子性/统一版本、Agent 持久版本门和 kernel 的 TCP/UDP 行为。
