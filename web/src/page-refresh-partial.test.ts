import { describe, expect, it, vi } from 'vitest'

import { createPageRefreshRegistry } from './page-refresh'

describe('page refresh registry', () => {
  it('refreshes only the handlers that read what changed', async () => {
    const registry = createPageRefreshRegistry()
    const servers = vi.fn()
    const users = vi.fn()
    registry.register(servers, { resources: ['servers'] })
    registry.register(users, { resources: ['users'] })
    await registry.runFor(['users'])
    expect(users).toHaveBeenCalledTimes(1)
    expect(servers).not.toHaveBeenCalled()
  })

  it('includes a handler that never said what it reads', async () => {
    const registry = createPageRefreshRegistry()
    const undeclared = vi.fn()
    registry.register(undeclared)
    await registry.runFor(['servers'])
    expect(undeclared).toHaveBeenCalledTimes(1)
  })

  it('still refreshes everything when the whole page is refreshed', async () => {
    const registry = createPageRefreshRegistry()
    const servers = vi.fn()
    const users = vi.fn()
    registry.register(servers, { resources: ['servers'] })
    registry.register(users, { resources: ['users'] })
    await registry.runAll()
    expect(servers).toHaveBeenCalledTimes(1)
    expect(users).toHaveBeenCalledTimes(1)
  })

  it('does nothing when a mutation declared no resources', async () => {
    const registry = createPageRefreshRegistry()
    const handler = vi.fn()
    registry.register(handler)
    await registry.runFor([])
    expect(handler).not.toHaveBeenCalled()
  })

  it('keeps one failing handler from stopping the others', async () => {
    const registry = createPageRefreshRegistry()
    const failing = vi.fn(() => { throw new Error('boom') })
    const healthy = vi.fn()
    registry.register(failing, { resources: ['servers'] })
    registry.register(healthy, { resources: ['servers'] })
    await registry.runFor(['servers'])
    expect(healthy).toHaveBeenCalledTimes(1)
  })
})
