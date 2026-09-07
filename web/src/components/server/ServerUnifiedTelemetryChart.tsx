import React, { useEffect, useMemo, useRef, useState } from 'react'
import type { ConnectivityResponse } from '../../connectivity-sla'
import {
  alignFailedProbePoints,
  alignUnifiedMetrics,
  buildAreaPath,
  buildLinePath,
  computeMaxLatency,
  DEFAULT_CONNECT_GAPS,
  DEFAULT_SMOOTH_LINES,
  splitSeriesSegments,
  type LatencyProbeResultSample,
  type MetricSeries,
  type ServerLatencyPoint,
  type ServerResourcePoint,
} from '../../server-unified-chart'

function formatBytes(value: number) {
  if (!Number.isFinite(value) || value <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let size = value
  let unit = 0
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024
    unit += 1
  }
  return `${size.toFixed(size >= 10 || unit === 0 ? 0 : 1)} ${units[unit]}`
}

export function ServerUnifiedTelemetryChart({
  resourcePoints = [],
  latencyPoints = [],
  regionalProbes = [],
  failedProbePoints = [],
  includeResources = true,
  windowHours = 24,
  windowEndAt,
  seriesEnabled,
  onSeriesEnabledChange,
  hideLegend = false,
  bucketCount = 60,
  chartHeight = 160,
}: {
  resourcePoints?: ServerResourcePoint[]
  latencyPoints?: ServerLatencyPoint[]
  regionalProbes?: LatencyProbeResultSample[]
  failedProbePoints?: ConnectivityResponse['failed_probe_points']
  includeResources?: boolean
  windowHours?: number
  windowEndAt?: string
  seriesEnabled?: Record<string, boolean>
  onSeriesEnabledChange?: React.Dispatch<React.SetStateAction<Record<string, boolean>>>
  hideLegend?: boolean
  bucketCount?: number
  chartHeight?: number
}) {
  const { seriesList, buckets } = useMemo(() => {
    const responseEnd = windowEndAt ? new Date(windowEndAt).getTime() : Number.NaN
    return alignUnifiedMetrics({ resourcePoints, latencyPoints, regionalProbes, includeResources, windowHours, bucketCount, now: Number.isFinite(responseEnd) ? responseEnd : Date.now() })
  }, [resourcePoints, latencyPoints, regionalProbes, includeResources, windowHours, windowEndAt, bucketCount])

  const [internalEnabled, setInternalEnabled] = useState<Record<string, boolean>>({})
  const [connectGaps, setConnectGaps] = useState(DEFAULT_CONNECT_GAPS)
  const [smoothLines, setSmoothLines] = useState(DEFAULT_SMOOTH_LINES)
  const enabledSeries = seriesEnabled ?? internalEnabled
  const setEnabledSeries = onSeriesEnabledChange ?? setInternalEnabled

  useEffect(() => {
    setEnabledSeries(prev => {
      let changed = false
      const next = { ...prev }
      seriesList.forEach(series => {
        if (next[series.id] === undefined) {
          next[series.id] = true
          changed = true
        }
      })
      return changed ? next : prev
    })
  }, [seriesList, setEnabledSeries])

  const toggleSeries = (id: string) => {
    setEnabledSeries(prev => ({ ...prev, [id]: !prev[id] }))
  }

  const toggleAll = (enable: boolean) => {
    const next: Record<string, boolean> = {}
    seriesList.forEach(series => { next[series.id] = enable })
    setEnabledSeries(next)
  }

  const maxLatency = useMemo(() => computeMaxLatency(buckets, enabledSeries), [buckets, enabledSeries])
  const [hoveredIdx, setHoveredIdx] = useState<number | null>(null)
  const svgRef = useRef<SVGSVGElement | null>(null)
  const chartTitleID = React.useId()
  const chartDescriptionID = React.useId()
  const gradientPrefix = React.useId().replace(/:/g, '')
  const hasPercentageSeries = includeResources
  const activeSeries = seriesList.filter(series => enabledSeries[series.id] !== false)
  const W = 1000
  const H = chartHeight
  const padL = hasPercentageSeries ? 45 : 12
  const padR = 56
  const padT = 12
  const padB = H - 25
  const plotW = W - padL - padR
  const plotH = padB - padT
  const getX = (idx: number) => padL + (idx / Math.max(1, buckets.length - 1)) * plotW
  const getBucketStartX = (idx: number) => idx <= 0 ? padL : (getX(idx - 1) + getX(idx)) / 2
  const getBucketEndX = (idx: number) => idx >= buckets.length - 1 ? W - padR : (getX(idx) + getX(idx + 1)) / 2
  const windowEndMS = windowEndAt ? new Date(windowEndAt).getTime() : Date.now()
  const failedProbeBuckets = useMemo(() => alignFailedProbePoints({
    points: failedProbePoints,
    windowHours,
    bucketCount: buckets.length,
    now: Number.isFinite(windowEndMS) ? windowEndMS : Date.now(),
  }), [failedProbePoints, windowHours, buckets.length, windowEndMS])
  const failedProbeCountByBucket = useMemo(
    () => new Map(failedProbeBuckets.map(point => [point.index, point.count])),
    [failedProbeBuckets],
  )

  const getY = (val: number, series: MetricSeries) => {
    if (series.yAxis === 'left') {
      const clamped = Math.max(0, Math.min(100, val))
      return padB - (clamped / 100) * plotH
    }
    const clamped = Math.max(0, Math.min(maxLatency, val))
    return padB - (clamped / maxLatency) * plotH
  }

  const handlePointerMove = (event: React.PointerEvent<SVGSVGElement>) => {
    if (!svgRef.current || !buckets.length) return
    const rect = svgRef.current.getBoundingClientRect()
    const x = event.clientX - rect.left
    const fraction = Math.max(0, Math.min(1, (x - (padL / W) * rect.width) / ((plotW / W) * rect.width)))
    setHoveredIdx(Math.round(fraction * (buckets.length - 1)))
  }

  const hoveredBucket = hoveredIdx !== null ? buckets[hoveredIdx] : null
  const hoveredFailedProbeCount = hoveredIdx !== null ? failedProbeCountByBucket.get(hoveredIdx) || 0 : 0

  return (
    <div className="komari-chart-container">
      {!hideLegend && (
        <div className="komari-chart-header">
          <div className="komari-chart-legend">
            {seriesList.map(series => {
              const active = enabledSeries[series.id] !== false
              return (
                <button
                  key={series.id}
                  type="button"
                  className={`komari-legend-chip${active ? ' active' : ''}`}
                  aria-pressed={active}
                  onClick={() => toggleSeries(series.id)}
                  title={`点击切换 ${series.label} 显示`}
                >
                  <span className="komari-legend-dot" style={{ backgroundColor: series.color }} />
                  <span>{series.label}</span>
                </button>
              )
            })}
            <div className="komari-legend-actions">
              <button type="button" className="komari-legend-action-btn" onClick={() => toggleAll(true)}>全选</button>
              <button type="button" className="komari-legend-action-btn" onClick={() => toggleAll(false)}>清空</button>
            </div>
            <ChartDrawOptions connectGaps={connectGaps} smoothLines={smoothLines} onConnectGaps={setConnectGaps} onSmoothLines={setSmoothLines} />
          </div>
        </div>
      )}
      {hideLegend && (
        <div className="komari-chart-header">
          <ChartDrawOptions connectGaps={connectGaps} smoothLines={smoothLines} onConnectGaps={setConnectGaps} onSmoothLines={setSmoothLines} />
        </div>
      )}
      <div className="komari-chart-canvas-wrap">
        <svg
          ref={svgRef}
          className="komari-chart-svg"
          viewBox={`0 0 ${W} ${H}`}
          preserveAspectRatio="none"
          role="img"
          aria-labelledby={`${chartTitleID} ${chartDescriptionID}`}
          onPointerMove={handlePointerMove}
          onPointerLeave={() => setHoveredIdx(null)}
        >
          <title id={chartTitleID}>服务器监控趋势</title>
          <desc id={chartDescriptionID}>显示已选择的负载与延迟时间序列；红色异常区块表示该时间桶发生实际公网探测丢包，普通缺报不会标记为丢包。</desc>
          <defs>
            {activeSeries.map((series, index) => (
              <linearGradient key={series.id} id={`${gradientPrefix}-${index}`} x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%" stopColor={series.color} stopOpacity="0.2" />
                <stop offset="88%" stopColor={series.color} stopOpacity="0.035" />
                <stop offset="100%" stopColor={series.color} stopOpacity="0" />
              </linearGradient>
            ))}
          </defs>
          {[0, 0.25, 0.5, 0.75, 1].map((pct, index) => {
            const y = padB - pct * plotH
            return (
              <g key={index}>
                <line x1={padL} y1={y} x2={W - padR} y2={y} className="komari-chart-grid-line" strokeDasharray="3 3" />
                {hasPercentageSeries && (
                  <text x={padL - 6} y={y + 3} textAnchor="end" className="komari-chart-axis-text">{Math.round(pct * 100)}%</text>
                )}
                <text x={W - padR + 6} y={y + 3} textAnchor="start" className="komari-chart-axis-text">{Math.round(pct * maxLatency)} ms</text>
              </g>
            )
          })}
          <g className="komari-loss-bands" aria-hidden="true">
            {failedProbeBuckets.map(point => {
              const startX = getBucketStartX(point.index)
              return (
                <rect
                  key={point.index}
                  x={startX}
                  y={padT}
                  width={Math.max(1, getBucketEndX(point.index) - startX)}
                  height={plotH}
                  rx="1.5"
                  fill="var(--danger, #ef4444)"
                  fillOpacity="0.14"
                  pointerEvents="none"
                />
              )
            })}
          </g>
          {activeSeries.map((series, seriesIndex) => {
            const segments = splitSeriesSegments(buckets, series.id, connectGaps)
            if (segments.length === 0) return null
            return (
              <g key={series.id}>
                {segments.map((segment, segmentIndex) => {
                  const points = segment.map(point => ({ x: getX(point.index), y: getY(point.value, series) }))
                  const linePath = buildLinePath(points, smoothLines)
                  const areaPath = buildAreaPath(points, padB, smoothLines)
                  const singlePoint = points.length === 1 ? points[0] : null
                  return (
                    <React.Fragment key={segmentIndex}>
                      {singlePoint ? (
                        <rect
                          x={getBucketStartX(segment[0].index)}
                          y={singlePoint.y}
                          width={Math.max(1, getBucketEndX(segment[0].index) - getBucketStartX(segment[0].index))}
                          height={Math.max(0, padB - singlePoint.y)}
                          fill={`url(#${gradientPrefix}-${seriesIndex})`}
                          className="komari-chart-area"
                        />
                      ) : areaPath ? (
                        <path d={areaPath} fill={`url(#${gradientPrefix}-${seriesIndex})`} className="komari-chart-area" />
                      ) : null}
                      {singlePoint ? (
                        <circle cx={singlePoint.x} cy={singlePoint.y} r="3" fill={series.color} stroke="#ffffff" strokeWidth="1.5" vectorEffect="non-scaling-stroke" />
                      ) : (
                        <path
                          d={linePath}
                          fill="none"
                          stroke={series.color}
                          strokeWidth="2.2"
                          strokeLinecap="round"
                          strokeLinejoin="round"
                          className="komari-chart-polyline"
                          vectorEffect="non-scaling-stroke"
                        />
                      )}
                    </React.Fragment>
                  )
                })}
              </g>
            )
          })}
          {hoveredIdx !== null && (
            <g>
              <line x1={getX(hoveredIdx)} y1={padT} x2={getX(hoveredIdx)} y2={padB} className="komari-crosshair" strokeDasharray="2 2" />
              {activeSeries.map(series => {
                const val = buckets[hoveredIdx]?.values[series.id]
                if (val == null) return null
                return (
                  <circle
                    key={series.id}
                    cx={getX(hoveredIdx)}
                    cy={getY(val, series)}
                    r="4.5"
                    fill={series.color}
                    stroke="#ffffff"
                    strokeWidth="2"
                    className="server-monitor-dot-glow"
                  />
                )
              })}
            </g>
          )}
          {buckets.length > 0 && (
            <g>
              <text x={padL} y={H - 6} textAnchor="start" className="komari-chart-axis-text">{buckets[0].timeLabel}</text>
              <text x={padL + plotW / 2} y={H - 6} textAnchor="middle" className="komari-chart-axis-text">{buckets[Math.floor(buckets.length / 2)].timeLabel}</text>
              <text x={W - padR} y={H - 6} textAnchor="end" className="komari-chart-axis-text">{buckets[buckets.length - 1].timeLabel}</text>
            </g>
          )}
        </svg>
        {hoveredBucket && (() => {
          const crosshairPct = (getX(hoveredIdx!) / W) * 100
          const isRightSide = crosshairPct > 50
          return (
            <div
              className={`komari-tooltip-popover ${isRightSide ? 'place-left' : 'place-right'}`}
              style={{
                left: isRightSide ? `calc(${crosshairPct}% - 14px)` : `calc(${crosshairPct}% + 14px)`,
                top: '8px',
              }}
            >
              <div className="komari-tooltip-time">{hoveredBucket.timeLabel}</div>
              <div className="komari-tooltip-list">
                {hoveredFailedProbeCount > 0 && (
                  <div className="komari-tooltip-row">
                    <span className="komari-tooltip-label">
                      <span className="komari-legend-dot" style={{ backgroundColor: 'var(--danger, #ef4444)' }} />
                      公网探测
                    </span>
                    <span className="komari-tooltip-val">丢包{hoveredFailedProbeCount > 1 ? ` × ${hoveredFailedProbeCount}` : ''}</span>
                  </div>
                )}
                {activeSeries.map(series => {
                  const val = hoveredBucket.values[series.id]
                  if (val == null) return null
                  let formattedVal = series.unit === '%' ? `${val.toFixed(1)}%` : `${Math.round(val)} ms`
                  if (series.id === 'memory' && hoveredBucket.memoryUsedBytes && hoveredBucket.memoryTotalBytes) {
                    formattedVal = `${val.toFixed(1)}% (${formatBytes(hoveredBucket.memoryUsedBytes)} / ${formatBytes(hoveredBucket.memoryTotalBytes)})`
                  }
                  return (
                    <div key={series.id} className="komari-tooltip-row">
                      <span className="komari-tooltip-label">
                        <span className="komari-legend-dot" style={{ backgroundColor: series.color }} />
                        {series.label}
                      </span>
                      <span className="komari-tooltip-val">{formattedVal}</span>
                    </div>
                  )
                })}
              </div>
            </div>
          )
        })()}
      </div>
    </div>
  )
}

function ChartDrawOptions({
  connectGaps,
  smoothLines,
  onConnectGaps,
  onSmoothLines,
}: {
  connectGaps: boolean
  smoothLines: boolean
  onConnectGaps: (value: boolean | ((current: boolean) => boolean)) => void
  onSmoothLines: (value: boolean | ((current: boolean) => boolean)) => void
}) {
  return (
    <div className="komari-chart-options" aria-label="延迟图绘制选项">
      <button
        type="button"
        className={`komari-chart-option${connectGaps ? ' active' : ''}`}
        aria-pressed={connectGaps}
        title="连接缺失时间桶两侧的有效延迟点；阴影始终保留"
        onClick={() => onConnectGaps(value => !value)}
      >断点连接</button>
      <button
        type="button"
        className={`komari-chart-option${smoothLines ? ' active' : ''}`}
        aria-pressed={smoothLines}
        title="使用平滑曲线显示延迟趋势"
        onClick={() => onSmoothLines(value => !value)}
      >平滑</button>
    </div>
  )
}
