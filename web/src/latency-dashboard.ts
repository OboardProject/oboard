import type { LatencyProbeTargetStat } from './connectivity-sla'
import type { UnifiedBucketPoint } from './server-unified-chart'

export const INCLUDE_PUBLIC_STATS_KEY = 'oboard.latency-overview.include-public'

export type LatencyOverviewStats = {
  avgMS: number | null
  jitterMS: number | null
  lossPercent: number | null
  successPercent: number | null
  sampleCount: number
  usedPublicFallback: boolean
}

export type LatencyAnomaly = {
  kind: 'high_latency' | 'high_loss'
  key: string
  label: string
  at: string | null
  latencyMS?: number | null
  lossPercent?: number | null
  detail: string
}

export function readIncludePublicStats(): boolean {
  try {
    return localStorage.getItem(INCLUDE_PUBLIC_STATS_KEY) === '1'
  } catch {
    return false
  }
}

export function writeIncludePublicStats(value: boolean) {
  try {
    localStorage.setItem(INCLUDE_PUBLIC_STATS_KEY, value ? '1' : '0')
  } catch {
    // Ignore quota or private-mode failures; the current session still uses the toggle.
  }
}

export function hasTaskProbeTargets(stats: LatencyProbeTargetStat[]): boolean {
  return stats.some(stat => stat.kind !== 'public')
}

export function shouldIncludePublicInOverview(stats: LatencyProbeTargetStat[], userEnabled: boolean): boolean {
  return userEnabled || !hasTaskProbeTargets(stats)
}

export function seriesIDForTarget(stat: Pick<LatencyProbeTargetStat, 'key' | 'kind'>): string {
  if (stat.kind === 'public' || stat.key === 'public') return 'public_latency'
  return `reg_${stat.key}`
}

export function probeMethodLabel(mode?: string): string {
  if (mode === 'icmp') return 'Ping'
  if (mode === 'http') return 'HTTP'
  if (mode === 'tcp') return 'TCP'
  return mode ? mode.toUpperCase() : '探测'
}

export function overviewSourceStats(stats: LatencyProbeTargetStat[], includePublic: boolean): LatencyProbeTargetStat[] {
  if (includePublic) return stats
  const tasks = stats.filter(stat => stat.kind !== 'public')
  return tasks.length > 0 ? tasks : stats
}

export function computeOverviewStats(stats: LatencyProbeTargetStat[], includePublic: boolean): LatencyOverviewStats {
  const rows = overviewSourceStats(stats, includePublic)
  const usedPublicFallback = !includePublic && rows.some(stat => stat.kind === 'public') && !rows.some(stat => stat.kind !== 'public')
  let weightedSum = 0
  let weight = 0
  let minMS: number | null = null
  let maxMS: number | null = null
  let sampleCount = 0
  let successCount = 0
  let reportCount = 0
  let availableCount = 0
  rows.forEach(stat => {
    const rowWeight = Number(stat.success_count || 0)
    if (stat.avg_ms != null && Number.isFinite(stat.avg_ms) && rowWeight > 0) {
      weightedSum += stat.avg_ms * rowWeight
      weight += rowWeight
    }
    if (stat.min_ms != null && Number.isFinite(stat.min_ms)) minMS = minMS == null ? stat.min_ms : Math.min(minMS, stat.min_ms)
    if (stat.max_ms != null && Number.isFinite(stat.max_ms)) maxMS = maxMS == null ? stat.max_ms : Math.max(maxMS, stat.max_ms)
    sampleCount += Number(stat.sample_count || 0)
    successCount += Number(stat.success_count || 0)
    reportCount += Number(stat.report_count || 0)
    availableCount += Number(stat.available_count || 0)
  })
  let lossPercent: number | null = null
  let successPercent: number | null = null
  if (sampleCount > 0) {
    successPercent = (successCount / sampleCount) * 100
    lossPercent = 100 - successPercent
  } else if (reportCount > 0) {
    successPercent = (availableCount / reportCount) * 100
    lossPercent = 100 - successPercent
  }
  return {
    avgMS: weight > 0 ? weightedSum / weight : null,
    jitterMS: minMS != null && maxMS != null ? maxMS - minMS : null,
    lossPercent,
    successPercent,
    sampleCount: sampleCount || reportCount,
    usedPublicFallback,
  }
}

export function computePercentiles(values: number[]): { p50: number | null; p95: number | null; p99: number | null } {
  const sorted = values.filter(value => Number.isFinite(value)).sort((left, right) => left - right)
  if (sorted.length === 0) return { p50: null, p95: null, p99: null }
  const at = (ratio: number) => {
    const index = Math.min(sorted.length - 1, Math.max(0, Math.ceil(ratio * sorted.length) - 1))
    return sorted[index]
  }
  return { p50: at(0.5), p95: at(0.95), p99: at(0.99) }
}

export function percentileValuesFromBuckets(buckets: UnifiedBucketPoint[], seriesIDs: string[]): number[] {
  const values: number[] = []
  buckets.forEach(bucket => {
    seriesIDs.forEach(id => {
      const value = bucket.values[id]
      if (value != null && Number.isFinite(value)) values.push(value)
    })
  })
  return values
}

export function rankWorstTargets(stats: LatencyProbeTargetStat[], limit = 10): LatencyProbeTargetStat[] {
  return [...stats].sort((left, right) => {
    const leftScore = Number(left.avg_ms ?? -1) + Number(left.loss_percent ?? 0) * 10
    const rightScore = Number(right.avg_ms ?? -1) + Number(right.loss_percent ?? 0) * 10
    return rightScore - leftScore
  }).slice(0, Math.max(1, limit))
}

export function pickAnomalies(stats: LatencyProbeTargetStat[], includePublic: boolean, p95: number | null): LatencyAnomaly[] {
  const rows = overviewSourceStats(stats, includePublic)
  const anomalies: LatencyAnomaly[] = []
  let highest = rows[0]
  rows.forEach(stat => {
    if ((stat.peak_latency_ms ?? stat.avg_ms ?? -1) > (highest?.peak_latency_ms ?? highest?.avg_ms ?? -1)) highest = stat
  })
  if (highest && (highest.peak_latency_ms != null || highest.avg_ms != null)) {
    const latency = highest.peak_latency_ms ?? highest.avg_ms
    const overP95 = p95 != null && latency != null && latency > p95
    anomalies.push({
      kind: 'high_latency',
      key: highest.key,
      label: highest.task_name || (highest.kind === 'public' ? '公网探测' : `${highest.province || ''} · ${highest.carrier || ''}`),
      at: highest.peak_latency_at || null,
      latencyMS: latency,
      detail: overP95 && p95 != null ? `高于 P95 ${Math.round(p95)}ms` : '窗口内最高延迟',
    })
  }
  let lossiest = rows[0]
  rows.forEach(stat => {
    if ((stat.peak_loss_percent ?? stat.loss_percent ?? -1) > (lossiest?.peak_loss_percent ?? lossiest?.loss_percent ?? -1)) lossiest = stat
  })
  if (lossiest && (lossiest.peak_loss_percent ?? lossiest.loss_percent ?? 0) > 0) {
    anomalies.push({
      kind: 'high_loss',
      key: lossiest.key,
      label: lossiest.task_name || (lossiest.kind === 'public' ? '公网探测' : `${lossiest.province || ''} · ${lossiest.carrier || ''}`),
      at: lossiest.peak_loss_at || null,
      lossPercent: lossiest.peak_loss_percent ?? lossiest.loss_percent,
      detail: '窗口内最高丢包',
    })
  }
  return anomalies
}

export function sparklineValues(buckets: UnifiedBucketPoint[], seriesID: string, limit = 16): Array<number | null> {
  const values = buckets.map(bucket => {
    const value = bucket.values[seriesID]
    return value != null && Number.isFinite(value) ? value : null
  })
  return values.slice(Math.max(0, values.length - limit))
}
