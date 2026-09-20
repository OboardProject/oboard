import { useState } from 'react'
import { useCoalescedReadRequest } from './request-coalesce'
import type { AuditClient } from './audit-changes'
export function AuditEventTimeline({ client, eventID, revision }: { client: AuditClient; eventID: number; revision: number }) {
  const [items, setItems] = useState<Array<{ id: number; actor: string; reason: string; status: string; created_at: number; execution_status: string }>>([])
  const [offset, setOffset] = useState(0)
  const [next, setNext] = useState<number | null>(null)
  const [error, setError] = useState(false)
  const [loading, setLoading] = useState(false)
  const path = `/audit/executions?event_id=${eventID}&limit=20&offset=${offset}`
  useCoalescedReadRequest(`${path}&revision=${revision}`, signal => client.request(path, { signal }), { onStart: () => { setLoading(true); setError(false); setItems([]); setNext(null) }, onSuccess: result => { setItems(result.items || []); setNext(result.next_offset) }, onError: () => setError(true), onSettled: () => setLoading(false) })
  const names: Record<string, string> = { pending: '待确认', observing: '观察中', handled: '已处置', closed: '已关闭', false_positive: '误报' }
  return <section><h3>处理时间线</h3>{loading ? <p role="status">正在读取处理记录…</p> : error ? <p role="alert">时间线读取失败。</p> : !items.length ? <p className="muted">暂无人工处理记录。</p> : <ol>{items.map(i => <li key={i.id}><time>{new Date(i.created_at * 1000).toLocaleString()}</time> · {i.actor} · {names[i.status] || '未知状态'}<p>{i.reason}</p><small>{i.execution_status === 'applied' ? '处理记录已保存（不代表节点访问限制）' : i.execution_status}</small></li>)}</ol>}<button type="button" className="ghost" disabled={loading || !offset} onClick={() => setOffset(Math.max(0, offset - 20))}>较新记录</button><button type="button" className="ghost" disabled={loading || next == null} onClick={() => setOffset(next!)}>较早记录</button></section>
}
