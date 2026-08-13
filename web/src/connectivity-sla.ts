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
  outages: { started_at: string; ended_at: string | null; duration_seconds: number; cause: string; started_before_window: boolean }[]
  data_start_at: string | null
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
