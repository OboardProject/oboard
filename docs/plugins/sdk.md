# SDK 完整参考：oboard-sdk-v1

所有方法是 `oboard.<组>.<方法>(object)` 的同步调用。必须在版本 capabilities 中声明**完整方法名**且获管理员批准。入参封闭，资源 ID 使用十进制字符串，不能传 JS number。成功返回 `{ok:true,result:<JSON>,operation_id?:string}`；失败抛 `{error_code,message}`。`ok` 表示 SDK 请求成功，不是 Agent 任务完成。类型见 [oboard.d.ts](../../sdk/plugins/oboard.d.ts)。

## 资源读取

| 方法 | 入参 | result |
|---|---|---|
| `servers.get` | `{server_id}` | `server_id,name,status,region_code,public_ipv4,public_ipv6,agent_version,agent_build,sing_box_version,last_seen_at` |
| `servers.list` | `{limit?}` | `{servers:[与 get 相同]}`；最多 50 个已授权节点，默认 50，无分页；不可把它当完整大规模清单 |
| `servers.status` | `{server_id}` | `server_id,control_connected,last_trusted_communication_at,observed_at,status_version,stale,maintenance,capabilities,agent_connected,core_running,inbound_available,config_applied,reason` |
| `metrics.latest` | `{server_id}` | `server_id,exists,stale,observed_at`；有样本时还有 `cpu_usage_percent,memory_used_bytes,memory_total_bytes` |
| `incidents.get` | `{incident_id}` | `incident_id,subject_server_id,status,kind,first_offline_at,detected_at,resolved_at,flap_count` |

状态不是简单布尔值：`control_connected` / `agent_connected` 当前为 `true` 或 `"unknown"`，`core_running` 当前没有内核存活证据而返回 `"unknown"`，不能由版本字段推断进程健康；`config_applied` 当前为 `"true"`/`"false"`/`"unknown"`，不应使用 `if (value)` 判断。`stale:true` 或缺失样本不能当成零负载或服务停机。`capabilities` 是节点报告的能力，未报告时可能为 null，不是插件授权。读取只返回限定字段，不返回 Agent token 或原始节点配置。

## 节点操作、通知与回执

| 方法 | 入参 | result / 注意事项 |
|---|---|---|
| `services.status` | `{server_id,service}` | 提交结构化状态任务，`operation_id,task_id,accepted`；不是同步主机服务状态 |
| `services.restart` | `{server_id,service}` | `operation_id,task_id,accepted,run_id,action_key`；需显式选择服务器授权，可能中断连接 |
| `host.poweroff` | `{server_id,reason}` | 高危电源任务回执，`operation_id,task_id,accepted,expires_at,stage` |
| `host.reboot` | 同上 | 同上；无 Shell 回退 |
| `notifications.send` | `{channel_id,title,body}` | 通过已配置主控通知通道发送；`operation_id,accepted,channel_id`，不是外部 HTTPS SDK |
| `operations.get` | `{operation_id}` | 同一 run 已创建操作的 `operation_id,status,capability,error_code`，有节点任务时 `task_id,task_status` |
| `operations.wait` | `{operation_id,wait_seconds?}` | 同上，最多等待 8 秒；超出/省略使用 8；等待耗用执行预算，可能返回 `waited:true` |

`service` 仅 `oboard-agent`、`oboard-sb` 或 `all`，不是任意 systemd/OpenRC 单元。节点须满足全局、服务器和 Agent 本地策略；电源还需管理员独立授权、`host_power_v1` 能力、在线节点，单次执行最多一个目标。Controller 自身的节点有额外保护。冲突中的部署/更新/卸载可能拒绝操作。

必须检查 `task_status` 的实际终态，不能把 action 的 `accepted`/`dispatching` 或 JS `succeeded` 解释为节点完成。网络中断或 power 导致失联不证明动作成功；`result_unknown` 必须人工核对。`operations.*` 不支持查询其他插件或其他 run 的操作；长流程请通过执行记录管理，不无限轮询。

## 安装配置

`config.get({})` → `{config:<object>}`。必须声明并获授 `config.get`；只读取当前插件安装的非敏感配置，没有目标插件 ID 参数。返回当前保存值而非运行开始快照，不自动合并进 `params`，也不包含密钥。未安装或已卸载时拒绝读取。

## 私有状态

- `state.get({key})` → `{key,exists,version,value?}`，不存在时 `version:"0"`。
- `state.compareAndSet({key,expected_version,value})` → `{key,version,value}`，版本返回字符串；**expected_version 入参是非负整数**，0 仅创建，不覆盖已有键。

每插件最多 32 个键。读后将版本安全地转换为整数；超出 `Number.MAX_SAFE_INTEGER` 时拒绝继续，避免误写。版本变化抛 `condition_changed`，重新读取再决定，不无界重试。状态不是加密秘密存储，也没有直接读其他插件、删除键、事务、多键锁或任意 SQL API。

## 受控 HTTPS

`network.request({request:{url,method,headers?,body?,timeout_seconds?},secret?})`。

`body` 是字符串；JSON 必须自己 `JSON.stringify`，并显式设置 Content-Type。timeout 默认 10 秒、允许 1–30 秒，同时受执行总预算约束。返回 `{status,headers,body}`。HTTP 非 2xx 本身不是网络异常，作者检查 status；重定向不会跟随。响应体不得直接写日志/结果；仅提取必要的非秘密业务字段。模拟模式结果是 `{simulated:true,...}`，没有 status。

需声明 `network.request`，清单 network 与管理员 grant 约束取交集。`secret` 是声明且获准的引用名，由宿主注入请求，不向 JS 返回明文；**使用 secret 的响应仅保留 status，headers 为 null、body 为空字符串**，避免上游回显凭据。无 secret 时才可读取受限响应内容；见 [安全](security.md)。请求上限 64 KiB、响应 256 KiB、头部 16 KiB，可进一步缩小。网络原始错误不会带上游内容返回；网关可能统一报告 `invalid_input` + 安全错误消息，不依赖底层 Go error 文本。

## 统一管理能力

调用形式：

```js
var response = oboard.management.query({
  management: { capability: "servers.get", input: { server_id: "123" } }
});
```

- `management.query`：调用允许的只读 capability。
- `management.preview`：对允许的写操作生成验证/变更预览，不把预览当应用成功；simulate 模式仅返回模拟回执，不包含真实 preview。
- `management.apply`：走宿主 Changeset 验证与审批策略，返回 `{changeset_id,status,approval_required,operation_id?}`；`approval_required:true` 表示已创建待审批变更，不是 SDK 调用失败，也不是已应用。审批后应在 Changeset/任务记录跟踪完成状态，不绕过现有 domain validation。
- 外层 `management` 对象为 `{capability,input,reason?,expected_revisions?}`，`expected_revisions` 是 string→string 的版本映射。具体 input/output 使用宿主该 capability 的当前公开 Schema，不能使用 REST URL、内部路由或原始任务 JSON 代替。

执行期间可用 `operations.get`/`operations.wait` 跟踪管理回执的 `operation_id`。Runner 成功退出不会使待审批变更失效；应用时仍重新检查插件启用状态、版本、授权、资源范围和触发用户当前权限。取消或失败的运行不能继续应用，撤销授权或停用插件会阻止尚未应用的变更。

版本和授权必须同时包含方法（如 `management.query`）与 `management:servers.get`。新能力目录项**不会**自动变为插件能力。当前窄白名单：

`inventory.read`、`servers.list`、`servers.get`、`servers.metrics.read`、`servers.latency_probes.read`、`servers.connectivity.read`、`servers.connectivity.sla`、`servers.connectivity.events`、`servers.dns_policy.get`、`deployments.plan`、`deployments.apply`、`servers.update`、`inbounds.create`、`inbounds.update`、`inbounds.delete`、`proxy_paths.create`、`proxy_paths.update`、`proxy_paths.delete`。

目录角色/作用域、资源过滤、创建许可、破坏性操作许可及拒绝审批仍生效。`management:*` 通配符、插件自授权、密钥管理、全局安全设置和任意节点 Shell 不可用。

## 错误与恢复

| error_code | 正确处理 |
|---|---|
| `invalid_input` | 修正输入/版本/网络策略；不要原样不断重试 |
| `permission_denied` / `resource_out_of_scope` | 请求管理员核对最小授权，不通过其他入口绕过 |
| `approval_required` | 完成宿主明确审批；插件不能批准自己 |
| `condition_changed` | 重新读取当前资源/状态并预览 |
| `target_offline` | 等节点恢复，再核对先前动作是否下发 |
| `capability_unsupported` | 检查宿主/Agent 能力版本，不退回 Shell |
| `operation_expired` | 重新审核操作时效；不重放已过期任务 |
| `idempotency_conflict` | 同幂等键不同载荷；修正调用流程 |
| `result_unknown` | 核对执行与节点状态，不认定成功或自动重做 |
| `runtime_unavailable` | 检查 Worker、隔离和开关 |
| `limit_exceeded` | 缩小批次/结果/调用次数 |
| `cancelled` | 停止新动作，已下发的任务另行核对 |
| `internal_error` | 保存无秘密的运行 ID 与阶段，交管理员排查 |

SDK 没有任意事件发布、直接获取密钥、原生后台作业或文件系统 API。不要根据规划自行调用未列出的名称。
