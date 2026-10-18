# SDK 参考

运行时 `oboard-js` 是 Goja 解释的 ECMAScript，**不是 Node.js，也不是浏览器**：没有 `require`、`import`、`process`、`Buffer`、文件系统、套接字、定时器、`fetch` 或 WebAssembly。`main.js` 必须在顶层声明 `function main(run)`（可为 `async`）。SDK 调用是同步的，返回值直接可用；`await` 也可正常使用。`main` 的返回值（可 JSON 序列化，≤64 KiB）记为本次执行结果。

```js
async function main(run) {
  const server = oboard.servers.get(env.TARGET_SERVER)
  const trace = oboard.network.trace({ server_id: server.server_id, target: 'example.com', mode: 'tcp', port: 443 })
  log.info('hops', trace.hops.length)
  return { reached: trace.reached }
}
```

## `run`

`run_id`、`plugin_id`、`plugin_version`、`instance_id`、`trigger`（`manual` / `interval` / `cron` / `event` / `ui` / `action`）、`scheduled_at`。界面刷新带 `page`，按钮再带 `action`。事件触发时还有 `event: { type, server_id, occurred_at }`。

## `env`

冻结对象。`env.NAME` 为已规范化的类型化值；`env.get(name, fallback)`、`env.has(name)`、`env.names()`、`env.type(name)`、`env.raw(name)`（保存时的文本形式）。`secret` 类型的值是 `SecretRef`。

## `SecretRef`

不透明引用，`String(ref)` 为 `[SecretRef NAME]`，从不包含明文。只能用于：

- `oboard.http.request` 的 `headers` / `query` 值，或 `auth: { type: 'bearer' | 'basic', secret }`；
- `oboard.crypto.hmacSha256/hmacSha1` 的密钥。

`ref.withPrefix('Bearer ')`、`ref.withSuffix(...)` 生成带固定前后缀的新引用。密钥明文只在网关内解析，响应体、响应头、最终 URL 与日志中出现的密钥（原文、URL 编码、Base64、Hex）都会被替换为 `[REDACTED]`。

## `oboard` 方法

| 方法 | 能力 | 说明 |
|---|---|---|
| `servers.get(id)` / `servers.list()` | `servers.read` | 名称、状态、地区、公网地址、在线与版本；`list` 只含授权范围内的服务器 |
| `servers.health(id)` | `servers.health.read` | Agent 连接、配置同步、连通性 |
| `servers.metrics(id)` | `servers.metrics.read` | 最近一次 CPU、内存、磁盘、连接与带宽采样 |
| `network.ping(opts)` | `network.ping` | `{server_id, target, count≤10, interval_ms, timeout_ms, packet_size, ip_family}` |
| `network.trace(opts)` | `network.trace` | `{server_id, target, mode: icmp\|udp\|tcp, port, max_hops≤30, queries_per_hop≤3, per_hop_timeout_ms}`，总时长 ≤30 秒 |
| `network.tcpProbe(opts)` | `network.tcp_probe` | `{server_id, host, port, timeout_ms}` |
| `network.dnsLookup(opts)` | `network.dns_lookup` | `{server_id, name, record_types: ['A','AAAA']}` |
| `network.httpProbe(opts)` | `network.http_probe` | `{server_id, url, method: GET\|HEAD, follow_redirects}`，只返回状态、耗时与 TLS 信息 |
| `http.request(opts)` | `http.request` | 经主控网关访问授权主机；见下文 |
| `state.get/list` | `state.read` | 实例私有键值 |
| `state.set/delete/compareAndSwap` | `state.write` | `compareAndSwap(key, expectedVersion, value)`，`0` 表示键不存在；冲突返回 `STATE_CONFLICT` |
| `notifications.send({title, body, channel_ids?})` | `notifications.send` | 向已授权的管理员通知渠道发送 |
| `users.get(id)` / `users.list()` | `users.read` | 已授权用户的用户名、昵称和状态 |
| `users.notify({user_ids, title, body})` | `users.notify` | 向已授权的指定用户推送，一次最多 64 人 |
| `plans.list()` | `plans.read` | 已授权套餐的名称和是否启用 |
| `plans.users(planId)` | `plans.read` | 该套餐当前有效用户 |
| `plans.notify({plan_id, title, body})` | `plans.notify` | 向该套餐当前有效用户推送，一次最多 256 人 |
| `ui.publish({page, document})` | `ui.page` | 发布一份封闭视图文档，覆盖该实例该页的最新快照 |
| `crypto.*` | 无（带 `SecretRef` 的 HMAC 需要 `secrets.use`） | `sha256`、`sha1`、`hmacSha256`、`hmacSha1`、`base64Encode/Decode`、`hexEncode/Decode`、`randomBytes`、`uuid` |

网络诊断由目标服务器上的 Agent 以原生 Go 套接字执行，目标必须是公网域名或公网 IP；服务器离线时立即返回 `SERVER_OFFLINE`，正在执行其他任务时返回 `SERVER_BUSY`。Windows Agent 只支持 `tcpProbe`、`dnsLookup`、`httpProbe`。

### `http.request`

```js
const response = oboard.http.request({
  url: 'https://api.example.com/v1/items',
  method: 'POST',
  json: { name: 'x' },
  auth: { type: 'bearer', secret: env.API_TOKEN },
  timeout_ms: 10000,
})
// { ok, status, headers, redirects, final_url, body | body_base64, json? }
```

只允许清单 `http.hosts` 中且被授权的主机与方法，只接受 HTTPS。每次连接都在拨号时解析并校验公网地址（防 DNS 重绑定），拒绝私网、回环、链路本地、云元数据、NAT64 前缀，以及主控自身与已登记节点的地址。重定向默认关闭，开启后最多 3 次且逐跳重新校验；请求带密钥时重定向强制关闭。请求体、响应体、耗时与每分钟次数均有上限。

## 界面

页面只出现在插件实例对话框的「界面」标签。清单用 `pages` 声明 `id`、`title` 和可选 `actions`，并声明能力 `ui.page`。插件不能提供 HTML、CSS 或脚本。

`oboard.ui.publish({ page, document })` 在任意一次执行里覆盖该页快照。文档是纯文本块：`stack`、`heading`、`text`、`metric`、`badge`、`table`、`binding`、`button`、`empty`。单文档 ≤64 KiB。校验失败返回 `INVALID_ARGUMENT`，不覆盖上一份成功快照。密钥明文会被遮盖。

`binding` 只读当前数据，不启动 Runner：`source` 为 `servers.get`、`servers.health` 或 `servers.metrics`，`server` 只能写成 `{ "$env": "变量名" }`，且该变量必须是单个服务器。面板读取时按清单、实例授权和当前操作员的资源范围解析。网络探测、外部 HTTPS 和通知的结果要写进快照，不能在打开页面时现场发出。

打开页面不会运行插件。操作员点「刷新」才排队 `trigger: "ui"` 的执行，`run.page` 是页面 id。`button` 的 `action` 必须写在该页的 `actions` 里；点击只排队 `trigger: "action"`，并带上 `run.action`。按钮没有自由输入。

```js
oboard.ui.publish({
  page: 'overview',
  document: {
    title: '巡检',
    body: [
      { type: 'metric', label: '丢包', value: '12%', tone: 'warning' },
      { type: 'binding', source: 'servers.metrics', server: { $env: 'TARGET_SERVER' } },
      { type: 'button', action: 'recheck', label: '立即复测' },
    ],
  },
})
```

## 通知

`notifications.send` 只发给管理员已经授权的通知渠道，Telegram 和 Bark 都可以。`users.notify` 和 `plans.notify` 发给用户自己启用、并订阅了「管理员通知」的渠道，Telegram 和 Bark 同样会计入 `queued`。没有任何这类渠道的用户计入 `unbound`，不会假装已经送达。返回 `{ recipients, queued, unbound }`。手动或界面触发时，还要落在当前操作员自己的用户或套餐范围内。标题最多 120 个字符，正文最多 3000 个字符。用户视图不含密码、订阅地址或代理凭证。

## 日志

`log.debug/info/warn/error(...)` 与 `console.*`。单行 ≤2 KiB、总计 ≤1000 行，控制字符被清理，密钥被遮盖。

## 错误

所有 SDK 失败抛出 `OBoardError`，`code` 稳定、可编程判断：

`CAPABILITY_DENIED`、`RESOURCE_DENIED`、`SERVER_NOT_FOUND`、`SERVER_OFFLINE`、`SERVER_BUSY`、`AGENT_POLICY_DENIED`、`UNSUPPORTED_CAPABILITY`、`TARGET_NOT_ALLOWED`、`HTTP_HOST_DENIED`、`HTTP_PRIVATE_ADDRESS_DENIED`、`HTTP_TIMEOUT`、`HTTP_FAILED`、`HTTP_RESPONSE_TOO_LARGE`、`OPERATION_TIMEOUT`、`OPERATION_FAILED`、`RATE_LIMITED`、`LIMIT_EXCEEDED`、`STATE_QUOTA_EXCEEDED`、`STATE_CONFLICT`、`SECRET_NOT_CONFIGURED`、`INVALID_ARGUMENT`、`CANCELLED`。

插件也可 `throw new OBoardError('MY_CODE', 'message')`，该码记为执行错误码。未捕获的其他异常记为 `SCRIPT_ERROR`；超时为 `RUN_TIMEOUT`；超出内存、调用栈或结果大小为 `RESOURCE_LIMIT`。
