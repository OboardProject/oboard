import { afterEach, describe, expect, it } from 'vitest'
import type { LatencyProbeTargetStat } from './connectivity-sla'
import {
  computeOverviewStats,
  computePercentiles,
  INCLUDE_PUBLIC_STATS_KEY,
  pickAnomalies,
  readIncludePublicStats,
  seriesIDForTarget,
  shouldIncludePublicInOverview,
  writeIncludePublicStats,
} from './latency-dashboard'

const publicTarget: LatencyProbeTargetStat = {
  key: 'public',
  kind: 'public',
  task_name: '公网探测',
  avg_ms: 20,
  min_ms: 10,
  max_ms: 40,
  jitter_ms: 30,
  sample_count: 10,
  success_count: 10,
  report_count: 10,
  available_count: 10,
  loss_percent: 0,
  success_percent: 100,
}

const taskTarget: LatencyProbeTargetStat = {
  key: 'task_8',
  kind: 'custom',
  task_id: 8,
  task_name: '网站',
  avg_ms: 100,
  min_ms: 80,
  max_ms: 140,
  jitter_ms: 60,
  sample_count: 8,
  success_count: 8,
  report_count: 8,
  available_count: 8,
  loss_percent: 0,
  success_percent: 100,
  peak_latency_ms: 140,
  peak_latency_at: '2026-09-07T10:00:00Z',
}

const memoryStorage = (() => {
  const values = new Map<string, string>()
  return {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => { values.set(key, value) },
    removeItem: (key: string) => { values.delete(key) },
  }
})()

Object.defineProperty(globalThis, 'localStorage', { value: memoryStorage, configurable: true })

afterEach(() => {
  memoryStorage.removeItem(INCLUDE_PUBLIC_STATS_KEY)
})

describe('latency dashboard overview', () => {
  it('excludes public probes from overview when task results exist', () => {
    expect(shouldIncludePublicInOverview([publicTarget, taskTarget], false)).toBe(false)
    const overview = computeOverviewStats([publicTarget, taskTarget], false)
    expect(overview.avgMS).toBe(100)
    expect(overview.jitterMS).toBe(60)
    expect(overview.usedPublicFallback).toBe(false)
  })

  it('falls back to public probes when a server only has public results', () => {
    expect(shouldIncludePublicInOverview([publicTarget], false)).toBe(true)
    const overview = computeOverviewStats([publicTarget], false)
    expect(overview.avgMS).toBe(20)
    expect(overview.usedPublicFallback).toBe(true)
  })

  it('includes public probes after the operator enables and persists the toggle', () => {
    writeIncludePublicStats(true)
    expect(readIncludePublicStats()).toBe(true)
    expect(shouldIncludePublicInOverview([publicTarget, taskTarget], true)).toBe(true)
    const overview = computeOverviewStats([publicTarget, taskTarget], true)
    expect(overview.avgMS).toBeCloseTo((20 * 10 + 100 * 8) / 18)
    expect(overview.jitterMS).toBe(130)
  })

  it('maps public and task stats onto the existing chart series ids', () => {
    expect(seriesIDForTarget(publicTarget)).toBe('public_latency')
    expect(seriesIDForTarget(taskTarget)).toBe('reg_task_8')
  })

  it('computes percentiles and names the highest-latency anomaly', () => {
    expect(computePercentiles([10, 20, 30, 40, 50])).toEqual({ p50: 30, p95: 50, p99: 50 })
    const anomalies = pickAnomalies([publicTarget, taskTarget], false, 90)
    expect(anomalies[0]).toMatchObject({ kind: 'high_latency', label: '网站', latencyMS: 140 })
    expect(anomalies[0].detail).toContain('P95')
  })
})
