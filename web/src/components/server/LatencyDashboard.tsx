import { useEffect, useMemo, useState } from 'react'
import { Switch } from '../ui/switch'
import { Select } from '../ui/select'
import type { ConnectivityResponse, ConnectivityWindowKey, LatencyProbeTargetStat } from '../../connectivity-sla'
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
  sparklineValues,
  writeIncludePublicStats,
} from '../../latency-dashboard'
import { alignUnifiedMetrics, REGIONAL_SERIES_COLORS, type ServerLatencyPoint } from '../../server-unified-chart'
import { ServerUnifiedTelemetryChart } from './ServerUnifiedTelemetryChart'

const GRANULARITY_OPTIONS = [
  { value: '30', label: '较粗' },
  { value: '60', label: '默认' },
  { value: '120', label: '较细' },
] as const

function formatMS(value: number | null | undefined) {
  return value == null || !Number.isFinite(value) ? '—' : `${Math.round(value)} ms`
}

function formatPercent(value: number | null | undefined) {
  return value == null || !Number.isFinite(value) ? '—' : `${value.toFixed(value >= 10 || value === 0 ? 0 : 1)}%`
}

function formatAnomalyTime(value: string | null | undefined) {
  if (!value) return ''
  const date = new Date(value)
  if (!Number.isFinite(date.getTime())) return ''
  return `${String(date.getHours()).padStart(2, '0')}:${String(date.getMinutes()).padStart(2, '0')}`
}

function TargetSparkline({ values, color }: { values: Array<number | null>; color: string }) {
  const width = 72
  const height = 22
  const finite = values.filter((value): value is number => value != null)
  const max = Math.max(1, ...finite)
  const step = values.length > 1 ? width / (values.length - 1) : width
  const points = values.map((value, index) => {
    const x = index * step
    const y = value == null ? height : height - (value / max) * (height - 2)
    return `${x},${y}`
  }).join(' ')
  return (
    <svg className="latency-target-spark" viewBox={`0 0 ${width} ${height}`} aria-hidden="true">
      <polyline points={points} fill="none" stroke={color} strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  )
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
}: {
  response: ConnectivityResponse
  windowKey: ConnectivityWindowKey
  windowHours: number
  windowLabels: Record<ConnectivityWindowKey, string>
  latencyWindowOptions: ConnectivityWindowKey[]
  onWindowChange: (key: ConnectivityWindowKey) => void
  onWindowKeyDown: (event: React.KeyboardEvent<HTMLButtonElement>, index: number) => void
  publicMode?: string
}) {
  const stats = response.probe_target_stats || []
  const hasTasks = hasTaskProbeTargets(stats)
  const [includePublicPref, setIncludePublicPref] = useState(readIncludePublicStats)
  const [worstOnly, setWorstOnly] = useState(false)
  const [compareMode, setCompareMode] = useState(false)
  const [granularity, setGranularity] = useState('60')
  const [enabledSeries, setEnabledSeries] = useState<Record<string, boolean>>({})
  const includePublic = shouldIncludePublicInOverview(stats, includePublicPref)
  const bucketCount = Number(granularity) || 60

  const aligned = useMemo(() => alignUnifiedMetrics({
    latencyPoints: (response.latency_points || []) as ServerLatencyPoint[],
    regionalProbes: response.regional_latency_points || [],
    includeResources: false,
    windowHours,
    bucketCount,
    now: response.window?.to ? new Date(response.window.to).getTime() : Date.now(),
  }), [response.latency_points, response.regional_latency_points, response.window?.to, windowHours, bucketCount])

  useEffect(() => {
    setEnabledSeries(prev => {
      const next = { ...prev }
      aligned.seriesList.forEach(series => {
        if (next[series.id] === undefined) next[series.id] = true
      })
      return next
    })
  }, [aligned.seriesList])

  const visibleStats = useMemo(() => {
    const ranked = worstOnly ? rankWorstTargets(stats, 10) : stats
    return ranked
  }, [stats, worstOnly])

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

  const toggleTarget = (stat: LatencyProbeTargetStat) => {
    const id = seriesIDForTarget(stat)
    setEnabledSeries(prev => {
      if (compareMode) return { ...prev, [id]: prev[id] === false }
      const next: Record<string, boolean> = {}
      aligned.seriesList.forEach(series => { next[series.id] = series.id === id })
      if (prev[id] !== false && Object.values(prev).filter(Boolean).length === 1) {
        aligned.seriesList.forEach(series => { next[series.id] = true })
      }
      return next
    })
  }

  const setIncludePublic = (checked: boolean) => {
    setIncludePublicPref(checked)
    writeIncludePublicStats(checked)
  }

  return (
    <div className="latency-dashboard">
      <section className="latency-overview" aria-label={`概览 ${windowLabels[windowKey]}`}>
        <header className="latency-overview-head">
          <div>
            <h3>概览</h3>
            <span>{windowLabels[windowKey]}</span>
          </div>
          {hasTasks && (
            <label className="latency-overview-toggle">
              <span>计入公网探测</span>
              <Switch size="sm" checked={includePublicPref} onChange={setIncludePublic} ariaLabel="概览统计计入公网探测" />
            </label>
          )}
        </header>
        <div className="latency-overview-grid">
          <article className="latency-overview-card">
            <span>平均延迟</span>
            <strong>{formatMS(overview.avgMS)}</strong>
          </article>
          <article className="latency-overview-card">
            <span>抖动（波动）</span>
            <strong>{formatMS(overview.jitterMS)}</strong>
          </article>
          <article className="latency-overview-card">
            <span>平均丢包率</span>
            <strong>{formatPercent(overview.lossPercent)}</strong>
          </article>
          <article className="latency-overview-card">
            <span>平均成功率</span>
            <strong>{formatPercent(overview.successPercent)}</strong>
          </article>
        </div>
        {overview.usedPublicFallback && <p className="latency-overview-note">当前只有公网探测，概览已按公网结果统计。</p>}
      </section>

      <div className="latency-dashboard-main">
        <section className="latency-targets" aria-label="监控目标">
          <header className="latency-targets-head">
            <div>
              <h3>监控目标</h3>
              <span>{visibleStats.length} 个</span>
            </div>
            <div className="latency-targets-actions">
              <button type="button" className={worstOnly ? 'active' : ''} aria-pressed={worstOnly} onClick={() => setWorstOnly(value => !value)}>只看最差 10</button>
              <button type="button" className={compareMode ? 'active' : ''} aria-pressed={compareMode} onClick={() => setCompareMode(value => !value)}>对比模式</button>
            </div>
          </header>
          <ul className="latency-target-list">
            {visibleStats.map(stat => {
              const seriesID = seriesIDForTarget(stat)
              const active = enabledSeries[seriesID] !== false
              const color = seriesColor[seriesID] || (stat.kind === 'public' ? '#f59e0b' : REGIONAL_SERIES_COLORS[0])
              return (
                <li key={stat.key}>
                  <button type="button" className={`latency-target-item${active ? ' active' : ''}`} aria-pressed={active} onClick={() => toggleTarget(stat)}>
                    <span className="latency-target-dot" style={{ backgroundColor: color }} />
                    <span className="latency-target-copy">
                      <strong>{stat.task_name || (stat.kind === 'public' ? '公网探测' : `${stat.province || ''} · ${stat.carrier || ''}`)}</strong>
                      <small>{probeMethodLabel(stat.mode || (stat.kind === 'public' ? publicMode : undefined))}</small>
                    </span>
                    <span className="latency-target-metrics">
                      <span>{formatMS(stat.avg_ms)}</span>
                      <span>丢包 {formatPercent(stat.loss_percent)}</span>
                      <span>抖动 {formatMS(stat.jitter_ms)}</span>
                    </span>
                    <TargetSparkline values={sparklineValues(aligned.buckets, seriesID)} color={color} />
                  </button>
                </li>
              )
            })}
            {!visibleStats.length && <li className="latency-target-empty">暂无探测结果</li>}
          </ul>
        </section>

        <section className="latency-trend" aria-label="趋势">
          <header className="latency-trend-head">
            <h3>趋势</h3>
            <div className="latency-trend-controls">
              <label className="latency-granularity">
                <span>数据粒度</span>
                <Select value={granularity} onChange={event => setGranularity(event.target.value)} aria-label="数据粒度">
                  {GRANULARITY_OPTIONS.map(option => <option key={option.value} value={option.value}>{option.label}</option>)}
                </Select>
              </label>
              <div className="server-monitor-window-toggle" role="radiogroup" aria-label="延迟时间范围">
                {latencyWindowOptions.map((key, index) => (
                  <button
                    id={`server-latency-window-${key}`}
                    key={key}
                    type="button"
                    role="radio"
                    aria-checked={windowKey === key}
                    tabIndex={windowKey === key ? 0 : -1}
                    className={windowKey === key ? 'active' : ''}
                    onClick={() => onWindowChange(key)}
                    onKeyDown={event => onWindowKeyDown(event, index)}
                  >
                    {key}
                  </button>
                ))}
              </div>
            </div>
          </header>
          <ServerUnifiedTelemetryChart
            latencyPoints={response.latency_points || []}
            regionalProbes={response.regional_latency_points || []}
            failedProbePoints={response.failed_probe_points || []}
            includeResources={false}
            windowHours={windowHours}
            windowEndAt={response.window.to}
            seriesEnabled={enabledSeries}
            onSeriesEnabledChange={setEnabledSeries}
            hideLegend
            bucketCount={bucketCount}
            chartHeight={220}
          />
        </section>
      </div>

      <section className="latency-anomalies" aria-label="异常摘要">
        <header className="latency-anomalies-head">
          <h3>异常摘要</h3>
          <div className="latency-percentile-row">
            <span>P50 <strong>{formatMS(percentiles.p50)}</strong></span>
            <span>P95 <strong>{formatMS(percentiles.p95)}</strong></span>
            <span>P99 <strong>{formatMS(percentiles.p99)}</strong></span>
            <span>样本 <strong>{overview.sampleCount}</strong></span>
          </div>
        </header>
        {anomalies.length ? (
          <ul className="latency-anomaly-list">
            {anomalies.map(item => (
              <li key={`${item.kind}-${item.key}`}>
                <strong>{item.kind === 'high_latency' ? '最高延迟' : '最高丢包'}</strong>
                <span>
                  {item.label}
                  {item.kind === 'high_latency' ? ` ${formatMS(item.latencyMS)}` : ` ${formatPercent(item.lossPercent)}`}
                  {item.detail ? `（${item.detail}）` : ''}
                </span>
                {formatAnomalyTime(item.at) && <time>{formatAnomalyTime(item.at)}</time>}
              </li>
            ))}
          </ul>
        ) : <p className="latency-anomaly-empty">当前窗口没有突出异常。</p>}
      </section>
    </div>
  )
}
