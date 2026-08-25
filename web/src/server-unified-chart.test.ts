import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { alignFailedProbePoints, alignUnifiedMetrics, buildAreaPath, buildLinePath, computeMaxLatency, DEFAULT_CONNECT_GAPS, DEFAULT_SMOOTH_LINES, formatBucketTime, splitSeriesSegments } from './server-unified-chart'

const monitorSource = readFileSync(new URL('./main.tsx', import.meta.url), 'utf8')
const monitorStyles = readFileSync(new URL('./style.css', import.meta.url), 'utf8')

describe('server-unified-chart helper', () => {
  it('uses connect-gaps on and smoothing off as the dialog defaults', () => {
    expect(DEFAULT_CONNECT_GAPS).toBe(true)
    expect(DEFAULT_SMOOTH_LINES).toBe(false)
  })

  it('renders exactly two native pressed-state controls with compact spring feedback', () => {
    expect(monitorSource.match(/className={`komari-chart-option/g)).toHaveLength(2)
    expect(monitorSource).toContain('aria-pressed={connectGaps}')
    expect(monitorSource).toContain('aria-pressed={smoothLines}')
    expect(monitorSource).toContain('>断点连接</button>')
    expect(monitorSource).toContain('>平滑</button>')
    expect(monitorStyles).toContain('cubic-bezier(0.175, 0.885, 0.32, 1.5)')
    expect(monitorStyles).toContain('transform: scale(0.97) translateY(1px)')
  })

  it('keeps each enabled series shadow visible independently of connect-gaps', () => {
    expect(monitorSource).toContain('stopColor={series.color}')
    expect(monitorSource).toContain('const areaPath = buildAreaPath(points, padB, smoothLines)')
    expect(monitorSource).not.toContain('connectGaps ? buildAreaPath')
    expect(monitorSource).toContain('const singlePoint = points.length === 1 ? points[0] : null')
    expect(monitorSource).toContain('fill={`url(#${gradientPrefix}-${seriesIndex})`}')
  })

  it('renders failed probe buckets as anomaly-colored time bands instead of vertical markers', () => {
    expect(monitorSource).toContain('className="komari-loss-bands"')
    expect(monitorSource).toContain('fill="var(--danger, #ef4444)"')
    expect(monitorSource).toContain('红色异常区块表示该时间桶发生实际公网探测丢包')
    expect(monitorSource).toContain('丢包{hoveredFailedProbeCount > 1')
    expect(monitorSource).not.toContain('className="komari-loss-marker"')
  })

  it('formats bucket time correctly', () => {
    const ts = new Date('2026-08-13T12:34:00Z').getTime()
    expect(formatBucketTime(ts)).toMatch(/\d{2}-\d{2} \d{2}:\d{2}/)
  })

  it('aligns CPU, Memory, Public Latency, and Regional Latency probe data into buckets', () => {
    const now = new Date('2026-08-13T12:00:00Z').getTime()
    const result = alignUnifiedMetrics({
      resourcePoints: [
        {
          sampled_at: '2026-08-13T11:30:00Z',
          cpu_usage_percent: 15.5,
          memory_used_bytes: 512 * 1024 * 1024,
          memory_total_bytes: 1024 * 1024 * 1024,
        },
      ],
      latencyPoints: [
        {
          bucket_at: '2026-08-13T11:30:00Z',
          avg_ms: 22,
        },
      ],
      regionalProbes: [
        {
          kind: 'regional',
          province: '广东',
          carrier: '电信',
          checked_at: '2026-08-13T11:30:00Z',
          available: true,
          latency_ms: 35,
        },
        {
          province: '北京',
          carrier: '联通',
          at: '2026-08-13T11:30:00Z',
          avg_ms: 48,
        },
      ],
      windowHours: 24,
      bucketCount: 60,
      now,
    })

    expect(result.seriesList.map(s => s.id)).toEqual([
      'cpu',
      'memory',
      'public_latency',
      'reg_北京 · 联通',
      'reg_广东 · 电信',
    ])
    expect(result.buckets).toHaveLength(60)

    // Find bucket with populated data
    const nonEmp = result.buckets.find(b => Object.keys(b.values).length > 0)
    expect(nonEmp).toBeDefined()
    expect(nonEmp?.values.cpu).toBeCloseTo(15.5)
    expect(nonEmp?.values.memory).toBeCloseTo(50)
    expect(nonEmp?.values.public_latency).toBe(22)
    expect(nonEmp?.values['reg_广东 · 电信']).toBe(35)
    expect(nonEmp?.values['reg_北京 · 联通']).toBe(48)
  })

  it('splits a series wherever a bucket has no value', () => {
    const segments = splitSeriesSegments([
      { timestamp: 1, timeLabel: 't1', values: { public_latency: 20 } },
      { timestamp: 2, timeLabel: 't2', values: { public_latency: null } },
      { timestamp: 3, timeLabel: 't3', values: { public_latency: 30 } },
    ], 'public_latency')

    expect(segments).toEqual([
      [{ index: 0, value: 20 }],
      [{ index: 2, value: 30 }],
    ])
  })

  it('connects finite points across empty buckets when requested', () => {
    const buckets = [
      { timestamp: 1, timeLabel: 't1', values: { public_latency: 20 } },
      { timestamp: 2, timeLabel: 't2', values: { public_latency: null } },
      { timestamp: 3, timeLabel: 't3', values: { public_latency: 30 } },
    ]

    expect(splitSeriesSegments(buckets, 'public_latency', true)).toEqual([[
      { index: 0, value: 20 },
      { index: 2, value: 30 },
    ]])
  })

  it('uses linear paths by default and only emits curves when smoothing is enabled', () => {
    const points = [{ x: 0, y: 10 }, { x: 10, y: 5 }, { x: 20, y: 12 }]
    expect(buildLinePath(points, false)).toBe('M 0.0,10.0 L 10.0,5.0 L 20.0,12.0')
    expect(buildLinePath(points, true)).toContain(' C ')
    expect(buildAreaPath(points, 20, false)).toBe('M 0.0,10.0 L 10.0,5.0 L 20.0,12.0 L 20.0,20.0 L 0.0,20.0 Z')
  })

  it('aligns failed probes to the same first, middle, and last chart bucket indexes', () => {
    const now = new Date('2026-08-13T12:00:00Z').getTime()
    const start = now - 60 * 60 * 1000
    expect(alignFailedProbePoints({
      points: [
        { at: new Date(start).toISOString(), count: 1 },
        { at: new Date(start + 30 * 60 * 1000).toISOString(), count: 2 },
        { at: new Date(now - 1).toISOString(), count: 1 },
      ],
      windowHours: 1,
      bucketCount: 60,
      now,
    })).toEqual([
      { index: 0, count: 1 },
      { index: 30, count: 2 },
      { index: 59, count: 1 },
    ])
  })

  it('keeps every aggregate when multiple latency points share a chart bucket', () => {
    const now = new Date('2026-08-13T12:00:00Z').getTime()
    const result = alignUnifiedMetrics({
      latencyPoints: [
        { at: '2026-08-13T11:10:00Z', avg_ms: 10, count: 1 },
        { at: '2026-08-13T11:40:00Z', avg_ms: 30, count: 3 },
      ],
      includeResources: false,
      windowHours: 1,
      bucketCount: 1,
      now,
    })

    expect(result.buckets[0].values.public_latency).toBe(25)
  })

  it('computes max latency dynamically for scaling right Y-axis', () => {
    const buckets = [
      { timestamp: 1, timeLabel: 't1', values: { cpu: 20, public_latency: 50, 'reg_广东 · 电信': 120 } },
      { timestamp: 2, timeLabel: 't2', values: { cpu: 40, public_latency: 80, 'reg_广东 · 电信': 200 } },
    ]
    const enabled = { cpu: true, public_latency: true, 'reg_广东 · 电信': true }
    const maxLat = computeMaxLatency(buckets, enabled)
    expect(maxLat).toBeGreaterThanOrEqual(230)
  })
})
