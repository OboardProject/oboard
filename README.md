# oboard

**中文** | [English](README.en.md)

OBoard 控制面主控（Controller），提供面板、REST/WebSocket API、订阅渲染，以及已签名的 Agent / 内核分发。Agent 与内核代码位于独立仓库 `OboardProject/oboard-agent`。

## 特点

- **面板与 API**：Web 界面、REST `/api/v1`、Agent WebSocket/回调、订阅、安装脚本与发布包下载
- **配置生成**：主控侧校验、代理路径拓扑与订阅渲染，不链接 sing-box
- **二进制安装**：默认端口 `2787`；可通过 `OBOARD_BASE_PATH` 启用隐藏路径
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

### 首位管理员

未设置 `OBOARD_ADMIN_PASSWORD` 时，安装器会为首位管理员生成一次性随机密码，并在首次启动时于 controller 服务日志中打印一次。登录后请立即修改。


## 许可证

Copyright 2026 OBoard contributors.

`oboard`（Controller 与 Web）以 [GNU GPL v3](LICENSE) 发布。  
可在该许可证允许的范围内使用、修改与再分发本软件。
