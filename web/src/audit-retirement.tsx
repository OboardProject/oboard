import { useState } from 'react'
import { useCoalescedReadRequest } from './request-coalesce'
import { applyAuditChange, prepareAuditChange, type AuditClient } from './audit-changes'

type Batch = { id: number; state: string; deadline: string; deployment_version: number }
type Preview = { preflight: Record<string, number | boolean | string>; batch?: Batch; accounts?: Array<{ user_id: number; decision: string; reason: string }>; nodes?: Array<{ server_id: number; status: string; configuration_state: string; authorization_confirmed: boolean; users_confirmed: boolean }> }
const phases: Record<string, string> = { review: '待复核', transition: '有限过渡期', credential_revoking: '正在撤销代理凭证', awaiting_node_confirmation: '等待节点确认', complete: '节点撤销已确认' }
export function AuditRetirement({ client }: { client: AuditClient }) {
  const [open, setOpen] = useState(false)
  const [version, refresh] = useState(0)
  const [data, setData] = useState<Preview | null>(null)
  const [offset, setOffset] = useState(0)
  const [days, setDays] = useState(7)
  const [reason, setReason] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [prepared, setPrepared] = useState<{ id: string; label: string } | null>(null)
  useCoalescedReadRequest(`device-retirement:${offset}:${version}`, signal => client.request(`/device-retirement?offset=${offset}`, { signal }), { onSuccess: (v: Preview) => setData(v), onError: () => setError('迁移状态读取失败，请检查权限并重试') }, { enabled: open })
  const prepare = async (capability: string, input: object, label: string) => {
    setBusy(true); setError('')
    try { const change = await prepareAuditChange(client, capability, { confirm: true, ...input }, label, crypto.randomUUID()); setPrepared({ id: change.id, label }) }
    catch (e) { setError(e instanceof Error ? e.message : '迁移校验失败') } finally { setBusy(false) }
  }
  const apply = async () => {
    if (!prepared) return
    setBusy(true); setError('')
    try { await applyAuditChange(client, prepared.id, prepared.label); setPrepared(null); refresh(n => n + 1) }
    catch (e) { setError(e instanceof Error ? e.message : '迁移操作失败；请查看持久状态后重试，不要删除数据') } finally { setBusy(false) }
  }
  return <section className="settings-card"><h3>统一账号订阅迁移</h3><p className="muted">仅用于退出历史独立权限。不会签发新的独立链接；下载入口失效不代表代理凭证已撤销。开始前请完成一致性备份和恢复验证。</p><button type="button" className="ghost" onClick={() => setOpen(v => !v)}>{open ? '收起迁移预检' : '查看迁移预检'}</button>
    {open && <>{error && <p role="alert" className="form-error">{error}</p>}{!data ? <p role="status">读取中…</p> : <>
      <p>涉及账号 {data.preflight.affected_accounts} · 历史代理凭证 {data.preflight.legacy_credentials} · 受限对象 {data.preflight.restricted_objects} · 未完成节点同步 {data.preflight.pending_node_sync}</p>
      {data.preflight.grace_deadline && <p role="status">初始过渡截止：{data.preflight.grace_deadline}。该期限不会因重启或重试延长，请在截止前通知用户更新为账号订阅并完成复核。</p>}
      <p className="muted">预检不输出秘密。当前拓扑不一定覆盖曾经部署但已删除的节点，不能据此推断其凭证已失效。</p>
      {data.batch ? <><p>批次 #{data.batch.id}：{phases[data.batch.state] || data.batch.state} · 截止 {data.batch.deadline} · 目标部署版本 {data.batch.deployment_version || '尚未下发'}</p>
        {data.batch.state === 'review' && <><label>复核理由<textarea aria-label="迁移复核理由" value={reason} maxLength={500} onChange={e => setReason(e.target.value)} /></label>{data.accounts?.map(a => <div key={a.user_id}><strong>账号 #{a.user_id}</strong> · {a.decision === 'pending' ? '待复核' : a.decision === 'account_authorized' ? '允许正常账号授权' : '保留限制'}<p>{a.reason}</p><button type="button" disabled={busy || !reason.trim()} onClick={() => void prepare('device_retirement.review', { batch_id: data.batch!.id, user_id: a.user_id, decision: 'account_authorized', reason }, `确认账号 #${a.user_id} 的现有授权映射`)}>确认账号授权映射</button><button type="button" className="ghost" disabled={busy || !reason.trim()} onClick={() => void prepare('device_retirement.review', { batch_id: data.batch!.id, user_id: a.user_id, decision: 'retain_restriction', reason }, `保留账号 #${a.user_id} 的待核实限制`)}>保留限制</button></div>)}<button type="button" disabled={busy} onClick={() => void prepare('device_retirement.advance', { batch_id: data.batch!.id, phase: 'transition' }, '全部复核完成后进入有截止期限的过渡并下发目标配置')}>校验进入过渡</button></>}
        {['transition', 'credential_revoking', 'awaiting_node_confirmation'].includes(data.batch.state) && <><p className="form-error">撤销旧代理凭证不可自动回滚。已导入旧配置将不能建立新连接，授权租约失效会终止旧连接；用户应先改用账号订阅。离线节点保持未确认。</p><button type="button" disabled={busy} onClick={() => void prepare('device_retirement.advance', { batch_id: data.batch!.id, phase: 'revoke' }, '撤销历史代理凭证并下发新配置；不可恢复已撤销秘密')}>校验撤销及下发</button><button type="button" className="ghost" disabled={busy} onClick={() => void prepare('device_retirement.finalize', { batch_id: data.batch!.id }, '验证全部节点配置及授权确认，只有检查通过才完成批次')}>核验节点完成情况</button></>}
        {data.nodes?.map(n => <p key={n.server_id}>节点 #{n.server_id} · {n.status === 'online' ? '在线' : '离线或未知'} · 配置 {n.configuration_state} · 授权 {n.authorization_confirmed ? '已确认' : '未确认'} · 运行用户 {n.users_confirmed ? '已确认' : '未确认'}</p>)}
        <button type="button" className="ghost" disabled={!offset} onClick={() => setOffset(Math.max(0, offset - 100))}>上一页</button><button type="button" className="ghost" disabled={(data.accounts?.length || 0) < 100 && (data.nodes?.length || 0) < 100} onClick={() => setOffset(offset + 100)}>下一页</button>
      </> : <><label>过渡天数<input type="number" min="1" max="14" value={days} onChange={e => setDays(Number(e.target.value))} /></label><label>迁移原因<textarea aria-label="迁移原因" maxLength={500} value={reason} onChange={e => setReason(e.target.value)} /></label><button type="button" disabled={busy || !reason.trim() || days < 1 || days > 14} onClick={() => void prepare('device_retirement.start', { deadline: new Date(Date.now() + days * 86400000).toISOString(), reason }, '创建统一订阅迁移批次，先逐账号复核，不立即撤销')}>校验创建迁移批次</button></>}
      <p className="muted">历史收缩不会自动执行；最老升级与备份恢复边界未满足时，系统明确拒绝删除旧表。</p>
      {prepared && <div><p role="status">{prepared.label}</p><button type="button" disabled={busy} onClick={() => void apply()}>确认执行已校验操作</button><button type="button" className="ghost" onClick={() => setPrepared(null)}>取消</button></div>}
      <button type="button" className="ghost" disabled={busy} onClick={() => refresh(n => n + 1)}>刷新迁移状态</button>
    </>}</>}
  </section>
}
