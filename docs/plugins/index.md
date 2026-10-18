# OBoard 插件

插件是**不受信任的代码**：它只能声明需要哪些**能力**，由 OBoard 代为执行；插件永远不能描述或执行命令。

```text
插件代码 ──SDK 调用──▶ Capability Gateway ──▶ 结构化操作（读取、诊断、HTTPS、状态、通知）
              ▲            │ 每次调用重新检查：
              │            │ 清单声明 ∩ 管理员授权 ∩ 资源范围 ∩ 运行策略 ∩ 发起者权限
   隔离 Runner（无网络、无文件、无子进程）
```

## 文档

1. [清单与环境变量](manifest.md)：`manifest.json`、13 种环境变量类型、条件字段、自定义变量与三阶段校验。
2. [SDK 参考](sdk.md)：`main(run)`、`oboard.*`、`env`、`SecretRef`、界面快照、加密工具与错误码。
3. [安全模型](security.md)：隔离 Runner、能力目录、授权与资源范围、HTTP 网关、Agent 诊断、密钥与审计。
4. [打包、签名与安装](packaging.md)：`.obplugin` 格式、发布者签名、`oboard-plugin-tool`、GitHub 安装、更新与权限差异。
5. [运维](operations.md)：安装运行环境、实例、计划与事件、执行记录、自动暂停、备份恢复。

## 开发资源

- [`sdk/plugins/oboard.d.ts`](../../sdk/plugins/oboard.d.ts)：编辑器类型声明（运行时不支持 TypeScript）。
- [`sdk/plugins/manifest.schema.json`](../../sdk/plugins/manifest.schema.json)：清单 JSON Schema；Controller 的语义校验始终是最终依据。
- [`examples/plugins`](../../examples/plugins)：`minimal`、`environment-demo`、`external-api`、`trace-monitor`。
- [`cmd/oboard-plugin-tool`](../../cmd/oboard-plugin-tool)：`check`、`pack`、`keygen`、`inspect`。

## 永远不提供

Shell 与命令执行、`remote_exec`、`remote_operation`、终端 / PTY、任意 Agent 任务、主机电源操作、主机文件系统、主控数据库与内部 API、`require` / `import` / npm。这些不是“待授权”的能力，而是能力目录中不存在的类别；以 `shell`、`exec`、`command`、`process`、`spawn`、`terminal`、`pty`、`remote_exec`、`remote_operation`、`filesystem`、`database`、`sql`、`agent`、`controller`、`tasks`、`host`、`power`、`management` 等为首段的能力名会被清单校验直接拒绝。

## 与旧插件运行时的关系

旧运行时（`oboard-js-v1` 清单、版本/触发器绑定、声明式 UI、Webhook、服务重启与主机电源能力）已经退役，不提供兼容层。升级时旧插件的代码、授权、密钥、状态、执行记录和 Webhook 全部清除，插件执行保持关闭；旧格式清单安装时会被明确拒绝为“不兼容”。
