# OBoard

OBoard 是一个面向个人使用的多节点代理网络的 Linux 控制面板，用于集中管理服务器、订阅、证书与部署。它提供 Web 面板、MCP、订阅支持，以及已签名的 Agent / 内核分发。

## 特性

- 集中管理服务器、用户、订阅、代理路径、证书与 DNS
- 基于拓扑图的代理链路编辑，支持端口转发、链式代理、WARP、规则分流等情况
- 按服务器生成并下发已签名配置，支持在线任务、健康检查与失败重试
- 提供一次性令牌注册、数据统计及审计台功能
- 面向个人及小范围分发，不适用于商业用途

## 安装方法

Oboard 面板基于二进制运行

[安装](https://oboardproject.github.io/)

默认安装根目录为 `/opt/oboard`，默认端口为 `2787`

## 相关仓库

- [oboard-agent](https://github.com/OboardProject/oboard-agent)：节点侧 Agent 与内置代理内核 `oboard-sb`

## 许可证

Copyright 2026 OBoard contributors.

`oboard`以 [GNU GPL v3](LICENSE) 发布。
