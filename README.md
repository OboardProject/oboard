# OBoard

OBoard 是一个面向多节点代理网络的 Linux 控制面，用于集中管理服务器、订阅、证书与部署。它提供 Web 面板、REST/WebSocket API、订阅渲染，以及已签名的 Agent / 内核分发。

## 特性

- 集中管理服务器、用户、订阅、代理路径、证书与 DNS
- 按服务器生成并下发已签名配置，支持在线任务、健康检查与失败重试
- 渲染订阅并分发已校验的 Agent / 内核安装包，支持 `OBOARD_BASE_PATH`
- 提供一次性令牌注册、签名任务、加密存储与审计控制
- 官方 Linux 二进制安装脚本支持安装、更新、卸载，并可通过 `VERSION` 参数选择稳定版或开发版

## 安装方法

OBoard Controller 以 Linux 二进制安装包发布，支持 `amd64` 和 `arm64`。官方安装脚本覆盖安装、更新与卸载，并通过 `VERSION=latest` 或 `VERSION=dev` 选择稳定版或开发版。

默认安装根目录为 `/opt/oboard`，默认端口为 `2787`。

## 相关仓库

- [oboard-agent](https://github.com/OboardProject/oboard-agent)：节点侧 Agent 与内置代理内核 `oboard-sb`

## 许可证

Copyright 2026 OBoard contributors.

`oboard`（Controller 与 Web）以 [GNU GPL v3](LICENSE) 发布。可在该许可证允许的范围内使用、修改与再分发本软件。
