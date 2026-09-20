# 定时、事件与 Webhook

触发器不是清单里的安装脚本，也不会随包自动启用。它固定 `plugin_id`、已发布的 `revision_id`、`params`、`env` 和 `spec`；首次创建停用。绑定内容变化后重新审核绑定级 grant，不可复用旧摘要授权。安装版本切换不自动改写触发绑定。

本页路径均相对 Controller 的 `OBOARD_BASE_PATH`，例如基路径 `/panel` 下使用 `/panel/api/v1/plugin-triggers`。管理请求使用宿主认证及 v1 响应封装；SDK 的字符串 ID 约定不改变 Web 管理对象中数字 `plugin_id` / `revision_id` / `binding_id` 的类型。

## 定时绑定

通过插件详情创建触发器，或使用 `POST /api/v1/plugin-triggers`。例如每五分钟对一个明确授权的测试节点做只读巡检（数字 ID 仅作示意，替换为真实安装/版本）：

```json
{
  "plugin_id": 12,
  "revision_id": 34,
  "name": "五分钟巡检",
  "kind": "interval",
  "spec": {"timezone":"UTC","interval_seconds":300,"catchup_once":false,"max_delay_seconds":60},
  "params": {"server_id":"123","diagnose":false},
  "env": {}
}
```

`GET /api/v1/plugin-triggers/{id}` 返回绑定、调度状态和未来时间槽。`PATCH` 更新需要 `expected_binding_revision` 查询参数或 `binding_revision` 字段，防止覆盖并发修改。管理员应在最终绑定内容确定后授权；启停也要检查当前绑定版本和授权状态。

| kind | spec 字段 | 行为 |
|---|---|---|
| `once` | `timezone`, `once_at` | RFC3339 或 `YYYY-MM-DDTHH:mm:ss` 本地时间；一次时间槽 |
| `interval` | `timezone`, `interval_seconds` | 10–2592000 秒，按 Unix 时间槽对齐，不是上次执行结束后等待 |
| `cron` | `timezone`, `cron` | 五字段：分、时、日、月、周；无秒字段，例如 `0 9 * * 1-5` |
| `event` | `timezone`, `event` | 下节列出的事件；不是任意字符串消息总线 |

`timezone` 必需，使用 IANA 名（如 `Asia/Shanghai` 或 `UTC`）。避免在夏令时切换窗口安排不可逆操作；查看实际 `next_slots`，不要用浏览器本地时区猜测执行时间。

定时任务晚于原槽一分钟且未设置 `catchup_once` 时跳过；`max_delay_seconds>0` 可进一步收紧容忍延迟。包含电源能力的版本迟到超过两秒即跳过，不应靠补跑执行关机。`catchup_once` 不能当成停机期间每个槽都必定重放的保证；应检查调度记录而非据名称推断次数。单插件并发默认为 1，重叠、已重复、条件变化、暂停或停用可能跳过/拒绝入队。

## 事件绑定

当前事件名：`server.offline`、`server.recovered`、`task.failed`、`task.timed_out`、`metric.condition_entered`、`metric.condition_cleared` 和独立验签入口使用的 `plugin.webhook`。

- `subject_server_ids` 过滤事件主体；`target_server_ids` 描述目标，不能代替 grant 的服务器范围。
- `sustain_seconds` 用于离线持续条件，到期再次检查故障仍存在，已恢复不执行。
- `metric` 可声明 `metric`、`operator`、`threshold`、`duration_seconds`、`clear_threshold`、`clear_duration_seconds`。支持 `cpu` / `cpu_usage_percent`（百分数）、`memory` / `memory_used_ratio`（0–1），运算符 `>` / `gt`、`>=` / `gte`、`<` / `lt`、`<=` / `lte`。
- 指标样本缺失或超过三分钟是未知，不是零；只有新样本推进持续时间。为便于独立跟踪条件，每个指标绑定只选择一个主体节点。
- `repeat_while_offline` 不提供“每隔 N 秒自动重试”合同。需要周期巡检时用明确的 interval 绑定。

JS 仍从 `params` 读取绑定参数。事件主体可从 `env.OBOARD_EVENT_SUBJECT_ID` 读取；不要假定事件快照全部自动合并进 params。Run 保存调度/事件元数据用于排查。SDK 没有事件发布或动态注册订阅方法；不要用失败事件无界触发同一维护动作。

## 入站 Webhook：设置

Webhook 不是公开执行接口。它固定到一个 `kind:"event"`、`spec:{"timezone":"UTC","event":"plugin.webhook"}` 的绑定和管理员批准的绑定级 grant。外部调用者只能提供本次参数，不能选择版本、grant、env 或目标权限。

只有交互式 Web 管理员可管理 endpoint；服务账号、插件和 MCP 客户端不能代为授予这一外部触发权限：

| 请求 | 输入 / 返回 data |
|---|---|
| `GET /api/v1/plugin-webhooks?plugin_id=12` | `{webhooks:[...]}`；不返回密钥 |
| `POST /api/v1/plugin-webhooks` | 输入 `{binding_id,grant_id}`；返回 `{webhook,secret}`，密钥只出现这一次，endpoint 默认停用 |
| `PATCH /api/v1/plugin-webhooks/{id}` | 输入 `{expected_generation,enabled,rotate_secret?}`；返回 `{webhook,secret?}`，仅轮换时再次提供新密钥 |

先启用并核对最终触发绑定，授予其当前内容权限，再创建 endpoint，最后使用返回的 `generation` 显式启用 endpoint。`enabled` 和正整数 `expected_generation` 必填；轮换不会自动把停用状态改为启用。绑定版本/代码/grant 变化后旧 endpoint 不会静默跟随，需按新授权重建。全局插件关闭、调度暂停、插件停用或绑定停用时不能接受新执行。

## 入站 Webhook：签名合同

发送 `POST <base>/api/v1/plugin-webhooks/receive/{id}`。`id` 是宿主生成的 64 字符标识，不带 query。Body 是 **params 本身**，不是 `{params:...}` 包装，必须满足版本 `params_schema`，最多 64 KiB。不发送凭据、机密配置或不必要的个人数据，因为参数会进入执行快照。

三个请求头各只能出现一次：

- `X-Oboard-Timestamp`：规范十进制 Unix 秒，无前导零，允许与主控相差最多 ±300 秒。
- `X-Oboard-Nonce`：16 个随机字节编码为 32 位十六进制，每次新业务投递生成一次。
- `X-Oboard-Signature`：HMAC-SHA256 的 64 位十六进制结果。

HMAC key 是创建/轮换返回的 **secret 原始 UTF-8 字符串**，不是 hex 解码后的字节。消息是下面四行之后紧接原始 body 字节（第三个值 nonce 后有换行，body 后不额外添加换行）：

```text
oboard-plugin-webhook-v1\n
<endpoint-id>\n
<timestamp>\n
<nonce>\n
<exact-body-bytes>
```

可直接用于外部 Python 发送端的签名函数（不运行在插件 JS 内）：

```python
import hashlib
import hmac
import json
import secrets
import time

def webhook_request(endpoint_id, secret, params):
    body = json.dumps(params, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    timestamp = str(int(time.time()))
    nonce = secrets.token_hex(16)
    message = ("oboard-plugin-webhook-v1\n" + endpoint_id + "\n" + timestamp + "\n" + nonce + "\n").encode("utf-8") + body
    signature = hmac.new(secret.encode("utf-8"), message, hashlib.sha256).hexdigest()
    return body, {
        "Content-Type": "application/json",
        "X-Oboard-Timestamp": timestamp,
        "X-Oboard-Nonce": nonce,
        "X-Oboard-Signature": signature,
    }
```

用可信 HTTP 客户端向上述 HTTPS URL 发送返回的**同一份 body 字节**和 headers。不要再序列化、不跟随重定向，不记录密钥或认证头。端点有意不返回插件结果/权限细节。独立校验向量：key=`key`，endpoint=`endpoint`，timestamp=`1800000000`，nonce 为 32 个 `a`，body 为 UTF-8 `{"hello":"world"}`，签名应为 `ddb2cdcb379de5dc2778b8b0a1e98f2260e44699dea0212064c2a260a893c0bf`；测试用 endpoint 不是实际可调用 ID。

## 回执、去重和重试

- `202`：v1 `data` 为 `{accepted:true,run_id:<数字>}`，只证明排队，不证明执行成功。授权用户通过运行记录查询后续状态。
- `400`：参数/JSON/URL 无效；`401`：认证失败或 endpoint 不可用；`403`：绑定/授权/运行状态不允许；`409`：nonce 已使用；`413`：body 过大；`429`：限流或并发上限。限流响应可能带 `Retry-After: 60`。
- 每 endpoint 每 UTC 分钟最多 60 次尝试，失败签名也计数；不把错误响应体当业务 JSON。
- nonce 在入队前持久化，重复投递不会排第二次。发生中断时可能消耗 nonce 却没有成功入队，属于安全失败，不承诺 exactly-once。请求超时后先核对执行记录；换新 nonce 会成为新投递，不能用它盲目重试不可逆操作。
- 轮换/停用使旧授权路径失效，但无法撤回已经接受的外部请求/Agent 任务。恢复备份后 endpoint 停用，敏感授权需重新建立，不能自动重放历史投递。

## 长流程边界

Run / RunAction 和节点任务会持久化，插件本身没有可恢复 continuation、自动网络重试或无限后台作业 API。短执行只提交有限步骤、保存非秘密业务状态并返回；需要跨次跟踪的流程应由明确的触发/管理流程推进，且 `operations.get/wait` 只允许本次 run 创建的操作。不要用 JS 循环等待数小时。
