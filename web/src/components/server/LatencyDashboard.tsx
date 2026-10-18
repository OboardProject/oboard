import React, { useEffect, useMemo, useState } from 'react'
import { AlertTriangle, CheckCircle2, Info } from 'lucide-react'
import { Select } from '../ui/select'
import { OverflowMenu } from '../ui/overflow-menu'
import type { ConnectivityResponse, LatencyChartResponse, ConnectivityWindowKey, LatencyProbeTargetStat } from '../../connectivity-sla'
import {
  computeOverviewStats,
  computePercentiles,
  hasTaskProbeTargets,
  overviewSourceStats,
  percentileValuesFromBuckets,
  pickAnomalies,
  probeMethodLabel,
  rankWorstTargets,
  readIncludePublicStats,
  seriesIDForTarget,
  shouldIncludePublicInOverview,
  writeIncludePublicStats,
  type LatencyAnomaly,
} from '../../latency-dashboard'
import { alignUnifiedMetrics, REGIONAL_SERIES_COLORS, type ServerLatencyPoint } from '../../server-unified-chart'
import { ConnectivityDetails } from './ConnectivityDetails'
import { ServerUnifiedTelemetryChart } from './ServerUnifiedTelemetryChart'
import '../../styles/server-monitor.css'

const EMPTY_STATS: LatencyProbeTargetStat[] = []

const GRANULARITY_OPTIONS = [
  { value: '180', label: '标准' },
  { value: '360', label: '精细' },
] as const

function formatMS(value: number | null | undefined) {
  return value == null || !Number.isFinite(value) ? '—' : `${Math.round(value)} ms`
}

function formatMetricNumber(value: number | null | undefined) {
  return value == null || !Number.isFinite(value) ? '—' : String(Math.round(value))
}

function formatPercent(value: number | null | undefined) {
  return value == null || !Number.isFinite(value) ? '—' : `${value.toFixed(value >= 10 || value === 0 ? 0 : 1)}%`
}

function formatPercentNumber(value: number | null | undefined) {
  return value == null || !Number.isFinite(value) ? '—' : value.toFixed(value >= 10 || value === 0 ? 0 : 1)
}

function formatSampleCount(value: number) {
  return new Intl.NumberFormat('zh-CN', { notation: value >= 10_000 ? 'compact' : 'standard', maximumFractionDigits: 1 }).format(value)
}

function formatAnomalyTime(value: string | null | undefined) {
  if (!value) return '窗口内'
  const date = new Date(value)
  if (!Number.isFinite(date.getTime())) return '窗口内'
  return `${String(date.getHours()).padStart(2, '0')}:${String(date.getMinutes()).padStart(2, '0')}`
}

function formatResolution(seconds: number) {
  if (!Number.isFinite(seconds) || seconds <= 0) return '未知'
  if (seconds % 3600 === 0) return `${seconds / 3600} 小时`
  if (seconds % 60 === 0) return `${seconds / 60} 分钟`
  return `${seconds} 秒`
}

function Kpi({ label, value, unit, title, hint, tone = '' }: { label: string; value: string; unit?: string; title?: string; hint?: string; tone?: string }) {
  return <div className={`lp-kpi${tone ? ` is-${tone}` : ''}`} title={title}>
    <span className="lp-kpi-label">{label}</span>
    <span className="lp-kpi-value"><strong>{value}</strong>{unit && value !== '—' ? <small>{unit}</small> : null}</span>
    {hint ? <span className="lp-kpi-hint">{hint}</span> : null}
  </div>
}

function AnomalySummary({
  anomalies,
  p95,
  seriesColor,
  stats,
  onSelect,
}: {
  anomalies: LatencyAnomaly[]
  p95: number | null
  seriesColor: Record<string, string>
  stats: LatencyProbeTargetStat[]
  onSelect: (stat: LatencyProbeTargetStat) => void
}) {
  if (!anomalies.length) return <p className="lp-anomaly-empty"><CheckCircle2 size={15} aria-hidden="true" />当前时间范围未发现明显异常</p>
  return <ul className="lp-anomalies">
    {anomalies.map(item => {
      const stat = stats.find(row => row.key === item.key)
      const color = stat ? seriesColor[seriesIDForTarget(stat)] : undefined
      const loss = item.kind === 'high_loss'
      return <li key={`${item.kind}-${item.key}`}>
        <button type="button" className="lp-anomaly" onClick={() => stat && onSelect(stat)} disabled={!stat} title={stat ? `突出显示 ${item.label} 曲线` : undefined}>
          <time dateTime={item.at || undefined}>{formatAnomalyTime(item.at)}</time>
          <span className={`lp-anomaly-kind ${loss ? 'is-danger' : 'is-warning'}`}><AlertTriangle size={12} aria-hidden="true" />{loss ? '高丢包' : '高延迟'}</span>
          <span className="lp-anomaly-target"><i style={color ? { backgroundColor: color } : undefined} />{item.label}</span>
          <strong>{item.kind === 'high_latency' ? formatMS(item.latencyMS) : formatPercent(item.lossPercent)}</strong>
          <span className="lp-anomaly-detail">{item.kind === 'high_latency' && p95 != null && (item.latencyMS || 0) > p95 ? `高于 P95 ${Math.round(p95)} ms` : item.detail}</span>
        </button>
      </li>
    })}
  </ul>
}

export function LatencyDashboard({
  response,
  windowKey,
  windowHours,
  windowLabels,
  latencyWindowOptions,
  onWindowChange,
  onWindowKeyDown,
  publicMode,
  serverID,
  detailsClient,
  detailsRevision = 0,
  statusLabel,
  statusTone = 'fair',
  actions,
}: {
  response: ConnectivityResponse | LatencyChartResponse
  windowKey: ConnectivityWindowKey
  windowHours: number
  windowLabels: Record<ConnectivityWindowKey, string>
  latencyWindowOptions: ConnectivityWindowKey[]
  onWindowChange: (key: ConnectivityWindowKey) => void
  onWindowKeyDown: (event: React.KeyboardEvent<HTMLButtonElement>, index: number) => void
  publicMode?: string
  serverID?: number
  detailsClient?: React.ComponentProps<typeof ConnectivityDetails>['client']
  detailsRevision?: number
  statusLabel?: string
  statusTone?: 'great' | 'fair' | 'poor'
  actions?: React.ReactNode
}) {
  const stats = response.probe_target_stats || EMPTY_STATS
  const hasTasks = hasTaskProbeTargets(stats)
  const [includePublicPref, setIncludePublicPref] = useState(readIncludePublicStats)
  const [worstOnly, setWorstOnly] = useState(false)
  const [compareMode, setCompareMode] = useState(false)
  const [granularity, setGranularity] = useState('360')
  const [enabledSeries, setEnabledSeries] = useState<Record<string, boolean>>({})
  const includePublic = shouldIncludePublicInOverview(stats, includePublicPref)
  const bucketCount = Number(granularity) || 360
  const effectiveWindowHours = Math.max(1, (Date.parse(response.window.to) - Date.parse(response.window.from)) / 3_600_000) || windowHours

  const aligned = useMemo(() => alignUnifiedMetrics({
    latencyPoints: (response.latency_points || []) as ServerLatencyPoint[],
    regionalProbes: response.regional_latency_points || [],
    includeResources: false,
    windowHours: effectiveWindowHours,
    bucketCount,
    now: response.window?.to ? new Date(response.window.to).getTime() : Date.now(),
  }), [response.latency_points, response.regional_latency_points, response.window?.to, effectiveWindowHours, bucketCount])

  useEffect(() => {
    setEnabledSeries(prev => {
      let changed = false
      const next = { ...prev }
      aligned.seriesList.forEach(series => {
        if (next[series.id] === undefined) { next[series.id] = true; changed = true }
      })
      return changed ? next : prev
    })
  }, [aligned.seriesList])

  const visibleStats = useMemo(() => worstOnly ? rankWorstTargets(stats, 10) : stats, [stats, worstOnly])
  const chartEnabledSeries = useMemo(() => {
    if (!worstOnly) return enabledSeries
    const visibleIDs = new Set(visibleStats.map(seriesIDForTarget))
    const next = { ...enabledSeries }
    aligned.seriesList.forEach(series => {
      if (!visibleIDs.has(series.id)) next[series.id] = false
    })
    return next
  }, [aligned.seriesList, enabledSeries, visibleStats, worstOnly])
  const overview = useMemo(() => computeOverviewStats(stats, includePublic), [stats, includePublic])
  const overviewSeriesIDs = useMemo(
    () => overviewSourceStats(stats, includePublic).map(seriesIDForTarget),
    [stats, includePublic],
  )
  const percentiles = useMemo(
    () => computePercentiles(percentileValuesFromBuckets(aligned.buckets, overviewSeriesIDs)),
    [aligned.buckets, overviewSeriesIDs],
  )
  const anomalies = useMemo(() => pickAnomalies(stats, includePublic, percentiles.p95), [stats, includePublic, percentiles.p95])
  const seriesColor = useMemo(() => {
    const colors: Record<string, string> = {}
    aligned.seriesList.forEach(series => { colors[series.id] = series.color })
    return colors
  }, [aligned.seriesList])

  const focusTarget = (stat: LatencyProbeTargetStat) => {
    const id = seriesIDForTarget(stat)
    setEnabledSeries(() => {
      const next: Record<string, boolean> = {}
      aligned.seriesList.forEach(series => { next[series.id] = series.id === id })
      return next
    })
  }

  const toggleTarget = (stat: LatencyProbeTargetStat) => {
    const id = seriesIDForTarget(stat)
    setEnabledSeries(prev => {
      if (compareMode) return { ...prev, [id]: prev[id] === false }
      const activeCount = aligned.seriesList.reduce((count, series) => count + (prev[series.id] === false ? 0 : 1), 0)
      if (prev[id] !== false && activeCount === 1) {
        const all: Record<string, boolean> = {}
        aligned.seriesList.forEach(series => { all[series.id] = true })
        return all
      }
      const next: Record<string, boolean> = {}
      aligned.seriesList.forEach(series => { next[series.id] = series.id === id })
      return next
    })
  }

  const setIncludePublic = (checked: boolean) => {
    setIncludePublicPref(checked)
    writeIncludePublicStats(checked)
  }

  const metadata = 'metadata' in response ? response.metadata : null
  const observedAt = metadata?.observed_through ? new Date(metadata.observed_through) : null
  const observedTime = observedAt && Number.isFinite(observedAt.getTime())
    ? observedAt.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
    : '尚无报告'
  const metadataDetail = metadata ? [
    metadata.observed_through ? `完整时间：${new Date(metadata.observed_through).toLocaleString()}` : '尚无有效报告',
    `采样分辨率：${metadata.resolution_seconds} 秒`,
    `数据来源：${metadata.coverage.source}`,
    metadata.stale ? '当前显示缓存结果' : '',
    metadata.aggregation_state === 'catching_up' ? '完整摘要仍在生成，本次使用有上限的明细读取' : '',
    metadata.coverage.retention_clipped ? '已按监控保留期限裁剪范围' : '',
    metadata.coverage.legacy_curve_reports ? '历史连通性记录仅补充公网曲线' : '',
    stats.some(stat => (stat.measurement_revision_count || 0) > 1) ? '窗口内存在目标测量变化，统计包含多个修订' : '',
    metadata.coverage.legacy_measurement_revision ? '部分历史记录未包含完整测量修订' : '',
  ].filter(Boolean).join('；') : ''
  const anomalyPanel = <AnomalySummary anomalies={anomalies} p95={percentiles.p95} seriesColor={seriesColor} stats={stats} onSelect={focusTarget} />
  const lossTone = overview.lossPercent != null && overview.lossPercent >= 5 ? 'danger' : overview.lossPercent != null && overview.lossPercent > 0 ? 'warning' : ''
  const successTone = overview.successPercent != null && overview.successPercent < 95 ? 'danger' : overview.successPercent != null && overview.successPercent < 100 ? 'warning' : ''

  return (
    <div className="lp">
      <div className="lp-bar">
        <div className="lp-status">
          {statusLabel ? <span className={`lp-health is-${statusTone}`}><i aria-hidden="true" />{statusLabel}</span> : null}
          {metadata ? <>
            <span className="lp-status-item">{metadata.stale ? '缓存至' : '更新于'} <strong>{observedTime}</strong></span>
            <span className="lp-status-item">每 <strong>{formatResolution(metadata.resolution_seconds)}</strong> 采样</span>
            {metadata.stale ? <span className="lp-flag is-warning">缓存数据</span> : null}
            {metadata.aggregation_state === 'catching_up' ? <span className="lp-flag is-warning">汇总中</span> : null}
            {metadata.coverage.retention_clipped ? <span className="lp-flag">范围已裁剪</span> : null}
            <details className="lp-details">
              <summary title="数据范围与质量详情" aria-label="数据范围与质量详情"><Info size={14} aria-hidden="true" /></summary>
              <p>{metadataDetail}</p>
            </details>
          </> : null}
        </div>
        <div className="lp-bar-actions">
          <div className="lp-segmented" role="radiogroup" aria-label="延迟时间范围">
            {latencyWindowOptions.map((key, index) => (
              <button
                id={`server-latency-window-${key}`}
                key={key}
                type="button"
                role="radio"
                aria-checked={windowKey === key}
                tabIndex={windowKey === key ? 0 : -1}
                className={windowKey === key ? 'active' : ''}
                title={windowLabels[key]}
                onClick={() => onWindowChange(key)}
                onKeyDown={event => onWindowKeyDown(event, index)}
              >{key}</button>
            ))}
          </div>
          {actions}
        </div>
      </div>

      <section className="lp-kpis" aria-label="延迟统计摘要">
        <Kpi label="平均延迟" value={formatMetricNumber(overview.avgMS)} unit="ms" hint={overview.usedPublicFallback ? '仅有公网探测' : undefined} />
        <Kpi label="P50" value={formatMetricNumber(percentiles.p50)} unit="ms" />
        <Kpi label="P95" value={formatMetricNumber(percentiles.p95)} unit="ms" title="按显示时间桶的均值计算，不是逐包分位数" />
        <Kpi label="P99" value={formatMetricNumber(percentiles.p99)} unit="ms" />
        <Kpi label="丢包率" value={formatPercentNumber(overview.lossPercent)} unit="%" tone={lossTone} />
        <Kpi label="成功率" value={formatPercentNumber(overview.successPercent)} unit="%" tone={successTone} />
        <Kpi label="抖动" value={formatMetricNumber(overview.jitterMS)} unit="ms" />
        <Kpi label="样本" value={formatSampleCount(overview.sampleCount)} />
      </section>

      <section className="lp-main" aria-label={`趋势 ${windowLabels[windowKey]}`}>
        <aside className="lp-targets" aria-label="监控目标">
          <header className="lp-card-head">
            <h3>监控目标 <span>{visibleStats.length}{compareMode ? ' · 对比中' : ''}</span></h3>
            <OverflowMenu
              label="目标与统计设置"
              width={210}
              triggerClassName="ghost icon-button lp-more"
              groups={[{ key: 'display', items: [
                { key: 'worst', label: worstOnly ? '显示全部目标' : '只看最差 10', onSelect: () => setWorstOnly(value => !value) },
                { key: 'compare', label: compareMode ? '关闭对比模式' : '开启对比模式', onSelect: () => setCompareMode(value => !value) },
                ...(hasTasks ? [{ key: 'public', label: includePublicPref ? '概览排除公网探测' : '概览计入公网探测', onSelect: () => setIncludePublic(!includePublicPref) }] : []),
              ] }]}
            />
          </header>
          <ul className="lp-target-list">
            {visibleStats.map(stat => {
              const seriesID = seriesIDForTarget(stat)
              const active = enabledSeries[seriesID] !== false
              const color = seriesColor[seriesID] || (stat.kind === 'public' ? '#f59e0b' : REGIONAL_SERIES_COLORS[0])
              const name = stat.task_name || (stat.kind === 'public' ? '公网探测' : `${stat.province || ''} · ${stat.carrier || ''}`)
              const loss = Number(stat.loss_percent || 0)
              return (
                <li key={stat.key}>
                  <button type="button" className={`lp-target${active ? ' active' : ''}`} aria-pressed={active} onClick={() => toggleTarget(stat)} title={`${name}；点击${compareMode ? '显示或隐藏' : '突出显示'}曲线`}>
                    <span className="lp-target-swatch" style={{ backgroundColor: color }} />
                    <span className="lp-target-copy">
                      <strong>{name}</strong>
                      <small>{probeMethodLabel(stat.mode || (stat.kind === 'public' ? publicMode : undefined))}<span className={loss >= 5 ? 'is-danger' : loss > 0 ? 'is-warning' : ''}>丢包 {formatPercent(stat.loss_percent)}</span><span>抖动 {formatMS(stat.jitter_ms)}</span></small>
                    </span>
                    <strong className="lp-target-value">{formatMS(stat.avg_ms)}</strong>
                  </button>
                </li>
              )
            })}
            {!visibleStats.length && <li className="lp-empty">暂无探测结果</li>}
          </ul>
        </aside>

        <section className="lp-chart" aria-label="延迟趋势图">
          <header className="lp-card-head">
            <h3>延迟趋势 <span>{windowLabels[windowKey]}</span></h3>
            <label className="lp-granularity">
              <span>精度</span>
              <Select value={granularity} onChange={event => setGranularity(event.target.value)} aria-label="显示精度">
                {GRANULARITY_OPTIONS.map(option => <option key={option.value} value={option.value}>{option.label}</option>)}
              </Select>
            </label>
          </header>
          <ServerUnifiedTelemetryChart
            aligned={aligned}
            latencyPoints={response.latency_points || []}
            regionalProbes={response.regional_latency_points || []}
            failedProbePoints={response.failed_probe_points || []}
            includeResources={false}
            windowHours={effectiveWindowHours}
            windowEndAt={response.window.to}
            seriesEnabled={chartEnabledSeries}
            onSeriesEnabledChange={setEnabledSeries}
            hideLegend
            bucketCount={bucketCount}
            chartHeight={280}
            compactOptions
          />
        </section>
      </section>

      {serverID && detailsClient ? <ConnectivityDetails
        serverID={serverID}
        windowKey={windowKey}
        client={detailsClient}
        anomalyCount={anomalies.length}
        anomalyPanel={anomalyPanel}
        refreshRevision={detailsRevision}
      /> : <section className="latency-insights-fallback" aria-label="异常事件">{anomalyPanel}</section>}
    </div>
  )
}
