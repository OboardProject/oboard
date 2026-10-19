export interface MetricSeries {
  id: string
  label: string
  color: string
  unit: '%' | 'ms'
  yAxis: 'left' | 'right'
}

// regionalSeriesKey identifies the probe task that produced a sample. Several
// tasks may share one province+carrier target, so the task id wins when present.
export function regionalSeriesKey(probe: { kind?: string; task_id?: number; province?: string; carrier?: string }): string {
  if (probe.kind === 'public') return 'public'
  if (probe.task_id && probe.task_id > 0) return `task_${probe.task_id}`
  return `${probe.province || ''} · ${probe.carrier || ''}`
}

export function isLatencySeriesKey(key: string): boolean {
  return Boolean(key) && key !== ' · '
}

// regionalSeriesLabel prefers the probe task name so charts stay readable when
// several tasks watch the same province and carrier.
export function regionalSeriesLabel(probe: { kind?: string; task_name?: string; province?: string; carrier?: string }): string {
  const name = (probe.task_name || '').trim()
  if (name) return name
  if (probe.kind === 'public') return '公网探测'
  return `${probe.province || ''} · ${probe.carrier || ''}`
}

export interface LatencyProbeResultSample {
  probe_id?: number | string
  server_id?: number
  kind?: 'public' | 'regional' | 'custom'
  task_id?: number
  task_name?: string
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

// One run of points the chart draws as a single line. `bridgeFrom` is set only
// when the connect-gaps switch joins this run to the previous one across a
// failure or a silent gap; it carries the last point before the gap so the
// chart can draw that join with distinct styling.
export interface SeriesSegment {
  points: SeriesSegmentPoint[]
  bridgeFrom?: SeriesSegmentPoint
}

// Successful samples of one probe task always connect, however long its probe
// interval is relative to a chart bucket. A line breaks only where the task
// actually failed between two successes, or where it went silent for much
// longer than its own cadence (agent offline, task paused).
const SILENT_GAP_INTERVALS = 6

function medianReportingInterval(points: SeriesSegmentPoint[]): number {
  if (points.length < 2) return 1
  const intervals: number[] = []
  for (let index = 1; index < points.length; index += 1) {
    intervals.push(points[index].index - points[index - 1].index)
  }
  intervals.sort((a, b) => a - b)
  const middle = Math.floor(intervals.length / 2)
  const median = intervals.length % 2 === 1 ? intervals[middle] : (intervals[middle - 1] + intervals[middle]) / 2
  return Math.max(1, median)
}

export interface FailedProbePoint {
  at: string
  count: number
}

export const DEFAULT_CONNECT_GAPS = false
export const DEFAULT_SMOOTH_LINES = true
export const DEFAULT_CLIP_SPIKES = false

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

// failedIndexes lists the buckets in which this series recorded a failed probe.
// A failure strictly between two successful buckets breaks the line; with
// connectGaps the two successes are joined anyway and the failure stays visible
// as a red marker.
export function splitSeriesSegments(buckets: UnifiedBucketPoint[], seriesID: string, connectGaps = false, failedIndexes: number[] = []): SeriesSegment[] {
  const points: SeriesSegmentPoint[] = []
  buckets.forEach((bucket, index) => {
    const value = bucket.values[seriesID]
    if (value != null && Number.isFinite(value)) points.push({ index, value })
  })
  if (points.length === 0) return []

  const failures = [...failedIndexes].sort((a, b) => a - b)
  let cursor = 0
  const failedBetween = (from: number, to: number) => {
    while (cursor < failures.length && failures[cursor] <= from) cursor += 1
    return cursor < failures.length && failures[cursor] < to
  }
  const silentLimit = Math.ceil(medianReportingInterval(points) * SILENT_GAP_INTERVALS)
  const segments: SeriesSegment[] = [{ points: [points[0]] }]
  for (let index = 1; index < points.length; index += 1) {
    const previous = points[index - 1]
    const current = points[index]
    const failed = failedBetween(previous.index, current.index)
    const silent = current.index - previous.index > silentLimit
    if (failed && connectGaps && !silent) {
      segments[segments.length - 1].points.push(current)
      continue
    }
    if (failed || silent) segments.push({ points: [], bridgeFrom: connectGaps ? previous : undefined })
    segments[segments.length - 1].points.push(current)
  }
  return segments
}

// Upper-only Hampel filter over each series' own successful samples, so empty
// buckets between sparse probes never disable it. A sample is replaced by the
// median of its five-sample neighbourhood only when it stands far above that
// median while the neighbourhood itself is calm; two or more high samples in a
// row keep the median high and survive as a real latency change.
export function suppressLatencySpikes(buckets: UnifiedBucketPoint[], seriesList: MetricSeries[]): UnifiedBucketPoint[] {
  const result = buckets.map(bucket => ({ ...bucket, values: { ...bucket.values } }))
  const median = (values: number[]) => {
    const sorted = [...values].sort((a, b) => a - b)
    const middle = Math.floor(sorted.length / 2)
    return sorted.length % 2 === 1 ? sorted[middle] : (sorted[middle - 1] + sorted[middle]) / 2
  }
  for (const series of seriesList) {
    if (series.unit !== 'ms') continue
    const samples: { index: number; value: number }[] = []
    buckets.forEach((bucket, index) => {
      const value = bucket.values[series.id]
      if (value != null && Number.isFinite(value) && value >= 0) samples.push({ index, value })
    })
    if (samples.length < 3) continue
    // A spike needs a normal neighbour on both sides, so the first and last
    // samples are never judged.
    for (let position = 1; position < samples.length - 1; position += 1) {
      const window = samples.slice(Math.max(0, position - 2), Math.min(samples.length, position + 3)).map(sample => sample.value)
      if (window.length < 3) continue
      const baseline = median(window)
      const deviation = median(window.map(value => Math.abs(value - baseline)))
      const threshold = Math.max(5, baseline * 0.5, 3 * 1.4826 * deviation)
      if (samples[position].value > baseline + threshold) result[samples[position].index].values[series.id] = baseline
    }
  }
  return result
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
    const clamp = (value: number, a: number, b: number) => Math.max(Math.min(a, b), Math.min(Math.max(a, b), value))
    const control1X = clamp(current.x + (next.x - previous.x) / 6, current.x, next.x)
    const control1Y = clamp(current.y + (next.y - previous.y) / 6, current.y, next.y)
    const control2X = clamp(next.x - (following.x - current.x) / 6, current.x, next.x)
    const control2Y = clamp(next.y - (following.y - current.y) / 6, current.y, next.y)
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
  failedBuckets: Record<string, { index: number; count: number }[]>
} {
  const windowMs = Math.max(1, windowHours) * 3600 * 1000
  const startTime = now - windowMs
  const bucketDuration = windowMs / bucketCount

  // 1. Discover all probe-task series. A task owns one target, so the task
  // identity is the series key and the task name is the series label.
  const regionalTargets = new Map<string, string>()
  regionalProbes.forEach(probe => {
    if (probe.kind === 'public') return
    const key = regionalSeriesKey(probe)
    if (!isLatencySeriesKey(key)) return
    if (!regionalTargets.has(key)) regionalTargets.set(key, regionalSeriesLabel(probe))
  })

  // 2. Build series list
  const seriesList: MetricSeries[] = includeResources ? [
    { id: 'cpu', label: 'CPU 使用率', color: '#3b82f6', unit: '%', yAxis: 'left' },
    { id: 'memory', label: '内存使用率', color: '#10b981', unit: '%', yAxis: 'left' },
    { id: 'public_latency', label: '公网探测', color: '#f59e0b', unit: 'ms', yAxis: 'right' },
  ] : [
    { id: 'public_latency', label: '公网探测', color: '#f59e0b', unit: 'ms', yAxis: 'right' },
  ]

  let colorIdx = 0
  const regTargetKeys = Array.from(regionalTargets.keys()).sort((a, b) => (regionalTargets.get(a) || a).localeCompare(regionalTargets.get(b) || b, 'zh-CN'))
  regTargetKeys.forEach(targetKey => {
    seriesList.push({
      id: `reg_${targetKey}`,
      label: regionalTargets.get(targetKey) || targetKey,
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

  const failureTotals = new Map<string, Map<number, number>>()
  const addFailure = (seriesID: string, bucketIndex: number, count: number) => {
    const counts = failureTotals.get(seriesID) || new Map<number, number>()
    counts.set(bucketIndex, (counts.get(bucketIndex) || 0) + Math.max(1, count))
    failureTotals.set(seriesID, counts)
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

  // 6. Map regional and custom probe tasks
  regionalProbes.forEach(probe => {
    if (probe.kind === 'public') return
    const key = regionalSeriesKey(probe)
    if (!isLatencySeriesKey(key)) return

    const isAggregated = Boolean(probe.at)
    const timeString = isAggregated ? probe.at : probe.checked_at
    if (!isAggregated && probe.available === false) {
      const failedIndex = timeString ? getBucketIndex(new Date(timeString).getTime()) : -1
      if (failedIndex >= 0) addFailure(`reg_${key}`, failedIndex, Number(probe.count) || 1)
      return
    }
    const rawValue = isAggregated ? probe.avg_ms : probe.latency_ms
    if (!timeString || rawValue == null) return

    const value = Number(rawValue)
    if (!Number.isFinite(value)) return
    const idx = getBucketIndex(new Date(timeString).getTime())
    if (idx >= 0) {
      addLatency(idx, `reg_${key}`, value, Number(probe.count || 1))
    }
  })

  latencyTotals.forEach(total => {
    buckets[total.bucketIndex].values[total.seriesID] = total.weightedSum / total.weight
  })

  const failedBuckets: Record<string, { index: number; count: number }[]> = {}
  failureTotals.forEach((counts, seriesID) => {
    failedBuckets[seriesID] = Array.from(counts.entries()).sort((a, b) => a[0] - b[0]).map(([index, count]) => ({ index, count }))
  })

  return { seriesList, buckets, failedBuckets }
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
