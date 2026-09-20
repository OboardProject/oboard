# JavaScript 运行时

`main.js` 是单个 UTF-8 文件，最多 256 KiB。宿主使用 Goja 解析和编译，执行顶层语句后调用全局 `function main()`，无参数；输入从全局 `params` 读取。返回值必须能 JSON 编码，建议总是返回明确的 object/array。不要返回函数、循环对象、Promise 或未定义值。顶层代码也计入时间预算。

Goja 支持的 ECMAScript 语法以当前宿主工具编译结果为准，不承诺浏览器/Node 版本兼容。可使用普通对象、数组、字符串、`JSON`、`Math` 和 `Date`；`Date` 是运行环境时间，不作为幂等键。无 DOM、浏览器 `window`、`fetch`、XHR、Node `process`、`Buffer`、`fs`、定时器或模块加载器。禁止 `require(...)` / ES module import，工具不安装 npm 依赖。TypeScript 必须预先编译/打包成兼容单文件，类型声明不是 TypeScript 执行器。

## 全局对象

- `params`：经 Schema 验证的本次 JSON 输入。
- `env`：宿主合并的声明环境和系统运行元数据，值为字符串；不是 `os.Environ()`。
- `oboard`：同步宿主 SDK，见 [完整参考](sdk.md)。每个调用都消耗预算。
- `log.info(message)` / `log.warn(message)` / `log.error(message)`：短文本日志，无结构化字段参数。单条最多保留 4000 字节，移除 ESC 字符，总预算默认 256 KiB。不要记录秘密、请求体或上游响应；不得依赖脱敏识别所有格式。

```js
function main() {
  try {
    var value = oboard.state.get({ key: "counter" }).result;
    return { exists: value.exists, version: value.version };
  } catch (error) {
    log.warn("读取状态失败");
    return { error_code: error.error_code || "internal_error" };
  }
}
```

SDK 失败抛出 `{error_code,message}`，不保证是原生 Error。不要向最终用户原样展示未知错误消息。`async function main` 虽可能通过语法编译，但宿主不会等待 Promise，**不受支持**；也不应在顶层调用 SDK。工具只编译、不执行，存在 `main` 及其真实行为由执行测试验证。

## 资源和执行语义

默认 30 秒、64 MiB、100 次 SDK、10 次管理动作、64 KiB 结果，调用栈最多 128。单插件默认并发 1，主控默认并发 2；系统设置还可能缩小预算。`operations.wait` 每次最多 8 秒且消耗同一执行时间。不能在插件中通过循环等待长任务。

Runner 需要宿主隔离环境可用，缺失隔离时应明确失败，不回退到 Controller 进程执行。不要将模拟模式视作真实环境验证：模拟时管理动作、服务状态任务和 CAS 可能返回 `{simulated:true,...}`，不是实际网络响应/节点证据。

一次执行固定版本与输入；SDK 自动生成动作键 `<方法>:<调用序号>`。相同动作键不同载荷失败，不保证第三方网络具有 exactly-once 语义。Runner 崩溃、取消、超时后，已下发动作可能仍执行。恢复动作前先检查现有记录，绝不盲目重试电源、付款或外部通知。
