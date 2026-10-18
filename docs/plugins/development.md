# 插件开发规范与验证

本文约束外部 API 集成、测试与发布。完整字段见 [SDK 参考](sdk.md)，运行环境排障见 [运维](operations.md)。

## HTTP 参数类型与编码

- headers、query 的每个值必须是字符串或 SecretRef。网关不会自动转换数字、布尔值、数组或对象。
- 数字与布尔参数按目标 API 的规范转为字符串，例如 PageSize: String(1)。数组或对象仅在 API 要求 JSON 字符串时使用 JSON.stringify，不能直接作为 query 值。
- 按 API 规则省略可选参数，不要将缺失值变成字面量 undefined 或 null，也不要因值为 0 或 false 就无条件丢弃。
- query 传未做 URL 编码的字符串，由网关编码。不要预编码值后再交给网关；签名协议要求的编码单独用于构造签名原文。
- json 会序列化为 JSON 请求体，不能与 body 同时指定。接口要求表单时，不能用 json 替代。

~~~js
const query = {
  RegionId: env.REGION_ID,
  InstanceIds: JSON.stringify([env.ECS_INSTANCE_ID]),
  PageSize: String(1),
}
~~~

PageSize: 1 会触发 INVALID_ARGUMENT，错误信息为 “header and query values must be strings or secret references”。HTTP 调用计数增加不代表请求已到达远端：预算可能在参数校验前已经计入。

## 签名与密钥引用

SecretRef 不是密钥字符串。不能用字符串拼接、模板字符串或 String(ref) 获取或变换密钥；这些操作只能得到引用的展示文字。前后缀使用 withPrefix / withSuffix，HMAC 交给 oboard.crypto，明文只在 Controller 网关内解析。

阿里云 RPC HMAC-SHA1（SignatureVersion=1.0）请求必须满足：

1. 先确定最终参数值，再按参数名排序；签名参数集合不含 Signature。
2. 参数名和值按 RFC 3986 编码；空格为 %20 而非 +；!'()* 必须转义，~ 保留。拼接规范化查询串后，再按协议编码为签名原文。
3. 签名原文中的 HTTP 方法和路径必须与实际请求一致。根路径 POST 的原文为 POST&%2F& 加编码后的规范化查询串。
4. HMAC 密钥必须为 **AccessKeySecret 加末尾 &**，摘要输出必须为 **Base64**。SDK 默认摘要输出为 hex，必须显式指定。
5. 签名后不得改变参与签名的参数、时间戳、nonce、HTTP 方法或路径。

~~~js
function percentEncode(value) {
  return encodeURIComponent(String(value)).replace(/[!'()*]/g, function (ch) {
    return '%' + ch.charCodeAt(0).toString(16).toUpperCase()
  })
}

// query 已包含最终的字符串参数，且尚无 Signature。
const canonical = Object.keys(query).sort()
  .map(key => percentEncode(key) + '=' + percentEncode(query[key])).join('&')
const stringToSign = 'POST&' + percentEncode('/') + '&' + percentEncode(canonical)
query.Signature = oboard.crypto.hmacSha1(
  env.ACCESS_KEY_SECRET.withSuffix('&'),
  stringToSign,
  { output: 'base64' },
)
~~~

时间戳使用当前请求时间，格式遵循目标 API；nonce 每次请求重新生成。run.scheduled_at 适合判断业务周期，不能作为延迟执行时的签名时间。上例仅适用于对应的阿里云 RPC 协议，不能直接套用于其他云 API 或签名版本。

收到 SignatureDoesNotMatch 时，先逐字符比较服务端返回的 string-to-sign 和本地原文。原文一致再检查密钥后缀、摘要算法、输出编码及配置的 AK/SK 是否配对。测试使用虚构凭据；诊断不得输出真实密钥、完整签名请求或含敏感值的签名原文。

## 回归测试要求

测试替身必须模拟真实 SDK 的输入约束，不能对任意请求直接返回成功。外部 API 插件至少验证：

- 每种 API 动作都能构造合法请求，覆盖数字、布尔、数组和可选参数；HTTP 测试入口检查 query/header 值类型。
- 从实际发送的参数独立重建服务端签名原文，再用测试密钥验证摘要；不能只调用同一生产签名函数来生成预期值。
- 覆盖密钥后缀、Base64 输出、参数排序、特殊字符、方法/路径与时间戳。SecretRef 测试替身应拒绝被强制转换为明文。
- 将已出现的故障变成回归用例，确认旧实现失败、修复后通过，例如遗漏 & 和数字 PageSize。
- 模拟 API 拒绝、超时及空结果。读取失败时，不得基于缺失数据继续执行外部资源变更。开关机等写操作使用测试替身，不使用真实资源做单元测试。

Node.js 可作为本地测试工具，但插件实际运行于 Goja。Node 测试通过不代表沙箱和网关已经验证；清单检查也只解析、编译源码，不执行插件。发布前还需在实际 OBoard 环境验证使用到的 SDK 路径及返回结构，并记录真实运行是否完成。

## Runner 维护要求

Worker/Runner 的隔离实现由宿主维护，插件不能自行绕过：

- Bubblewrap 参数必须经目标 Linux 环境验证，不要照搬其他容器工具的参数。--preserve-fds 曾导致 Unknown option，使实际 Runner 无法启动。
- 当前 RPC 使用 stdin/stdout，stderr 用于启动诊断。stdout 不得写普通诊断文本；插件日志通过结构化 RPC 返回。
- 自检和实际运行必须使用相同的 Bubblewrap 参数，还需验证真实子进程的请求、能力调用、回包、日志和完成结果往返。只检查参数字符串或自检启动不足以证明执行链路可用。
- 保留网络命名空间隔离、IPAddressDeny=any、seccomp 和资源限制。外部 HTTPS 经 Controller 网关，节点探测经 Agent；不得为修复启动或签名错误直接给 Worker 开放网络。
- 未知进程退出不能直接归为资源耗尽。保留有界启动诊断，区分运行环境错误、已确认的 OOM、超时和取消。

## 发布前检查

修改已发布插件内容时递增 manifest.json.version；相同版本不同内容会被拒绝。测试代码留在源码仓库，打包只包含 [允许的文件](packaging.md)，不要把测试目录、依赖、缓存或凭据塞入 .obplugin。

提交推送不等于面板已安装新版本。更新后检查执行记录里的插件版本，再运行验证；若连续失败导致实例自动暂停，修复后还需恢复实例。报告结果时区分本地测试通过、已推送、已安装及真实执行成功。
