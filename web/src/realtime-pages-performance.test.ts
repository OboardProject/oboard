import { describe, expect, it } from 'vitest'

import { realtimeInvalidatedPages, realtimePageRefreshDelayMS, scheduleRealtimeRefresh } from './realtime-pages'

describe('cross-session page refresh coordination', () => {
  it('maps a configuration event to the active page and uses a bounded refresh delay', () => {
    const pages = realtimeInvalidatedPages({ type: 'invalidate', sequence: 1, resources: ['configuration'] }, 'servers', ['servers'])
    expect(pages.has('servers')).toBe(true)
    expect(realtimePageRefreshDelayMS).toBeLessThanOrEqual(2_000)
  })

  it('consumes an invalidation already covered by a pending request', () => {
    const dirtyPages = new Set(['servers'])
    const timer = scheduleRealtimeRefresh({
      page: 'servers',
      activePage: 'servers',
      visible: true,
      dirtyPages,
      hasPendingRequest: true,
      schedule: () => 1,
      refresh: () => { throw new Error('duplicate refresh') },
    })
    expect(timer).toBeUndefined()
    expect(dirtyPages.has('servers')).toBe(false)
  })
})
