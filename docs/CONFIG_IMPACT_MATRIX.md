# 配置影响面矩阵

本表是配置自动协调的入口清单。原则：保存期望状态后自动协调；命令型操作保持显式触发。REST v2/MCP 写操作仍必须经过 capability、Changeset、审批、resource boundary 和 audit。

| 领域 | 直接 Web/REST 写入口 | Capability / 后台入口 | 自动协调 | 影响范围 |
| --- | --- | --- | --- | --- |
| 服务器 | `POST /servers`, `PATCH/DELETE /servers/:id` | `servers.onboard`, `servers.update`, `servers.delete` | 完整部署 | 新增/修改为目标服务器；删除为剩余完整拓扑 |
| 服务器到期 | `PATCH /servers/:id` 到期字段, `POST /servers/:id/extend-expiry` | `servers.update`, `servers.extend_expiry` | 不触发 Agent | 仅主控到期元数据、自动续期和通知调度 |
| 入口 | `POST/PATCH/DELETE /inbounds` | `inbounds.create/update/delete` | 完整部署 | 入口所属服务器及以该入口为根的路径成员；可信转发前缀再扩展 |
| 管理出口 | `POST/PATCH/DELETE /outbounds` | `outbounds.*` | 完整部署 | owner 与 next server |
| 第三方出口 | create/update/delete/import | `external_outbounds.*` | 完整部署 | scope server 或所有服务器；路径引用由部署影响分析扩展 |
| 代理路径 | path/step create/update/delete, reuse, direct branch | `topology.write`, `topology.reuse_inbound`, `proxy_paths.*`, `proxy_path_steps.*` | 完整部署 | 路径成员；可信转发前缀自动扩展为完整范围 |
| 路由规则 | rule create/update/delete/place/reorder | `routing_rules.*` | 完整部署 | 规则 owner/path stage；当前保守全拓扑合并 |
| 路由规则集定义 | create/update/delete | `routing_rule_sets.create/update/delete` | 完整部署 | 引用服务器；保存流程不等待 Agent |
| 端口转发 | create/update/delete | `port_forwards.*` | 完整部署 | source + target |
| 隧道 | create/update/delete | `tunnels.*` | 完整部署 | source + target |
| DNS 列表 | create/update/delete/set-default | `dns_lists.*` | 完整部署 | 引用列表的服务器；全局默认变更保守全量 |
| 服务器 DNS policy | `POST /servers/:id/dns-policy` | `servers.dns_policy.set` | 完整部署 | 指定服务器 |
| WARP profile | create/update/delete | WARP 管理 capability | 完整部署 | profile server 及使用该 profile 的路径 |
| 用户与组 | user/group/member create/update/delete | `users.*`, `user_groups.*`, `user_group_members.*` | 完整部署 | 可能改变凭据/授权，保守全量 |
| 设备凭据 | device create/revoke/rotate/access state | `user_devices.update/revoke` 及审计后台 | 完整部署，异步 | 使用凭据的服务器；当前保守全量 |
| 套餐版本/节点 | plan version、node ordering、publish/disable | `subscription_plans.*`, `access_changes.*` | 草稿/待发布只保存期望；有效版本由 access-change 两阶段激活，激活事务进入统一配置修订并异步完整部署 | `AffectedAuthServers`；不把草稿或中间授权态推给 Agent |
| 用户套餐分配 | `/users/plan-assignment/apply` | `user_plan_bindings.*`, access-change worker | pending 绑定不触发；激活事务进入统一配置修订并异步完整部署 | 受影响节点服务器；离线服务器保留待收敛状态 |
| 用户节点例外 | exception create/update/delete/batch | `user_node_exceptions.*`, access-change worker | active 状态变更进入统一配置修订；pending 仍由 access-change 激活 | 受影响节点服务器；离线服务器保留待收敛状态 |

## 保持显式触发的命令

以下操作不属于“保存配置”，不得由普通配置写自动执行：

- `deployments.apply`：保留为管理/API 兼容的显式强制全量重建能力；Web 正常流程不再依赖它。
- `configuration_sync.retry`：只在 failed 状态下人工重试，仍经过 capability/Changeset/审批或 Operator REST 权限。
- Agent/Controller/relay 更新、Agent 配置更新、网络诊断、日志收集/管理。
- 入站/出口/端口转发/延迟/MTU/DNS benchmark 探测。
- routing rule set `refresh`：显式外部抓取；内容变化后使用现有聚焦 `apply_core_config`。
- 证书签发/续签/HTTP-01、DNS record 操作、备份创建/恢复、通知测试、AI 审查。
- enrollment token、subscription token/one-time token、session revoke 等安全命令。

## 协调与去重

- 普通保存接口成功且 `configuration_revision` 实际增长后才标记 `configuration_sync_states`；4xx/5xx、no-op 和 preview 不标记。配置修订在 SQLite 写事务内产生，Controller 重启会修复提交后尚未写入协调行的服务器；相同修订的 `synced` 行不重复部署。
- 入口、代理路径和路径步骤写入按响应实体或写入前资源反查路径成员，不得因 URL 中缺少数字 ID 就默认把所有服务器标记为待同步。全局校验错误可以阻塞同一部署准备批次，但 UI 必须区分“问题来源服务器”和“被阻塞的同步任务”，不能把两者都称为受影响服务器。
- 150ms 合并窗口只生成最新 revision；每秒数据库恢复扫描覆盖通知丢失和 Controller 重启。
- 新 revision 失败预算重置，单 revision 最多 6 次自动重试；failed 状态保留脱敏错误并允许人工重试。
- 未领取的旧 `apply_deployment` 会被 supersede；running 任务完成后再次比较最新 desired revision。
- 离线 Agent 的自动配置任务保持 pending；重连、hello/heartbeat health report 和 recovery scan 都会重新唤醒。
