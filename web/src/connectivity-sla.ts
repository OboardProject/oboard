// SLA timeline for the public-connectivity view. The Agent records one metric
// sample per heartbeat (~10-20s) while connected, so a gap between samples
// means the server stopped reporting: that time must count as downtime for SLA
// purposes instead of showing "no data".

export type ConnectivitySample = {
  connectivity_available?: boolean
  connectivity_latency_ms?: number
  sampled_at: string
}

export type ConnectivityTimelineSample<T extends ConnectivitySample = ConnectivitySample> = T & {
  offline_synthetic: boolean
}

export const OFFLINE_SAMPLE_STEP_MS = 60_000

export function serverOfflineThresholdMs(server: { offline_after_seconds?: number }): number {
  const seconds = Number(server.offline_after_seconds)
  return (Number.isFinite(seconds) && seconds > 0 ? seconds : 120) * 1000
}

export function buildSlaTimeline<T extends ConnectivitySample>(
  samples: T[],
  server: { status?: string; offline_after_seconds?: number; connectivity_probe_enabled?: boolean },
  nowMs = Date.now(),
): ConnectivityTimelineSample<T>[] {
  const sorted = samples
    .filter((sample): sample is T & { connectivity_available: boolean } => sample.connectivity_available !== undefined)
    .map(sample => ({ ...sample, offline_synthetic: false }))
    .sort((a, b) => Date.parse(a.sampled_at) - Date.parse(b.sampled_at))
  if (sorted.length === 0 && server.connectivity_probe_enabled === false) return sorted
  const reporting = samples.slice().sort((a, b) => Date.parse(a.sampled_at) - Date.parse(b.sampled_at))
  if (reporting.length === 0) return sorted

  const thresholdMs = serverOfflineThresholdMs(server)
  const windows: { startMs: number; endMs: number }[] = []
  for (let i = 1; i < reporting.length; i++) {
    const prevMs = Date.parse(reporting[i - 1].sampled_at)
    const nextMs = Date.parse(reporting[i].sampled_at)
    if (nextMs - prevMs > thresholdMs) windows.push({ startMs: prevMs, endMs: nextMs })
  }
  const lastMs = Date.parse(reporting[reporting.length - 1].sampled_at)
  if (String(server.status || '').toLowerCase() === 'offline' || nowMs - lastMs > thresholdMs) {
    windows.push({ startMs: lastMs, endMs: nowMs })
  }
  if (windows.length === 0) return sorted

  const synthetic: ConnectivityTimelineSample<T>[] = []
  for (const window of windows) {
    for (let atMs = window.startMs + thresholdMs; atMs < window.endMs; atMs += OFFLINE_SAMPLE_STEP_MS) {
      synthetic.push({
        ...reporting[0],
        connectivity_available: false,
        connectivity_latency_ms: 0,
        sampled_at: new Date(atMs).toISOString(),
        offline_synthetic: true,
      })
    }
  }
  return [...sorted, ...synthetic].sort((a, b) => Date.parse(a.sampled_at) - Date.parse(b.sampled_at))
}
