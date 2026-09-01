# 配置保存与自动收敛规范

状态：设计基线，供 Controller、Agent、Web、REST v2、MCP 和测试实现共同遵循。

## 1. 目标模型

OBoard 的“配置”是 Controller 数据库中的期望状态。管理写操作成功表示期望状态已经原子保存；它不表示 Agent 已执行完成。所有影响 Agent 运行态、入口/出口、转发/隧道、DNS 策略或用户可见节点/凭据的写操作，提交后进入同一个配置协调器。测试、探测、更新、证书签发、备份恢复和其他命令型任务不因本规范自动执行。

状态分为四层：

- `desired`: 数据库中已通过授权和领域校验的当前期望状态。
- `revision`: 由配置相关数据库事务产生的单调期望修订，用于发现保存发生，不作为 Agent 执行版本。`routing_cache_revision` 仍只服务派生缓存；探测结果、规则集抓取和 access-change 生命周期不会制造自动配置修订。
- `deployment`: 针对受影响服务器生成的完整或聚焦任务，使用现有正整数 `config_version`。
- `applied`: Agent 持久保存并通过健康报告/任务结果回传的已应用版本和摘要。

最终一致条件为：对每个受影响服务器，存在最新期望修订对应的成功部署，且 Agent 回报的已应用摘要与该部署 payload 摘要相同。只要不满足，UI 显示“保存成功，正在同步”或“同步失败”，不能显示“已同步”。

## 2. 持久状态

Controller 增加一个单行或按服务器可查询的持久协调状态表，推荐字段如下：

```text
server_id              primary key
wanted_revision         integer not null
wanted_digest           text not null
state                   pending|preparing|queued|running|synced|failed
last_config_version     integer not null default 0
last_task_id            integer not null default 0
retry_count             integer not null default 0
next_retry_at           text
last_error              text not null default ''
changed_at              text not null
updated_at              text not null
```

期望修订必须在同一 SQLite 写事务内由 `configuration_revision` 触发器产生；协调状态写入和任务状态转换使用 SQLite 事务。配置修订水位是崩溃恢复事实来源，协调表用于持久化待处理状态、重启恢复和 UI 查询，不用内存队列代替数据库。Controller 启动会按当前水位修复缺失或落后的服务器状态，但相同水位的 `synced` 状态不会被重新打开。

不增加旧 schema 兼容读取。若实现采用新表，必须按 `docs/UPDATE_MIGRATIONS.md` 登记真实旧状态、幂等性、失败行为、首次稳定版本和移除门槛，并添加从真实旧状态启动的升级测试。

## 3. 保存事务与影响分析

1. 请求先执行现有授权、Changeset/Workflow 规则和资源版本检查。
2. 请求在当前存储边界内完成领域校验和必要的资源存在性检查，并原子写入期望数据。跨表复合操作必须使用现有事务/Changeset；Web 创建代理路径及任意步骤使用同一个 `POST /proxy-paths` 请求的 `initial_steps`；路由目标还携带 `routing_rule_id`，Controller 先保存不触发运行态修订的禁用草稿，最终验证后在一个 SQLite 事务中启用路径并绑定规则。已有路径的多步恢复使用 `/proxy-path-steps/batch` 单事务追加；新入口的默认直出分支可在 `/proxy-paths/direct-branches` 携带 `routing_rule`，事务性启用分支并创建规则；删除分流区块使用 `/routing-rules/batch-delete` 整批成功或回滚。透明前缀冲突只提示用户先独立停用/删除，不再由一个 UI 动作串行删除其它分支。非法候选不留下部分步骤，也不会进入 Agent 运行态。reuse 继续使用现有服务端复合接口；MCP/Changeset 的 `topology.write` 也调用同一个路径+步骤事务原语，并可在新 direct 路径中携带 `routing_rule` 原子创建根规则；规则组删除对应 `routing_rules.batch_delete` capability。两者都保留输入/输出 schema、resource resolver、revision resolver、审批和 denied policy。所有复合操作都不能依赖多个独立请求的偶然时序。
3. 提交成功后读取当前配置修订，记录受影响资源类别与候选服务器，向协调器发出合并提示。提示丢失不能影响恢复，因为配置修订在写事务内持久化，协调器启动扫描和 Agent 通信检查都以数据库状态为准。
4. 协调器在短窗口内合并同一服务器的连续保存，只保留最新 `wanted_revision`。中间拓扑状态可以存在于数据库事务之间，但绝不生成或派发中间态任务；最终状态无效时进入 `failed`，不覆盖 Agent 当前有效配置。
5. 影响分析必须覆盖服务器、入口/出口、代理路径和步骤、路由规则、端口转发、隧道、DNS 列表/策略、WARP、用户/用户组、套餐修订、用户节点分配和设备凭据。套餐草稿、pending 绑定/例外不产生运行态修订；access-change 激活 active 版本/绑定/例外的事务推进同一个 `configuration_revision` 并唤醒统一协调器。可信透明转发继续按完整前缀扩展服务器集合。
6. 入口、代理路径和路径步骤的 REST 写入必须从写入前资源或成功响应实体解析根入口及路径成员服务器。不能仅因集合 POST 的 URL 不含资源 ID、或删除会改变资源本身，就退化为全服务器同步。若部署准备的全局校验阻塞了多个已领取任务，错误状态必须同时保留具体问题资源和被阻塞任务集合，不能声称每台任务服务器都拥有该问题资源。

## 4. 任务选择与幂等

- 拓扑、端口、密钥、SSH、可信转发、转发/隧道或跨服务器关系变化必须生成完整 `apply_deployment`。
- 仅不改变拓扑资源的核心 sing-box/DNS 内容可生成 `apply_core_config`，且不得借此引入可信转发或端口拓扑。
- 同一服务器同一 wanted revision 只允许一个准备中的 deployment；新修订会 supersede 未领取的旧配置任务。正在执行的任务不强行中断，结果回传后立即检查并补发最新修订。
- 任务仍使用签名 v2、Agent server identity、正版本门和 payload 摘要。相同版本相同内容必须幂等；相同版本不同内容和旧版本必须拒绝。
- 任务准备失败不回滚已保存期望状态；记录脱敏错误、指数退避且有最大重试次数/最大退避时间，最终保留可人工重试的 failed 状态。离线服务器保留 pending，不伪造“Agent 无法接收”的永久失败。
- 自动协调批次中的问题必须按路径成员和服务器隔离。一个入口或路径无法通过准备校验时，只把该路径成员记为 failed；与问题路径无关且能独立生成有效配置的服务器继续创建并执行任务。可信透明转发成员仍作为一个不可拆分集合：任一成员失败时只暂停该集合，不得阻塞集合外服务器。显式全量部署仍保持全量校验和全有或全无的准备语义。
- Controller 重启时扫描协调状态和 pending/running 任务。running 任务按现有超时/重入规则恢复；没有任务但 wanted revision 大于 last applied 的服务器重新准备。

## 5. Agent 通信收敛

现有 WebSocket 任务通道继续是执行通道，不新增旁路监听器。为让通信本身成为校验点：

- Agent 的 `HealthReport` 增加仅包含版本和摘要的 `applied_config_version`、`applied_config_digest`，不上传配置内容、凭据或秘密。Agent 从持久版本状态读取并缓存该值。
- Controller 在 hello/heartbeat 中可携带 `desired_config_revision` 和是否存在待同步状态；它只是提示，不能替代数据库任务。
- Controller 收到 hello、heartbeat 或 health report 时，比较 desired 与 applied；若不一致则幂等唤醒该服务器的协调器/任务通知。丢失 WebSocket wake 时，通信检查仍能补发，重连也会完整重算。
- Agent 启动、任务成功、幂等重放和本地 active 资产修复后都更新持久摘要；配置漂移、缺失或摘要不一致时，健康报告触发 Controller 重新准备最新状态。
- Agent 侧「已应用」必须以运行中的 `oboard-sb` 为准，不能只以磁盘 `sing-box.json` 为准。Agent 通过内核 `GET /runtime/status` 取得运行中的 operational digest；不一致时先本地重启并复验，仍不一致才让任务失败。内核未通告 `runtime_config_digest_v1` 时保持原有基于文件的行为，以支持 Agent 先于内核升级的滚动发布。
- 一次漂移只允许一次恢复：`MarkConfigurationSyncDrift` 只在服务器不处于 `failed`/`preparing`/`queued`/`running`、且不是已经处于 `agent_drift` 触发的 `pending` 时才新开恢复，并返回是否真的新开。心跳每次都上报同一漂移不得反复重开同一部署。
- 刚刚 `synced` 的服务器在短时间内收到仍指向更旧 `config_version` 的收敛回报时，视为部署成功前产生的在途回报并忽略；只有稳定后仍不一致才算新的漂移。
- 流量、连接审计和指标上报只做计费与运行策略协调，永远不得推进期望修订、创建 `config_version` 或派发 `apply_deployment` / `apply_core_config`。这一点由回归测试固定。
- Controller 与 Agent 仍只通过 JSON wire contract 通信，双方模型、协议文档、测试和 release gate 必须同时更新。不得导入对方内部包。

## 6. Web 保存与实时反馈

- 每个编辑动作的提交按钮表达“保存”；顶部移除全局“下发配置”，改为同步状态展示。失败状态提供问题资源名称、所属服务器、系统 ID、被阻塞任务与原始错误，并能直接跳转、居中和选中对应拓扑资源；修复后可重试同步，不再要求用户重新点击全量部署。
- 写响应至少通过 JSON 元数据或响应头返回 `desired_revision`、`sync_state`、`affected_server_ids` 和 correlation id；返回实体直接合并到当前页面缓存。
- Controller 实时事件新增配置同步语义，至少包含资源、修订、受影响服务器、状态和错误摘要。事件丢失时按 sequence resync；事件只触发精确页面刷新/patch，不触发无关页面全量 reload。
- 前端 mutation coordinator 负责：单请求去重、短批次合并、成功 patch、失败回滚、活动页精确失效和后台重试状态；长任务只显示 queued/running，不阻塞表单。
- 保留手动刷新作为诊断工具，不把它作为完成正常操作的必要步骤。所有目标流程必须通过响应和 realtime event 自然显示保存结果及同步进度。

## 7. REST、MCP 与授权边界

Web REST 适配器、REST v2、MCP Fast Path、Changeset/Workflow 和后台任务都调用同一应用层“期望状态已提交”钩子。这个钩子只记录/触发配置协调，不替代：

- Changeset 的授权、资源边界、基线修订、审批和 `denied` policy；
- access-change 的 prepare/activate/finalize 生命周期；
- 任务签名、Agent 身份和秘密脱敏；
- 命令型任务的显式确认。

任何新公开操作都必须同步更新 capability catalog、schema、execution mapping、resource resolution、审计和测试。不得在 Web handler 中单独复制部署策略。

## 8. 性能与可观测性

在预热 SQLite、固定服务器/拓扑 fixture、无外部网络和不计 Agent 长任务执行时间的基准条件下，记录以下时间点：

```text
request_start
state_commit
response_sent
reconcile_enqueued
config_prepared
agent_task_created
agent_task_dispatched
agent_applied_acknowledged
ui_event_received
ui_page_consistent
```

目标：

- 用户本地提交状态在 100ms 内可见；
- 普通管理写 API p95 <= 500ms；
- 在线 Agent 从 state_commit 到 task dispatched p95 <= 2s；
- 可见页面从其他会话 state_commit 到一致 p95 <= 2s。

可复现复测命令（任务级 Go 缓存放在工作区 `.cache`，结果输出不提交）：

```bash
cd oboard
GOCACHE="$PWD/../.cache/task-mszoydwp/go-build" \\
GOTMPDIR="$PWD/../.cache/task-mszoydwp/go-tmp" \\
go test ./internal/controller -run '^TestConfigurationPerformanceSLO$' -count=1 -v
```

该测试使用预热 SQLite、固定 32 个样本、无外部网络和不执行 Agent 长任务的条件，测量管理 PATCH 从请求开始到响应完成，以及在线 Agent 从配置提交钩子到 WebSocket 收到 `apply_deployment`。2026-08-20 最终本地复测：管理写 p95 `1.71ms`（max `2.08ms`），在线派发 p95 `156.9ms`（max `372.7ms`）；均满足 SLO。CPU profile 命令为 `go test ./internal/controller -run '^$' -bench '^BenchmarkTaskDispatchThroughput$' -benchtime=2s -cpuprofile=../dist/test/configuration-dispatch.pprof`，热点主要是 WebSocket/SQLite syscall 等待，没有同步部署生成阻塞写响应。

相同场景的请求与刷新对照：改造前服务器保存至少是 `PATCH /servers/:id` + 当前页 `GET /page-data`，不会创建 Agent 任务；需要额外的拓扑预读、`POST /deployments/apply` 和部署后 `GET /page-data`，其他会话只能等待粗粒度失效/轮询。改造后保存是一次写请求，响应直接带实体、`desired_revision` 和同步行；实时事件精确标记受影响页面，当前页面只安排一次 600ms 后的后台协调请求，不再调用全局部署接口。`configuration-real-controller-performance.test.tsx` 构建并启动真实 `cmd/controller` 子进程和临时 SQLite，使用真实登录 token 挂载生产 `OBoardAppRoot` 的服务器页面；第二会话连续执行 32 次真实 PATCH，经过 Controller `poll-events`、`page-data` 和 React DOM 提交。测量点是写响应返回的 `state_committed_at` 到 DOM 出现新实体，p95 `803ms`，并断言 32 个页面全部一致；测试退出时终止 Controller 并删除二进制、数据库和专用 Go cache。页面请求已在途时由协调器消费失效，不发重复请求；`ConfigurationSyncStatus` 行为测试实际挂载状态组件并覆盖保存中、同步中、失败重试点击和失败回滚。跨项目 `TestControllerAndAgentProcessesConvergeOfflineSavedConfiguration` 构建并启动真实 Controller 与 Agent 两个二进制，验证离线保存、Agent 重连、签名部署、结果回调、applied 状态持久化及 Controller `synced` 终态。
日志、任务结果和实时事件只暴露版本、状态、资源 ID 和脱敏错误。为协调器增加计数器：提交量、合并量、准备耗时、任务去重量、重试量、失败量、当前 pending 服务器和各阶段延迟分位数。

## 9. 实现顺序与验证

1. 先建立并保留基线和配置影响面清单。
2. 实现存储状态、统一提交钩子、协调 worker、任务合并和恢复扫描。
3. 同步更新 Agent 健康字段/协议和收敛测试（如实际采用该字段）。
4. 接入 Web REST、REST v2/MCP/Changeset 语义，逐类覆盖服务器、拓扑、节点分配和 DNS。
5. 重构 Web mutation/cache/realtime 状态，移除全局手动下发。
6. 使用 profiling 和基准验证性能，不以删除校验、放宽授权或隐藏失败换取速度。
7. 更新协议/设计规范和必要迁移登记，运行 Controller、Agent、Web 及跨边界测试。

完整交付必须证明：单次和快速连续保存、复合拓扑原子性、并发修改、离线重连、Controller 重启、通知丢失、旧任务抑制、失败重试、Agent 漂移修复、UI 成功/回滚/跨会话实时更新均成立。
