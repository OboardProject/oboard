export type MonitoringSample = {
  available: boolean
  latency_ms: number | null
  sample_count: number
  success_count: number
  checked_at: string
}

export type MonitoringDisplay = {
  name: string
  enabled: boolean
  samples: MonitoringSample[]
}

export function connectivityLatencyLabel(status: string, latency: number | null | undefined) {
  if (status !== 'available' || latency == null || !Number.isFinite(latency) || latency < 0) return '—'
  return latency < 1 ? '<1 ms' : `${Math.round(latency)} ms`
}

export function monitoringLoss(samples: MonitoringSample[]) {
  let sent = 0
  let received = 0
  for (const sample of samples) {
    if (!Number.isFinite(sample.sample_count) || sample.sample_count <= 0) continue
    sent += sample.sample_count
    received += Math.min(sample.sample_count, Math.max(0, sample.success_count))
  }
  return sent ? (sent - received) / sent * 100 : null
}

export function serverMonitoring(display: MonitoringDisplay | undefined, online: boolean) {
  const samples = display?.enabled ? display.samples.slice(-20) : []
  const latest = samples[samples.length - 1]
  const status = !online ? 'offline' : !display?.enabled ? 'disabled' : !latest ? 'pending' : latest.available ? 'available' : 'unavailable'
  const segments = (type: 'latency' | 'loss') => {
    const tones = samples.map(sample => {
      if (type === 'loss') {
        const loss = monitoringLoss([sample])
        return loss == null ? '' : loss === 0 ? 'ok' : loss < 10 ? 'fair' : 'poor'
      }
      if (!sample.available) return 'poor'
      const latency = sample.latency_ms
      return latency == null || !Number.isFinite(latency) || latency < 0 ? '' : latency < 80 ? 'ok' : latency < 180 ? 'fair' : 'poor'
    })
    return [...Array.from({ length: 20 - tones.length }, () => ''), ...tones]
  }
  return {
    name: display?.name || '公网探测',
    status,
    latest,
    latency: connectivityLatencyLabel(status, latest?.latency_ms),
    loss: online && display?.enabled ? monitoringLoss(samples) : null,
    qualitySegments: segments('latency'),
    lossSegments: segments('loss'),
  }
}
