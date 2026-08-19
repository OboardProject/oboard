import { describe, expect, it } from 'vitest'

import { realtimeInvalidatedPages } from './realtime-pages'

describe('realtime page invalidation', () => {
  it('invalidates only configuration surfaces for another session save', () => {
    const pages = realtimeInvalidatedPages(
      { type: 'invalidate', sequence: 9, resources: ['configuration'] },
      'servers',
      ['servers', 'notifications', 'settings'],
    )
    expect([...pages].sort()).toEqual(['dashboard', 'dns', 'nodes', 'plans', 'proxy-paths', 'servers', 'tasks', 'users'])
    expect(pages.has('notifications')).toBe(false)
    expect(pages.has('settings')).toBe(false)
  })

  it('invalidates every cached page plus the active page after a sequence resync', () => {
    const pages = realtimeInvalidatedPages(
      { type: 'resync_required', sequence: 10 },
      'servers',
      ['dashboard', 'notifications'],
    )
    expect([...pages].sort()).toEqual(['dashboard', 'notifications', 'servers'])
  })

  it('ignores transport readiness without a reconnect', () => {
    expect(realtimeInvalidatedPages({ type: 'ready', sequence: 1 }, 'servers', ['dashboard']).size).toBe(0)
  })
})
