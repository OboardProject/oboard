# 开发、测试和故障定位

## 验证层次

1. 编辑器关联 `sdk/plugins/manifest.schema.json` / `ui.schema.json`，JS 项目关联 `oboard.d.ts`。
2. `oboard-plugin-tool pack <project> <new.zip>`、`check <zip>` 验证入口、容器、清单、Goja 编译和 UI；不会运行顶层代码、下载源码映射或请求外网。
3. 对 `main()` 使用模拟宿主 SDK 的执行测试，检查每次能力/参数、失败分支和返回值。示例的 Goja 执行测试位于 `cmd/oboard-plugin-tool/examples_test.go`，用真实 Runner，但 SDK 全部模拟，不触碰节点或公网。
4. 在测试 Controller 导入、审核、授予最小权限，执行 simulate；确认模拟回执被正确处理。
5. 仅对已授权测试节点和专用外部服务进行 live 验证。验证撤销权限/停用后失败，越界节点被拒绝，更新不静默扩权。

SDK 类型用于编辑器，不会限制恶意 JS；后端验证才是安全边界。`check` 通过不代表 main 存在、Promise 得到等待或网络可达，不证明 isolation/cgroup 可用。

## 本仓库聚焦测试

从 OBoard 模块目录运行，使用工作区任务专属缓存和临时目录：

```sh
export WORKSPACE=/absolute/path/to/workspace
export GOWORK=off
export GOCACHE="$WORKSPACE/.cache/plugin-doc-tests/go"
export GOTMPDIR="$WORKSPACE/.cache/plugin-doc-tests/tmp"
export TMPDIR="$WORKSPACE/.cache/plugin-doc-tests/tmp"
mkdir -p "$GOCACHE" "$GOTMPDIR"
go test ./cmd/oboard-plugin-tool
rm -rf "$WORKSPACE/.cache/plugin-doc-tests"
```

测试会打包所有例子、验证两份 Schema、运行 Goja 示例（包含只读、模拟、明确确认、错误和权限失败路径），并检查运行时方法与类型声明合同。它不启动开发服务器、不部署、不提交、不访问 GitHub。

## 三个例子的设置

- **node-inspection**：声明/授权 `servers.status`，指定一个测试服务器，输入 `{"server_id":"123"}`。没有写入或自动修复；未知/陈旧状态按原样展示。
- **https-notification**：将 manifest 和 main.js 中 example.com 同步换成你控制的公共 HTTPS 服务，重新打包；管理员给该 origin 的 POST 及 `notify_token` 引用授权并配置专用 Bearer 令牌。输入 `{"message":"测试通知"}`。不要返回上游原文；成功只表示服务 HTTP 接受，不证明消息送达最终用户。
- **batch-operations**：授予 selected 测试节点的 `servers.get`、`services.restart`、`operations.get`，启用节点插件操作策略。输入 `{"server_ids":"123,124","service":"oboard-sb","confirm":false}` 先预览；核对后单独提交 confirm:true。代码最多五个节点、不含电源动作、不自动重试，不把接受回执当完成。

## 常见问题

- `manifest` 错误：查看字段拼写、能力名、参数 Schema 复杂度；不要加入 `$schema` 或工具链专用字段。
- 源码编译失败：使用宿主 Goja 支持的语法，去掉 TS、import、require；不要把 source map 或开发依赖装进包。
- UI 表格空：该组件必须有自己的查询 action；返回数组或 `{rows:[...]}`，列 key 必须匹配，不能依赖其他组件的结果。
- `runtime_unavailable`：管理员检查 Worker/隔离/全局开关，不在源码中绕过。
- `permission_denied`：声明、grant、调用者、资源或秘密引用至少一项未满足；不要简单扩大为全节点权限。
- GitHub 404/限流：确认公开仓库根地址和 ref；可用本地 ZIP，不粘贴私人 token。

反馈只含插件版本、包摘要、无秘密运行 ID、阶段和 error_code。不要上传完整数据库、源码内秘密、响应体或授权头。
