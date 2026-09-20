# OBoard 插件开发者文档

本目录描述当前 `oboard-js-v1` / `oboard-sdk-v1` 插件合同。插件在 Controller 侧的独立 Worker / 隔离 Runner 中执行；不是 Node.js 扩展，不在 Agent 或浏览器中执行第三方 JavaScript。没有 npm 运行时、任意 Shell、数据库连接、文件系统 SDK 或系统管理后门。

## 从这里开始

1. [快速入门](quickstart.md)：模板、开发、打包、安装、授权、首次运行。
2. [架构与生命周期](architecture.md)：插件、不可变版本、安装、授权、执行和节点操作的区别。
3. [清单参考](manifest.md)：字段、限制与校验。
4. [JavaScript 运行时](runtime.md)：入口、全局对象、同步调用和资源预算。
5. [SDK 参考](sdk.md)：每个方法的入参、返回、权限和失败语义。
6. [权限、网络与密钥](security.md)：拒绝优先、身份交集、SSRF 边界和日志规则。
7. [声明式页面](ui.md)：页面、组件、表单、表格和动作。
8. [事件、定时与 Webhook](automation.md)：实际支持的触发形式与未支持边界。
9. [配置与私有状态](data.md)：参数、环境声明、配置、CAS、升级。
10. [测试与调试](testing.md)：本地验证、错误定位和契约测试。
11. [GitHub 仓库、打包与发布](distribution.md)：仓库地址安装、固定提交、ZIP 合同。
12. [更新、卸载与恢复](operations.md)：授权重审、回退、备份及未知结果处理。

## 可直接使用的开发资源

- [`sdk/plugins/manifest.schema.json`](../../sdk/plugins/manifest.schema.json)：安装包清单 JSON Schema。
- [`sdk/plugins/ui.schema.json`](../../sdk/plugins/ui.schema.json)：声明式 UI JSON Schema。
- [`sdk/plugins/oboard.d.ts`](../../sdk/plugins/oboard.d.ts)：编辑器类型声明，不代表运行时支持 TypeScript。
- [`examples/plugins/template`](../../examples/plugins/template)：最小仓库模板。
- [`examples/plugins/node-inspection`](../../examples/plugins/node-inspection)：只读节点巡检。
- [`examples/plugins/https-notification`](../../examples/plugins/https-notification)：受控公共 HTTPS 通知。
- [`examples/plugins/batch-operations`](../../examples/plugins/batch-operations)：带显式确认的批量服务维护面板。
- [`cmd/oboard-plugin-tool`](../../cmd/oboard-plugin-tool)：`pack` 和 `check` 命令。

JSON Schema 帮助编辑器发现形状错误；Controller 的语义校验、授权和运行时配额始终是最终依据。字节大小、唯一标识符、保留环境名、参数 Schema 复杂度和资源权限不能仅靠 JSON Schema 判定。

## 支持范围的理解

安装一个包不等于信任作者、不等于授权，也不等于启用执行。SDK 返回“接受操作”不等于节点已经完成。第三方仓库地址不是供应链签名；固定提交和内容摘要让管理员审核的内容可追溯，但仍应审查源码。

本文不会把设计中的能力写成已提供的 API。没有文档化入口的功能，不应通过内部 HTTP、Go 包或直接数据库访问绕过；特别是常驻进程、长时间 Promise 作业、任意前端代码及入站 Webhook 的支持情况应先看对应章节。
