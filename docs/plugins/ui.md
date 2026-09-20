# 声明式页面 ui.json

UI 文件最多 256 KiB，根对象只有 `pages`。页面只展示宿主组件，不能携带 HTML、JS、CSS、iframe、外部图片或任意导航。所有文字按文本处理。完整机器格式见 [UI Schema](../../sdk/plugins/ui.schema.json)。

## 页面与组件

`pages` 为 1–8 项，每页 `{id,title,description?,components}`。页面 ID 唯一，`components` 最多 32 项。所有 ID/字段名/列 key 遵守 `^[a-z][a-z0-9_-]{0,63}$`；组件 ID 在页内唯一。title 非空最多 128 字节，description 最多 2048 字节。

每个组件含 `{id,type,title?,text?,columns?,fields?,action?}`。组件 title 最多 128 字节，text 最多 8192 字节。

| type | 内容 | 动作 |
|---|---|---|
| `text` | 静态 text | 不允许 |
| `stat` | 静态摘要 text | 不允许；没有实时表达式绑定 |
| `table` | 1–16 个 `{key,label}` 列 | 可选查询 action |
| `form` | 0–32 个 fields | 必须有 action，表单在宿主弹窗中填写 |
| `button` | 标题/说明 | 必须有 action |

不支持任意结果路径、模板表达式或跨组件数据绑定。表格动作返回顶层数组或 `{rows:[...]}`，按列 key 读取每行，最多显示 200 行；无行时显示空态。其他组件的动作返回值由宿主展示，不会执行返回的 HTML。每个动作结果仅更新触发它的组件，不刷新别的表格。

## 表单与动作

字段 `{name,label,type,required?,options?}`，type 为 `text`、`number`、`boolean` 或 `select`。select 必须有 1–64 个唯一非空文本 options（各最多 128 字节），其他类型不能带 options。没有 password/file 字段；秘密必须用管理员密钥管理。

动作 `{label,params?}`，label 非空最多 128 字节，params 最多 32 个标识符键。固定 params 不得与表单字段同名。用户表单值和固定参数合并后调用同一个 `main()`，可用参数如 `action` 自行分派；清单 params_schema 必须覆盖所有动作输入。

```json
{"pages":[{"id":"hello","title":"问候","components":[{"id":"greet","type":"button","action":{"label":"读取问候","params":{}}}]}]}
```

UI 按安装包版本审查，不能绕过能力或资源授权。用户点击使用该用户与插件授权的交集；定时触发不是用户点击的提权替代。运行中宿主禁用重复提交并轮询执行记录，失败显示错误，不把“排队”标为成功。若运行环境不可用、插件停用或版本无授权，应修复授权状态而非在浏览器绕过 API。

`node-inspection` 演示查询表单；`batch-operations` 演示先预览、再显式确认的维护输入。UI 的确认框不是高危授权本身，后台仍需验证。
