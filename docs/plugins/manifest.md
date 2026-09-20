# manifest.json 完整参考

安装入口是仓库/ZIP **根目录**的 UTF-8 `manifest.json`。它是封闭对象，不接受任意扩展字段。参照 [JSON Schema](../../sdk/plugins/manifest.schema.json) 与 [模板](../../examples/plugins/template/manifest.json)。`$schema` 是编辑器配置，不应加进实际清单；在编辑器外部关联 Schema。

| 字段 | 类型 / 默认 | 合同 |
|---|---|---|
| `plugin_id` | string，必需 | 最长 128 字节，`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`，长期稳定 |
| `name` | string，必需 | 非空，最多 80 字节 |
| `version` | string，必需 | 最多 128 字节，`数字.数字.数字`，可带 `-预发布` / `+构建` 后缀 |
| `description` | string，必需 | 非空，最多 2000 字节 |
| `schema_version` | integer，必需 | `1` |
| `runtime` | string，必需 | `oboard-js-v1` |
| `sdk_version` | string，必需 | `oboard-sdk-v1` |
| `entry` | string，必需 | `main`，不是文件路径 |
| `params_schema` | JSON Schema，可省略/null | 本次运行参数，未声明时只接受空参数 |
| `config_schema` | JSON Schema，可省略 | 安装配置；省略时使用 `params_schema`。建议明确声明，与动作输入分开 |
| `env` | array，默认空 | `{name, default?, overridable, sensitive}`；值都是字符串 |
| `secrets` | array，默认空 | `{name, purpose}`，声明宿主密钥引用，不存明文 |
| `capabilities` | 非空 string[]，必需 | SDK 方法名及明确的 `management:<能力名>`，见 [SDK](sdk.md) |
| `resource_types` | string[]，默认空 | 资源类别说明，例如 `servers`；不是资源授权 |
| `limits` | object，默认宿主限制 | 下表预算；只能缩小实际上限 |
| `output_schema` | JSON Schema，可省略 | 输出描述；当前 Runner 不据此自动校验返回值，作者需测试 |
| `network` | object，联网时必需 | 精确 `allowed_origins`、`allowed_methods`，可选 `max_request_bytes`、`max_response_bytes`，见 [安全](security.md) |

不要写 `ui_content_sha256`，这是宿主对 UI 内容生成的绑定字段。没有任意 `assets`、`dependencies`、`scripts`、npm 或动态入口字段；也没有可由作者指定的宿主最低版本字段，发布说明应标出验证过的 OBoard 版本。

## 参数和配置 Schema

最大 64 KiB，嵌套深度最多 6，总节点最多 80。禁止任何 `$ref`（包括本地引用），避免复杂 Schema。推荐 object + properties + required + additionalProperties:false；Schema 不会自动填充参数默认值。配置与输入都不放密钥。Schema 的结构限制和实际参数验证由宿主负责，编辑器校验不能代替它。

## 环境声明

`default` 缺省为空；`overridable:false` 不允许触发或运行覆盖。`sensitive` 不是密钥仓库，不要以此为由将真实密码写入清单。禁止覆盖系统名：`OBOARD_RUN_ID`、`OBOARD_PLUGIN_ID`、`OBOARD_REVISION_ID`、`OBOARD_TRIGGER_ID`、`OBOARD_SCHEDULED_AT`、`OBOARD_EVENT_SUBJECT_ID`、`OBOARD_SUBJECT_SERVER_ID`、`OBOARD_TARGET_SERVER_ID`、`OBOARD_RUN_MODE` 及 `RUN_ID`、`PLUGIN_ID`、`REVISION_ID`、`TRIGGER_ID`、`SERVER_ID`。

## limits

| 字段 | 默认有效值 | 声明范围 / 语义 |
|---|---:|---|
| `timeout_seconds` | 30 | 0–300；0 使用默认，目前有效值不超过 30 秒或更低的系统限制 |
| `memory_mib` | 64 | 0–64；0 使用默认，进程隔离限制而非精确 JS 堆大小 |
| `sdk_calls` | 100 | 0–100；所有 SDK 调用累计 |
| `manage_actions` | 10 | 0–10；外部请求、通知、管理写入和高危动作累计 |
| `log_bytes` | 262144 | 正值可缩小日志预算 |
| `result_bytes` | 65536 | 正值可缩小返回 JSON 预算 |

显式 0 不是禁止调用，想禁止某能力应不声明/不授权它。宿主还限制并发、目标数、状态配额，见 [运行时](runtime.md)。字段长度在宿主按 UTF-8 字节计算，JSON Schema 的 maxLength 是字符数；最终必须执行工具校验。
