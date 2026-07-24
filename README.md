# oboard

**中文** | [English](README.en.md)

OBoard 控制面主控（Controller），提供面板、REST/WebSocket API、订阅渲染，以及已签名的 Agent / 内核分发。Agent 与内核代码位于独立仓库 `OboardProject/oboard-agent`。

## 特点

- **面板与 API**：Web 界面、REST `/api/v1`、Agent WebSocket/回调、订阅、安装脚本与发布包下载
- **配置生成**：主控侧校验、代理路径拓扑与订阅渲染，不链接 sing-box
- **灵活安装**：二进制或 Docker；默认端口 `2787`；可通过 `OBOARD_BASE_PATH` 启用隐藏路径
- **证书管理**：面板托管或手动 DNS-01（含通配符），以及 Agent 侧 HTTP-01，基于 `acme.sh`
- **签名分发**：发布脚本仅打包同级 `oboard-agent` 仓库中已签名的 Agent / 内核产物

## 仓库结构

| 路径 | 说明 |
|------|------|
| `cmd/controller` | Controller 程序入口 |
| `internal/controller` | REST API、WebSocket、安装/更新脚本，以及静态资源与下载分发 |
| `internal/core` | 配置生成、校验与订阅渲染 |
| `web` | Web 面板 |
| `deploy` | 服务单元与部署资产 |

## 构建

```bash
go test ./...
cd web && npm run build
cd .. && go build -o ../dist/controller/oboard-controller ./cmd/controller
```

> 本地开发时，建议将编译产物输出至工作区上级目录的 `dist/`，以避免污染本仓库目录。

Controller 发布脚本会定位同级的 `../oboard-agent` 仓库，调用其发布构建，并仅打包已签名的 Agent / 内核产物供面板下载。

## 安装与管理

默认端口为 `2787`。

### 二进制安装

```bash
curl -fsSL https://raw.githubusercontent.com/OboardProject/oboard/main/scripts/install.sh | sudo bash
```

### Docker 安装（推荐）

最新稳定镜像：

```bash
curl -fsSL https://raw.githubusercontent.com/OboardProject/oboard/main/scripts/install-docker.sh | sudo sh
```

镜像默认监听容器端口 `2787`

### 首位管理员

未设置 `OBOARD_ADMIN_PASSWORD` 时，安装器会为首位管理员生成一次性随机密码。Docker 在安装时打印；二进制安装在首次启动时于 controller 服务日志中打印一次。登录后请立即修改。

### 隐藏路径

设置 `OBOARD_BASE_PATH`，可将主控所有对外入口统一挂到同一 URL 路径下：

```bash
OBOARD_BASE_PATH=/your-private-path oboard-controller
```

等价命令行参数为 `-base-path /your-private-path`。此前缀作用于面板及其静态资源、`/api/v1`、Agent WebSocket/回调、订阅、`/install`、`/downloads` 与 `/healthz`。以上例为例，面板地址为 `http://127.0.0.1:2787/your-private-path`，未带前缀的路径返回 `404`。

| 用途 | 示例 |
|------|------|
| 面板地址 | `https://panel.example.com/hidden` |
| 带前缀的 Docker 安装 | `sudo env OBOARD_BASE_PATH=/hidden sh`（配合 install-docker.sh） |
| 二进制环境文件 | `/etc/oboard/controller.env` |

Docker 安装示例：

```bash
curl -fsSL https://raw.githubusercontent.com/OboardProject/oboard/main/scripts/install-docker.sh \
  | sudo env OBOARD_BASE_PATH=/your-private-path sh
```

二进制安装时，可将同一环境变量传给安装脚本，或写入 `/etc/oboard/controller.env`。环境变量仅用于初始化新数据库；之后以数据库中持久化的主控设置为准。

管理员可在 **设置 > 基础设置** 中后续修改前缀。主控会立即同时接受新旧路径，并为每台已注册 Agent 入队一条 `update_agent_config` 任务，展示各 Agent 进度。仅当全部目标 Agent 通过新地址回报成功后，才移除旧路径。失败或离线 Agent 可手动重试，不会自动重试循环；进行中的迁移可在主控重启后继续。前缀须为以 `/` 开头的 ASCII URL 安全值；较长的随机值可降低被扫到的概率，但不能替代 HTTPS、鉴权、防火墙或可信反向代理。

### 同机部署 Agent

Controller 与 Agent 是独立应用。同一台机器可同时运行两者：先装 Controller，在面板中添加本机服务器，再执行面板生成的 Agent 安装命令。

### 证书

证书管理使用 `acme.sh`。Controller 支持面板托管或手动 DNS-01（含通配符证书），对接 Cloudflare、阿里云 DNS、腾讯 DNSPod、腾讯 ESA 与华为云。HTTP-01 在选定 Agent 上执行，需要入站 TCP 80 端口。私钥在 Controller 侧加密保存；每台 Agent 仅可拉取绑定到其已启用入口的证书版本，并以私有权限存放在 `state_dir` 下。

### 数据备份与恢复

管理员可在 **设置 > 数据备份** 创建加密备份，或设置每日、每周自动备份。备份包含主控数据库、证书续期状态和恢复所需的受保护数据，不包含可重建的运行日志、下载缓存和程序文件。

恢复密码用于加密每一份备份，设置后不会再次显示，请单独妥善保管。可选用一个 S3 兼容存储（AWS S3、Cloudflare R2、MinIO、Backblaze B2 等）或 WebDAV 目标保存加密副本。本地和远端分别设置滚动保留数量；本地副本过期后仍可由面板从当前第三方目标取回。更换第三方目标不会删除旧目标中的文件。

上传备份时，面板会先检查恢复密码、文件完整性和版本兼容性，再询问是否立即恢复。恢复前会自动创建保护备份，主控随后短暂重启，已有登录会话失效，并将恢复后的配置重新下发到受管节点。备份只能恢复到相同或更新版本的主控；请先升级目标主控，再恢复较新备份。

### 发布

GitHub Actions 以同一组 Controller 与 Agent 修订构建二进制包与 Docker 镜像。开发镜像标签为 `dev`，稳定镜像为 `latest`，并保留精确版本标签以便锁定安装。每次发布前都会跑 Controller、Agent、内核测试以及 Web 构建。

## 安全边界

OBoard Controller 是特权控制面。能入队配置、转发、隧道、服务或更新任务的管理员与操作员，对已注册 Agent 主机应视为 root 等价身份。请使用 HTTPS、限制面板访问、保护会话与发布签名密钥，并在疑似泄露后轮换 Agent 凭据。

## Agent 协议

单体开发阶段，Controller / Agent 的 JSON 线协议约定见工作区文档 [`../docs/AGENT_PROTOCOL.md`](../docs/AGENT_PROTOCOL.md)。面向用户的 UI 文案请与该开发文档分开维护。本仓库工程规范见 [`../AGENTS.md`](../AGENTS.md)。

## 许可证

Copyright 2026 OBoard contributors.

`oboard`（Controller 与 Web）以 [GNU GPL v3](LICENSE) 发布。  
可在该许可证允许的范围内使用、修改与再分发本软件。
