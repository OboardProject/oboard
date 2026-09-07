import { describe, expect, it } from 'vitest'

import { createPageRefreshRegistry, pageRefreshIncludesLiveServers } from './page-refresh'

describe('page refresh scope', () => {
  it('refreshes live server status on pages that show it', () => {
    expect(pageRefreshIncludesLiveServers('servers')).toBe(true)
    expect(pageRefreshIncludesLiveServers('dashboard')).toBe(true)
    expect(pageRefreshIncludesLiveServers('proxy-paths')).toBe(true)
    expect(pageRefreshIncludesLiveServers('tasks')).toBe(true)
    expect(pageRefreshIncludesLiveServers('plans')).toBe(false)
    expect(pageRefreshIncludesLiveServers('account')).toBe(false)
  })
})

describe('page refresh registry', () => {
  it('runs every registered handler and unregisters on dispose', async () => {
    const registry = createPageRefreshRegistry()
    const calls: string[] = []
    const disposeFirst = registry.register(() => { calls.push('first') })
    registry.register(async () => { calls.push('second') })

    await registry.runAll()
    expect(calls).toEqual(['first', 'second'])

    disposeFirst()
    calls.length = 0
    await registry.runAll()
    expect(calls).toEqual(['second'])
  })

  it('keeps other handlers running when one extra refresh fails', async () => {
    const registry = createPageRefreshRegistry()
    const calls: string[] = []
    registry.register(() => { throw new Error('satellite failed') })
    registry.register(() => { calls.push('ok') })

    await expect(registry.runAll()).resolves.toBeUndefined()
    expect(calls).toEqual(['ok'])
  })
})
