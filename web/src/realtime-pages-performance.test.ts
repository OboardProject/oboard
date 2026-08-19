import { afterEach, describe, expect, it, vi } from 'vitest'

import { realtimeInvalidatedPages, realtimePageRefreshDelayMS, scheduleRealtimeRefresh } from './realtime-pages'

describe('cross-session page consistency timing', () => {
  afterEach(() => vi.useRealTimers())

  it('refreshes the affected visible page within the 2 second SLO', () => {
    vi.useFakeTimers()
    const samples = 32
    const latencies: number[] = []
    for (let index = 0; index < samples; index++) {
      const pages = realtimeInvalidatedPages({ type: 'invalidate', sequence: index + 1, resources: ['configuration'] }, 'servers', ['servers'])
      const dirtyPages = new Set(['servers'])
      let consistentAt = -1
      scheduleRealtimeRefresh({
        page: 'servers',
        activePage: 'servers',
        visible: true,
        dirtyPages,
        hasPendingRequest: false,
        schedule: (callback, delayMS) => setTimeout(callback, delayMS),
        refresh: page => {
          if (pages.has(page)) consistentAt = realtimePageRefreshDelayMS
        },
      })
      vi.advanceTimersByTime(realtimePageRefreshDelayMS)
      latencies.push(consistentAt)
    }
    const sorted = [...latencies].sort((a, b) => a - b)
    const p95 = sorted[Math.ceil(samples * 0.95) - 1]
    expect(p95).toBe(realtimePageRefreshDelayMS)
    expect(p95).toBeLessThanOrEqual(2_000)
  })

  it('consumes an invalidation already covered by a pending request', () => {
    vi.useFakeTimers()
    const dirtyPages = new Set(['servers'])
    const timer = scheduleRealtimeRefresh({
      page: 'servers',
      activePage: 'servers',
      visible: true,
      dirtyPages,
      hasPendingRequest: true,
      schedule: (callback, delayMS) => setTimeout(callback, delayMS),
      refresh: () => { throw new Error('duplicate refresh') },
    })
    expect(timer).toBeUndefined()
    expect(dirtyPages.has('servers')).toBe(false)
  })
})
