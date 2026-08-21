# OBoard

**中文** | [English](README.en.md)

OBoard 是一个面向多节点代理网络的 Linux 控制面，集中管理服务器、配置、订阅、证书与部署。它提供 Web 面板、REST/WebSocket API、订阅渲染，以及已签名的 Agent / 内核发布分发。节点侧 Agent 与内置代理内核 `oboard-sb` 位于独立仓库 `OboardProject/oboard-agent`。

## 概览

- **集中管理**：通过面板和 API 管理服务器、用户、订阅、代理路径、证书与 DNS
- **部署调度**：按服务器生成并下发已签名配置，支持在线任务、健康检查、失败重试与可见的任务状态
- **订阅与分发**：渲染订阅，提供已校验的 Agent / 内核安装包，并支持 `OBOARD_BASE_PATH`
- **安全边界**：一次性令牌注册、签名任务、加密存储、审计控制与受限操作
- **简单安装**：一条脚本完成安装、更新、卸载，并可切换稳定版与开发版

## 安装方法

OBoard Controller 仅支持 Linux，官方安装包支持 `amd64` 和 `arm64`。默认安装根目录为 `/opt/oboard`，默认端口为 `2787`；安装脚本会自动识别 systemd 或 OpenRC。

### 安装

安装最新稳定版（若当前没有稳定版，安装器会回退到开发版）：

```bash
curl -fsSL https://raw.githubusercontent.com/OboardProject/oboard/main/scripts/install.sh | sudo env bash
```

明确安装开发版（开发版是跟随 main 构建的可变发布，建议用于测试环境）：

```bash
curl -fsSL https://raw.githubusercontent.com/OboardProject/oboard/main/scripts/install.sh | sudo env VERSION=dev bash
```

需要自定义安装根目录时，可传入 `INSTALL_DIR`：

```bash
curl -fsSL https://raw.githubusercontent.com/OboardProject/oboard/main/scripts/install.sh | sudo env INSTALL_DIR=/data/oboard bash
```

### 更新

重新执行安装脚本，检测到已安装的 Controller 后会自动执行更新，并保留现有配置、账号和数据。

保持当前已保存的更新渠道：

```bash
curl -fsSL https://raw.githubusercontent.com/OboardProject/oboard/main/scripts/install.sh | sudo env bash
```

更新到最新稳定版：

```bash
curl -fsSL https://raw.githubusercontent.com/OboardProject/oboard/main/scripts/install.sh | sudo env VERSION=latest bash
```

更新到开发版：

```bash
curl -fsSL https://raw.githubusercontent.com/OboardProject/oboard/main/scripts/install.sh | sudo env VERSION=dev bash
```

### 切换稳定版与开发版

切换渠道与更新使用同一套命令，安装器会将渠道写入 `$INSTALL_DIR/config/controller.env` 的 `OBOARD_UPDATE_CHANNEL`：

切换到稳定版：

```bash
curl -fsSL https://raw.githubusercontent.com/OboardProject/oboard/main/scripts/install.sh | sudo env VERSION=latest bash
```

切换到开发版：

```bash
curl -fsSL https://raw.githubusercontent.com/OboardProject/oboard/main/scripts/install.sh | sudo env VERSION=dev bash
```

切换后，不带 `VERSION` 的更新会按当前已保存渠道执行。若已安装固定版本，脚本会要求显式传入 `VERSION=latest` 或 `VERSION=dev` 后再更新。

### 卸载

保留配置、数据库和证书（默认目录下再次安装时会自动沿用；自定义目录需重新传入同一 `INSTALL_DIR`）：

```bash
curl -fsSL https://raw.githubusercontent.com/OboardProject/oboard/main/scripts/install.sh | sudo env OBOARD_ACTION=uninstall bash
```

连同整个安装根目录一起删除：

```bash
curl -fsSL https://raw.githubusercontent.com/OboardProject/oboard/main/scripts/install.sh | sudo env OBOARD_ACTION=uninstall OBOARD_PURGE_DATA=1 bash
```

### 首位管理员

首次安装时，安装器会提示创建超级管理员。若未设置密码，它会生成一次性随机密码并在安装完成界面显示一次；该密码只显示一次，请先保存再关闭窗口，登录后请立即修改。

## 许可证

Copyright 2026 OBoard contributors.

`oboard`（Controller 与 Web）以 [GNU GPL v3](LICENSE) 发布。可在该许可证允许的范围内使用、修改与再分发本软件。
