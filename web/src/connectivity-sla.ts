export type ConnectivityWindowKey = '1h' | '6h' | '12h' | '24h' | '7d' | '30d'

export type ConnectivityWindow = {
  key: ConnectivityWindowKey
  from: string
  to: string
  bucket_seconds: number
}

export type ConnectivityBucket = {
  start_at: string
  end_at: string
  sla_percent: number | null
  available_seconds: number
  unavailable_seconds: number
  unknown_seconds: number
  avg_latency_ms: number | null
}

export type ConnectivityResponse = {
  server_id: number
  retention_days: number
  window: ConnectivityWindow
  summary: {
    sla_percent: number | null
    available_seconds: number
    unavailable_seconds: number
    unknown_seconds: number
    observed_seconds: number
    coverage_percent: number
    outage_count: number
    longest_outage_seconds: number
  }
  probes: { total: number; available: number; failed: number }
  latency: {
    avg_ms: number | null
    min_ms: number | null
    max_ms: number | null
    p95_ms: number | null
    successful_probe_count: number
  }
  current: {
    status: 'available' | 'unavailable' | 'offline' | 'disabled' | 'pending'
    latency_ms: number
    checked_at: string | null
    error: string
  }
  buckets: ConnectivityBucket[]
  latency_points: { at: string; avg_ms: number; min_ms: number; max_ms: number; count: number }[]
  failed_probe_points: { at: string; count: number }[]
  regional_latency_points: { kind: 'regional' | 'custom'; task_id?: number; task_name?: string; province: string; carrier: string; available: true; latency_ms: number; min_latency_ms: number; max_latency_ms: number; count: number; checked_at: string }[]
  probe_target_stats: LatencyProbeTargetStat[]
  regional_data_start_at: string | null
  outages: { started_at: string; ended_at: string | null; duration_seconds: number; cause: string; started_before_window: boolean }[]
  data_start_at: string | null
}

export type LatencyProbeTargetStat = {
  key: string
  kind: 'public' | 'regional' | 'custom' | string
  task_id?: number
  task_name?: string
  mode?: string
  province?: string
  carrier?: string
  avg_ms: number | null
  min_ms: number | null
  max_ms: number | null
  jitter_ms: number | null
  sample_count: number
  success_count: number
  report_count: number
  available_count: number
  loss_percent: number | null
  success_percent: number | null
  peak_latency_ms?: number | null
  peak_latency_at?: string | null
  peak_loss_percent?: number | null
  peak_loss_at?: string | null
}

export function connectivityRequestPath(serverID: number | string, window: ConnectivityWindowKey = '24h') {
  return `/servers/${serverID}/connectivity?window=${window}`
}

export function connectivitySlaDisplay(value: number | null | undefined) {
  return value == null || !Number.isFinite(Number(value)) ? '—' : `${Number(value).toFixed(2)}%`
}

export function connectivityBucketTone(slaPercent: number | null, unknownSeconds: number, observedSeconds: number) {
  if (observedSeconds <= 0 || slaPercent == null) return 'none'
  if (slaPercent >= 99) return 'great'
  if (slaPercent >= 95) return 'fair'
  if (slaPercent > 0) return 'poor'
  return unknownSeconds > observedSeconds ? 'poor' : 'down'
}

export function formatConnectivityDuration(seconds: number) {
  if (!Number.isFinite(seconds) || seconds < 0) return '—'
  if (seconds < 60) return `${Math.round(seconds)} 秒`
  const totalMinutes = Math.round(seconds / 60)
  if (totalMinutes < 60) return `${totalMinutes} 分钟`
  const hours = Math.floor(totalMinutes / 60)
  const minutes = totalMinutes % 60
  return minutes ? `${hours} 小时 ${minutes} 分` : `${hours} 小时`
}

export type LatencyChartResponse = Pick<ConnectivityResponse, 'server_id' | 'retention_days' | 'window' | 'latency_points' | 'failed_probe_points' | 'regional_latency_points' | 'probe_target_stats' | 'regional_data_start_at'> & {
  metadata: {
    requested_from: string
    requested_to: string
    effective_from: string
    effective_to: string
    resolution_seconds: number
    generated_at: string
    observed_through: string | null
    aggregation_state: 'ready' | 'catching_up' | 'unavailable'
    coverage: { source: 'raw'; retention_clipped: boolean; has_samples: boolean; legacy_curve_reports: boolean }
    statistics_basis: string
    stale: boolean
  }
}

export function latencyChartRequestPath(serverID: number | string, window: ConnectivityWindowKey = '24h', maxPoints = 360) {
  return `${connectivityRequestPath(serverID, window)}&view=chart&max_points=${maxPoints}`
}

export type ConnectivityDetailsMetadata = {
  generated_at: string
  observed_through: string | null
  source: 'raw_state_events'
  retention_clipped: boolean
  statistics_basis: string
}

export type ConnectivitySLAResponse = Pick<ConnectivityResponse, 'server_id' | 'retention_days' | 'window' | 'summary' | 'buckets' | 'outages'> & {
  metadata: ConnectivityDetailsMetadata
}

export type ConnectivityEvent = {
  id: number
  server_id: number
  kind: string
  available: boolean | null
  latency_ms: number
  error: string
  source: string
  effective_at: string
  event_key: string
  created_at: string
}

export type ConnectivityEventsResponse = Pick<ConnectivityResponse, 'server_id' | 'retention_days' | 'window'> & {
  events: ConnectivityEvent[]
  next_cursor: string
  has_more: boolean
  metadata: ConnectivityDetailsMetadata
}

export function connectivityDetailsRequestPath(serverID: number, window: ConnectivityWindowKey, view: 'sla' | 'events', cursor = '') {
  const query = new URLSearchParams({ window, view })
  if (view === 'events') {
    query.set('limit', '100')
    if (cursor) query.set('cursor', cursor)
  }
  return `/servers/${serverID}/connectivity?${query}`
}
