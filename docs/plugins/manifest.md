# 清单与环境变量

`manifest.json` 是插件唯一的声明来源：能力、HTTP 范围、资源需求、环境变量、触发方式和限额都只在这里声明。未知字段、旧运行时字段（`plugin_id`、`sdk_version`、`schema_version`、`params_schema`、`config_schema`、`env`）一律拒绝。

```json
{
  "id": "acme.trace-monitor",
  "name": "路由追踪监控",
  "version": "1.2.0",
  "description": "从所选服务器定期 Trace 目标。",
  "runtime": "oboard-js",
  "entry": "main.js",
  "capabilities": ["servers.read", "network.trace", "state.read", "state.write"],
  "resources": { "servers": { "min": 1, "max": 5, "reason": "在这些服务器上执行 Trace" } },
  "environment": [
    { "name": "SOURCES", "type": "servers", "label": "探测服务器", "required": true, "max_items": 5 },
    { "name": "TARGET", "type": "string", "label": "目标", "required": true }
  ],
  "triggers": { "schedule": true, "events": ["server.online"] },
  "limits": { "timeout": "60s", "agent_operations": 10 }
}
```

| 字段 | 规则 |
|---|---|
| `id` | 小写点分标识，如 `acme.trace-monitor`；`oboard.` 前缀保留。同一 `id` 的新包是更新。 |
| `version` | SemVer。 |
| `runtime` / `entry` | 固定为 `oboard-js` / `main.js`。 |
| `capabilities` | 只能取自能力目录（见 [安全模型](security.md)）。 |
| `http` | 使用 `http.request` 时必填：`hosts`（精确主机名或单个前导 `*.`，最多 32 个，不接受 IP）与 `methods`。 |
| `resources.servers` | 服务器数量需求与理由，展示给管理员审核。 |
| `pages` | 使用 `ui.page` 时必填，最多 8 页。每页有 `id`、`title`，以及可选 `actions`（最多 8 个）。新增页面或操作算权限扩大。 |
| `triggers` | `schedule` 允许 interval/cron；`events` 取 `server.online`、`server.offline`，需要 `events.server_status`。界面刷新与按钮不在这里声明。 |
| `limits` | 只能**降低**主机上限：`timeout`（1s–2m）、`memory_mib`（16–128）、`sdk_calls`（≤1000）、`http_requests`（≤100）、`agent_operations`（≤20）、`log_bytes`（≤256 KiB）。 |

## 环境变量

每个字段有 `name`（`^[A-Z][A-Z0-9_]{0,63}$`，`OBOARD_` 前缀保留）、`type`、`label`，可选 `description`、`required`、`default`、`placeholder`。

| 类型 | 值 | 额外约束 |
|---|---|---|
| `string` | 单行文本 | `min_length`、`max_length`、`pattern` |
| `text` | 多行文本 | 同上 |
| `integer` | 整数（不接受带引号的数字） | `min`、`max` |
| `number` | 数值 | `min`、`max` |
| `boolean` | 布尔 | |
| `select` | 单个选项值 | `options` |
| `multi_select` | 选项值数组 | `options`、`min_items`、`max_items` |
| `server` | 服务器稳定 ID（字符串） | `filter`：`enrolled`、`online`、`ipv4`、`ipv6`（只影响选择器） |
| `servers` | 服务器 ID 数组 | `filter`、`min_items`、`max_items` |
| `secret` | 不存值，单独加密保存 | 代码中得到 `SecretRef`；需要 `secrets.use` |
| `url` | 绝对 URL | `schemes`（`https`、`http`） |
| `duration` | 整秒时长，如 `30s`、`5m`、`1h30m` | `min_duration`、`max_duration` |
| `json` | 任意 JSON（≤16 KiB） | |

`secret`、`server`、`servers` 不能有默认值。

**服务器变量是配置，不是权限。** 在环境里选择一台服务器不会授予任何访问权；网关按管理员授权的资源范围判断，运行时若选择的服务器不在授权范围内，调用以 `RESOURCE_DENIED` 失败。

### 条件字段

`depends_on: { "field": "MODE", "equals": "advanced" }` 只支持“等于”。不活动的字段不校验、不传入运行时；被依赖字段本身不活动时，依赖它的字段也不活动。

### 自定义变量

实例可以添加清单未声明的变量（类型限于 `string`、`text`、`integer`、`number`、`boolean`、`server`、`servers`、`secret`、`url`、`duration`、`json`）。名称不能与清单字段重复，代码通过 `env.get('NAME')` 读取。自定义变量同样不扩大任何权限。

### 三阶段校验

1. **清单**：安装、发布与编辑器诊断时校验 Schema 本身（类型、约束组合、默认值、条件引用）。
2. **保存**：实例保存环境变量时按 Schema 规范化值（`integer` 拒绝 `"12"`、时长规范化为 `5m` 等），返回逐字段错误。
3. **运行**：每次运行前重新评估——必填项、密钥是否已配置、服务器是否仍存在、是否仍在授权范围内。结果为 `ok`、`configuration_required` 或 `configuration_invalid`，后两者不会启动 Runner。
