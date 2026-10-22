# 打包、签名与安装

## `.obplugin`

ZIP 容器，最大 2 MiB、最多 8 个文件、解压后 ≤1 MiB，只允许：

| 文件 | 必需 | 说明 |
|---|---|---|
| `manifest.json` | ✓ | ≤64 KiB |
| `main.js` | ✓ | ≤512 KiB UTF-8 |
| `icon.png` | | PNG |
| `README.md`、`LICENSE` | | UTF-8 文本 |
| `signature.json` | | 发布者签名 |

路径穿越、绝对路径、目录、链接、重复名、加密条目、非 Deflate/Store 压缩、原生二进制内容一律拒绝。检查过程只解析与编译，**从不执行**插件代码。

包摘要 `sha256` 覆盖除 `signature.json` 外的所有文件（按名称排序，含名称与长度），与 ZIP 顺序、时间戳、压缩方式无关。

## 发布者签名

`signature.json` 使用 Ed25519 对内容摘要签名，发布者身份为 `ed25519:<公钥 SHA-256>`。未签名的包显示为“本地（未签名）”。同一插件的更新必须来自同一发布者，否则以 `PUBLISHER_CHANGED` 拒绝；在面板编辑器中发布的版本始终是本地发布者。

## `oboard-plugin-tool`

以下从 OBoard 工作区的 `oboard/` 仓库目录执行；构建缓存、二进制、私钥和包均留在工作区根目录。已有发布者必须复用原签名密钥，不能每次发布都生成新身份。

```bash
(
set -eu
# 使用本次任务独占的缓存目录；结束后只清理该目录。
mkdir -p ../.cache
plugin_cache=$(mktemp -d ../.cache/plugin-build.XXXXXX)
trap 'rm -rf -- "$plugin_cache"' EXIT
mkdir -p "$plugin_cache/go" "$plugin_cache/tmp" ../dist/bin ../dist/plugins ../.tmp
GOCACHE="$PWD/$plugin_cache/go" GOTMPDIR="$PWD/$plugin_cache/tmp" \
  go build -o ../dist/bin/oboard-plugin-tool ./cmd/oboard-plugin-tool
../dist/bin/oboard-plugin-tool check examples/plugins/trace-monitor
# 仅首次建立发布者身份时执行，私钥不得提交或放入插件目录。
../dist/bin/oboard-plugin-tool keygen ../.tmp/plugin-publisher.pem
../dist/bin/oboard-plugin-tool pack -key ../.tmp/plugin-publisher.pem -publisher "Acme" \
  examples/plugins/trace-monitor ../dist/plugins/trace-monitor.obplugin
../dist/bin/oboard-plugin-tool inspect ../dist/plugins/trace-monitor.obplugin
)
```

中途失败应停止发布并清理本次专用缓存；不要删除签名私钥或预先存在的共享缓存。外部插件仓库同样应将私钥、缓存和生成包放在仓库之外。

- `check`：与主控相同的清单与源码校验，外加文本级未声明能力检查与未使用变量提示。它可能漏掉动态或别名调用，不是安全审计，也不运行 `main`；完整验证见 [开发规范](development.md)。
- `pack`：生成确定性的归档；带 `-key` 时签名。私钥必须是 `0600`。
- `keygen`：生成 PKCS#8 PEM Ed25519 私钥（不覆盖已有文件）并打印发布者身份。

## 安装与更新

面板“安装插件”支持上传 `.obplugin` 或填写 GitHub 公共仓库（仓库根目录包含上述文件，可指定分支、标签或提交）。流程：

1. **检查**：解析、校验并展示名称、版本、发布者与签名状态、包摘要、申请的权限与 HTTP 主机、永久禁止的能力、需要配置的变量。GitHub 来源解析为不可变提交。
2. **确认安装**：主控再次读取来源，要求提交与包摘要与预览一致，否则要求重新预览。
3. 安装后插件处于停用状态，需要创建实例、完成配置并由管理员授权。

## GitHub 来源与一键更新

从 GitHub 安装时，主控会记录仓库、分支/标签（留空表示默认分支）与解析出的提交。详情页显示这些来源信息；插件卡片显示 `owner/repo`。

- **检查更新**：管理员打开详情页时自动检查，也可手动刷新。主控只读取记录的仓库与分支：提交未变化时仅两次轻量请求，不读取包内容；提交变化才完整读取并校验，再与当前版本比较。内容摘要相同视为已是最新。
- **一键更新**：更新来源只能是安装时记录的仓库与分支，请求不能改变它。确认的提交与包摘要必须与 GitHub 当前内容一致，分支在检查之后又移动则要求重新检查。新版本没有扩大权限时直接更新；扩大权限或新增必填项时先展示差异再确认，更新后实例在重新审核授权前不会运行。
- **固定提交**：以完整提交 ID 安装的插件没有可更新的版本。
- **拒绝的情况**：发布者与已安装版本不同（`PUBLISHER_CHANGED`）；仓库里的版本号已安装过但内容不同，需要发布者提升 `version`。
- 检查与更新仅交互式管理员可用，MCP 不可调用（目录项 `plugins.source.check`、`plugins.source.update` 为 AdminOnly）。

对已安装的插件，预览会列出与当前版本的**权限差异**：新增/移除的能力、主机、方法、事件、密钥、变量，变量类型变化，新增必填项，资源上限扩大。扩大权限的更新激活后，实例在重新审核授权前不会运行；被移除的变量会从实例配置中删除，授权自动收窄到新清单。

面板编辑器中，诊断实时显示清单问题、未声明能力与自动生成的配置面板预览；保存草稿不会影响运行中的版本，发布后成为新的本地版本。
