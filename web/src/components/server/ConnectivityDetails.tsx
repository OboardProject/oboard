import { useId, useState } from 'react'
import {
  connectivityDetailsRequestPath,
  connectivitySlaDisplay,
  formatConnectivityDuration,
  type ConnectivitySLAResponse,
  type ConnectivityEventsResponse,
  type ConnectivityWindowKey,
} from '../../connectivity-sla'
import { useServerMonitorQuery } from '../../use-server-monitor-query'

type Props = {
  serverID: number
  windowKey: ConnectivityWindowKey
  client: Parameters<typeof useServerMonitorQuery>[0]
}

const eventLabels: Record<string, string> = {
  probe_enabled: '探测启用', probe_disabled: '探测停用', probe_target_changed: '探测目标变更',
  probe_result: '探测报告', server_offline: '服务器离线',
  controller_connected: '主控连接恢复', controller_disconnected: '主控连接断开',
}

function ReadError({ error, retry }: { error: unknown; retry: () => void }) {
  if (!error) return null
  return <div className="connectivity-coverage-note danger-text" role="alert">
    <span>读取失败：{error instanceof Error ? error.message : String(error)}</span>
    <button type="button" className="ghost" onClick={retry}>重试</button>
  </div>
}

function SLASection({ serverID, windowKey, client }: Props) {
  const query = useServerMonitorQuery<ConnectivitySLAResponse>(client, connectivityDetailsRequestPath(serverID, windowKey, 'sla'), true)
  const result = query.response
  return <>
    <ReadError error={query.error} retry={query.refresh} />
    {query.loading ? <p role="status">正在读取在线率…</p> : null}
    {result ? <>
      <p className="connectivity-coverage-note">在线率 = 在线时长 ÷（在线 + 离线时长）。未知时长单独统计。</p>
      <div className="latency-overview-grid">
        {[
          ['在线率', connectivitySlaDisplay(result.summary.sla_percent)],
          ['在线', formatConnectivityDuration(result.summary.available_seconds)],
          ['离线', formatConnectivityDuration(result.summary.unavailable_seconds)],
          ['未知', formatConnectivityDuration(result.summary.unknown_seconds)],
          ['覆盖率', connectivitySlaDisplay(result.summary.coverage_percent)],
          ['故障次数', String(result.summary.outage_count)],
          ['最长故障', formatConnectivityDuration(result.summary.longest_outage_seconds)],
        ].map(([label, value]) => <article key={label} className="latency-overview-card"><span>{label}</span><strong>{value}</strong></article>)}
      </div>
      <p className="connectivity-coverage-note">统计范围 {new Date(result.window.from).toLocaleString()} — {new Date(result.window.to).toLocaleString()}{result.metadata.retention_clipped ? ' · 已按保留期限裁剪' : ''}</p>
      <h4>最近故障（最多 10 次）</h4>
      {result.outages.length ? <ul className="connectivity-outages">{result.outages.map(outage => <li key={outage.started_at}>
        <span>{new Date(outage.started_at).toLocaleString()}{outage.started_before_window ? '（窗口开始前已发生）' : ''} — {outage.ended_at ? new Date(outage.ended_at).toLocaleString() : '窗口结束时尚未恢复'}</span>
        <strong>{formatConnectivityDuration(outage.duration_seconds)}</strong>
      </li>)}</ul> : <p>此窗口没有已知故障。</p>}
    </> : null}
    <button type="button" className="ghost" onClick={query.refresh} disabled={query.loading}>刷新在线率</button>
  </>
}

function EventsSection({ serverID, windowKey, client }: Props) {
  const [cursor, setCursor] = useState('')
  const query = useServerMonitorQuery<ConnectivityEventsResponse>(client, connectivityDetailsRequestPath(serverID, windowKey, 'events', cursor), true)
  const result = query.response
  return <>
    <ReadError error={query.error} retry={query.refresh} />
    {query.loading ? <p role="status">正在读取事件…</p> : null}
    {result ? <>
      <p className="connectivity-coverage-note">每页最多 100 条；翻页期间固定时间范围，新报告在返回首页后读取。</p>
      {result.events.length ? <ul className="connectivity-outages">{result.events.map(event => <li key={event.id}>
        <span><time dateTime={event.effective_at}>{new Date(event.effective_at).toLocaleString()}</time> · {eventLabels[event.kind] || event.kind}{event.kind === 'probe_result' ? ` · ${event.available === null ? '未知' : event.available ? `可用 · ${event.latency_ms} ms` : '失败'}` : ''}{event.error ? ` · ${event.error}` : ''}</span>
      </li>)}</ul> : <p>本页没有事件；数据可能已按保留策略过期。</p>}
    </> : null}
    <div className="dialog-actions">
      <button type="button" className="ghost" onClick={() => cursor ? setCursor('') : query.refresh()} disabled={query.loading}>返回首页并刷新</button>
      <button type="button" className="ghost" onClick={() => result?.has_more && setCursor(result.next_cursor)} disabled={query.loading || !result?.has_more}>下一页</button>
    </div>
  </>
}

export function ConnectivityDetails(props: Props) {
  const [slaOpen, setSlaOpen] = useState(false)
  const [eventsOpen, setEventsOpen] = useState(false)
  const id = useId()
  return <section className="connectivity-section" aria-label="连通性详情">
    <div className="connectivity-section-head">
      <button type="button" className="ghost" aria-expanded={slaOpen} aria-controls={`${id}-sla`} onClick={() => setSlaOpen(value => !value)}>在线率与故障摘要</button>
      <button type="button" className="ghost" aria-expanded={eventsOpen} aria-controls={`${id}-events`} onClick={() => setEventsOpen(value => !value)}>诊断事件</button>
    </div>
    <div id={`${id}-sla`} hidden={!slaOpen}>{slaOpen ? <SLASection {...props} /> : null}</div>
    <div id={`${id}-events`} hidden={!eventsOpen}>{eventsOpen ? <EventsSection {...props} /> : null}</div>
  </section>
}
