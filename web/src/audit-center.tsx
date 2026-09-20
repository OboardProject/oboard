import { useState, type ReactNode, type ComponentProps } from 'react'
import { RefreshCw, Settings2, X } from 'lucide-react'
import { useCoalescedReadRequest } from './request-coalesce'
import { useRegisterPageRefresh } from './page-refresh-context'
import { MotionDialogPanel } from './components/ui/motion'
import { AuditEventReview } from './audit-event-review'
import { AuditAssistance } from './audit-assistance'
import { AuditEventTimeline } from './audit-event-timeline'
import { AuditEventEvidence } from './audit-event-evidence'
import { AuditStatus } from './audit-status'
import type { AuditClient } from './audit-changes'

export type AuditScore = { lower: number; upper: number; status: string; level: string; lower_contribution?: { sources: number; minutes: number; source_thresholds: { start: number; full: number }; duration_thresholds: { start: number; full: number } } }
export type AccountSnapshot = {
  account_id: number; status: string; as_of: string; window_start?: string; window_end?: string
  activity: AuditScore | null; exposure: AuditScore | null; resource: AuditScore | null
  quality?: Record<string, { state: string; reason_code: string }>
  action_block_reasons?: string[]
  versions?: { model: string; baseline: string; source: string }
  policy?: { version: string }
  features?: { evidence_cutoff: string; data_revision: number }
}
type AuditRow = {
  id?: number; account_id?: number; user_id?: number; username?: string; nickname?: string
  risk_type?: string; status?: string; state?: string; updated_at?: string; last_seen_at?: string; score?: number; evaluation_status?: string
  snapshot?: AccountSnapshot | null
  assistance?: ComponentProps<typeof AuditAssistance>['saved']
  revision?: number; review_status?: string; event_id?: number; actor?: string; reason?: string; created_at?: number; execution_status?: string
}
type AuditPage = { items?: AuditRow[]; limit?: number; offset?: number; next_offset?: number | null; audit_logs?: unknown[] }
type View = 'events' | 'accounts' | 'executions' | 'operations'
const views: Array<[View, string]> = [['events', '待处理'], ['accounts', '用户活动'], ['executions', '处置记录'], ['operations', '操作日志']]
const riskNames: Record<string, string> = { activity: '异常并发活动', exposure: '订阅扩散线索', resource: '资源压力' }
const stateNames: Record<string, string> = { pending: '待确认', open: '待确认', observing: '观察中', handled: '已处置', closed: '已关闭', false_positive: '误报', recovered: '已恢复', queued: '待执行', applying: '下发中', applied: '已应用', partial_failure: '部分失败' }
const qualityNames: Record<string, string> = { identity_trusted: '账号归属', source_usable: '来源可用', deduplicated: '报告去重', measurement_valid: '计量有效', coverage_complete: '采集覆盖', time_aligned: '时间对齐', source_set_complete: '来源集合', baseline_ready: '历史基线', history_complete: '订阅历史', freshness: '数据新鲜度', capability_supported: '采集能力' }
export function auditScoreText(score: AuditScore | null | undefined): string {
  if (!score || !['complete', 'range'].includes(score.status)) return '不可评估'
  if (!Number.isFinite(score.lower) || !Number.isFinite(score.upper)) return '不可评估'
  return score.lower === score.upper ? `${score.lower} 分` : `${score.lower}～${score.upper} 分（可能范围）`
}
function SnapshotFacts({ snapshot }: { snapshot: AccountSnapshot | null | undefined }) {
  if (!snapshot) return <p className="muted">待评估：尚无账号风险快照。缺少数据不代表没有风险。</p>
  return <>
    <p>评估状态：{snapshot.status === 'evaluated' ? '已评估（质量各项独立核实）' : snapshot.status === 'stale' ? '数据过期' : '不可评估'}</p>
    <p>证据窗口：{snapshot.window_start || '未知'} — {snapshot.window_end || '未知'}</p>
    <dl>{(['activity', 'exposure', 'resource'] as const).map(kind => <div key={kind}><dt>{riskNames[kind]}</dt><dd>{auditScoreText(snapshot[kind])}</dd></div>)}</dl>
    {snapshot.activity?.lower_contribution && <p>活动依据：至少 {snapshot.activity.lower_contribution.sources} 个来源达到共同活跃规模的分钟，累计 {snapshot.activity.lower_contribution.minutes} 分钟。规模开始计分 {snapshot.activity.lower_contribution.source_thresholds.start}、贡献上限 {snapshot.activity.lower_contribution.source_thresholds.full}；时长开始计分 {snapshot.activity.lower_contribution.duration_thresholds.start} 分钟、贡献上限 {snapshot.activity.lower_contribution.duration_thresholds.full} 分钟。此结果不证明固定一组来源持续在线。</p>}
    <p>计算时间：{snapshot.as_of || '未知'} · 证据截止：{snapshot.features?.evidence_cutoff || '未知'}</p>
    <p>模型 {snapshot.versions?.model || '未知'} · 策略 {snapshot.policy?.version || '未知'} · 基线 {snapshot.versions?.baseline || '未知'} · 来源归并 {snapshot.versions?.source || '未知'} · 数据修订 {snapshot.features?.data_revision ?? '未知'}</p>
    <h3>数据质量</h3>
    {!Object.keys(snapshot.quality || {}).length && <p>质量明细不可用，不代表采集完整。</p>}
    {Object.entries(snapshot.quality || {}).map(([key, value]) => <p key={key}>{qualityNames[key] || key}：{value.state === 'satisfied' ? '满足' : value.state === 'unsatisfied' ? '不满足' : '未知'}{value.reason_code ? `（${value.reason_code}）` : ''}</p>)}
    <p className="muted">分数表示规则定义的异常强度，不是违规概率。来源组不代表设备；资源压力不参与行为风险相加。</p>
  </>
}
export function AuditCenter({ client, isAdmin, enabled, settings, renderLogs }: {
  client: AuditClient
  isAdmin: boolean; enabled: boolean; settings: ReactNode; renderLogs: (rows: unknown[], loading: boolean) => ReactNode
}) {
  const [view, setView] = useState<View>('events')
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [offset, setOffset] = useState(0)
  const [revision, setRevision] = useState(0)
  const [page, setPage] = useState<AuditPage | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [selected, setSelected] = useState<AuditRow | null>(null)
  const [detail, setDetail] = useState<AuditRow | null>(null)
  const [detailLoading, setDetailLoading] = useState(false)
  const [detailError, setDetailError] = useState('')
  const detailPath = `/audit/events?event_id=${selected?.id ?? 0}`
  useCoalescedReadRequest(`${detailPath}&revision=${revision}`, signal => client.request(detailPath, { signal }), {
    onStart: () => { setDetailLoading(true); setDetailError(''); setDetail(null) },
    onSuccess: (response: AuditPage) => {
      const row = response.items?.find(item => item.id === selected?.id)
      setDetail(row || null)
      if (!row) setDetailError('事件不存在或无权查看')
    },
    onError: () => setDetailError('详情加载失败，请关闭后重试'),
    onSettled: () => setDetailLoading(false),
  }, { enabled: view === 'events' && selected != null && !settingsOpen })
  const [userFilter, setUserFilter] = useState('')
  const [userID, setUserID] = useState('')
  const limit = 50
  const path = view === 'operations' ? `/audit-logs?limit=${limit}&offset=${offset}` : `/audit/${view}?limit=${limit}&offset=${offset}${view === 'events' ? '&status=pending' : ''}${userID ? `&user_id=${encodeURIComponent(userID)}` : ''}`
  useCoalescedReadRequest(`${path}&revision=${revision}`, signal => client.request(path, { signal }), {
    onStart: () => { setLoading(true); setError(''); setPage(null) },
    onSuccess: (response: AuditPage) => setPage(response),
    onError: () => setError('加载失败，请检查权限或稍后重试'),
    onSettled: () => setLoading(false),
  }, { enabled: !settingsOpen })
  useRegisterPageRefresh(() => { if (!settingsOpen) setRevision(value => value + 1) })
  const items = page?.items || []
  const count = view === 'operations' ? page?.audit_logs?.length || 0 : items.length
  return <section className="panel audit-console-panel"><div className="panel-body">
    <header className="settings-card-head"><div><h2>审计中心</h2><p className="muted">汇总订阅更新、连接活动和管理操作，识别异常并提供可追溯的处理依据。</p></div>{isAdmin && <button type="button" className="ghost" onClick={() => { setSelected(null); setSettingsOpen(true) }}><Settings2 size={16} />审计配置</button>}</header>
    <div className="audit-console-banner"><div><strong>{enabled ? '行为审计已启用 · 仅告警' : '行为审计已关闭 · 历史只读'}</strong><p>记录异常并通知管理员，不因行为评分自动限制账号。已有访问限制、配额和独立请求限流不受影响。</p><span>采集状态与更新时间以账号快照为准；暂无快照时待评估。</span></div></div>
    <AuditStatus client={client} enabled={!settingsOpen} revision={revision} />
    <nav className="audit-console-tabs" aria-label="审计视图">{views.map(([key, label]) => <button type="button" key={key} aria-current={view === key ? 'page' : undefined} className={view === key ? 'active' : ''} onClick={() => { setView(key); setOffset(0); setPage(null); setSelected(null) }}>{label}</button>)}</nav>
    {view === 'accounts' && <p className="muted">按账号查看订阅更新与连接活动。来源与客户端信息用于辅助分析，不代表准确的设备数量。</p>}
    <form className="audit-console-toolbar" onSubmit={event => { event.preventDefault(); setUserID(userFilter); setOffset(0) }}>{view !== 'operations' && <><label>账号 ID <input type="number" min="1" step="1" value={userFilter} onChange={event => setUserFilter(event.target.value)} placeholder="全部账号" /></label><button type="submit" className="ghost">筛选</button></>}<button type="button" className="ghost" disabled={loading || settingsOpen} onClick={() => setRevision(value => value + 1)}><RefreshCw size={15} />刷新</button></form>
    {error ? <p role="alert" className="form-error">{error}；当前数据不可用，不代表没有风险。</p> : loading ? <p role="status">正在加载…</p> : view === 'operations' ? renderLogs(page?.audit_logs || [], loading) : !items.length ? <p className="muted">{view === 'accounts' ? '暂无账号快照，等待评估。' : view === 'events' ? '暂无待处理事件；没有事件不代表采集完整或没有风险。' : '暂无处置记录。行为模型默认仅告警。'}</p> : <div className="audit-user-table-wrap"><table className="audit-user-table"><thead><tr><th>对象</th><th>{view === 'accounts' ? '活动与扩散' : '问题'}</th><th>证据状态</th><th>{view === 'executions' ? '执行状态' : '处置状态'}</th><th>最近更新</th><th>查看</th></tr></thead><tbody>{items.map((row, index) => <tr key={row.id ?? row.account_id ?? row.user_id ?? index}>
      <td>{row.nickname || row.username || `账号 #${row.account_id ?? row.user_id ?? '未知'}`}</td>
      <td>{view === 'executions' ? <>{stateNames[row.status || ''] || '事件处理'}<span>{row.reason}</span></> : view === 'accounts' ? <><span>活动：{auditScoreText(row.snapshot?.activity)}</span><span>扩散：{auditScoreText(row.snapshot?.exposure)}</span></> : <>{riskNames[row.risk_type || ''] || '待核实异常'}{row.score != null && <span>{row.score} 分（事件保存分数）</span>}</>}</td>
      <td>{view === 'executions' ? `操作者：${row.actor || '未知'}` : (row.evaluation_status || row.snapshot?.status) === 'evaluated' ? '已评估，查看质量明细' : (row.evaluation_status || row.snapshot?.status) === 'stale' ? '数据过期' : (row.evaluation_status || row.snapshot?.status) === 'not_evaluable' ? '数据不足或部分缺失' : '待评估'}</td>
      <td>{view === 'accounts' ? row.snapshot ? '已有快照' : '待评估' : stateNames[row.execution_status || row.review_status || row.status || row.state || ''] || '状态未知'}</td>
      <td>{row.created_at ? new Date(row.created_at * 1000).toLocaleString() : row.updated_at || row.last_seen_at || row.snapshot?.as_of || '未知'}</td>
      <td><button type="button" className="ghost" onClick={() => { setDetail(null); setDetailError(''); setDetailLoading(view !== 'accounts'); if (view === 'executions' && row.event_id) { setView('events'); setSelected({ ...row, id: row.event_id }) } else { setSelected(row) } }}>查看依据</button></td>
    </tr>)}</tbody></table></div>}
    <div className="settings-actions"><button type="button" className="ghost" disabled={!offset || loading} onClick={() => setOffset(value => Math.max(0, value - limit))}>上一页</button><span>第 {Math.floor(offset / limit) + 1} 页</span><button type="button" className="ghost" disabled={loading || Boolean(error) || (view === 'operations' ? count < limit : page?.next_offset == null)} onClick={() => setOffset(value => view === 'operations' ? value + limit : page?.next_offset ?? value)}>下一页</button></div>
    {settingsOpen && <MotionDialogPanel onCancel={() => setSettingsOpen(false)} className="audit-detail-dialog"><header className="dialog-head"><h2>审计配置</h2><button type="button" className="ghost icon-button" aria-label="关闭审计配置" onClick={() => setSettingsOpen(false)}><X /></button></header>{settings}</MotionDialogPanel>}
    {selected && <MotionDialogPanel onCancel={() => setSelected(null)} className="audit-detail-dialog"><header className="dialog-head"><h2>事实与判断边界</h2><button type="button" className="ghost icon-button" aria-label="关闭详情" onClick={() => setSelected(null)}><X /></button></header>{view === 'events' ? detailLoading ? <p role="status">正在加载详情…</p> : detailError ? <p role="alert">{detailError}</p> : <><SnapshotFacts snapshot={detail?.snapshot} />{detail?.id && <><AuditEventTimeline key={detail.id} client={client} eventID={detail.id} revision={detail.revision || 0} /><AuditEventEvidence key={`evidence:${detail.id}`} client={client} eventID={detail.id} revision={detail.revision || 0} isAdmin={isAdmin} /></>}</> : <SnapshotFacts snapshot={selected.snapshot} />}{view === 'events' && !detailLoading && !detailError && isAdmin && detail?.id && detail.user_id && detail.revision && <><AuditEventReview key={`${detail.id}:${detail.revision}`} client={client} event={{ id: detail.id, user_id: detail.user_id, revision: detail.revision, review_status: detail.review_status }} onSaved={() => setRevision(n => n + 1)} /><AuditAssistance client={client} event={{ id: detail.id, user_id: detail.user_id, revision: detail.revision }} saved={detail.assistance} onRefresh={() => setRevision(n => n + 1)} /></>}<p className="muted">展示保存的快照，不在查看时重新计算风险；未采集的目的地明细无法事后补回。</p></MotionDialogPanel>}
  </div></section>
}
