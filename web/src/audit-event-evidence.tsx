import { useState } from 'react'
import { useCoalescedReadRequest } from './request-coalesce'
import type { AuditClient } from './audit-changes'

type EvidencePage = {
  items: Array<{ account_id: number; server_id: number; event_time: string; upload_bytes: number; download_bytes: number; connection_count: number }>
  limit: number; offset: number; next_offset: number | null; status: 'partial' | 'unknown'; reason: string
}

export type AuditEventEvidenceProps = { client: AuditClient; eventID: number; revision?: number; isAdmin: boolean }

export function AuditEventEvidence(props: AuditEventEvidenceProps) {
  if (!props.isAdmin) return null
  return <EvidencePanel key={`${props.eventID}:${props.revision ?? 0}`} {...props} />
}

function EvidencePanel({ client, eventID }: AuditEventEvidenceProps) {
  const [open, setOpen] = useState(false)
  const [offset, setOffset] = useState(0)
  const [page, setPage] = useState<EvidencePage | null>(null)
  const [error, setError] = useState(false)
  const [loading, setLoading] = useState(false)
  const path = `/audit/evidence?event_id=${eventID}&limit=20&offset=${offset}`
  useCoalescedReadRequest<EvidencePage>(path, signal => client.request(path, { signal }), {
    onStart: () => { setLoading(true); setError(false); setPage(null) },
    onSuccess: setPage,
    onError: () => setError(true),
    onSettled: () => setLoading(false),
  }, { enabled: open })
  const buttonClass = 'ghost transition-transform active:scale-[0.97] active:translate-y-px ease-[cubic-bezier(0.175,0.885,0.32,1.5)]'
  return <section>
    <button type="button" className={buttonClass} aria-expanded={open} onClick={() => { setOpen(!open); setPage(null); setOffset(0) }}>{open ? '收起诊断证据' : '查看诊断证据'}</button>
    {open && <div aria-busy={loading}>
      <p className="muted">仅展示事件时间窗内已留存的连接记录，不补采、不重新评分。订阅原始记录未采集；空结果可能为未采集、已过期或容量受限，不代表没有活动。</p>
      {loading ? <p role="status">正在读取…</p> : error ? <p role="alert">证据读取失败，请收起后重试。</p> : page && <>
        {!page.items.length ? <p>没有可展示的留存记录，覆盖情况未知。</p> : <><p className="muted">以下为部分留存证据，不能证明覆盖完整。</p><ul>{page.items.map((item, index) => <li key={index}>
          <time>{new Date(item.event_time).toLocaleString()}</time> · 账号 #{item.account_id} · 节点 #{item.server_id} · {item.connection_count} 次连接 · 上行 {item.upload_bytes.toLocaleString()} 字节 / 下行 {item.download_bytes.toLocaleString()} 字节
        </li>)}</ul></>}
        <button type="button" className={buttonClass} disabled={!offset} onClick={() => setOffset(Math.max(0, offset - 20))}>上一页</button>
        <button type="button" className={buttonClass} disabled={page.next_offset == null} onClick={() => setOffset(page.next_offset!)}>下一页</button>
      </>}
    </div>}
  </section>
}
