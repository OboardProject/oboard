import { useState } from 'react'
import { applyAuditChange, prepareAuditChange, type AuditClient } from './audit-changes'

type Event = { id: number; user_id: number; revision: number; review_status?: string }
export function AuditEventReview({ client, event, onSaved }: { client: AuditClient; event: Event; onSaved: () => void }) {
  const [status, setStatus] = useState(event.review_status || 'pending')
  const [reason, setReason] = useState('')
  const [days, setDays] = useState(1)
  const [prepared, setPrepared] = useState<{ id: string; reason: string } | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const temporary = status === 'observing' || status === 'false_positive'
  const prepare = async () => {
    setBusy(true); setError('')
    try {
      const input = { event_id: event.id, user_id: event.user_id, expected_revision: event.revision, status, reason, ...(temporary ? { expires_at: new Date(Date.now() + days * 86400000).toISOString() } : {}) }
      const result = await prepareAuditChange(client, 'audit.events.review', input, reason, crypto.randomUUID())
      setPrepared({ id: result.id, reason })
    } catch (e) { setError(e instanceof Error ? e.message : '校验失败') } finally { setBusy(false) }
  }
  const apply = async () => {
    if (!prepared) return
    setBusy(true); setError('')
    try { await applyAuditChange(client, prepared.id, prepared.reason); onSaved() }
    catch (e) { setError(e instanceof Error ? e.message : '处理失败，请刷新事件后重试') } finally { setBusy(false) }
  }
  return <section className="settings-card"><h3>核实与处理</h3><p className="muted">本操作只记录本事件的人工处理结论，不封禁账号、不轮换凭证、不停止新证据采集。误报与观察到期后不再抑制通知。</p>
    {error && <p role="alert" className="form-error">{error}</p>}
    <label>处理状态<select aria-label="事件处理状态" value={status} disabled={busy} onChange={e => { setStatus(e.target.value); setPrepared(null) }}><option value="pending">待确认</option><option value="observing">观察中</option><option value="handled">已处置</option><option value="closed">已关闭</option><option value="false_positive">误报</option></select></label>
    {temporary && <label>有效天数<input aria-label="处理有效天数" type="number" min="1" max="30" value={days} onChange={e => { setDays(Number(e.target.value)); setPrepared(null) }} /></label>}
    <label>依据与理由<textarea aria-label="处理理由" maxLength={2048} value={reason} onChange={e => { setReason(e.target.value); setPrepared(null) }} /></label>
    {prepared ? <><p role="status">变更已校验，确认后写入可追溯记录；若证据版本已更新将拒绝旧操作。</p><button type="button" disabled={busy} onClick={() => void apply()}>确认记录处理结果</button></> : <button type="button" disabled={busy || !reason.trim() || (temporary && (days < 1 || days > 30))} onClick={() => void prepare()}>{busy ? '正在校验…' : '校验处理结果'}</button>}
  </section>
}
