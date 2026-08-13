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
}

export interface UnifiedBucketPoint {
  timestamp: number
  timeLabel: string
  values: Record<string, number | null>
  memoryUsedBytes?: number
  memoryTotalBytes?: number
}

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
    if (probe.kind === 'regional' && probe.province && probe.carrier) {
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
      buckets[idx].values.public_latency = Number(pt.avg_ms)
    }
  })

  // 6. Map regionalProbes
  regionalProbes.forEach(probe => {
    if (probe.kind === 'regional' && probe.province && probe.carrier && probe.checked_at && probe.available) {
      const key = `${probe.province} · ${probe.carrier}`
      const seriesId = `reg_${key}`
      const ts = new Date(probe.checked_at).getTime()
      const idx = getBucketIndex(ts)
      if (idx >= 0) {
        buckets[idx].values[seriesId] = Number(probe.latency_ms || 0)
      }
    }
  })

  return { seriesList, buckets }
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
