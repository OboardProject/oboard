# 权限、网络和密钥

## 权限交集

实际能力 = 不可变版本声明 ∩ 管理员授权 ∩ 当前调用者权限 ∩ 资源范围 ∩ 当前系统/节点安全策略。声明不是授权，角色不是无限授权。手动/UI 触发不能借插件身份提权；后台触发使用管理员授予的插件身份。每次 SDK 调用重验当前授权与执行租约，不能靠运行开始时的快照绕过撤销。

节点重启等写入应使用 selected 节点集合；电源权限单独审批，受服务器开关、Agent 本地安全策略及目标数限制。插件不能修改自己的 grant、读取 Agent token、执行任意 root Shell、操作 Controller SQLite 或转发任意管理 HTTP。

## HTTPS 策略

```json
{"allowed_origins":["https://notify.example.com"],"allowed_methods":["POST"],"max_request_bytes":4096,"max_response_bytes":4096}
```

清单 `network` 和管理员授权 constraints 中 `network` 都需包含所需 origin/method，空集合拒绝全部。origin 是 HTTPS scheme + 精确主机 + 可选端口，没有路径、query、fragment、userinfo 或通配符。允许方法 GET/HEAD/POST/PUT/PATCH/DELETE/OPTIONS。只授予真正需要的方法与目标。

宿主在 DNS 解析和拨号处验证公共 IP 并固定拨号地址，不允许 loopback、私网、链路本地、云元数据地址等非公共地址；TLS 仍以原主机验证。关闭环境代理、cookies 和重定向跟随。禁止用转发头或 Host 等字段操纵网络边界；私网集成不支持，不能配置“全部内网”来绕过。

## 密钥

清单仅声明 `{name,purpose}`，管理员在独立秘密管理入口写入。密文由宿主使用会话秘密加密，管理接口不返回明文。调用 `network.request` 时使用 `secret` 引用，并要求版本声明和 grant 的 `secrets` 数组都准许该名字。当前网络集成将秘密作为 Bearer 认证注入，不是通用模板替换；插件不能指定读取秘密的 SDK。

密钥不应进入仓库、manifest、params、env 默认值、KV、UI、结果或日志。外部服务可能以任意编码回显请求凭据，因此带 `secret` 的调用由宿主丢弃响应 body/headers，只暴露状态码；示例也只输出状态码。不要依赖这类调用读取远端业务响应。加密存储不能防止已获准目标恶意使用收到的密钥：授权 origin 就是信任其接收所授权秘密。

## 内容和供应链

GitHub 地址固定到 commit 并显示包摘要；这不证明作者身份或代码安全，也不是签名插件市场。检查代码和权限差异后才安装授权。脚本、清单、UI 任一内容变化必须重新审核，不能只根据同名函数或同版本号信任更新。

运行租约失效、取消或停用只能停止新宿主调用；已被 Agent/外部系统接受的操作不能承诺撤销。恶意代码可能消耗自身配额，隔离与预算失效时拒绝运行而非降级。模拟测试不是隔离审计，也不证明线上权限正确。
