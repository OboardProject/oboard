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

```bash
go run ./cmd/oboard-plugin-tool check examples/plugins/trace-monitor
go run ./cmd/oboard-plugin-tool keygen publisher.pem
go run ./cmd/oboard-plugin-tool pack -key publisher.pem -publisher "Acme" examples/plugins/trace-monitor trace-monitor.obplugin
go run ./cmd/oboard-plugin-tool inspect trace-monitor.obplugin
```

- `check`：与主控相同的清单与源码校验，外加未声明能力检查（代码调用了清单未声明的 SDK 方法时失败）与未使用变量提示。
- `pack`：生成确定性的归档；带 `-key` 时签名。私钥必须是 `0600`。
- `keygen`：生成 PKCS#8 PEM Ed25519 私钥（不覆盖已有文件）并打印发布者身份。

## 安装与更新

面板“安装插件”支持上传 `.obplugin` 或填写 GitHub 公共仓库（仓库根目录包含上述文件，可指定分支、标签或提交）。流程：

1. **检查**：解析、校验并展示名称、版本、发布者与签名状态、包摘要、申请的权限与 HTTP 主机、永久禁止的能力、需要配置的变量。GitHub 来源解析为不可变提交。
2. **确认安装**：主控再次读取来源，要求提交与包摘要与预览一致，否则要求重新预览。
3. 安装后插件处于停用状态，需要创建实例、完成配置并由管理员授权。

对已安装的插件，预览会列出与当前版本的**权限差异**：新增/移除的能力、主机、方法、事件、密钥、变量，变量类型变化，新增必填项，资源上限扩大。扩大权限的更新激活后，实例在重新审核授权前不会运行；被移除的变量会从实例配置中删除，授权自动收窄到新清单。

面板编辑器中，诊断实时显示清单问题、未声明能力与自动生成的配置面板预览；保存草稿不会影响运行中的版本，发布后成为新的本地版本。
