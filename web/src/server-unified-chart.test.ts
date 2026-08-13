import { describe, expect, it } from 'vitest'
import { alignUnifiedMetrics, computeMaxLatency, formatBucketTime } from './server-unified-chart'

describe('server-unified-chart helper', () => {
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
          kind: 'regional',
          province: '北京',
          carrier: '联通',
          checked_at: '2026-08-13T11:30:00Z',
          available: true,
          latency_ms: 48,
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
