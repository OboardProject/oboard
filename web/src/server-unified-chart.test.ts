import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { alignFailedProbePoints, alignUnifiedMetrics, buildAreaPath, buildLinePath, computeMaxLatency, DEFAULT_CONNECT_GAPS, DEFAULT_SMOOTH_LINES, DEFAULT_CLIP_SPIKES, suppressLatencySpikes, formatBucketTime, splitSeriesSegments } from './server-unified-chart'

const monitorSource = readFileSync(new URL('./components/server/ServerUnifiedTelemetryChart.tsx', import.meta.url), 'utf8')
const monitorStyles = readFileSync(new URL('./style.css', import.meta.url), 'utf8')

describe('server-unified-chart helper', () => {
  it('uses smooth curves and gap connection by default with optional spike suppression', () => {
    expect(DEFAULT_CONNECT_GAPS).toBe(true)
    expect(DEFAULT_SMOOTH_LINES).toBe(true)
    expect(DEFAULT_CLIP_SPIKES).toBe(false)
  })

  it('renders exactly two native pressed-state controls with compact spring feedback', () => {
    expect(monitorSource.match(/className={`komari-chart-option/g)).toHaveLength(2)
    expect(monitorSource).toContain('aria-pressed={connectGaps}')
    expect(monitorSource).toContain('aria-pressed={clipSpikes}')
    expect(monitorSource).toContain('>断点连接</button>')
    expect(monitorSource).toContain('>削峰</button>')
    expect(monitorStyles).toContain('cubic-bezier(0.175, 0.885, 0.32, 1.5)')
    expect(monitorStyles).toContain('transform: scale(0.97) translateY(1px)')
  })

  it('keeps each enabled series shadow visible independently of connect-gaps', () => {
    expect(monitorSource).toContain('stopColor={series.color}')
    expect(monitorSource).toContain('const areaPath = buildAreaPath(points, padB, DEFAULT_SMOOTH_LINES)')
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
    expect(result.seriesList.find(series => series.id === 'public_latency')?.label).toBe('公网探测')
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

  it('includes custom probe tasks that have no province or carrier', () => {
    const now = new Date('2026-09-07T12:00:00Z').getTime()
    const result = alignUnifiedMetrics({
      regionalProbes: [{
        kind: 'custom',
        task_id: 8,
        task_name: '网站',
        checked_at: '2026-09-07T11:30:00Z',
        available: true,
        latency_ms: 18,
      }],
      includeResources: false,
      windowHours: 1,
      bucketCount: 6,
      now,
    })
    expect(result.seriesList.map(series => series.id)).toEqual(['public_latency', 'reg_task_8'])
    expect(result.seriesList[1].label).toBe('网站')
    const populated = result.buckets.find(bucket => bucket.values.reg_task_8 != null)
    expect(populated?.values.reg_task_8).toBe(18)
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


describe('optional latency spike suppression', () => {
  const series = [{ id: 'latency', label: 'latency', unit: 'ms', yAxis: 'right', color: 'red' }, { id: 'cpu', label: 'cpu', unit: '%', yAxis: 'left', color: 'blue' }] as const
  const filter = (values: Array<number | null>) => {
    const buckets = values.map((value, timestamp) => ({ timestamp, timeLabel: '', values: { latency: value, cpu: value } }))
    const before = structuredClone(buckets)
    const output = suppressLatencySpikes(buckets, [...series])
    expect(buckets).toEqual(before)
    expect(output.map(bucket => bucket.values.cpu)).toEqual(values)
    return output.map(bucket => bucket.values.latency)
  }
  it('removes isolated and two-point high spikes without changing input or resource data', () => {
    expect(filter([20, 20, 200, 20, 20])).toEqual([20, 20, 20, 20, 20])
    expect(filter([20, 20, 200, 220, 20, 20])).toEqual([20, 20, 20, 20, 20, 20])
  })
  it('preserves sustained plateaus, steps and ordinary variation', () => {
    for (const values of [[20,20,200,200,200,20,20], [20,20,200,200,200,200], [20,22,25,21,20]]) expect(filter(values)).toEqual(values)
  })
  it('does not cross missing samples or guess at window boundaries', () => {
    for (const values of [[20,null,200,20,20], [20,20,200,null,20], [200,20,20,20,200]]) expect(filter(values)).toEqual(values)
  })
  it('does not manufacture low spikes when drawing the default smooth curve', () => {
    const path = buildLinePath([{ x: 0, y: 10 }, { x: 1, y: 0 }, { x: 2, y: 0 }, { x: 3, y: 10 }], DEFAULT_SMOOTH_LINES)
    const coordinates = path.match(/-?\d+\.\d+,-?\d+\.\d+/g) || []
    for (const coordinate of coordinates) {
      const y = Number(coordinate.split(',')[1])
      expect(y).toBeGreaterThanOrEqual(0)
      expect(y).toBeLessThanOrEqual(10)
    }
  })
})


it('retains every returned point at fine precision and only pairs them at standard precision', () => {
  const now = Date.parse('2026-09-12T00:00:00Z')
  const start = now - 86400000
  const latencyPoints = Array.from({ length: 360 }, (_, index) => ({ at: new Date(start + (index + 0.5) * 240000).toISOString(), avg_ms: index + 1, count: 1 }))
  const fine = alignUnifiedMetrics({ latencyPoints, includeResources: false, now, bucketCount: 360 })
  const standard = alignUnifiedMetrics({ latencyPoints, includeResources: false, now, bucketCount: 180 })
  expect(fine.buckets.map(bucket => bucket.values.public_latency)).toEqual(latencyPoints.map(point => point.avg_ms))
  expect(standard.buckets).toHaveLength(180)
  expect(standard.buckets[0].values.public_latency).toBe(1.5)
  expect(standard.buckets[179].values.public_latency).toBe(359.5)
})
