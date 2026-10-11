# 代理流量 DSCP 标记

设置页提供全局、逐台和批量开关，默认关闭，标记固定为 EF（DSCP 46）。全局开启覆盖现有和新接入服务器；关闭全局后，恢复各服务器保留的单独设置。

## 支持范围

已接入的内核代理 socket 包括 VLESS、AnyTLS、Shadowsocks、SOCKS5、Snell、Mieru 以及 direct outbound。

**HY2（Hysteria2）暂不支持 DSCP 标记。** 当前 QUIC 发送路径的逐包 ECN 控制信息可能覆盖 socket 的 DSCP，仅设置 IP_TOS / IPV6_TCLASS 不能保证 HY2 外层数据包携带 EF。现阶段保留 ECN、GSO、批量发送和拥塞控制行为，不通过关闭这些功能来实现标记。

SSH、realm、WARP 不在本功能范围内。直接路由动作也未覆盖。HY2 目标侧使用已支持的 direct outbound 时，该 socket 的标记不代表 HY2 外层 QUIC 已获支持。Windows 的 socket 标记状态为 unsupported。

## 生效与性能

- 策略通过独立签名任务 apply_proxy_dscp 下发，切换不重启、不重新部署代理。
- TCP 新连接使用当前策略，已有 TCP 连接自然结束；共享 UDP socket 和监听 socket 在线更新。
- 保留 ECN，无新增逐包处理、数据流包装、载荷拷贝、防火墙规则或队列调度。socket 创建和策略切换仍有少量系统调用开销，不承诺绝对零成本。
- 离线服务器保留期望设置，待上线同步；失败可在管理服务器中重试。

## 状态与接口

保存设置不等于节点已经应用。管理列表区分等待同步、离线、同步失败、部分支持及不支持；启用时的 partial 和 report.excluded 明确表示覆盖限制。DSCP 只是标签，不保证上游保留标签、优先调度或带宽增加。

全局设置字段为 proxy_dscp_enabled。逐台 REST 路径为 /api/v1/servers/:id/proxy-dscp，受 Controller base path 约束；自动化使用 servers.proxy_dscp.read / servers.proxy_dscp.update，写入经过服务器范围授权和 Changeset。该设置不授予 WebSSH 或远程执行权限。

### IPv6 MPTCP 内核限制

Linux 上已确认的 MPTCP socket（SO_PROTOCOL = IPPROTO_MPTCP）若对 IPV6_TCLASS 返回 EOPNOTSUPP，报告为支持范围限制，不计入 socket_errors。双栈监听必须独立尝试 IPv4 IP_TOS，不能因 IPv6 设置失败而跳过 IPv4。仅在 IPv4 成功（或 socket 为 IPv6-only）时，将该已知 IPv6 限制加入 report.excluded 的 ipv6-mptcp 项；不计为完整 socket 标记成功。普通 TCP 的错误、IPv4 设置错误以及权限或描述符错误仍作为真实错误报告。

Web 显示“部分支持：IPv6 MPTCP 的 DSCP 受内核限制”，不针对该限制提示重试；真实标记错误与策略读取错误分别显示。没有改动 MPTCP、ECN、监听地址、代理转发或 QoS 规则。设置再次应用时重新观察限制。此项沿用现有 v1 报告字段，不增加 API 版本或迁移代码。
