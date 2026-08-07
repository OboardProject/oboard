import { describe, expect, it } from 'vitest'
import { buildSlaTimeline, OFFLINE_SAMPLE_STEP_MS, serverOfflineThresholdMs } from './connectivity-sla'

const online = { status: 'online', offline_after_seconds: 0, connectivity_probe_enabled: true }
const offline = { status: 'offline', offline_after_seconds: 0, connectivity_probe_enabled: true }

function sample(iso: string, available = true): { connectivity_available: boolean; connectivity_latency_ms: number; sampled_at: string; server_id: number } {
  return { connectivity_available: available, connectivity_latency_ms: available ? 12 : 0, sampled_at: iso, server_id: 1 }
}

describe('serverOfflineThresholdMs', () => {
  it('defaults to the controller offline_after default of 120s', () => {
    expect(serverOfflineThresholdMs({ offline_after_seconds: 0 })).toBe(120_000)
    expect(serverOfflineThresholdMs({})).toBe(120_000)
  })

  it('uses the per-server offline_after_seconds when set', () => {
    expect(serverOfflineThresholdMs({ offline_after_seconds: 300 })).toBe(300_000)
  })
})

describe('buildSlaTimeline', () => {
  it('returns an empty timeline without samples', () => {
    expect(buildSlaTimeline([], online)).toEqual([])
  })

  it('keeps continuous samples untouched', () => {
    const samples = [
      sample('2026-08-07T00:00:00Z'),
      sample('2026-08-07T00:00:20Z'),
      sample('2026-08-07T00:00:40Z'),
    ]
    const timeline = buildSlaTimeline(samples, online, Date.parse('2026-08-07T00:01:00Z'))
    expect(timeline).toHaveLength(3)
    expect(timeline.every(item => !item.offline_synthetic)).toBe(true)
    expect(timeline[0]).toMatchObject({ connectivity_available: true, server_id: 1 })
  })

  it('fills an internal reporting gap with unavailable samples', () => {
    const samples = [
      sample('2026-08-07T00:00:00Z'),
      sample('2026-08-07T00:05:00Z'),
    ]
    const timeline = buildSlaTimeline(samples, online, Date.parse('2026-08-07T00:06:00Z'))
    const synthetic = timeline.filter(item => item.offline_synthetic)
    expect(synthetic).toHaveLength(3)
    expect(synthetic.map(item => item.sampled_at)).toEqual([
      '2026-08-07T00:02:00.000Z',
      '2026-08-07T00:03:00.000Z',
      '2026-08-07T00:04:00.000Z',
    ])
    expect(synthetic.every(item => item.connectivity_available === false)).toBe(true)
    expect(timeline).toHaveLength(5)
  })

  it('counts an ongoing offline period from the last sample to now', () => {
    const samples = [sample('2026-08-07T01:00:00Z')]
    const nowMs = Date.parse('2026-08-07T01:10:00Z')
    const timeline = buildSlaTimeline(samples, offline, nowMs)
    const synthetic = timeline.filter(item => item.offline_synthetic)
    const expected = Math.floor((nowMs - Date.parse('2026-08-07T01:00:00Z') - 120_000) / OFFLINE_SAMPLE_STEP_MS)
    expect(synthetic).toHaveLength(expected)
    expect(synthetic.every(item => item.connectivity_available === false)).toBe(true)
    expect(timeline[timeline.length - 1].sampled_at).toBe(synthetic[synthetic.length - 1].sampled_at)
  })

  it('extends the tail when the last sample is older than the threshold even if status is stale', () => {
    const samples = [sample('2026-08-07T01:00:00Z')]
    const timeline = buildSlaTimeline(samples, { ...online, status: 'online' }, Date.parse('2026-08-07T01:10:00Z'))
    expect(timeline.some(item => item.offline_synthetic)).toBe(true)
  })

  it('does not invent downtime when probing is disabled and no probe results exist', () => {
    const samples = [
      { connectivity_available: undefined, connectivity_latency_ms: 0, sampled_at: '2026-08-07T00:00:00Z', server_id: 1 },
      { connectivity_available: undefined, connectivity_latency_ms: 0, sampled_at: '2026-08-07T00:05:00Z', server_id: 1 },
    ]
    const timeline = buildSlaTimeline(samples, { status: 'offline', offline_after_seconds: 0, connectivity_probe_enabled: false }, Date.parse('2026-08-07T00:10:00Z'))
    expect(timeline).toHaveLength(0)
  })

  it('still synthesizes during the first probe-pending minute after probing was enabled', () => {
    const samples = [
      { connectivity_available: undefined, connectivity_latency_ms: 0, sampled_at: '2026-08-07T00:00:00Z', server_id: 1 },
      { connectivity_available: undefined, connectivity_latency_ms: 0, sampled_at: '2026-08-07T00:05:00Z', server_id: 1 },
    ]
    const timeline = buildSlaTimeline(samples, online, Date.parse('2026-08-07T00:10:00Z'))
    expect(timeline.some(item => item.offline_synthetic)).toBe(true)
  })
})
