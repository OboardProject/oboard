# 配置、环境与私有状态

## 三类输入

1. `params_schema`：一次动作输入，运行快照固定后不可更改，例如节点 ID、查询条件、确认开关；UI 表单/手动运行/触发器提供 params。
2. `config_schema`：安装级非秘密配置；保存时校验，省略 Schema 时采用 params_schema。模板明确使用空配置 Schema，避免把必填运行参数误当安装配置。
3. `env` 声明：字符串默认值及是否允许覆盖；不是宿主环境变量继承，也不是密钥存储。系统 `OBOARD_*` 字段不可覆盖。

安装配置不会自动合并为本次 `params`，二者是不同数据边界。声明并获得 `config.get` 能力后，使用 `oboard.config.get({}).result.config` 读取本插件当前安装的非敏感配置；它不是执行开始时的快照，执行途中修改配置会影响后续读取。插件不能传入其他插件 ID；卸载后拒绝读取。密钥不在该结果内。参数 Schema 的 default 不是注入机制，JS 应自行处理可选字段。

## 私有 KV 和 CAS

```js
function main() {
  var old = oboard.state.get({ key: "counter" }).result;
  var version = Number(old.version);
  if (!Number.isSafeInteger(version)) throw { error_code: "invalid_input", message: "状态版本超出安全整数范围" };
  return oboard.state.compareAndSet({
    key: "counter", expected_version: version,
    value: { schema: 1, count: old.exists ? old.value.count + 1 : 1 }
  }).result;
}
```

需要声明并获准 `state.get` 和 `state.compareAndSet`。插件私有命名空间由宿主 run 身份确定，调用者不能传另一个 plugin_id。最多 32 个键，合理限制每个 JSON 值。0 版本只创建，正版本条件更新；版本不一致返回 `condition_changed`，读后再决定，不用“最后写入覆盖”。模拟 CAS 不写入真实状态。

KV 没有密钥加密语义、跨键事务或自动状态迁移。业务状态自行携带 `schema`/版本字段，升级处理必须幂等、小批次、可停止，并且不能依赖无限运行时间。切换 JS 版本不回滚数据；不兼容数据的旧代码应明确拒绝执行，而不是忽略未知字段并覆盖。

## 保留、卸载与恢复

卸载总是停触发、撤销 grant、取消未完成 run 并删除密钥；选择保留时保留配置/KV，不表示仍可执行。选择清理时清除配置/KV，历史版本与运行审计仍保留关联，不能把它描述成完全抹除全部记录。

备份恢复走 Controller 的加密备份与恢复流程，恢复后关闭运行和调度；重新配置/确认敏感授权。不要直接复制 SQLite 或明文导出密钥。旧脚本数据没有迁移成插件的兼容保证；用户已明确未使用旧功能，本次不自动转换历史脚本。
