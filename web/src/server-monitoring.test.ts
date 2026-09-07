import { describe, expect, it } from 'vitest'
import { connectivityLatencyLabel, monitoringLoss, serverMonitoring, type MonitoringSample } from './server-monitoring'

const sample = (overrides: Partial<MonitoringSample> = {}): MonitoringSample => ({ available: true, latency_ms: 0, sample_count: 3, success_count: 3, checked_at: '2026-09-08T00:00:00Z', ...overrides })

describe('server monitoring targets', () => {
  it('shows sub-millisecond successes without mistaking absent or failed readings for zero', () => {
    expect(connectivityLatencyLabel('available', 0)).toBe('<1 ms')
    expect(connectivityLatencyLabel('available', 0.3)).toBe('<1 ms')
    expect(connectivityLatencyLabel('available', 1)).toBe('1 ms')
    for (const value of [null, undefined, NaN, -1]) expect(connectivityLatencyLabel('available', value)).toBe('—')
    expect(connectivityLatencyLabel('unavailable', 0)).toBe('—')
    const card = serverMonitoring({ name: '公网探测', enabled: true, samples: [sample()] }, true)
    expect(card.latency).toBe('<1 ms')
    expect(card.qualitySegments.at(-1)).toBe('ok')
  })

  it('weights partial packet loss by sent packets, not by successful rounds', () => {
    const samples = [sample({ sample_count: 10, success_count: 8 }), sample({ sample_count: 2, success_count: 1 })]
    expect(monitoringLoss(samples)).toBe(25)
    const card = serverMonitoring({ name: '网站', enabled: true, samples }, true)
    expect(card.name).toBe('网站')
    expect(card.loss).toBe(25)
    expect(card.lossSegments.slice(-2)).toEqual(['poor', 'poor'])
  })

  it('uses only the selected target latest 20 rounds and never falls back for offline or disabled targets', () => {
    const display = { name: '任务 A', enabled: true, samples: [sample({ available: false, success_count: 0 }), ...Array.from({ length: 20 }, () => sample({ latency_ms: 47 }))] }
    expect(serverMonitoring(display, true).loss).toBe(0)
    expect(serverMonitoring(display, true).latency).toBe('47 ms')
    expect(serverMonitoring(display, true).qualitySegments).toHaveLength(20)
    for (const card of [serverMonitoring(display, false), serverMonitoring({ ...display, enabled: false }, true), serverMonitoring(undefined, true)]) {
      expect(card.latency).toBe('—')
      expect(card.loss).toBeNull()
    }
    expect(serverMonitoring({ ...display, samples: [] }, true).status).toBe('pending')
  })
})
