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
| `controller-db-20260829-server-cpu-cores` | Controller | SQLite schema / wire protocol | `dev-0fc17b734fa3` | 待发布 | 生效中 | - |
| `controller-db-20260829-server-display-tags` | Controller | SQLite schema | `dev-5e3028465dda` | 待发布 | 生效中 | - |
| `controller-db-20260829-subscription-client-templates` | Controller | SQLite schema | `dev-57aafe877b1c` | 待发布 | 生效中 | - |
| `controller-db-20260828-traffic-policy-revision-triggers` | Controller | SQLite schema / trigger rebuild | `dev-57aafe877b1c` | 待发布 | 生效中 | - |
| `controller-db-20260828-remove-vless-tls-vision` | Controller | SQLite seed / data backfill | `dev-fa658a2d2473` | 待发布 | 生效中 | - |
| `controller-db-20260828-update-fleet` | Controller | SQLite schema / data backfill | `dev-4d9ba516be1d` | 待发布 | 生效中 | - |
| `controller-db-20260825-port-forward-external-target` | Controller | SQLite schema | `dev-e2a63295bcfc` | 待发布 | 生效中 | - |
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
| `controller-db-20260822-server-expiry` | Controller | SQLite schema | `dev-49c99f6415e7` | 待发布 | 生效中 | - |
| `controller-db-20260822-subscription-output-filters` | Controller | SQLite schema | `dev-e8a8239c3cd79` | 待发布 | 生效中 | - |
| `controller-db-20260824-anytls-padding-presets` | Controller | SQLite seed / data backfill | `dev-8e5b40cc790e` | 待发布 | 生效中 | - |
| `controller-db-20260823-node-presets` | Controller | SQLite schema / seed | `dev-936aac8ad0f2` | 待发布 | 生效中 | - |
| `controller-db-20260825-server-traffic-quota` | Controller | SQLite schema | `dev-05b18611eabf` | 待发布 | 生效中 | - |
| `controller-db-20260825-transport-tfo-capability` | Controller | SQLite schema | `dev-05b18611eabf` | 待发布 | 生效中 | - |
| `controller-db-20260831-server-service-start-and-traffic-reset` | Controller | SQLite schema | `dev-1584fa60fd17` | 待发布 | 生效中 | - |
| `controller-db-20260826-preset-tfo-defaults` | Controller | SQLite seed / data backfill | `dev-4ef9a80efa97` | 待发布 | 生效中 | - |
| `controller-db-20260826-hy2-salamander-presets` | Controller | SQLite seed / data backfill | `dev-c8f4a4dd07dd` | 待发布 | 生效中 | - |
| `controller-db-20260827-remote-access` | Controller | SQLite schema | `dev-82937c69f06c` | 待发布 | 生效中 | - |
| `controller-db-20260828-traffic-ledger-v2` | Controller | SQLite schema / wire protocol | `dev-5fedab310ae8` | 待发布 | 生效中 | - |
| `controller-db-20260829-plan-reconcile` | Controller | SQLite schema / runtime | `dev-a994c031245a` | 待发布 | 生效中 | - |
| `controller-db-20260830-family-split-templates` | Controller | SQLite schema / data backfill | `dev-pending` | 待发布 | 生效中 | - |

## 生效中的迁移

### controller-db-20260830-family-split-templates

- **引入日期：** 2026-08-30
- **引入提交：** `OboardProject/oboard@dev-pending`（待发布前以开发通道计）
- **引入版本：** `dev-pending`
- **首次稳定版：** 待发布
- **所有者：** Controller `internal/store`、`internal/model`、`internal/core`、`internal/controller`、`internal/capability`、`internal/application`、Web
- **类别：** SQLite schema / data backfill
- **原因：** `family_split` 从同入口兄弟分支改为可复用双栈模板，避免每条规则复制一套 IPv4/IPv6 路径，并让模板接到任意阶段。
- **源状态：** `routing_rules` 使用 `ipv4_target_proxy_path_id` / `ipv6_target_proxy_path_id`；`proxy_paths.inbound_id` NOT NULL；无 `family_split_templates`，无 `proxy_paths.template_id` / `family`。
- **目标状态：** 表 `family_split_templates`；`routing_rules.family_split_template_id`；`proxy_paths.inbound_id` 可空；`kind=family_branch` 且 `(template_id, family)` 唯一；启用的 `family_split` 引用计数打开分支；旧兄弟路径后缀跳数复制到新模板后保留原路径；删除 `ipv4_target_proxy_path_id` / `ipv6_target_proxy_path_id`。
- **实现位置：** `oboard/internal/store/family_split_templates.go`、`oboard/internal/store/store.go`（建表）、`oboard/internal/core/family_split.go`、`oboard/internal/controller/family_split_templates.go`、`oboard/internal/capability/catalog_traffic.go`
- **更新脚本：** 无专用脚本。Controller 打开 SQLite 时 `migrateFamilySplitTemplates`：建表、补列、重建可空 `inbound_id`、回填模板、删旧列。
- **数据影响：** 每条旧 `family_split` 规则生成一份模板并复制分流后后缀跳数；不删除旧兄弟分支；新库不再包含 ipv4/ipv6 目标列。
- **重复执行：** 建表/补列/删列幂等；已绑定 `family_split_template_id` 的规则不再回填。
- **失败行为：** 迁移失败阻止打开数据库。
- **回归测试：** `TestFamilySplitTemplatesMigrateFromSiblingBranchSchema`、`TestRoutingRuleFamilyDNSStrategyMigratesFromPreviousSchema`、`TestFamilySplitRoutingRuleRESTLifecycle`、`TestFamilySplitRoutingRuleCapabilityAndResourceFilter`、`TestFamilySplitOutboundsForceTargetEntryFamilies`、`TestSubscriptionFamilySplitEmitsSingleLogicalNode`
- **移除条件：** 最老支持数据库与所有可恢复备份已包含模板表且不再出现 ipv4/ipv6 目标列；恢复入口不得导入缺少该表的旧库。
- **移除状态：** 生效中

### controller-db-20260829-plan-reconcile

- **引入日期：** 2026-08-29
- **引入提交：** `OboardProject/oboard@a994c031245a`（待发布前以开发通道计）
- **引入版本：** `dev-a994c031245a`
- **首次稳定版：** 待发布
- **所有者：** Controller `internal/store`、`internal/controller`、`internal/model`、Web
- **类别：** SQLite schema / runtime
- **原因：** 套餐系统需从“pending 互斥锁”改为“控制面即时保存、数据面异步收敛”。原 `pending_revision_id` 既是最新保存又是编辑锁，导致后台部署期间无法连续保存、拖拽排序被阻塞、全量目录加载过重。需彻底分离 Desired（latest）/ Applied（current）/ Reconciling（后台协调）三态。
- **源状态：** `subscription_plans.pending_revision_id` 非空时 `CreatePlanVersion` 直接返回 `ErrPlanVersionApplying`，前端据此禁用编辑与排序；`ActivatePlanVersionGuarded` 要求 `pending==candidate` 且递增 `lock_version`，导致后台激活与前台编辑产生虚假 409；无独立协调表，保存即同步创建 `access_change`，快速编辑产生部署风暴；详情页为拿节点名而全量拉取 `assignable-nodes`。
- **目标状态：** 新增 `subscription_plan_reconcile_states(plan_id PK, applying_revision_id, status, last_access_change_id, blocked_reason, blocked_json, attempt_count, created_at, updated_at)`；`pending_revision_id` 重定义为后台正在处理的版本，不再作为编辑锁；`CreatePlanVersion` 始终基于 `latest_revision_id` 创建新不可变版本，`latest` 可领先 `current` 任意多个版本，后台通过 `PlanReconciler` 追最新（`Current → Latest`，中间版本仅保留历史）；`ActivatePlanVersionGuarded` 仅校验 `current==expected` 且 candidate 归属 plan，不再校验 `pending`，不再递增 `lock_version`（`lock_version` 仅由管理员/MCP 编辑递增）；取消 `access_change` 不回滚 `latest`；详情页直接返回富化节点与 `current/latest/reconcile_state`，前端通过 `usePlanMutationQueue` 乐观更新、请求串行、409 自动 rebase 一次。
- **实现位置：** `oboard/internal/store/store.go`（建表、迁移 `MigratePlanReconcileStates`）、`oboard/internal/store/plans.go`（`CreatePlanVersion` 去锁、`ActivatePlanVersionGuarded` 去锁与去 `lock_version`）、`oboard/internal/store/plan_reconcile.go`（`subscription_plan_reconcile_states` CRUD）、`oboard/internal/model/types.go`（`PlanReconcileState`）、`oboard/internal/controller/plan_reconciler.go`（新增 `StartSubscriptionPlanReconciler`、`signalPlanReconcile`、`reconcileOnePlan`）、`oboard/internal/controller/plans.go`/`plan_membership_rules.go`/`access_changes.go`（保存后仅 `signalPlanReconcile`，有空闲时才立即创建 `plan_publish`，否则由协调器合并）、`oboard/cmd/controller/main.go`（启动协调器）、`oboard/web/src/pages/SubscriptionPlansPage.tsx`（去 `applying` 禁用、去 `membershipChanged` 拖拽阻塞、详情不拉全目录、拖拽与增删即时保存、409 重试）、`oboard/web/src/components/node-ordering/PlanNodeOrderingPanel.tsx`（去 `applying` 禁用）、`oboard/web/src/hooks/usePlanMutationQueue.ts`。
- **更新脚本：** 无专用脚本。Controller 打开 SQLite 时 `create table if not exists` 并执行 `MigratePlanReconcileStates`：将既有 `pending_revision_id` 迁移为 `reconcile_states.applying_revision_id` 与状态（`preparing/activating/finalizing/failed/queued` 取自 `access_changes`）。
- **数据影响：** 新增协调表，历史 `pending` 保留为 `applying` 指针；不删除历史版本，不改写 `lock_version` 历史；`current` 仅通过安全 `prepare/activate/finalize` 推进，未部署节点不进入用户订阅。
- **重复执行：** `create table if not exists` 与 `MigratePlanReconcileStates` 按 `plan_id` 幂等；重复打开不覆盖已迁移状态；`CreatePlanVersion` 对 `failed` 的旧 `pending` 自动 `cancelled` 并清 `pending`，对 `active` 的 `pending` 保留原指针仅更新 `latest`。
- **失败行为：** 建表或迁移失败阻止打开数据库；协调器失败记 `failed` 状态，等待下一次保存或 `recovery` 扫描（1 秒）重试；`Activate` 冲突返回 `ErrPlanRevisionConflict` 由协调器重试。
- **回归测试：** `TestSubscriptionPlanCRUDAndNodeVersions`（连续保存、激活不 bump 锁、自动追最新）、`TestPlanVersionActivationConflictKeepsCurrent`（`pending` 不再校验）、`TestPlanVersionChangeClassification`（排序不再被 `pending` 阻塞）、`TestFailedPendingPlanVersionIsSupersededByNextSave` / `TestSavingSameFailedPlanVersionQueuesFreshDeployment`（`failed` 自动 supersede）、`SubscriptionPlansPage.test.tsx`（详情不拉全目录、即时保存、拖拽可继续）。
- **移除条件：** 最老支持数据库与所有可恢复备份已包含 `subscription_plan_reconcile_states` 且 `pending_revision_id` 不再承担编辑锁语义；恢复入口不得导入缺少该表的旧库；`ErrPlanVersionApplying` 对普通 `CreatePlanVersion` 的使用已完全移除。
- **移除状态：** 生效中

### controller-db-20260829-server-cpu-cores

- **引入日期：** 2026-08-29
- **引入提交：** `OboardProject/oboard@0fc17b734fa3639742066ccbd8f88427bd622a27`（Agent `OboardProject/oboard-agent@f3e0f38677f9875e58fed58d558cfff3de24d1cf`）
- **引入版本：** `dev-0fc17b734fa3`
- **首次稳定版：** 待发布
- **所有者：** Controller `internal/store`、`internal/model`、`internal/controller`、Agent `internal/agent`、`internal/model`、Web
- **类别：** SQLite schema / wire protocol
- **原因：** 服务器卡片 CPU 副标题需要逻辑核心数；此前 Web 从 `cpu` 型号字符串里猜核数，会把 `E3-12xx v2` 这类型号当成核数。
- **源状态：** `servers` 没有 `cpu_cores` 列；Agent `HealthReport` 没有 `cpu_cores`。
- **目标状态：** `servers.cpu_cores integer not null default 0`；健康报告/入网携带独立的 `cpu_cores`（逻辑 CPU 数）。`cpu` 只保存型号。省略或 `0` 保留上次值。
- **实现位置：** `oboard/internal/store/store.go`（`create table` 与 `ensureColumn`）、`health_report.go`、`oboard/internal/controller/server.go`、`realtime.go`、`oboard-agent/internal/agent/probe.go`、`agent.go`
- **更新脚本：** 无专用脚本。Controller 打开 SQLite 时幂等补列。
- **数据影响：** 既有服务器核数为 0，直到新 Agent 上报；不改写 `cpu` 型号。
- **重复执行：** `ensureColumn`；重复打开不改写已有核数。
- **失败行为：** 补列失败会阻止打开数据库。
- **回归测试：** `TestServerCPUCoresMigrateFromPreviousSchema`、`TestApplyHealthReportPersistsCPUCoresWithoutParsingModel`、`TestCPUCountFromCPUInfoDoesNotParseModelName`
- **移除条件：** 最老支持数据库和所有可恢复备份必须已包含该列；恢复入口不得导入缺少它的 schema；入网/心跳路径不再出现无 `cpu_cores` 的 Agent。
- **移除状态：** 生效中。

### controller-db-20260829-server-display-tags

- **引入日期：** 2026-08-29
- **引入提交：** `OboardProject/oboard@5e3028465dda0c8050c03542e242f9c01c1a79a0`
- **引入版本：** `dev-5e3028465dda`
- **首次稳定版：** 待发布
- **所有者：** Controller `internal/store`、`internal/model`、`internal/controller`、`internal/capability`、`internal/application`、Web
- **类别：** SQLite schema
- **原因：** 服务器卡片底部标签改为用户自行设置，不再用 IP 栈/监控方式等系统标志冒充标签。
- **源状态：** `servers` 没有 `display_tags_json` 列。
- **目标状态：** `servers.display_tags_json text not null default '[]'`，存最多 8 个 `{text,tone}` 标签；缺列的旧库补列后为空数组。
- **实现位置：** `oboard/internal/store/store.go`（`create table` 与 `ensureColumn`）、`oboard/internal/model/server_display_tags.go`、`oboard/internal/controller/server.go`、`server_update_operation.go`、`oboard/internal/capability/catalog.go`
- **更新脚本：** 无专用脚本。Controller 打开 SQLite 时幂等补列。
- **数据影响：** 既有服务器标签为空；不改写其他服务器字段。
- **重复执行：** `ensureColumn`；重复打开不改写已有 JSON。
- **失败行为：** 补列失败会阻止打开数据库。
- **回归测试：** `TestServerDisplayTagsMigrateFromPreviousSchema`、`TestNormalizeServerDisplayTags`、`TestMCPServerDisplayTagsRoundTripAndPatchOmit`
- **移除条件：** 最老支持数据库和所有可恢复备份必须已包含该列；恢复入口不得导入缺少它的 schema。
- **移除状态：** 生效中。

### controller-db-20260829-subscription-client-templates

- **引入日期：** 2026-08-29
- **引入提交：** `OboardProject/oboard@57aafe877b1cb080a320f43d0718ed796e898171`
- **引入版本：** `dev-57aafe877b1c`
- **首次稳定版：** 待发布
- **所有者：** Controller `internal/store`、`internal/controller`、`internal/capability`、Web
- **类别：** SQLite schema
- **原因：** 每个公开订阅客户端需要可编辑完整配置外壳；审计需要区分请求 format 与解析后 format，以及 `format=auto` 是否发生 UA 识别。内置模板不写入数据库。
- **源状态：** 无 `subscription_client_templates` 表；`subscription_pull_audits` 无 `requested_format` / `auto_detected` 列。
- **目标状态：** 存在 `subscription_client_templates(format PK, content, revision, base_builtin_digest, updated_by, updated_at)`；审计表有 `requested_format`（默认空）和 `auto_detected`（默认 0）。无行表示使用嵌入 builtin；Reset 删除覆盖行。
- **实现位置：** `oboard/internal/store/store.go`（`create table`、`ensureColumn`）、`oboard/internal/store/subscription_templates.go`、`oboard/internal/store/subscription_audit.go`
- **更新脚本：** 无专用脚本。Controller 打开 SQLite 时幂等建表并补列。
- **数据影响：** 不灌入 builtin 模板；不改写既有审计行的 `format`（解析后目标）。新列默认空/0。
- **重复执行：** `create table if not exists` 与 `ensureColumn`；重复打开不改写已有覆盖或审计值。
- **失败行为：** 建表或补列失败会阻止打开数据库。
- **回归测试：** `TestSubscriptionTemplateAndAuditColumnsMigrateFromPreviousSchema`、`TestSubscriptionClientTemplatesStartAsBuiltin`、`TestSubscriptionClientTemplatePutResetAndConflict`
- **移除条件：** 最老支持数据库和所有可恢复备份必须已包含该表与两列；恢复入口不得导入缺少它们的 schema。当前 builtin 无行语义测试继续保留。
- **移除状态：** 生效中。

### controller-db-20260828-traffic-policy-revision-triggers

- **引入日期：** 2026-08-28
- **引入提交：** `OboardProject/oboard@57aafe877b1cb080a320f43d0718ed796e898171`
- **引入版本：** `dev-57aafe877b1c`
- **首次稳定版：** 待发布
- **所有者：** Controller `internal/store`、`internal/controller`；Agent `internal/agent`
- **类别：** SQLite schema、trigger rebuild、wire protocol
- **原因：** 流量 ledger 更新 `users.traffic_used_bytes` 曾通过无条件 `routing_rev_users_update` 使 routing snapshot 失效，并与完整 JSON 比较一起把 runtime policy 放大成 `apply_deployment`。历史库使用 `CREATE TRIGGER IF NOT EXISTS`，同名错误 trigger 不会被替换。
- **源状态：** `routing_rev_users_update` / `config_rev_users_update` 可能是无条件 UPDATE；缺少 `traffic_policy_revision` 表；`configuration_sync_states` 缺少 `trigger_reason` / `sync_strategy`。
- **目标状态：** 启动时 DROP 全部 `routing_rev_%` / `config_rev_%` 后按当前代码重建；`users` 不再安装 `config_rev_users_*`；`routing_rev_users_update` 仅在身份/授权字段变化时推进 routing cache；新增独立 `traffic_policy_revision`；流量用量不再使 routing cache 或 `configuration_revision` 增长。
- **实现位置：** `oboard/internal/store/routing_revision.go`、`traffic.go`、`configuration_sync.go`、`store.go`；`oboard/internal/controller/config_comparison.go`、`traffic_policy.go`、`configuration_reconciler.go`；Agent `apply_traffic_policy`。
- **更新脚本：** 无专用脚本。Controller 打开 SQLite 时幂等 DROP+CREATE 触发器并建表扩列。
- **数据影响：** 不改写业务流量计数；不回退已增长的 `configuration_revision`。启动时可将仅 runtime policy 不同的 pending `apply_deployment` 标为 superseded。
- **重复执行：** DROP IF EXISTS + CREATE 可重复；`traffic_policy_revision` 使用 `insert or ignore`。
- **失败行为：** 触发器安装失败阻止打开数据库。
- **回归测试：** `TestLegacyUnconditionalRevisionTriggersAreReplacedOnMigrate`、`TestCommitTrafficLedgerDoesNotAdvanceConfigurationOrRoutingRevision`、`TestTrafficReportsDoNotTriggerConfigurationDeployment`。
- **移除条件：** 最老支持数据库和所有可恢复备份必须已包含当前 trigger 定义和 `traffic_policy_revision` 表。
- **移除状态：** 生效中。

### controller-db-20260828-remove-vless-tls-vision

- **引入日期：** 2026-08-28
- **引入提交：** `OboardProject/oboard@fa658a2d24734405aaccf74147ef8b72b49452ff`
- **引入版本：** `dev-fa658a2d2473`
- **首次稳定版：** 待发布
- **所有者：** Controller `internal/store`、`internal/controller`、`internal/capability`、Web
- **类别：** SQLite seed / data backfill
- **原因：** 内置入口/节点预设不再提供「VLESS TLS Vision」（TCP + TLS + Vision，需要证书）。VLESS 证书 TLS 入口只保留 WebSocket；Reality Vision 与无 TLS 的 TCP 不受影响。
- **源状态：** `node_presets` 存在 `kind='vless-tls-vision'` 的内置或自定义行；入口 `config_json` 可能引用其 `node_preset_id`；MCP/REST `kind` 枚举与面板创建列表包含该 kind。
- **目标状态：** 种子、kind 枚举、MCP schema 与创建选择器不再包含 `vless-tls-vision`；打开数据库时删除该 kind 的预设行，并从入口 `config_json` 去掉对应 `node_preset_id`；既有 VLESS+TLS+Vision 入口的 `flow`/`tls` 保持不变，GET 不再推断该 kind。
- **实现位置：** `oboard/internal/store/node_presets.go`（`migrateRemoveVLESSTLSVisionPreset`）、`store.go`；`oboard/internal/controller/inbound_kind.go`、`mcp_recipes.go`、`mcp_catalog.go`、`mcp_resources.go`、`mcp_prompts.go`；`oboard/internal/capability/catalog.go`、`catalog_network.go`；`oboard/web/src/main.tsx`、`web/src/components/NodePresetsPanel.tsx`。
- **更新脚本：** 无专用脚本。Controller 打开 SQLite 时在补种内置预设之后删除该 kind。
- **数据影响：** 删除 `kind='vless-tls-vision'` 的预设行；剥离引用这些行的入口 `node_preset_id`；不改写 `flow`、TLS 或证书字段。新建入口无法再选择该 kind；创建/PATCH 提交 `kind=vless-tls-vision` 被拒绝。
- **重复执行：** 无匹配行时立即返回；剥离与删除按 kind/id 判断。重复打开不会改写已剥离的入口 JSON。
- **失败行为：** 查询、剥离或删除失败会阻止 Controller 打开数据库。
- **回归测试：** `TestRemoveVLESSTLSVisionPresetFromLegacyBuiltin` 从真实旧内置行和带 `node_preset_id` 的入口打开数据库，验证预设删除、引用剥离、协议配置保留与幂等；`TestNodePresetsSeededAndProtected`、`TestApplyInboundKindRejectsVLESSTLSVision`、`TestInferredInboundKindOmitsVLESSTLSVision`、`TestInboundSchemaCarriesProtocolGuidance`、`TestNodePresetCapabilitiesAreManagementOnlyAndExecutable` 覆盖当前种子、kind 拒绝与 MCP 枚举。
- **移除条件：** 在通用删除门槛之外，最老支持数据库和所有可恢复备份都不得再含 `vless-tls-vision` 预设行；当前种子、kind 拒绝与 MCP 枚举测试继续保留。
- **移除状态：** 生效中。

### controller-db-20260828-update-fleet

- **引入日期：** 2026-08-28
- **引入提交：** `OboardProject/oboard@4d9ba516be1d7fa7908a75142c7b52d3e7c3e5dc`
- **引入版本：** `dev-4d9ba516be1d`
- **首次稳定版：** 待发布
- **所有者：** Controller `internal/store`、`internal/controller`、Web；Agent 重连仅本地退避
- **类别：** SQLite schema / data backfill
- **原因：** 大规模服务器下 Controller 更新与 Agent 滚动更新必须与服务器数量解耦：Online Backup、单一活跃更新运行、轻量候选查询、唯一 `update_agent` 任务。
- **源状态：** 使用 `VACUUM INTO` 备份；无 `controller_update_runs` / `agent_fleet_update_state` / `agent_update_retries`；同一服务器可有多条活跃 `update_agent`；每分钟 `ListServers()` 扫描自动更新。
- **目标状态：** Online Backup；至多一个非终态 Controller 更新运行；重复活跃 `update_agent` 被折叠后建立唯一索引；Fleet coordinator 按槽位滚动；`agent_fleet_update_state.rolling` 标记管理员一键滚动，任务完成后继续填槽，不依赖自动更新开关。
- **实现位置：** `oboard/internal/store/backup.go`、`store.go` migrate、`agent_update.go`、`controller_update_run.go`；`oboard/internal/controller/agent_update_coordinator.go`
- **更新脚本：** 进程启动 `migrate()` 执行 DDL 与重复任务折叠
- **数据影响：** 为同一服务器保留最新一条活跃 `update_agent`，其余标为 failed/superseded；新建表与索引；为已有 fleet 状态表补 `rolling` 列（默认 0）；不改 Agent 任务 payload。
- **重复执行：** `CREATE IF NOT EXISTS`、`ensureColumn(rolling)` 与按 `newer.id` 折叠，幂等
- **失败行为：** 迁移失败阻止 Store 打开
- **回归测试：** `TestAgentUpdateIndexesExist`、`TestAgentFleetRollingColumnMigratesFromPreviousSchema`、`TestEnqueueUniqueAgentTaskSuppressesDuplicates`、`TestListAgentUpdateCandidatesIsLightweight`、`TestAgentFleetCoordinatorRespectsConcurrency`、`TestOperatorFleetRollContinuesWithoutAutoUpdate`
- **移除条件：** 产品声明并实施最老可直接升级版本，且该版本已包含本 schema；备份恢复拒绝更老 schema
- **移除状态：** 生效中

### controller-db-20260825-port-forward-external-target

- **引入日期：** 2026-08-25
- **引入提交：** `OboardProject/oboard@e2a63295bcfc715da4c2bf8739fd69c84d5d56c8`
- **引入版本：** `dev-e2a63295bcfc`
- **首次稳定版：** 待发布
- **所有者：** Controller `internal/store`、`internal/core`、`internal/controller`、Web
- **类别：** SQLite schema
- **原因：** 独立端口转发需要支持不依赖受管目标服务器的任意 IP/域名与端口；受管服务器只在需要自动解析地址和纳入拓扑影响范围时选择。
- **源状态：** `port_forwards.target_server_id` 为 `NOT NULL` 外键；REST、MCP、拓扑校验和 Web 创建表单都要求选择另一台受管服务器，只有一台服务器时无法创建独立转发。
- **目标状态：** `port_forwards.target_server_id` 为可空外键。未选择目标服务器时 `target_address` 必填，部署和探测只使用解析后的 `target_address:target_port`，拓扑与授权范围仅包含入口服务器；选择目标服务器时继续支持留空地址自动解析。
- **实现位置：** `oboard/internal/store/store.go`、`internal/model/types.go`、`internal/core/forwards.go`、`internal/core/topology.go`、`internal/controller/server.go`、`internal/controller/mcp_network.go`、`internal/capability/catalog_forwards.go`；`oboard/web/src/components/proxy-path/TrafficForwardingDialog.tsx`、`web/src/main.tsx`。
- **更新脚本：** 无专用脚本。Controller 打开 SQLite 时检测 `pragma_table_info('port_forwards')`；仅当 `target_server_id` 仍为 `NOT NULL` 时在事务内重建表并恢复索引。
- **数据影响：** 原有转发行及其目标服务器外键原样保留；新模型允许该字段保存为 `NULL`。不改写监听地址、端口、探测设置或转发后端。
- **重复执行：** 新表的 `target_server_id` 已可空时迁移直接返回；重复打开或显式 `Migrate` 不重建表、不改写既有行。
- **失败行为：** 表创建、数据复制、旧表删除、重命名或索引恢复任一步失败都会回滚事务并阻止 Controller 打开数据库，不留下半迁移表。
- **回归测试：** `TestPortForwardTargetServerBecomesNullable` 从真实旧 `NOT NULL` 表和既有受管转发行启动，验证数据保留、可空约束、外部目标读写和重复迁移；Core、REST、MCP capability 与 Web 测试覆盖外部目标计划、拓扑忽略、授权、表单默认值和条件必填。
- **移除条件：** 在通用删除门槛之外，最老支持数据库和所有可恢复备份都必须已包含可空 `target_server_id`，且恢复入口不得再次导入旧 `NOT NULL` 表。
- **移除状态：** 生效中。

### controller-db-20260825-transport-tfo-capability

- **引入日期：** 2026-08-25
- **引入提交：** `OboardProject/oboard@05b18611eabf86c103e9359534e73e49cc6a2d6a`
- **引入版本：** `dev-05b18611eabf`
- **首次稳定版：** 待发布
- **所有者：** Controller `internal/store`、`internal/controller`、Agent `internal/agent`
- **类别：** SQLite schema
- **原因：** 传输能力模型把通用 MUX、协议原生复用和 TCP Fast Open 拆开后，Snell 参数预设需要保存 TFO 选项，服务器需要保存 Agent 上报的 `net.ipv4.tcp_fastopen` 位掩码，用于提示监听侧 TFO 是否真正生效。
- **源状态：** `snell_profiles` 没有 `tcp_fast_open` 列；`servers` 没有 `tcp_fastopen_state` / `tcp_fastopen_value` 列，面板无法区分“配置已开启”和“内核允许”。
- **目标状态：** `snell_profiles.tcp_fast_open integer not null default 0`；`servers.tcp_fastopen_state text not null default ''` 与 `servers.tcp_fastopen_value integer not null default 0`。空状态表示未上报，Controller 始终按上报的位掩码重算状态。
- **实现位置：** `oboard/internal/store/store.go`（`create table` 与 `ensureColumn`）、`snell_profiles.go`、`health_report.go`；`oboard/internal/model/types.go`；`oboard/internal/controller/server.go`、`mcp_snell.go`、`realtime.go`；`oboard-agent/internal/agent/tcp_fastopen.go`、`agent.go`。
- **更新脚本：** 无专用脚本。Controller 打开 SQLite 时通过 `ensureColumn` 幂等补列，默认值保持旧行为（TFO 关闭、能力未知）。
- **数据影响：** 仅新增带默认值的列；不改写已有 Snell 预设、服务器状态或入口配置，不改变 Agent 部署内容。
- **重复执行：** `ensureColumn` 仅在列缺失时执行 `ALTER TABLE`；重复打开不覆盖已保存的 TFO 选项或最近一次上报的位掩码。缺少该字段的心跳不会清除已知状态。
- **失败行为：** 补列失败会阻止 Controller 打开数据库；Agent 读取 sysctl 失败按 `unavailable` 上报，不影响心跳。
- **回归测试：** `TestTCPFastOpenColumnsMigrateFromPreviousSchema` 从真实旧 schema（删除三列后重开）验证补列、默认值、旧行参数保留和读写往返；`TestTCPFastOpenFromFile` 覆盖 Agent 侧位掩码解析。
- **移除条件：** 在通用删除门槛之外，最老支持数据库和所有可恢复备份必须已包含这三列。
- **移除状态：** 生效中。

### controller-db-20260826-preset-tfo-defaults

- **引入日期：** 2026-08-26
- **引入提交：** `OboardProject/oboard@4ef9a80efa97`
- **引入版本：** `dev-4ef9a80efa97`
- **首次稳定版：** 待发布
- **所有者：** Controller `internal/store`、`internal/core`、Web
- **类别：** SQLite seed / data backfill
- **原因：** 支持 TCP Fast Open 的预设（VLESS、AnyTLS、Shadowsocks、Mieru TCP、SOCKS5、Snell）默认应开启 TFO 以减少握手延迟；旧内置预设均未包含该选项，新建入口需手动勾选。
- **源状态：** `node_presets` 内置模板的 `config_json` 不含 `tcp_fast_open`；`snell_profiles` 内置行 `tcp_fast_open=0`（新建表默认 0）；Web 新建预设草稿默认关闭。
- **目标状态：** 支持 TFO 的 `node_presets` 内置模板写入 `"tcp_fast_open":true`（HY2 除外，Mieru 仅 TCP，VLESS QUIC 跳过）；`snell_profiles` 内置行 `tcp_fast_open=1`，新建表默认 1；Web 新建草稿默认开启；已有未显式关闭的内置模板通过迁移补齐。
- **实现位置：** `oboard/internal/store/node_presets.go`（内置种子与 `migratePresetTCPFastOpen`）、`store.go`（建表默认、种子与迁移注册）；`oboard/internal/core/transport_capability.go`（注释）；`oboard/web/src/components/NodePresetsPanel.tsx`、`SnellProfilesPanel.tsx`。
- **更新脚本：** 无专用脚本。Controller 打开 SQLite 时先通过 `ensureColumn` 幂等补列，再对内置行执行幂等回填：Snell `builtin=1 and tcp_fast_open=0` 置 1，Node 预设对仍缺少该键的内置行注入 `true`（HY2、Mieru UDP、VLESS QUIC 跳过，显式 `false` 保留）。
- **数据影响：** 仅改写仍缺少 TFO 键的内置预设；已显式设置 `false` 的预设保留关闭；Snell 自定义预设不改写；既有入口已保存的 `config_json` 不回填。
- **重复执行：** Snell 更新按 `builtin=1 and tcp_fast_open=0` 判断，Node 预设按 JSON 键存在判断，均幂等；重复打开不覆盖已显式关闭的预设。
- **失败行为：** 查询或更新失败阻止 Controller 打开数据库，无半迁移状态。
- **回归测试：** `TestTCPFastOpenColumnsMigrateFromPreviousSchema` 更新为校验内置 Snell 回填为开、自定义保持关；手动回退验证（移除 TFO 后重开补齐）覆盖 Node 预设与 Snell；`TestNodePresetsSeededAndProtected` 仍覆盖种子校验。
- **移除条件：** 在通用删除门槛之外，最老支持数据库和所有可恢复备份必须已包含带 TFO 的内置预设，且恢复入口不得导入旧内置模板；当前种子与校验保留。
- **移除状态：** 生效中。

### controller-db-20260826-hy2-salamander-presets

- **引入日期：** 2026-08-26
- **引入提交：** `OboardProject/oboard@c8f4a4dd07dd51e7247abb3ed001eba7b75b2731`
- **引入版本：** `dev-c8f4a4dd07dd`
- **首次稳定版：** 待发布
- **所有者：** Controller `internal/store`、`internal/controller`、`internal/capability`、Web
- **类别：** SQLite seed / data backfill
- **原因：** HY2 需要标准 TLS 与 Salamander 两套入口形态；带宽属于入口而不是预设；Salamander 混淆密码必须按入口随机生成，不得写入节点预设。
- **源状态：** `node_presets` 只有 `kind='hy2-tls'` 的内置行，名称「Hysteria2」，`config_json` 含 `"up_mbps":100,"down_mbps":100`，没有 `hy2-salamander`；未编辑的种子行保留初始 `updated_at=2026-01-01T00:00:00Z`。
- **目标状态：** 未编辑的旧 `hy2-tls` 行原位改为「Hysteria2 标准」并去掉带宽字段；幂等新增「Hysteria2 Salamander」行，模板只含 `obfs.type=salamander`；所有 HY2 预设在规范化时剥离 `up_mbps`/`down_mbps` 与 `obfs.password`。新建入口默认上 1000 / 下 500，Salamander 密码由 Controller 在每个入口生成。
- **实现位置：** `oboard/internal/store/node_presets.go`（种子、`migrateHY2Presets`、`normalizeNodePresetConfig`）、`store.go`；`oboard/internal/controller/inbound_kind.go`、`server.go`、`mcp_recipes.go`；`oboard/internal/core/protocols.go`；`oboard/internal/capability/catalog.go`、`catalog_network.go`；`oboard/web/src/components/NodePresetsPanel.tsx`、`web/src/main.tsx`。
- **更新脚本：** 无专用脚本。Controller 打开 SQLite 时先按内置 `kind` 补种缺失行，再回填未编辑的旧 `hy2-tls`，最后剥离全部 HY2 预设中的带宽与 Salamander 密码。
- **数据影响：** 只改写仍保持原始种子时间且仍为旧带宽 JSON 的 `hy2-tls` 内置行名称/备注/模板；其余 HY2 预设仅删除带宽和混淆密码键。既有入口已保存的 `config_json` 不回填带宽或 Salamander。
- **重复执行：** 按 `kind+builtin` 判断缺失种子；旧行只有在原始时间与旧 JSON 同时匹配时回填；带宽/密码剥离按 JSON 键存在判断。重复打开不会新增同 kind 的内置行或覆盖编辑后的预设名称。
- **失败行为：** 查询、补种、名称冲突检查或更新失败会阻止 Controller 打开数据库；迁移不改写入口。
- **回归测试：** `TestHY2PresetsMigrateFromLegacyBuiltin` 从真实旧 `node_presets` 表和带带宽的内置行打开数据库，验证原位回填、Salamander 预设补种、无重复 kind 和重复打开幂等；`TestNormalizeNodePresetStripsHY2BandwidthAndSecrets`、`TestApplyInboundKindHY2Defaults`、`TestHY2SalamanderObfsValidation` 覆盖当前种子、入口默认值与校验。
- **移除条件：** 在通用删除门槛之外，最老支持数据库和所有可恢复备份都必须已包含两套 HY2 预设且内置模板不再含带宽或 Salamander 密码；当前种子与校验测试继续保留。
- **移除状态：** 生效中。

### controller-db-20260825-server-traffic-quota

- **引入日期：** 2026-08-25
- **引入提交：** `OboardProject/oboard@05b18611eabf86c103e9359534e73e49cc6a2d6a`
- **引入版本：** `dev-05b18611eabf`
- **首次稳定版：** 待发布
- **所有者：** Controller `internal/store`、`internal/controller`、`internal/capability`、Web
- **类别：** SQLite schema
- **原因：** 服务器统计需要按重置周期配置并展示周期流量限额，用于运维侧的容量规划与阈值提醒；限额仅用于展示与计算使用率，不阻断现有连接或改变部署。
- **源状态：** `server_telemetry` 没有 `traffic_limit_bytes` 列，服务器卡片仅展示实时速率和本周期已用总量，无法配置限额或显示使用率。
- **目标状态：** `server_telemetry` 存在 `traffic_limit_bytes integer not null default 0`（0 表示不限量）；Controller 在创建/更新时校验 `>=0` 并持久化，MCP `servers.get/list` 输出包含该字段；Web 在服务器创建/编辑弹窗中提供“周期流量限额”输入，并在卡片、列表与详情中展示 `已用 / 限额 · 百分比` 及细进度条，限额为 0 时保持原有总量展示。
- **实现位置：** `oboard/internal/model/types.go`、`oboard/internal/store/store.go`（`create table` 与 `ensureColumn`）、`oboard/internal/controller/server.go`、`oboard/internal/capability/catalog.go`、`oboard/internal/application/dto.go`；`oboard/web/src/components/proxy-path/types.ts`、`oboard/web/src/main.tsx`、`oboard/web/src/style.css`。
- **更新脚本：** 无专用脚本。Controller 打开 SQLite 时通过 `ensureColumn` 幂等添加列，默认 0 保持旧行为。
- **数据影响：** 仅新增带默认值的限额列；不重写已有流量统计或重置周期数据，不改变 Agent 上报或部署。
- **重复执行：** `ensureColumn` 仅当列缺失时执行 `ALTER TABLE`，重复打开不改写已保存限额。
- **失败行为：** 列扩展失败会阻止 Controller 打开数据库；限额校验失败返回 400，不写入持久化状态。
- **回归测试：** 现有 `server` 相关 Store/Controller 套件覆盖读写；新增限额为 0 时保持不限量展示，限额 >0 时卡片展示 `已用/限额` 与进度条语义正确，MCP 输出通过 `servers.get` schema 校验。
- **移除条件：** 在通用删除门槛之外，最老支持数据库和所有可恢复备份必须已包含 `traffic_limit_bytes`，且恢复入口不得导入缺少该列的 `server_telemetry`。
- **移除状态：** 生效中。

### controller-db-20260824-anytls-padding-presets

- **引入日期：** 2026-08-24
- **引入提交：** `OboardProject/oboard@8e5b40cc790e3b8e097d584936dab6c6e5b2e2a9`
- **引入版本：** `dev-8e5b40cc790e`
- **首次稳定版：** 待发布
- **所有者：** Controller `internal/store`、`internal/core`、Web
- **类别：** SQLite seed / data backfill
- **原因：** 原 `anytls-basic` 内置预设只启用 TLS，因而使用 sing-anytls 的上游默认填充；当前模型要求提供两套明确、可编辑且都不同于上游默认值的 OBoard 填充预设，并在入口保存前校验规则。
- **源状态：** `node_presets` 只有 `kind='anytls-basic'` 的 TLS-only 内置行（`config_json={"tls":{"enabled":true}}`），没有 `anytls-large-padding`；未编辑的种子行保留初始 `updated_at=2026-01-01T00:00:00Z`。
- **目标状态：** 未编辑的旧 `anytls-basic` 行原位改为“AnyTLS 均衡填充”并写入均衡规则；幂等新增“AnyTLS 大包填充”行。两个预设都使用服务端 inbound `padding_scheme`，Controller 不把该字段写入客户端订阅或 AnyTLS outbound。
- **实现位置：** `oboard/internal/store/node_presets.go`、`store.go`；`oboard/internal/core/anytls_padding.go`、`protocols.go`、`subscription_formats.go`；`oboard/web/src/main.tsx`、`components/NodePresetsPanel.tsx`。
- **更新脚本：** 无专用脚本。Controller 打开 SQLite 时先按内置 `kind` 补种缺失行，再运行填充预设回填。
- **数据影响：** 只改写仍保持原始种子时间和 TLS-only 配置的 `anytls-basic` 内置行；已编辑的内置行和自定义预设不改写。既有入口已保存的 `config_json` 不回填，后续新建或重新套用预设的入口使用新方案。
- **重复执行：** 按 `kind+builtin` 判断缺失种子；旧行只有在原始时间与旧 JSON 同时匹配时回填，首次成功后不再匹配。重复打开不会新增同 kind 的内置行或覆盖编辑后的预设。
- **失败行为：** 查询、补种、名称冲突检查或更新失败会阻止 Controller 打开数据库；不会留下只更新一部分入口配置的状态，因为迁移不改写入口。
- **回归测试：** `TestAnyTLSPaddingPresetsMigrateFromLegacyBuiltin` 从真实旧 `node_presets` 表和 TLS-only 内置行打开数据库，验证原位回填、第二套预设补种、无重复 kind 和重复打开幂等；`TestNodePresetsSeededAndProtected`、`TestNormalizeNodePresetRejectsInvalidAnyTLSPadding` 覆盖当前种子与校验。
- **移除条件：** 在通用删除门槛之外，最老支持数据库和所有可恢复备份都必须已包含两套 AnyTLS 预设，且不再可能导入初始时间戳的 TLS-only `anytls-basic` 行；当前种子与校验测试继续保留。
- **移除状态：** 生效中。

### controller-db-20260823-node-presets

- **引入日期：** 2026-08-23
- **引入提交：** `OboardProject/oboard@936aac8ad0f2`
- **引入版本：** `dev-936aac8ad0f2`
- **首次稳定版：** 待发布
- **所有者：** Controller `internal/store`、`internal/controller`、Web
- **类别：** SQLite schema / seed
- **原因：** 设置页从仅 Snell 参数预设扩展为节点预设；VLESS / HY2 / AnyTLS / SS / Mieru / SOCKS5 需要可编辑的默认配置模板，且不得把密钥放进模板。
- **源状态：** 数据库没有 `node_presets` 表；入口创建只使用 Web 硬编码默认值。
- **目标状态：** `node_presets` 表存在，并幂等种子 12 套内置模板；入口可通过 `config_json.node_preset_id` 引用。`snell_profiles` 保持不变。
- **实现位置：** `oboard/internal/store/store.go`、`oboard/internal/store/node_presets.go`；`oboard/internal/model/types.go`；`oboard/internal/controller/server.go`、`mcp_node_presets.go`、`inbound_automation.go`；`oboard/internal/capability/catalog_network.go`；`oboard/web/src/components/NodePresetsPanel.tsx`。
- **更新脚本：** 无专用脚本。Controller 打开 SQLite 时 `create table if not exists` 并 `insert or ignore` 种子。
- **数据影响：** 仅新增表与内置行；不改写既有入口或 Snell 预设；不改变 Agent 配置。
- **重复执行：** 表与种子均幂等；重复打开不会覆盖已改名或已编辑的内置行（按 `name` 忽略插入）。
- **失败行为：** 建表或种子失败阻止 Controller 打开数据库。
- **回归测试：** `TestNodePresetsMigrateFromPreviousSchema` 从缺少该表的旧库打开并验证种子；`TestNodePresetsSeededAndProtected`、`TestNodePresetsCRUDAndReferenceGuard`、`TestNodePresetUsageCountsInboundReference` 覆盖种子、CRUD 与引用保护。
- **移除条件：** 在通用删除门槛之外，最老支持数据库和所有可恢复备份必须已包含 `node_presets`。
- **移除状态：** 生效中。

### controller-db-20260822-subscription-output-filters

- **引入日期：** 2026-08-22
- **引入提交：** `OboardProject/oboard@e8a8239c3cd79`
- **引入版本：** `dev-e8a8239c3cd79`
- **首次稳定版：** 待发布
- **所有者：** Controller `internal/store`、`internal/controller`、Web
- **类别：** SQLite schema
- **原因：** 组合订阅（组合订阅输出）需要保存 Sub-Store 式有序过滤规则管线，在渲染时对合并节点列表执行按名字正则/协议/地区/分组的保留与排除。
- **源状态：** `subscription_outputs` 没有 `filters_json` 列，所有输出直接输出全部选中节点。
- **目标状态：** `filters_json` 列存在（默认 `''` = 不过滤），旧输出行为不变；保存时 Controller 校验规则语义（正则可编译、协议/地区白名单、分组归属），store 仅做 8192 字节上限保护。
- **实现位置：** `oboard/internal/store/store.go`、`oboard/internal/store/node_workspace.go`；`oboard/internal/model/types.go`、`oboard/internal/core/subscription_filters.go`；`oboard/internal/controller/node_workspace.go`、`node_workspace_automation.go`、`oboard/internal/capability/node_workspace.go`；`oboard/web/src/pages/NodeWorkspacePage.tsx`、`style.css`。
- **更新脚本：** 无专用脚本。Controller 打开 SQLite 时执行幂等 `ensureColumn` 迁移。
- **数据影响：** 仅新增可空过滤 JSON 列；不重写既有业务数据，不改变 Agent 配置；空过滤等于旧行为。
- **重复执行：** `ensureColumn` 仅当列缺失时执行；重复打开和 `Migrate` 不会改写已保存的过滤规则。
- **失败行为：** 列扩展失败阻止 Controller 打开数据库；损坏的 `filters_json` 按空规则表读取，不阻断订阅渲染。
- **回归测试：** `TestSubscriptionOutputFiltersMigrateFromPreviousSchema` 从缺少该列的真实旧 schema 打开并验证默认不过滤、读写往返与重复迁移幂等；`TestSubscriptionOutputFiltersRoundTripLimitsAndCorruption` 覆盖存取、上限与损坏容错；Controller 测试覆盖 REST 保存/校验、预览统计与订阅下发过滤。
- **移除条件：** 在通用删除门槛之外，最老支持数据库和所有可恢复备份必须已包含 `filters_json`；恢复入口不得导入缺少过滤列的组合订阅 schema。
- **移除状态：** 生效中。

### controller-db-20260822-server-expiry

- **引入日期：** 2026-08-22
- **引入提交：** `OboardProject/oboard@49c99f6415e7`
- **引入版本：** `dev-49c99f6415e7`
- **首次稳定版：** 待发布
- **所有者：** Controller `internal/store`、`internal/controller`、Web
- **类别：** SQLite schema
- **原因：** 服务器到期日、自动续期和到期提醒需要新的运行管理字段；这些字段属于主控侧运维状态，不应放进 `servers` 主表触发路由/配置 revision。
- **源状态：** `server_telemetry` 没有 `expires_at`、`renewal_cycle`、`auto_renew_enabled`、`expiry_notify_enabled`、`last_auto_renewed_at` 列。
- **目标状态：** 上述列存在，旧服务器默认不自动续期、默认开启到期提醒、续期周期为月付；Controller 每分钟检查 3 天宽限后的自动续期和每日到期提醒。
- **实现位置：** `oboard/internal/store/store.go`、`oboard/internal/store/server_expiry.go`；`oboard/internal/controller/server_expiry.go`、`notifications.go`、`server.go`；`oboard/web/src/main.tsx`、`components/proxy-path/types.ts`、`style.css`。
- **更新脚本：** 无专用脚本。Controller 打开 SQLite 时执行幂等 `ensureColumn` 迁移。
- **数据影响：** 新增可空到期日和最近自动续期时间，以及三个带默认值的设置列；不重写既有业务数据，不改变 Agent 配置。
- **重复执行：** `ensureColumn` 仅当列缺失时执行；迁移可重复打开和 `Migrate`，不会改写已保存的到期状态。
- **失败行为：** 任一列扩展失败会阻止 Controller 打开数据库；到期/续期调度失败只记录日志，由下一分钟任务重试。
- **回归测试：** `TestServerExpiryColumnsMigrateFromPreviousSchema` 从缺少新列的真实旧 schema 打开并验证默认值；`TestServerExpiryRoundTripAndRenewalState` 验证读写；Controller 测试覆盖自动续期、提醒去重、REST/MCP 延长。
- **移除条件：** 在通用删除门槛之外，最老支持数据库和所有可恢复备份必须已包含上述列；恢复入口不得导入缺少到期字段的 schema。
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

### controller-db-20260827-remote-access

- **引入日期：** 2026-08-27
- **引入提交：** `OboardProject/oboard@82937c69f06c9b4616f906352f43d81acabbb6fe`
- **引入版本：** `dev-82937c69f06c`
- **首次稳定版：** 待发布
- **所有者：** Controller `internal/store`、`internal/controller`、`internal/capability`、`internal/mcpauth`、Web
- **类别：** SQLite schema
- **原因：** Remote Terminal 与 MCP 主机执行需要独立的全局/服务器策略、Privileged Grant、Step-up 挑战和仅元数据审计，且升级后必须保持全部关闭。
- **源状态：** 不存在 `server_remote_access_policies`、`server_remote_access_status`、`mcp_privileged_grants`、`remote_access_audit`、`step_up_challenges`、`consumed_step_up_tokens`。
- **目标状态：** 上述表以 `create table if not exists` 存在；策略布尔列默认 0；`mcp_privileged_grants.oauth_grant_id` 唯一并随 OAuth Grant 级联；终端会话不进 SQLite。
- **实现位置：** `oboard/internal/store/store.go`、`oboard/internal/store/remote_access.go`
- **更新脚本：** 进程启动 `Open()` 执行
- **数据影响：** 仅新增表，不改现有业务行
- **重复执行：** `create table if not exists`，幂等
- **失败行为：** 启动失败
- **回归测试：** `TestRemoteAccessTablesMigrateFromPreviousSchema`
- **移除条件：** 最老直接升级版本与可恢复备份均已包含这些表，且恢复入口拒绝缺少它们的 schema
- **移除状态：** 生效中

### controller-db-20260831-server-service-start-and-traffic-reset


- **引入日期：** 2026-08-31
- **引入提交：** `OboardProject/oboard@1584fa60fd17`
- **引入版本：** `dev-1584fa60fd17`
- **首次稳定版：** 待发布
- **所有者：** Controller `internal/store`、`internal/controller`、`internal/capability`、Web `oboard/web/src/main.tsx`
- **类别：** SQLite schema
- **原因：** 服务器流量统计需与计费周期对齐，重置日应随起租日/到期日自动推导，避免运维手动填错导致当月统计错位。流量为服务器侧月度统计，与套餐/用户流量独立，分钟精度无意义，仅保留日精度。
- **源状态：** `server_telemetry` 没有 `service_start_at` 列；`traffic_reset_mode/day` 默认为 `monthly/1`，创建时若用户提供 `2025-07-05~2026-07-09` 的账期且未显式指定重置日，仍为 1 日重置，与起租日不一致。
- **目标状态：** `server_telemetry` 存在 `service_start_at text`（可空，ISO8601），创建/更新时若 `traffic_reset_mode/day` 均未显式指定，则按 `service_start_at`(优先)或 `expires_at` 的日(按 `traffic_timezone` 所在时区)推导 `traffic_reset_mode=month_day` 与 `traffic_reset_day=anchor日`；已显式指定则永不覆盖。季付仍为每月同日重置。Web 在到期与续期区新增“计费开始日”输入并自动预览推导结果，MCP `servers.onboard/update` 同步支持`service_start_at` 与自动推导语义。
- **实现位置：** `oboard/internal/model/types.go`、`oboard/internal/store/store.go`（`create table` 与 `ensureColumn`、读写）、`oboard/internal/controller/server.go`（`deriveServerTrafficReset`、`servers` POST/PATCH）、`oboard/internal/controller/api_v1.go`（`applyServerOnboardingDefaults`）、`oboard/internal/controller/server_update_operation.go`（`servers.update` 自动化）、`oboard/internal/capability/catalog.go`（`servers.onboard/update` schema 描述）；`oboard/web/src/main.tsx`、`oboard/web/src/server-expiry.ts`。
- **更新脚本：** 无专用脚本。Controller 打开 SQLite 时通过 `ensureColumn` 幂等补列，默认空保持旧行为（无起租日时按到期日推导，无任何锚点时保持 `monthly/1`）。
- **数据影响：** 仅新增可空列；不重写已有流量统计或重置周期数据，不改变 Agent 上报或部署。已存在服务器保持原 `traffic_reset` 值，首次编辑账期且未显式指定重置日时才按新锚点自动更新。
- **重复执行：** `ensureColumn` 仅当列缺失时执行 `ALTER TABLE`；推导仅在创建或账期变更且重置日未显式指定时触发，重复打开不改写已显式指定的重置日。
- **失败行为：** 列扩展失败会阻止 Controller 打开数据库；重置日推导失败回落为 `monthly/1`，不阻断创建/更新，校验仍执行。
- **回归测试：** `go vet`、`tsc` 与 `vite build` 通过；新增手动验证：`service_start_at=2025-07-05, expires_at=2026-07-09` -> `month_day/5`；仅 `expires_at=2026-07-09` -> `month_day/9`；显式 `traffic_reset_day=15` 保持 15 不被覆盖。Store 迁移由 `ensureColumn` 覆盖，重复启动幂等。
- **移除条件：** 在通用删除门槛之外，最老支持数据库和所有可恢复备份必须已包含 `service_start_at`，且恢复入口不得导入缺少该列的 `server_telemetry`；所有受支持 Controller 版本必须实现相同的起租日起租优先推导语义。
- **移除状态：** 生效中。

### controller-db-20260828-traffic-ledger-v2

- **引入日期：** 2026-08-28
- **引入提交：** `OboardProject/oboard@5fedab310ae87ed7a6d4270daa353a21899bf549`
- **引入版本：** `dev-5fedab310ae8`
- **首次稳定版：** 待发布
- **所有者：** Controller `internal/store`、`internal/controller`、`internal/capability`、Web；Agent `internal/agent`；kernel `kernel/oboard-sb`
- **类别：** SQLite schema / wire protocol
- **原因：** 把流量从客户端 delta 上报升级为 checkpoint range 账本，修复同 epoch 计数倒退误计、损坏 `traffic-state.json` 静默清空、以及 `lease=0` 未强制配额的 P0。
- **源状态：** `traffic_reports` 只有 delta 字段；无 `traffic_counter_streams` / `traffic_reconciliation_events`；`traffic_leases` 无 revision/state/TTL；Controller 仅在 `RemainingBytes>0` 时设置 `lease_enforced`；Agent JSON 解析失败当作空状态。
- **目标状态：** `traffic_reports` 增加 range 字段（历史 delta 行保持 `protocol_version=1` 且 `stream_id` 为空，无历史 checkpoint 回填）；新增 stream checkpoint 与对账事件表；有限额度用户始终 `lease_enforced=true`，`lease=0` 以 baseline 封顶。`POST /api/v1/agent/traffic-reports` 只接受 checkpoint ranges + streams，拒绝 delta `items` 与并行 `protocol_version`。唯一索引 `idx_traffic_reports_range` 在 `stream_id <> ''` 上成立。Kernel 能力名为 `traffic_ledger`。Agent `traffic-state.json` schema_version=2，损坏则 `state_corrupt` 并对 Controller 对账。
- **实现位置：** `oboard/internal/store/traffic.go`、`store.go`；`oboard/internal/controller/traffic.go`、`server.go`；`oboard/internal/capability/catalog_traffic_ledger.go`；`oboard/web/src/main.tsx`；`oboard-agent/internal/agent/traffic.go`、`ssh_inbounds.go`；`oboard-agent/kernel/oboard-sb/cmd/oboard-sb/main.go`。
- **更新脚本：** Controller 打开 SQLite 时 `migrateTrafficLedgerV2` 幂等补列建表，并 `drop index if exists idx_traffic_reports_v2_range` 后建立 `idx_traffic_reports_range`。Agent 首次读到无 epoch 的旧 `traffic-state.json` 把 leftover pending 转成 range 后写 schema_version=2。
- **数据影响：** 不删除 `traffic_reports` / `traffic_periods`，不重算 `users.traffic_used_bytes`。离线 Agent 未消费 Lease 保持 reserved（`expired_unsettled` 不回收）。
- **重复执行：** `ensureColumn` 与 `create table/index if not exists` / `drop index if exists`；重复打开不改写已有行。
- **失败行为：** 列或表创建失败会阻止 Controller 打开数据库。checkpoint gap/overlap 拒绝入账并记对账事件，不静默裁剪。
- **回归测试：** `TestTrafficLedgerV2MigratesFromPreviousSchema`、`TestTrafficLedgerCoversHistoricalProtocolVersionTwoRows`、`TestTrafficLedgerV2IsIdempotentAfterLostACK`、`TestTrafficLedgerV2SameRangeDifferentReportIDIsCovered`、`TestAgentTrafficRejectsDeltaItems`、`TestTrafficRuntimePoliciesEnforceZeroLeaseWithoutExceedingGlobalQuota`
- **移除条件：** 最老直接升级版本与可恢复备份均已包含 range 列、stream 表和 `idx_traffic_reports_range`；恢复入口拒绝缺少 stream 表的备份。
- **移除状态：** 生效中

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
