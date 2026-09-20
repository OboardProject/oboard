# 快速入门

## 1. 准备工具和最小仓库

需要与宿主版本一致的 `oboard-plugin-tool`。从 OBoard 源码构建时使用模块要求的 Go 版本；不需要安装 Node.js。以下示例中 `WORKSPACE` 是源码仓库的父工作区，构建输出和临时缓存不得放进产品源码仓库。

```sh
export WORKSPACE=/absolute/path/to/workspace
export SOURCE="$WORKSPACE/oboard"
export GOWORK=off
export GOCACHE="$WORKSPACE/.cache/plugin-quickstart/go"
export GOTMPDIR="$WORKSPACE/.cache/plugin-quickstart/tmp"
mkdir -p "$GOCACHE" "$GOTMPDIR" "$WORKSPACE/dist/bin" "$WORKSPACE/dist/plugins"
cd "$SOURCE"
go build -o "$WORKSPACE/dist/bin/oboard-plugin-tool" ./cmd/oboard-plugin-tool
```

复制 `examples/plugins/template` 的内容到自己的仓库根目录（不是复制整个 OBoard 仓库）。把 `manifest.json` 的 `plugin_id` 改成你拥有的稳定标识，例如 `acme.hello`，修改名称、版本和描述。只支持仓库根目录入口：

```text
my-plugin/
├── manifest.json
├── main.js
├── ui.json          # 可选
├── docs/            # 开发文档，不安装
├── tests/           # 开发测试，不安装
└── src/             # 可选构建输入，不安装
```

`main.js` 必须是最终的 UTF-8 单文件 JavaScript；宿主不构建 `src/`、不安装依赖、不读取 `package.json`。

## 2. 理解第一个插件

模板声明 `state.get`，读取一个插件私有键，没有节点或网络权限：

```js
function main() {
  var result = oboard.state.get({ key: "greeting" });
  return { message: result.result.exists ? result.result.value : "你好，OBoard" };
}
```

全局 `params` 是本次 JSON 输入，`env` 是宿主合并的已声明环境项。调用是同步的，成功返回 `{ok:true,result:...}`；失败抛出带 `error_code` 的值。**不要写 `async function main()`，不要用 `fetch`、`require` 或 ES module import。**

## 3. 打包并校验

```sh
"$WORKSPACE/dist/bin/oboard-plugin-tool" pack /absolute/path/to/my-plugin "$WORKSPACE/dist/plugins/acme-hello-1.0.0.zip"
"$WORKSPACE/dist/bin/oboard-plugin-tool" check "$WORKSPACE/dist/plugins/acme-hello-1.0.0.zip"
```

输出显示内容 SHA-256、源码字节数及是否含 UI，不打印源码或密钥。输出路径必须显式指定且不存在；工具绝不覆盖已有包或输入文件。`pack` 忽略开发目录，仅打包两个必需文件和可选 `ui.json`；`check` 不执行 JavaScript，也不证明权限、联网或节点动作一定成功。

## 4. 安装、授权、配置和运行

在插件管理入口导入 ZIP，或粘贴公开 GitHub 仓库根地址并选择 ref。审核插件身份、版本、摘要、固定提交及能力差异后确认安装。首次安装保持停用，管理员需单独授予精确能力和资源范围；没有授权时运行被拒绝是正常结果。

确认插件运行环境已安装，Worker 已连接且隔离可用。按宿主提示启用插件运行环境，不要把 Runner 改成以 Controller 身份执行来绕过隔离错误。模板只需授予 `state.get`；不需要服务器权限、网络权限或电源权限。

保存配置，启用插件，然后创建一次手动运行（输入 `{}`）。在执行记录查看结果和脱敏日志。带 UI 的包可从插件页面发起同一运行入口；UI 不会取得额外权限。

## 5. 逐步扩展

先试 `node-inspection`，由管理员给一个测试节点授权，用该节点的**字符串 ID**填写 `server_id`。定时巡检可将同样参数绑定到 interval 触发器；不要依赖浏览器页面常驻。

使用 `https-notification` 前阅读[安全](security.md)并配置精确公共 HTTPS 目标；使用 `batch-operations` 前先仅预览、只授权测试节点，再明确确认维护操作。示例没有生产默认节点或秘密。

更新代码必须发布新版本、重新打包、审核并重新授权；已有版本不能用同版本号替换内容。最后清理本次创建的构建缓存：

```sh
rm -rf "$WORKSPACE/.cache/plugin-quickstart"
```

不得删除别人的共享缓存。部署、发布 GitHub Release 或操作生产节点仍需独立授权。
