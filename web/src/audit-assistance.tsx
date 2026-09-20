import { useState } from 'react'
import { useCoalescedReadRequest } from './request-coalesce'
import { applyAuditChange, prepareAuditChange, type AuditClient } from './audit-changes'

type Assistance = { review_id?: string; status: string; error_code?: string; result?: { behavior_profile?: { usual_pattern?: string[]; current_pattern?: string[]; key_changes?: string[] }; findings?: Array<{ title?: string; observation?: string; interpretation?: string; verification_steps?: string[] }>; counter_evidence?: unknown[]; data_gaps?: unknown[] } | null }
export function AuditAssistance({ client, event, saved, onRefresh }: { client: AuditClient; event: { id: number; user_id: number; revision: number }; saved?: Assistance | null; onRefresh: () => void }) {
  const [open, setOpen] = useState(false)
  const [providers, setProviders] = useState<Array<{ id: string; name?: string }>>([])
  const [provider, setProvider] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [prepared, setPrepared] = useState<string | null>(null)
  useCoalescedReadRequest('audit-assistance-providers', signal => client.requestV2!('/ai/providers', { signal }), { onSuccess: (items: any[]) => setProviders(items.filter(p => p.enabled && p.endpoints?.some((e: any) => e.enabled && e.capability?.audit_ready))), onError: () => setError('辅助分析服务不可用；不影响规则判定与人工处理。') }, { enabled: open && Boolean(client.requestV2) })
  const prepare = async () => {
    setBusy(true); setError('')
    try { const result = await prepareAuditChange(client, 'audit.events.analyze', { user_id: event.user_id, event_id: event.id, expected_revision: event.revision, provider_id: provider }, '请求当前事件证据的辅助分析，不执行限制', crypto.randomUUID()); setPrepared(result.id) }
    catch (e) { setError(e instanceof Error ? e.message : '分析请求校验失败') } finally { setBusy(false) }
  }
  const apply = async () => {
    if (!prepared) return
    setBusy(true); setError('')
    try { await applyAuditChange(client, prepared, '确认仅发送本事件脱敏证据给所选服务'); onRefresh(); setPrepared(null) }
    catch (e) { setError(e instanceof Error ? e.message : '分析请求失败') } finally { setBusy(false) }
  }
  return <section className="settings-card"><h3>辅助分析</h3><p className="muted">根据当前事件的证据整理说明和排查建议，不直接执行限制。仅显式请求时向所选服务发送有界脱敏快照；同一证据复用结果。</p>
    {saved && <><p>状态：{({ queued: '排队中', running: '分析中', succeeded: '已完成', failed: '失败', unavailable: '服务不可用', cancelled: '已取消' } as Record<string, string>)[saved.status] || '未知'}</p>{saved.result && <><p>{saved.result.behavior_profile?.usual_pattern?.join('；')}</p><p>{saved.result.behavior_profile?.current_pattern?.join('；')}</p>{saved.result.behavior_profile?.key_changes?.map((v, i) => <p key={i}>{v}</p>)}{saved.result.findings?.map((v, i) => <div key={i}><strong>{v.title}</strong><p>{v.observation}</p><p>{v.interpretation}</p>{v.verification_steps?.map((step, j) => <p key={j}>{step}</p>)}</div>)}<p className="muted">以上为辅助说明，不是事实确认或执行指令。以评分证据与人工核实为准。</p></>}<button type="button" className="ghost" onClick={onRefresh}>刷新分析状态</button></>}
    {error && <p role="alert">{error}</p>}
    {!open ? <button type="button" className="ghost" onClick={() => setOpen(true)}>选择分析服务</button> : <><label>分析服务<select aria-label="辅助分析服务" value={provider} onChange={e => { setProvider(e.target.value); setPrepared(null) }}><option value="">请选择已配置服务</option>{providers.map(p => <option key={p.id} value={p.id}>{p.name || p.id}</option>)}</select></label>{!providers.length && <p className="muted">暂无可用分析服务，不影响审计工作。</p>}{prepared ? <button type="button" disabled={busy} onClick={() => void apply()}>确认请求辅助分析</button> : <button type="button" disabled={busy || !provider} onClick={() => void prepare()}>校验分析请求</button>}</>}
  </section>
}
