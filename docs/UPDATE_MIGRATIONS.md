# OBoard 更新迁移登记

本文登记为了让旧安装进入当前模型而保留的临时代码，包括数据库、配置、文件布局、
服务定义、本地运行状态、更新脚本和线协议迁移。普通功能更新不在这里记录。

目标是回答三个问题：哪个版本引入了什么迁移、哪些旧状态仍可能出现、什么时候可以安全
删除迁移和兼容代码。经过一段时间本身不是删除依据；只要仍允许从更老版本直接升级，
对应迁移就必须保留。

## 维护规则

1. 新增迁移时，在同一任务中添加登记项和从旧状态升级的回归测试。
2. `引入版本` 使用实际发布版本。仅进入开发通道时写
   `dev-<12 位提交>`，并把 `首次稳定版` 标为 `待发布`。
3. 发布稳定版时补全 `首次稳定版`，不得用 `main`、`latest` 或日期代替版本。
4. 迁移必须说明源状态、目标状态、数据影响、重复执行行为和失败行为。
5. 迁移状态只使用 `生效中`、`可移除`、`已移除`。标为 `可移除` 前必须逐项满足
   本文的删除门槛。
6. 删除迁移时保留登记项，填写移除版本和提交，并同时删除只服务于该迁移的测试夹具；
   当前模型的测试不得删除。
7. 恢复旧备份可能重新引入旧状态。数据库迁移的删除判断必须同时覆盖备份恢复入口，
   不能只检查在线更新路径。

本登记从 2026-08-12 开始执行。此前已经存在但尚未补录的迁移代码一律视为 `生效中`；
缺少登记不代表可以删除。移除前必须根据 Git 历史补全引入版本、源状态、测试和删除门槛。

## 删除门槛

一项迁移只有同时满足以下条件才可标为 `可移除`：

- 已知包含该迁移的首次稳定版，且不是可变的 `dev` 构建。
- 产品已经声明并实施最老可直接升级版本；该版本本身已完成此迁移，或者更新器强制旧版本
  先经过一个包含此迁移的中间版本。
- Controller、Agent、kernel、安装脚本和自更新器中所有可能生成源状态的版本均已退出支持范围。
- 支持的备份格式和恢复入口不会再导入源状态；否则恢复必须明确拒绝该备份并给出中转版本。
- 不存在滚动升级、回滚或跨项目版本差异再次生成源状态的路径。
- 已有测试证明当前 schema/配置/协议不依赖兼容分支；删除后完整发布校验通过。
- 移除任务已经列出代码、测试、文档和运维影响，不把数据修复责任静默转给用户。

如果项目尚未实施“最老支持升级版本”检查，数据库 DDL/DML 迁移默认不得仅因时间经过而删除。

## 登记摘要

| ID | 组件 | 类别 | 引入版本 | 首次稳定版 | 状态 | 移除版本 |
|---|---|---|---|---|---|---|
| `controller-db-20260812-connectivity-probe-target` | Controller | SQLite schema | `dev-26cd0a1013d1` | 待发布 | 生效中 | - |
| `controller-db-20260812-latency-probes` | Controller | SQLite schema | `dev-490892a0ae99` | 待发布 | 生效中 | - |
| `controller-db-20260813-unified-latency-probes` | Controller | SQLite schema / data backfill | `dev-0bb8ff77a4e9` | 待发布 | 生效中 | - |
| `controller-db-20260813-resource-history-recording` | Controller | SQLite schema / data lifecycle | `dev-65a552b0616d` | 待发布 | 生效中 | - |
| `controller-db-20260813-extended-load-metrics` | Controller | SQLite schema / data lifecycle | `dev-dbe8bf22ce18` | 待发布 | 生效中 | - |
| `controller-db-20260814-path-stage-routing` | Controller | SQLite schema / data backfill | `dev-718c60af30cf` | 待发布 | 生效中 | - |
| `controller-db-20260815-routing-rule-chains-sync` | Controller | SQLite schema | `dev-01666531f1e3` | 待发布 | 生效中 | - |
| `controller-db-20260817-monitoring-retention-indexes` | Controller | SQLite schema | `dev-326ecbb8110a` | 待发布 | 生效中 | - |
| `controller-db-20260818-node-workspace` | Controller | SQLite schema / data backfill | `dev-7ab2640d5900` | 待发布 | 生效中 | - |
| `controller-db-20260818-telegram-operations` | Controller | SQLite schema / data lifecycle | `dev-61ea3fa84687` | 待发布 | 生效中 | - |
| `controller-db-20260819-configuration-revision-watermark` | Controller | SQLite schema / runtime recovery | `dev-25ab8ae0b776` | 待发布 | 生效中 | - |
| `controller-db-20260821-routing-rule-dns-resolver` | Controller | SQLite schema | `dev-b5b1829cb668` | 待发布 | 生效中 | - |

## 生效中的迁移

### controller-db-20260821-routing-rule-dns-resolver

- **引入日期：** 2026-08-21
- **引入提交：** `OboardProject/oboard@b5b1829cb6680120783f0050739ea4ab7d328846`
- **引入版本：** `dev-b5b1829cb668`
- **首次稳定版：** 待发布
- **所有者：** Controller `internal/store`、`internal/controller`、`internal/core`
- **类别：** SQLite schema
- **原因：** 分流规则需要按手动域名或远程规则集选择 DNS 解析器；路由规则本身需要持久保存用户选择的解析服务。
- **源状态：** `routing_rules` 没有 DNS 解析器字段，Controller 无法为单条规则生成独立 DNS 规则。
- **目标状态：** `routing_rules` 新增非空默认空字符串的 `dns_resolver`；Controller 使用规则匹配与 `rule_set` 生成 `dns.rules`，把命中规则交给所选 DNS 服务器，旧规则保持默认 DNS 行为。
- **实现位置：** `oboard/internal/store/store.go`、`oboard/internal/model/types.go`、`oboard/internal/core/protocols.go`、`oboard/internal/controller/server.go`；Web 编排器与 MCP schema 同步增加字段。
- **更新脚本：** 无专用脚本。Controller 打开 SQLite 时通过 `migrateRoutingRuleScopes` 幂等补列。
- **数据影响：** 新增列，已有规则默认空值，不改变任何现有配置或匹配语义。
- **重复执行：** `pragma_table_info` 检查已存在列后跳过；重复执行不修改业务数据。
- **失败行为：** DDL 失败阻止 Controller 打开数据库，不留下部分配置。
- **回归测试：** `TestRoutingRuleChainAndSyncColumnsMigrateFromPreviousSchema` 从缺少 `dns_resolver` 的旧表启动，验证补列与重复迁移幂等。
- **移除条件：** 在通用删除门槛之外，所有支持数据库和备份恢复路径必须已包含该列，且不存在未保存 DNS 选择的规则。
- **移除状态：** 生效中。

### controller-db-20260819-configuration-revision-watermark

- **引入日期：** 2026-08-19
- **引入提交：** `OboardProject/oboard@25ab8ae0b77646b87f38aa7ce9d461c9effa25c3`；active 套餐/绑定触发器与复合拓扑原子修正：`OboardProject/oboard@570563b836848f00284d42f9d9b8f78ebcc3984f`、`OboardProject/oboard@8462675cf0c40de74a2b9a9fc4a70ff19ea62cec`、`OboardProject/oboard@805f434a70bd40ad1842b827ad3471c1da3600ec`、`OboardProject/oboard@506ce5a7ef87a41442db7322fa206ca5ef2051a3`、`OboardProject/oboard@6e171b3d65a1556313709ddf4f8b670f6fd451e5`
- **引入版本：** `dev-25ab8ae0b776`
- **首次稳定版：** 待发布
- **所有者：** Controller `internal/store`、`internal/controller`
- **类别：** SQLite schema、runtime recovery
- **原因：** 配置写事务提交与异步写入 `configuration_sync_states` 之间存在进程崩溃窗口；同时广义 `routing_cache_revision` 包含探测结果和其他派生数据，不能作为自动部署水位。
- **源状态：** 数据库只有 `routing_cache_revision`；配置写入依赖响应中间件在提交后写协调状态，崩溃后可能存在已保存配置但没有对应协调行。
- **目标状态：** 新增单行 `configuration_revision` 和仅覆盖运行态管理表的 INSERT/UPDATE/DELETE 触发器；包含 active 套餐版本、active 用户套餐绑定及 active 节点例外，draft/pending 生命周期不推进运行态水位；启动协调器按当前水位修复缺失或落后服务器状态，相同水位的 `synced` 状态保持不变。
- **实现位置：** `oboard/internal/store/store.go`、`oboard/internal/store/routing_revision.go`、`oboard/internal/store/configuration_sync.go`；`oboard/internal/controller/configuration_reconciler.go`、`agent_convergence.go`、`realtime.go`、`user_devices.go`。
- **更新脚本：** 无专用脚本。Controller 打开 SQLite 时幂等建表和触发器，启动协调器执行恢复扫描。
- **数据影响：** 新增单行水位和触发器，不改写业务数据；首次启动可能为已有部署基线且缺少协调行的服务器补建 `pending` 状态，随后按当前期望状态收敛。
- **重复执行：** DDL 和触发器使用 `IF NOT EXISTS`；启动修复仅在水位高于已有 `wanted_revision` 或协调行缺失时改变状态，不会重复打开同水位 `synced` 行。
- **失败行为：** DDL/触发器安装失败阻止 Controller 打开数据库；恢复写入失败记录日志并由下一次启动或心跳再次尝试，不丢失已保存期望状态。
- **回归测试：** `TestConfigurationRevisionTracksDesiredWritesAndIgnoresOperationalActivity`、`TestConfigurationRevisionIncludesActivePlanAndBindingTransitions`、`TestDisabledTopologyDraftDoesNotAdvanceRuntimeRevisionUntilActivation`、`TestEnsureConfigurationSyncRevisionDoesNotReopenCurrentState`、`TestConfigurationReconcilerRepairsMissingStateFromDurableRevision`、`TestProxyPathInitialStepCommitsAsOneTopologyUnit`。
- **移除条件：** 在通用删除门槛之外，所有支持数据库和备份恢复路径都必须已包含当前水位；必须确认不存在可提交配置而跳过该触发器的存储路径，并保留崩溃恢复回归测试。
- **移除状态：** 生效中。

### controller-db-20260818-telegram-operations

- **引入日期：** 2026-08-18
- **引入提交：** `OboardProject/oboard@61ea3fa846871ab871a2471d6da3a02adfbec483`
- **引入版本：** `dev-61ea3fa84687`
- **首次稳定版：** 待发布
- **所有者：** Controller `internal/store`、`internal/controller`
- **类别：** SQLite schema、data lifecycle、Controller 启动迁移
- **原因：** Telegram 运维 Bot 需要可防抖的节点失联事件、稳定恢复窗口、可编辑告警会话、
  绑定身份、一次性确认令牌、多实例长轮询租约、订阅隔离和广播逐目标投递状态。
- **源状态：** 数据库没有 `node_incidents`、`node_incident_telegram_messages`、
  `node_incident_actions`、`node_publication_isolations`、`telegram_binding_codes`、
  `telegram_bindings`、`telegram_bot_state`、`operation_confirmations`、
  `notification_broadcasts` 和 `notification_broadcast_targets` 表及对应索引。
- **目标状态：** 新表和索引存在；节点事件以 `active/recovering/resolved` 保存完整生命周期，
  Telegram 会话保存原消息与编辑失败信息，隔离只影响后续订阅渲染，永久移除动作在全量部署
  任务全部成功后才从 `deployment_pending` 变为 `succeeded`；绑定码、确认令牌和 Bot offset
  具有过期、一次性消费和 SQLite 租约语义。
- **实现位置：** `oboard/internal/store/store.go` 的 `Store.migrate`；
  `oboard/internal/store/node_incidents.go` 和 `notification_broadcasts.go` 的持久化、租约、
  状态机与投递；`oboard/internal/controller/node_incidents.go`、`node_incident_api.go`、
  `node_incident_automation.go`、`notification_broadcasts.go` 和 `tgbot.go` 的应用层、Bot、
  REST/MCP 适配与部署结果收敛。
- **更新脚本：** 无专用脚本。Controller 打开 SQLite 时自动执行幂等建表和索引迁移。
- **数据影响：** 仅新增事件、消息、操作、隔离、绑定、确认和广播记录；事件与隔离审计不因
  入口、服务器或用户后续删除而被级联删除。Telegram Bot token 只用于运行时请求和不可逆租约哈希，
  不写入新增持久化模型。临时隔离不停止入口、不改变 Agent 配置、不影响既有连接。
- **重复执行：** 所有 DDL/索引使用 `IF NOT EXISTS`；开放事件按服务器和事件类型唯一复用，
  恢复抖动增加同一事件版本；绑定码、确认令牌和广播使用一次性消费或幂等键；已成功的广播目标
  永不再次投递，Bot offset 由租约持有者单调推进。
- **失败行为：** 任一启动 DDL 失败会阻止 Controller 继续打开数据库；Telegram 编辑失败保留
  原消息错误并在恢复时补发关联消息；广播目标保留失败原因并按有限次数重试；完整部署失败将
  永久移除动作标记为 `failed`，不伪造成功状态。
- **回归测试：** `TestNodeOperationsTablesMigrateFromPreviousSchema` 从真实缺少新增表的旧数据库
  重新打开并验证重复迁移；`TestNodeIncidentLifecycleReusesFlapAndAutoRestoresIsolation` 覆盖事件
  去重、抖动复用、自动恢复和手动隔离保留；Controller 的 `TestNodeIncidentMonitorThresholdAndRecoveryWindow`
  覆盖 2 分钟/5 分钟防抖，`TestNodeIncidentTelegramRecoveryFallsBackWhenEditFails` 覆盖编辑失败回退，
  `TestSubscriptionIsolationHidesOnlySelectedInboundWithoutDeployment` 覆盖订阅过滤和不部署；
  Telegram 绑定、租约和广播幂等/不重复投递由 Store 测试覆盖。
- **移除条件：** 在通用删除门槛之外，所有支持备份和恢复路径必须包含上述表及事件审计数据；
  最老支持 Controller 必须已经生成当前事件/绑定/广播状态，且不再生成临时离线 notice 之外的旧
  Telegram 权限模型。Bot API 长轮询不再允许无绑定 Chat 直接获得操作权限。
- **移除状态：** 生效中。

### controller-db-20260818-node-workspace

- **引入日期：** 2026-08-18
- **引入提交：** `OboardProject/oboard@7ab2640d5900dc2ee4d70b8155bdb117722d9328`
- **引入版本：** `dev-7ab2640d5900`
- **首次稳定版：** 待发布
- **所有者：** Controller `internal/store`
- **类别：** SQLite schema、data backfill、Controller 启动迁移
- **原因：** 节点工作台需要持久保存用户私有节点组、加密第三方来源、加密导入节点和有序组合订阅；
  每个用户还必须具有不可删除的 OBoard 系统组和默认组合，以保持既有无 `profile_id` 订阅 URL
  的输出语义。
- **源状态：** 数据库没有 `node_groups`、`node_sources`、`imported_nodes`、
  `subscription_outputs` 和 `subscription_output_groups`；现有用户没有节点工作台默认资源。
- **目标状态：** 五张表及所有权、名称唯一、默认组合和刷新查询索引存在；每个现有用户拥有一个
  `system_key='oboard'` 的系统组和一个只关联该组的默认组合。新用户在创建事务中初始化相同资源。
- **实现位置：** `oboard/internal/store/store.go` 的 `Store.migrate` 创建表和索引；
  `oboard/internal/store/node_workspace.go` 的 `ensureNodeWorkspaceDefaultsForUser` 负责现有用户回填，
  `BootstrapAdmin` 和 `CreateUser` 在新用户事务内初始化默认资源。
- **更新脚本：** 安装新 Controller 并重启后，由 Controller 打开 SQLite 时自动执行；更新脚本
  没有单独的数据迁移分支。
- **数据影响：** 只新增表、索引和每个现有用户的两个默认资源及一条有序关联；不复制现有 OBoard
  节点、不改写用户套餐、例外、订阅凭证或审计数据。第三方来源和节点配置只在后续导入时以
  `OBOARD_SESSION_SECRET` 加密写入。
- **重复执行：** 表和索引使用 `IF NOT EXISTS`；默认组、默认组合和初始关联均通过所有权内的
  `NOT EXISTS` 条件补建，重复启动不会产生重复资源或改变用户已保存的组合顺序。
- **失败行为：** 任一 DDL 或默认资源补建失败会阻止 Controller 打开数据库；新用户初始化失败
  会回滚该用户创建事务，不会留下缺少默认节点资源的半成品账号。
- **回归测试：** `TestNodeWorkspaceMigratesPreviousSchemaAndInitializesDefaults` 从真实缺少五张新表的
  旧 schema 重新打开数据库，验证表、默认资源和重复迁移；Store 测试同时覆盖新用户事务初始化。
- **移除条件：** 在通用删除门槛之外，最老支持数据库和所有可恢复备份必须已经包含五张表及默认
  资源；恢复入口不得再导入该源 schema，所有受支持的新用户创建路径也必须直接写入当前模型。
- **移除状态：** 生效中。

### controller-db-20260812-connectivity-probe-target

- **引入日期：** 2026-08-12
- **引入提交：** `OboardProject/oboard@26cd0a1013d104a0cec7331618de7386e64e9f73`
- **引入版本：** `dev-26cd0a1013d1`
- **首次稳定版：** 待发布
- **所有者：** Controller `internal/store`
- **类别：** SQLite schema，Controller 启动迁移
- **原因：** 连通性探测目标功能开始读取
  `server_telemetry.connectivity_probe_target`。首次实现只更新了新建表定义和查询，旧数据库
  缺少该列时，所有依赖 `ListServers` 的 `/api/v2/ui/page-data` 请求返回 500。
- **源状态：** `server_telemetry` 表存在，但没有 `connectivity_probe_target` 列。
- **目标状态：** 增加非空 `TEXT` 列 `connectivity_probe_target`，默认值为 `auto`；已有行
  使用 `auto`，由 Controller 根据服务器区域解析实际探测目标。
- **实现位置：** `oboard/internal/store/store.go`，`Store.migrate` 的
  `serverTelemetryColumns` 清单，通过 `ensureColumn` 幂等执行。
- **更新脚本：** `scripts/update.sh` 没有专用修复分支。脚本安装新 Controller 并重启后，
  Controller 在打开数据库时执行迁移。
- **数据影响：** 不删除、不重写已有 telemetry 数据，只增加带默认值的列。
- **重复执行：** 列已存在时不执行 `ALTER TABLE`，可安全重复启动。
- **失败行为：** 数据库迁移失败会让 Controller 启动失败并记录具体 SQLite 错误，不以缺列状态
  继续提供 API。
- **回归测试：** `TestConnectivityProbeTargetMigratesFromPreviousSchema` 先移除该列模拟旧库，
  然后重新打开数据库，验证服务器列表读取和新字段写入。
- **移除条件：** 在通用删除门槛之外，最老支持的 Controller 数据库必须已包含该列；所有支持的
  备份也必须包含该列，或者恢复入口必须拒绝更老备份并指出可执行该迁移的中转版本。
- **移除状态：** 生效中。

### controller-db-20260812-latency-probes

- **引入日期：** 2026-08-12
- **引入提交：** `OboardProject/oboard@302964f672e7e43f651a8a809659f058668e73d3`
- **引入版本：** `dev-490892a0ae99`
- **首次稳定版：** 待发布
- **所有者：** Controller `internal/store`
- **类别：** SQLite schema，Controller 启动迁移
- **原因：** 区域延迟测试需要持久保存每台服务器的筛选设置、资源版本和历史采样结果。
- **源状态：** 数据库没有 `server_latency_probe_settings` 和
  `server_latency_probe_results` 表。
- **目标状态：** 两张表及服务器时间倒序索引存在；旧服务器按关闭、300 秒、3 个样本、
  全部地区运营商和最多 64 个目标的默认设置读取。
- **实现位置：** `oboard/internal/store/store.go`，`Store.migrate` 的幂等建表语句；
  `oboard/internal/store/latency_probe.go` 负责设置和结果读写。
- **更新脚本：** 安装新 Controller 并重启后，由 Controller 打开数据库时自动执行。
- **数据影响：** 只新增表和索引，不重写现有服务器、遥测或任务数据；结果生命周期由当前的
  `server_monitoring_retention_days` 全局设置统一控制。
- **重复执行：** 使用 `CREATE TABLE/INDEX IF NOT EXISTS`，可安全重复启动。
- **失败行为：** 数据库迁移失败会让 Controller 启动失败并记录 SQLite 错误，不在缺表状态
  下继续调度延迟任务。
- **回归测试：** `TestLatencyProbeTablesMigrateFromPreviousSchema` 从缺少两张表的数据库启动，
  验证表被重新创建；`TestLatencyProbeSettingsAndResultsRoundTrip` 验证当前模型读写。
- **移除条件：** 在通用删除门槛之外，最老支持的数据库和所有可恢复备份必须已包含这两张表，
  或恢复入口必须拒绝更老备份并指出可执行迁移的中转版本。
- **移除状态：** 生效中。

### controller-db-20260813-unified-latency-probes

- **引入日期：** 2026-08-13
- **引入提交：** `OboardProject/oboard@0bb8ff77a4e9dc43f101a439d86a6507fde5c30d`
- **引入版本：** `dev-0bb8ff77a4e9`
- **首次稳定版：** 待发布
- **所有者：** Controller `internal/store`
- **类别：** SQLite schema、data backfill、Controller 启动迁移
- **原因：** 公网可用性检测和地区延迟检测合并为一个延迟测试模型。当前设置需要测试方式、
  公网目标和精确的省份运营商组合；离线补报需要稳定的报告标识，SLA 需要记录主控连接与
  断开事件。
- **源状态：** `server_latency_probe_settings` 只有独立的省份数组和运营商数组，无法表达
  精确组合；`server_latency_probe_results` 没有 `report_id`、目标类型、测试方式、主机和端口；
  公网开关和目标保存在 `server_telemetry`；`server_connectivity_events.kind` 的约束不接受
  当前连接事件。
- **目标状态：** 设置表具有 `mode`、`public_target` 和 `regions_json`；结果表具有
  `report_id`、`kind`、`mode`、`host` 和 `port`，并为非空报告建立
  `(server_id, report_id, probe_id)` 唯一索引；旧公网开关和目标迁入统一设置，测试方式为
  TCP、周期为 60 秒。旧省份数组与运营商数组可能代表笛卡尔组合，无法无损推断用户原意，
  因此迁为明确的空组合，只保留公网目标。连接事件表约束允许
  `probe_target_changed`、`controller_connected` 和 `controller_disconnected`。
- **实现位置：** `oboard/internal/store/store.go` 的 `Store.migrate` 和
  `ensureConnectivityEventKinds`；`oboard/internal/store/latency_probe.go` 的
  `migrateUnifiedLatencyProbeSettings`。
- **更新脚本：** 安装新 Controller 并重启后，由 Controller 打开数据库时自动执行；更新脚本
  没有单独的数据修复分支。
- **数据影响：** 新增设置与结果列和部分唯一索引；旧地区结果保留并按地区 ICMP 结果读取；
  设置回填会继承旧公网开关和目标，清空无法无损转换的笛卡尔筛选。连接事件表在事务内重建，
  保留已有事件的 ID、时间和去重键。
- **重复执行：** DDL 通过列检查、`CREATE UNIQUE INDEX IF NOT EXISTS` 和事件约束检查幂等；
  设置回填使用 `app_settings` 键
  `migration.controller-db-20260813-unified-latency-probes`，成功提交后不再重复重写用户设置。
- **失败行为：** 设置回填和事件表重建各自在事务内回滚；任何 DDL、DML 或索引创建失败都会
  阻止 Controller 打开数据库，不会在新旧模型混合状态下继续运行。
- **回归测试：** `TestLatencyProbeTablesMigrateFromPreviousSchema` 从真实旧设置表和结果表启动，
  验证列、回填和旧结果保留；`TestConnectivityProbeTargetMigratesFromPreviousSchema` 验证旧 telemetry
  缺列恢复；`TestConnectivityProbeTargetEventConstraintUpgrade` 验证旧事件约束升级；
  `TestLatencyProbePublicResultDeduplicatesAndKeepsNewestCurrentState` 验证新唯一键和乱序补报状态。
- **移除条件：** 在通用删除门槛之外，最老支持的数据库和所有可恢复备份必须已经具有统一设置、
  结果列、唯一索引、回填标记和当前连接事件约束；恢复入口不得再导入独立省份/运营商筛选或
  缺少报告标识的当前结果。当前支持的 Controller 与 Agent 组合也不得再生成旧事件或旧设置模型。
- **移除状态：** 生效中。

### controller-db-20260813-resource-history-recording

- **引入日期：** 2026-08-13
- **引入提交：** `OboardProject/oboard@65a552b0616d5b13bbecf500226e3f2d836795dd`
- **引入版本：** `dev-65a552b0616d`
- **首次稳定版：** 待发布
- **所有者：** Controller `internal/store`
- **类别：** SQLite schema、data lifecycle、Controller 启动迁移
- **原因：** 每台服务器需要独立控制是否保存 CPU 和内存历史；关闭后仍需保留最新实时值和
  原有网络、流量、连通性样本。
- **源状态：** `server_telemetry` 没有 `resource_history_enabled`，
  `server_metric_samples` 没有 `resource_recorded`；已有样本全部包含 CPU 和内存值且无法区分
  是否允许作为历史返回。
- **目标状态：** telemetry 设置和每条样本分别具有非空布尔列；旧服务器和旧样本默认启用并
  视为已记录。关闭设置时，已有样本的 CPU、已用内存和总内存字段在同一事务内清零并标记为
  未记录；后续样本继续保存网络、流量和连通性字段，但资源字段保持为零。服务器表中的最新
  CPU 和内存值继续由 Agent 健康报告更新，供实时界面读取。
- **实现位置：** `oboard/internal/store/store.go` 的 `Store.migrate`、
  `updateServerTelemetrySettingsWithTransition`、`UpdateServerTelemetryReport` 和
  `ListServerResourceMetricPoints`。
- **更新脚本：** 安装新 Controller 并重启后，由 Controller 打开数据库时自动执行；更新脚本
  没有单独的数据修复分支。
- **数据影响：** 升级只新增带默认值的列，不重写旧样本。管理员首次关闭某台服务器的资源历史
  时会不可逆地清除该服务器现有样本中的 CPU 和内存历史值，但不会删除样本行或其他遥测数据。
- **重复执行：** DDL 通过列检查幂等；列已存在时不再执行 `ALTER TABLE`。关闭状态重复保存不会
  再次清理，关闭期间的新样本始终写入未记录标记和零资源值。
- **失败行为：** DDL 失败会阻止 Controller 打开数据库。设置变更与历史清理共享 SQLite 事务，
  任一写入失败都会回滚，不会出现设置已关闭但旧资源历史仍可查询的混合状态。
- **回归测试：** `TestServerResourceHistoryColumnsMigrateEnabledFromPreviousSchema` 从缺少两列的
  旧数据库启动并验证旧服务器和样本默认启用；
  `TestServerResourceHistoryCanBeDisabledWithoutStoppingNetworkSamples` 验证关闭时清理资源历史、
  继续写入网络样本并保留最新实时资源值。
- **移除条件：** 在通用删除门槛之外，最老支持的数据库和所有可恢复备份必须已包含两列；恢复
  入口不得再导入无法表达资源记录授权的样本。所有受支持 Controller 版本必须保证关闭设置时
  清理历史并阻止后续资源字段落盘。
- **移除状态：** 生效中。

### controller-db-20260813-extended-load-metrics

- **引入日期：** 2026-08-13
- **引入提交：** `OboardProject/oboard@dbe8bf22ce18d404b3581fb910e47680880cba3d`
- **引入版本：** `dev-dbe8bf22ce18`
- **首次稳定版：** 待发布
- **所有者：** Controller `internal/store`
- **类别：** SQLite schema、data lifecycle、Controller 启动迁移
- **原因：** 统一服务器监控需要在 CPU、内存和网络之外显示磁盘总量、TCP/UDP 连接数和进程数，
  并在启用负载历史时提供相同时间窗口的趋势。
- **源状态：** `servers` 只有表示磁盘已用量的 `disk_bytes`；`server_metric_samples` 没有磁盘、
  TCP/UDP 连接数或进程数字段；资源样本最多保留 48 小时。
- **目标状态：** `servers` 增加非空的磁盘总量、TCP/UDP 连接数和进程数列；样本表增加磁盘已用量、
  磁盘总量、TCP/UDP 连接数和进程数列，全部默认零。新健康报告更新当前值，启用负载历史时保存
  对应样本，关闭时新样本保持为零且关闭操作会清理旧样本中的全部负载字段。样本生命周期由
  `server_monitoring_retention_days` 统一控制，默认 7 天，可设置为 1 至 30 天。
- **实现位置：** `oboard/internal/store/store.go` 的 `Store.migrate`、`UpdateServerRuntimeState`、
  `UpdateServerTelemetryReport`、`updateServerTelemetrySettingsWithTransition` 和
  `ListServerResourceMetricPoints`。
- **更新脚本：** 安装新 Controller 并重启后，由 Controller 打开数据库时自动执行；更新脚本
  没有单独的数据修复分支。
- **数据影响：** 升级新增九个带零默认值的整数列，不回填无法从旧数据推导的指标。后续健康报告
  开始写入当前值和样本；Controller 数据库维护按当前统一保留期删除旧样本。管理员关闭负载历史会不可逆地
  清除该服务器样本中的 CPU、内存、磁盘、连接和进程值，但保留网络、流量和连通性字段。
- **重复执行：** 每列通过 `ensureColumn` 检查后幂等添加；已有列不会重复执行 `ALTER TABLE`。
  数据库维护按全局时间边界分批删除，可安全重复执行。
- **失败行为：** 任一 DDL 失败会阻止 Controller 打开数据库。遥测写入和历史开关清理仍保持原有
  事务边界；保留期维护失败会记录错误，并在下一次维护周期重试。
- **回归测试：** `TestServerResourceHistoryColumnsMigrateEnabledFromPreviousSchema` 从缺少全部扩展
  资源列的真实旧表结构重新启动，验证列被创建且当前值可写；
  `TestServerResourceHistoryCanBeDisabledWithoutStoppingNetworkSamples` 验证扩展负载样本可查询，
  关闭历史后全部负载字段清零而网络样本继续写入。
- **移除条件：** 在通用删除门槛之外，最老支持的数据库和所有可恢复备份必须已经包含九个新增列；
  恢复入口不得再导入缺少这些列的 schema。当前支持的 Controller 与 Agent 组合不得再生成缺少
  扩展负载字段的当前健康模型。
- **移除状态：** 生效中。

### controller-db-20260817-monitoring-retention-indexes

- **引入日期：** 2026-08-17
- **引入提交：** `OboardProject/oboard@326ecbb8110adfac9c9b132783b46bd7e207da81`
- **引入版本：** `dev-326ecbb8110a`
- **首次稳定版：** 待发布
- **所有者：** Controller `internal/store`
- **类别：** SQLite schema，Controller 启动迁移
- **原因：** 资源样本、地区延迟和公网探测事件改为由数据库维护统一执行可配置保留期清理；
  全局时间边界删除需要独立于服务器查询索引，避免随着监控历史增长而全表扫描。
- **源状态：** `server_latency_probe_results` 没有仅按 `checked_at` 排序的索引；
  `server_connectivity_events` 没有按 `kind,effective_at` 排序的索引。
- **目标状态：** 存在 `idx_server_latency_probe_results_checked` 和
  `idx_server_connectivity_events_kind_time`，分别支持地区延迟和公网探测事件的全局生命周期维护。
- **实现位置：** `oboard/internal/store/store.go`，`Store.migrate` 的幂等索引创建语句；事件约束表
  重建时也同步创建当前事件索引。
- **更新脚本：** 安装新 Controller 并重启后，由 Controller 打开 SQLite 时自动执行；更新脚本
  没有单独的数据迁移阶段。
- **数据影响：** 只新增两个索引，不重写或删除现有监控数据。后续数据库维护才会按管理员设置的
  1 至 30 天保留期分批清理数据，并为每台服务器保留截止点前最后一条公网探测事件作为 SLA 基线。
- **重复执行：** 启动迁移使用 `CREATE INDEX IF NOT EXISTS`；事件表重建在事务内创建同名当前索引，
  重复启动不会改写监控数据。
- **失败行为：** 索引创建失败会阻止 Controller 打开数据库并记录 SQLite 错误，不会在缺少维护索引
  的状态下继续运行。
- **回归测试：** `TestServerMonitoringMaintenanceIndexesMigrateFromPreviousSchema` 从缺少两个索引的
  上一版真实 schema 启动，验证索引创建以及重复迁移。
- **移除条件：** 在通用删除门槛之外，最老支持数据库和所有可恢复备份必须已经包含两个索引；
  恢复入口不得再导入缺少索引的 schema，且受支持 Controller 必须只通过当前统一维护路径执行清理。
- **移除状态：** 生效中。

### controller-db-20260814-path-stage-routing

- **引入日期：** 2026-08-14
- **引入提交：** `OboardProject/oboard@718c60af30cf4f6b920235dfcb1164bf2ec921f8`
- **引入版本：** `dev-718c60af30cf`
- **首次稳定版：** 待发布
- **所有者：** Controller `internal/store`
- **类别：** SQLite schema、data backfill、Controller 启动迁移
- **原因：** 分流规则需要绑定到单一代理分支的具体受控节点，并复用可刷新的远程规则集；原表只能
  用服务器和数字优先级表达全局规则，无法保证 A/B/C 节点内顺序或分支隔离。
- **源状态：** `routing_rules` 只有服务器、优先级、内联匹配和动作字段；不存在
  `routing_rule_sets`，旧规则没有作用域、路径节点、列表位置或匹配来源。
- **目标状态：** `routing_rules` 增加 `scope`、`proxy_path_id`、`stage_step_id`、
  `sort_position`、`match_source` 和 `rule_set_id`；旧行统一回填为 `server` 作用域及 `inline`
  匹配。新增 `routing_rule_sets` 保存 HTTPS 来源、条件请求元数据、最后成功快照、SHA-256 revision、
  刷新状态和错误；新编排器只创建 `path_stage` 规则。
- **实现位置：** `oboard/internal/store/store.go` 的 `Store.migrate` 和
  `migrateRoutingRuleScopes`，以及 `oboard/internal/store/routing_rule_sets.go`。
- **更新脚本：** 安装新 Controller 并重启后，由 Controller 打开 SQLite 时自动执行；更新脚本
  没有单独的数据迁移阶段。
- **数据影响：** 旧分流规则的匹配和动作保持不变，只增加默认作用域；不自动把旧服务器规则绑定
  到任何代理分支。新增规则集表为空，不进行远程抓取，直到操作员创建规则集。
- **重复执行：** 建表和索引使用 `IF NOT EXISTS`；每列在同一事务内检查后添加，回填只处理空作用域，
  已迁移数据库重复启动不改写现有节点规则。
- **失败行为：** 任一 DDL、回填或索引创建失败都会阻止 Controller 打开数据库；列扩展和回填事务
  整体回滚，不以新旧字段混合状态运行。
- **回归测试：** `TestRoutingRuleScopeMigrationFromPreviousTable` 从真实旧 `routing_rules` 表结构启动，
  验证列创建、旧行回填和重复迁移；`TestRoutingRuleSetReuseDeleteProtectionAndAtomicPlacement` 验证引用外键、
  跨节点原子排序及失败回滚。
- **移除条件：** 在通用删除门槛之外，最老支持数据库和所有可恢复备份必须已经包含新表和全部六列；
  恢复入口不得再导入只有服务器优先级的旧表。所有受支持 Controller 必须只写显式作用域，且 Agent
  资产协议必须已稳定支持 `routing_rule_set`。
- **移除状态：** 生效中。

### controller-db-20260815-routing-rule-chains-sync

- **引入日期：** 2026-08-15
- **引入提交：** `OboardProject/oboard@01666531f1e34ab869f4df68c95e1d403b418c99`
- **引入版本：** `dev-01666531f1e3`
- **首次稳定版：** 待发布
- **所有者：** Controller `internal/store`
- **类别：** SQLite schema、Controller 启动迁移
- **原因：** 单条分流规则需要拥有可继续连接服务器、外部节点和后续跳点的独立代理链路；相同匹配
  条件还需要在不同路径阶段间选择一次性复制或持续同步，而不能改变各放置点的出口动作和兜底链路。
- **源状态：** `routing_rules` 已能表达路径阶段和顺序，但没有规则专属目标路径或同步组；每条规则
  只能直接选择既有出口动作，跨阶段复用后也无法持续同步名称和匹配条件。
- **目标状态：** `routing_rules` 增加可空的 `target_proxy_path_id` 和非空的 `sync_group_id`。
  `target_proxy_path_id` 指向与当前规则共享前缀后发生分岔的代理路径；同一同步组事务性传播 `name`、
  `match_source`、`rule_set_id` 和 `match_json`，同时保留每个放置点各自的动作、目标代理路径、启用状态、
  阶段和顺序。未加入同步组的复用规则是一次性独立副本。
- **实现位置：** `oboard/internal/store/store.go` 的 `Store.migrate`、
  `migrateRoutingRuleScopes`、`CreateSyncedRoutingRule` 和 `UpdateRoutingRule`。
- **更新脚本：** 安装新 Controller 并重启后，由 Controller 打开 SQLite 时自动执行；更新脚本
  没有单独的数据迁移阶段。
- **数据影响：** 只新增两列和对应索引。旧规则的匹配、动作、阶段和顺序保持不变，目标路径为空且
  同步组为空，因此升级不会自动改变任何已有流量出口，也不会把旧规则加入同步关系。
- **重复执行：** 两列在同一事务内检查后添加，索引使用 `IF NOT EXISTS`；已迁移数据库重复启动
  不重写规则。同步创建和条件更新各自在单一事务内提交。
- **失败行为：** 列扩展或索引创建失败会阻止 Controller 打开数据库；同步组创建或条件传播任一步
  失败都会回滚该次事务，不留下只同步部分规则的状态。
- **回归测试：** `TestRoutingRuleChainAndSyncColumnsMigrateFromPreviousSchema` 从缺少两列和索引的
  上一版真实 schema 启动，验证列、索引和重复迁移；
  `TestSyncedRoutingRulesShareMatchesButKeepIndependentActions` 验证同步条件的原子传播、独立动作保留及
  一次性副本隔离。
- **移除条件：** 在通用删除门槛之外，最老支持数据库和所有可恢复备份必须已包含两列；恢复入口
  不得再导入缺少规则目标路径或同步组的 schema。所有受支持 Controller 必须仅按当前模型生成规则
  专属路径，并保证同步组不传播放置点动作和拓扑。
- **移除状态：** 生效中。

## 新登记模板

复制以下条目到“生效中的迁移”，并在“登记摘要”增加一行：

```markdown
### <component>-<category>-<YYYYMMDD>-<short-name>

- **引入日期：** YYYY-MM-DD
- **引入提交：** `owner/repository@<full-commit>`
- **引入版本：** `vX.Y.Z` 或 `dev-<12 位提交>`
- **首次稳定版：** `vX.Y.Z` 或 `待发布`
- **所有者：** Controller / Web / Agent / kernel / relay / installer / updater
- **类别：** SQLite schema / data backfill / config / filesystem / service / runtime state / wire protocol
- **原因：** <为什么当前模型需要迁移>
- **源状态：** <迁移前可识别状态>
- **目标状态：** <迁移完成后的唯一当前状态>
- **实现位置：** `<仓库/路径>`，`<函数、命令或脚本阶段>`
- **更新脚本：** <是否由 install/update/updater 调用，或者由进程启动执行>
- **数据影响：** <新增、重写、删除、权限或停机影响>
- **重复执行：** <幂等键、版本门或重复执行结果>
- **失败行为：** <回滚、停止启动、重试或人工处理方式>
- **回归测试：** `<从真实旧状态升级的测试名>`
- **移除条件：** <通用门槛之外的具体可验证条件>
- **移除状态：** 生效中
```

移除时把状态改为 `已移除`，并追加：

```markdown
- **移除版本：** `vX.Y.Z`
- **移除提交：** `owner/repository@<full-commit>`
- **移除依据：** <最老升级版本、备份下限和验证证据>
```
