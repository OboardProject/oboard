import { useState } from 'react'
import { useCoalescedReadRequest } from './request-coalesce'
import type { AuditClient } from './audit-changes'
type Status = { status: string; pending_event_count: number | null; pending_inbox_reports: number | null; pending_evaluations: number | null; pending_notifications: number | null; last_snapshot_time: string | null; degradation: string[]; collection_mode: string; audit_enabled: boolean }
export function AuditStatus({ client, enabled, revision }: { client: AuditClient; enabled: boolean; revision: number }) {
 const [value,setValue]=useState<Status|null>(null)
 const [error,setError]=useState(false)
 useCoalescedReadRequest(`audit-status:${revision}`,signal=>client.request('/audit/status',{signal}),{onSuccess:(v:Status)=>{setValue(v);setError(false)},onError:()=>setError(true)},{enabled})
 if(error)return <p role="alert">采集状态暂不可用，不代表没有积压或风险。</p>
 if(!value)return <p role="status">正在读取采集状态…</p>
 const degradation:Record<string,string>={snapshots_missing:'尚无快照',snapshots_stale_or_incomplete:'快照已过期或采集不完整',snapshot_read_budget_exceeded:'快照统计容量受限',pending_event_count_read_budget_exceeded:'待处理事件统计容量受限',pending_evaluations_read_budget_exceeded:'待评估账号统计容量受限',evaluation_read_budget_exceeded:'待评估账号统计容量受限',pending_notifications_read_budget_exceeded:'待发送通知统计容量受限',inbox_read_budget_exceeded:'待接收报告统计容量受限',inbox_capacity_reached:'待接收报告已达到容量上限'}
 return <section aria-label="审计状态" className="audit-console-banner"><div><strong>{value.audit_enabled?'行为采集已启用':'行为采集已关闭'} · {value.collection_mode==='light'?'轻量采集':value.collection_mode==='standard'?'标准采集':'临时诊断'} · 仅告警</strong><p>最新快照：{value.last_snapshot_time||'待评估'}</p><div className="audit-console-toolbar">{[['待处理事件',value.pending_event_count],['待接收应用',value.pending_inbox_reports],['待评估账号',value.pending_evaluations],['待发送通知',value.pending_notifications]].map(([label,n])=><span key={String(label)}>{label}：{n??'不可用'}</span>)}</div>{value.degradation?.length>0&&<p>采集提示：{value.degradation.map(r=>degradation[r]||r).join('；')}</p>}</div></section>
}
