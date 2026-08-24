export interface MetricSeries {
  id: string
  label: string
  color: string
  unit: '%' | 'ms'
  yAxis: 'left' | 'right'
}

export interface LatencyProbeResultSample {
  probe_id?: number | string
  server_id?: number
  kind?: 'public' | 'regional'
  province?: string
  carrier?: string
  host?: string
  port?: number
  mode?: 'tcp' | 'icmp'
  available?: boolean
  latency_ms?: number
  p95_latency_ms?: number
  jitter_ms?: number
  success_count?: number
  sample_count?: number
  error?: string
  checked_at?: string
  at?: string
  avg_ms?: number | null
  count?: number
}

export interface ServerResourcePoint {
  sampled_at: string
  cpu_usage_percent: number
  memory_used_bytes: number
  memory_total_bytes: number
  disk_used_bytes?: number
  disk_total_bytes?: number
  tcp_connection_count?: number
  udp_connection_count?: number
  process_count?: number
  network_upload_bps?: number
  network_download_bps?: number
}

export interface ServerLatencyPoint {
  bucket_at?: string
  at?: string
  avg_ms: number | null
  count?: number
}

export interface UnifiedBucketPoint {
  timestamp: number
  timeLabel: string
  values: Record<string, number | null>
  memoryUsedBytes?: number
  memoryTotalBytes?: number
}

export interface SeriesSegmentPoint {
  index: number
  value: number
}

export interface FailedProbePoint {
  at: string
  count: number
}

export const DEFAULT_CONNECT_GAPS = true
export const DEFAULT_SMOOTH_LINES = false

export const REGIONAL_SERIES_COLORS = [
  '#8b5cf6', // purple
  '#ec4899', // pink
  '#06b6d4', // cyan
  '#f43f5e', // rose
  '#14b8a6', // teal
  '#a855f7', // violet
  '#38bdf8', // sky
  '#fb923c', // orange
  '#4ade80', // green
]

export function formatBucketTime(ts: number): string {
  const d = new Date(ts)
  const month = String(d.getMonth() + 1).padStart(2, '0')
  const day = String(d.getDate()).padStart(2, '0')
  const hours = String(d.getHours()).padStart(2, '0')
  const mins = String(d.getMinutes()).padStart(2, '0')
  return `${month}-${day} ${hours}:${mins}`
}

export function splitSeriesSegments(buckets: UnifiedBucketPoint[], seriesID: string, connectGaps = false): SeriesSegmentPoint[][] {
  if (connectGaps) {
    const connected = buckets.flatMap((bucket, index) => {
      const value = bucket.values[seriesID]
      return value == null || !Number.isFinite(value) ? [] : [{ index, value }]
    })
    return connected.length > 0 ? [connected] : []
  }

  const segments: SeriesSegmentPoint[][] = []
  let current: SeriesSegmentPoint[] = []

  buckets.forEach((bucket, index) => {
    const value = bucket.values[seriesID]
    if (value == null || !Number.isFinite(value)) {
      if (current.length > 0) segments.push(current)
      current = []
      return
    }
    current.push({ index, value })
  })

  if (current.length > 0) segments.push(current)
  return segments
}

export function buildLinearPath(points: { x: number; y: number }[]): string {
  if (points.length === 0) return ''
  return points.map((point, index) => `${index === 0 ? 'M' : 'L'} ${point.x.toFixed(1)},${point.y.toFixed(1)}`).join(' ')
}

export function buildSmoothPath(points: { x: number; y: number }[]): string {
  if (points.length < 3) return buildLinearPath(points)

  let path = `M ${points[0].x.toFixed(1)},${points[0].y.toFixed(1)}`
  for (let index = 0; index < points.length - 1; index += 1) {
    const previous = index > 0 ? points[index - 1] : points[index]
    const current = points[index]
    const next = points[index + 1]
    const following = index < points.length - 2 ? points[index + 2] : next
    const control1X = current.x + (next.x - previous.x) / 6
    const control1Y = current.y + (next.y - previous.y) / 6
    const control2X = next.x - (following.x - current.x) / 6
    const control2Y = next.y - (following.y - current.y) / 6
    path += ` C ${control1X.toFixed(1)},${control1Y.toFixed(1)} ${control2X.toFixed(1)},${control2Y.toFixed(1)} ${next.x.toFixed(1)},${next.y.toFixed(1)}`
  }
  return path
}

export function buildLinePath(points: { x: number; y: number }[], smooth: boolean): string {
  return smooth ? buildSmoothPath(points) : buildLinearPath(points)
}

export function buildAreaPath(points: { x: number; y: number }[], baselineY: number, smooth: boolean): string {
  if (points.length < 2) return ''
  const linePath = buildLinePath(points, smooth)
  const first = points[0]
  const last = points[points.length - 1]
  return `${linePath} L ${last.x.toFixed(1)},${baselineY.toFixed(1)} L ${first.x.toFixed(1)},${baselineY.toFixed(1)} Z`
}

export function alignUnifiedMetrics({
  resourcePoints = [],
  latencyPoints = [],
  regionalProbes = [],
  includeResources = true,
  windowHours = 24,
  bucketCount = 60,
  now = Date.now(),
}: {
  resourcePoints?: ServerResourcePoint[]
  latencyPoints?: ServerLatencyPoint[]
  regionalProbes?: LatencyProbeResultSample[]
  includeResources?: boolean
  windowHours?: number
  bucketCount?: number
  now?: number
}): {
  seriesList: MetricSeries[]
  buckets: UnifiedBucketPoint[]
} {
  const windowMs = Math.max(1, windowHours) * 3600 * 1000
  const startTime = now - windowMs
  const bucketDuration = windowMs / bucketCount

  // 1. Discover all regional probe targets
  const regionalTargets = new Map<string, { province: string; carrier: string }>()
  regionalProbes.forEach(probe => {
    if ((probe.kind === 'regional' || probe.at) && probe.province && probe.carrier) {
      const key = `${probe.province} · ${probe.carrier}`
      if (!regionalTargets.has(key)) {
        regionalTargets.set(key, { province: probe.province, carrier: probe.carrier })
      }
    }
  })

  // 2. Build series list
  const seriesList: MetricSeries[] = includeResources ? [
    { id: 'cpu', label: 'CPU 使用率', color: '#3b82f6', unit: '%', yAxis: 'left' },
    { id: 'memory', label: '内存使用率', color: '#10b981', unit: '%', yAxis: 'left' },
    { id: 'public_latency', label: '公网延迟', color: '#f59e0b', unit: 'ms', yAxis: 'right' },
  ] : [
    { id: 'public_latency', label: '公网延迟', color: '#f59e0b', unit: 'ms', yAxis: 'right' },
  ]

  let colorIdx = 0
  const regTargetKeys = Array.from(regionalTargets.keys()).sort((a, b) => a.localeCompare(b, 'zh-CN'))
  regTargetKeys.forEach(targetKey => {
    seriesList.push({
      id: `reg_${targetKey}`,
      label: targetKey,
      color: REGIONAL_SERIES_COLORS[colorIdx % REGIONAL_SERIES_COLORS.length],
      unit: 'ms',
      yAxis: 'right',
    })
    colorIdx += 1
  })

  // 3. Initialize bucket array
  const buckets: UnifiedBucketPoint[] = []
  for (let i = 0; i < bucketCount; i += 1) {
    const bucketStart = startTime + i * bucketDuration
    const centerTs = bucketStart + bucketDuration / 2
    buckets.push({
      timestamp: centerTs,
      timeLabel: formatBucketTime(centerTs),
      values: {},
    })
  }

  const getBucketIndex = (tsMs: number) => {
    if (Number.isNaN(tsMs) || tsMs < startTime || tsMs > now) return -1
    const idx = Math.floor((tsMs - startTime) / bucketDuration)
    return Math.max(0, Math.min(bucketCount - 1, idx))
  }

  const latencyTotals = new Map<string, { bucketIndex: number; seriesID: string; weightedSum: number; weight: number }>()
  const addLatency = (bucketIndex: number, seriesID: string, value: number, weight: number) => {
    if (bucketIndex < 0 || !Number.isFinite(value)) return
    const normalizedWeight = Number.isFinite(weight) && weight > 0 ? weight : 1
    const key = `${bucketIndex}:${seriesID}`
    const total = latencyTotals.get(key) || { bucketIndex, seriesID, weightedSum: 0, weight: 0 }
    total.weightedSum += value * normalizedWeight
    total.weight += normalizedWeight
    latencyTotals.set(key, total)
  }

  // 4. Map resourcePoints (CPU & Memory)
  if (includeResources) resourcePoints.forEach(pt => {
    const ts = new Date(pt.sampled_at).getTime()
    const idx = getBucketIndex(ts)
    if (idx >= 0) {
      const bucket = buckets[idx]
      bucket.values.cpu = Number(pt.cpu_usage_percent || 0)
      if (pt.memory_total_bytes > 0) {
        bucket.values.memory = (Number(pt.memory_used_bytes || 0) / Number(pt.memory_total_bytes)) * 100
        bucket.memoryUsedBytes = Number(pt.memory_used_bytes)
        bucket.memoryTotalBytes = Number(pt.memory_total_bytes)
      }
    }
  })

  // 5. Map latencyPoints (Public Latency)
  latencyPoints.forEach(pt => {
    const timeStr = pt.at || pt.bucket_at
    if (!timeStr) return
    const ts = new Date(timeStr).getTime()
    const idx = getBucketIndex(ts)
    if (idx >= 0 && pt.avg_ms != null) {
      addLatency(idx, 'public_latency', Number(pt.avg_ms), Number(pt.count || 1))
    }
  })

  // 6. Map regionalProbes
  regionalProbes.forEach(probe => {
    if (!probe.province || !probe.carrier) return

    const isAggregated = Boolean(probe.at)
    const timeString = isAggregated ? probe.at : probe.checked_at
    const rawValue = isAggregated ? probe.avg_ms : probe.latency_ms
    if (!timeString || rawValue == null) return
    if (!isAggregated && (probe.kind !== 'regional' || !probe.available)) return

    const value = Number(rawValue)
    if (!Number.isFinite(value)) return
    const idx = getBucketIndex(new Date(timeString).getTime())
    if (idx >= 0) {
      const seriesId = `reg_${probe.province} · ${probe.carrier}`
      addLatency(idx, seriesId, value, Number(probe.count || 1))
    }
  })

  latencyTotals.forEach(total => {
    buckets[total.bucketIndex].values[total.seriesID] = total.weightedSum / total.weight
  })

  return { seriesList, buckets }
}

export function alignFailedProbePoints({
  points,
  windowHours,
  bucketCount,
  now,
}: {
  points: FailedProbePoint[]
  windowHours: number
  bucketCount: number
  now: number
}): { index: number; count: number }[] {
  if (bucketCount <= 0) return []
  const windowMs = Math.max(1, windowHours) * 60 * 60 * 1000
  const start = now - windowMs
  const bucketDuration = windowMs / bucketCount
  const counts = new Map<number, number>()
  points.forEach(point => {
    const timestamp = new Date(point.at).getTime()
    if (!Number.isFinite(timestamp) || timestamp < start || timestamp > now) return
    const index = Math.max(0, Math.min(bucketCount - 1, Math.floor((timestamp - start) / bucketDuration)))
    counts.set(index, (counts.get(index) || 0) + Math.max(1, Number(point.count) || 1))
  })
  return Array.from(counts.entries()).sort((left, right) => left[0] - right[0]).map(([index, count]) => ({ index, count }))
}

export function computeMaxLatency(buckets: UnifiedBucketPoint[], enabledSeries: Record<string, boolean>): number {
  let max = 10
  buckets.forEach(b => {
    Object.entries(b.values).forEach(([seriesId, val]) => {
      if (enabledSeries[seriesId] && val != null && seriesId !== 'cpu' && seriesId !== 'memory') {
        if (val > max) max = val
      }
    })
  })
  return Math.ceil(max * 1.15)
}
