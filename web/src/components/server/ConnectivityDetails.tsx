import { useId, useState, type ReactNode } from 'react'
import {
  connectivityDetailsRequestPath,
  connectivitySlaDisplay,
  formatConnectivityDuration,
  type ConnectivitySLAResponse,
  type ConnectivityEventsResponse,
  type ConnectivityWindowKey,
} from '../../connectivity-sla'
import { useServerMonitorQuery } from '../../use-server-monitor-query'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '../ui/tabs'

export type ConnectivityDetailsProps = {
  serverID: number
  windowKey: ConnectivityWindowKey
  client: Parameters<typeof useServerMonitorQuery>[0]
  anomalyCount?: number
  anomalyPanel?: ReactNode
  refreshRevision?: number
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

function SLASection({ serverID, windowKey, client }: ConnectivityDetailsProps) {
  const query = useServerMonitorQuery<ConnectivitySLAResponse>(client, connectivityDetailsRequestPath(serverID, windowKey, 'sla'), true)
  const result = query.response
  return <>
    <ReadError error={query.error} retry={query.refresh} />
    {query.loading ? <p role="status">正在读取在线率…</p> : null}
    {result ? <>
      <div className="connectivity-summary-metrics" aria-label="在线率与故障摘要">
        {[
          ['在线率', connectivitySlaDisplay(result.summary.sla_percent)],
          ['在线', formatConnectivityDuration(result.summary.available_seconds)],
          ['离线', formatConnectivityDuration(result.summary.unavailable_seconds)],
          ['未知', formatConnectivityDuration(result.summary.unknown_seconds)],
          ['覆盖率', connectivitySlaDisplay(result.summary.coverage_percent)],
          ['故障次数', String(result.summary.outage_count)],
          ['最长故障', formatConnectivityDuration(result.summary.longest_outage_seconds)],
        ].map(([label, value]) => <span key={label}><small>{label}</small><strong>{value}</strong></span>)}
      </div>
      <p className="connectivity-detail-note" title={`统计范围 ${new Date(result.window.from).toLocaleString()} — ${new Date(result.window.to).toLocaleString()}`}>在线率按在线时长 ÷（在线 + 离线时长）计算，未知时长单独统计{result.metadata.retention_clipped ? '；范围已按保留期限裁剪' : ''}。</p>
      <h4 className="connectivity-detail-heading">最近故障 <span>最多 10 次</span></h4>
      {result.outages.length ? <ul className="connectivity-outages">{result.outages.map(outage => <li key={outage.started_at}>
        <span>{new Date(outage.started_at).toLocaleString()}{outage.started_before_window ? '（窗口开始前已发生）' : ''} — {outage.ended_at ? new Date(outage.ended_at).toLocaleString() : '窗口结束时尚未恢复'}</span>
        <strong>{formatConnectivityDuration(outage.duration_seconds)}</strong>
      </li>)}</ul> : <p className="connectivity-detail-empty">此时间范围没有已知故障。</p>}
    </> : null}
  </>
}

function EventsSection({ serverID, windowKey, client }: ConnectivityDetailsProps) {
  const [cursor, setCursor] = useState('')
  const query = useServerMonitorQuery<ConnectivityEventsResponse>(client, connectivityDetailsRequestPath(serverID, windowKey, 'events', cursor), true)
  const result = query.response
  return <>
    <ReadError error={query.error} retry={query.refresh} />
    {query.loading ? <p role="status">正在读取事件…</p> : null}
    {result ? <>
      {result.events.length ? <ul className="connectivity-outages connectivity-event-list">{result.events.map(event => <li key={event.id}>
        <time dateTime={event.effective_at}>{new Date(event.effective_at).toLocaleString()}</time>
        <span>{eventLabels[event.kind] || event.kind}{event.kind === 'probe_result' ? ` · ${event.available === null ? '未知' : event.available ? `可用 · ${event.latency_ms} ms` : '失败'}` : ''}{event.error ? ` · ${event.error}` : ''}</span>
      </li>)}</ul> : <p className="connectivity-detail-empty">本页没有事件；数据可能已按保留策略过期。</p>}
    </> : null}
    <div className="connectivity-pagination">
      <span>每页最多 100 条 · 翻页期间固定时间范围</span>
      <button type="button" className="ghost" onClick={() => setCursor('')} disabled={query.loading || !cursor}>返回首页并刷新</button>
      <button type="button" className="ghost" onClick={() => result?.has_more && setCursor(result.next_cursor)} disabled={query.loading || !result?.has_more}>下一页</button>
    </div>
  </>
}

export function ConnectivityDetails(props: ConnectivityDetailsProps) {
  const { anomalyCount = 0, anomalyPanel, refreshRevision = 0 } = props
  const id = useId()
  const tabs = [
    ...(anomalyPanel ? [{ value: 'anomalies', label: `异常事件 ${anomalyCount}` }] : []),
    { value: 'sla', label: '在线率与故障' },
    { value: 'events', label: '诊断事件' },
  ]
  const [activeTab, setActiveTab] = useState(anomalyPanel ? 'anomalies' : 'sla')
  const selectTab = (value: string) => {
    setActiveTab(value)
    window.requestAnimationFrame(() => document.getElementById(`${id}-tab-${value}`)?.focus())
  }
  const handleKeyDown = (event: React.KeyboardEvent<HTMLButtonElement>, index: number) => {
    if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
    event.preventDefault()
    const nextIndex = event.key === 'Home' ? 0
      : event.key === 'End' ? tabs.length - 1
        : (index + (event.key === 'ArrowRight' ? 1 : -1) + tabs.length) % tabs.length
    selectTab(tabs[nextIndex].value)
  }

  return <section className="connectivity-section connectivity-detail-tabs" aria-label="连通性详情">
    <Tabs value={activeTab} onValueChange={setActiveTab}>
      <TabsList className="connectivity-section-head" aria-label="网络探测详情">
        {tabs.map((tab, index) => <TabsTrigger
          id={`${id}-tab-${tab.value}`}
          key={tab.value}
          value={tab.value}
          aria-controls={`${id}-panel-${tab.value}`}
          tabIndex={activeTab ? (activeTab === tab.value ? 0 : -1) : (index === 0 ? 0 : -1)}
          onKeyDown={event => handleKeyDown(event, index)}
        >{tab.label}</TabsTrigger>)}
      </TabsList>
      {anomalyPanel ? <TabsContent id={`${id}-panel-anomalies`} aria-labelledby={`${id}-tab-anomalies`} tabIndex={0} value="anomalies" className="connectivity-detail-panel">{anomalyPanel}</TabsContent> : null}
      <TabsContent id={`${id}-panel-sla`} aria-labelledby={`${id}-tab-sla`} tabIndex={0} value="sla" className="connectivity-detail-panel"><SLASection key={`sla:${refreshRevision}`} {...props} /></TabsContent>
      <TabsContent id={`${id}-panel-events`} aria-labelledby={`${id}-tab-events`} tabIndex={0} value="events" className="connectivity-detail-panel"><EventsSection key={`events:${refreshRevision}`} {...props} /></TabsContent>
    </Tabs>
  </section>
}
