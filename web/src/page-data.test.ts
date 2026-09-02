import { describe, expect, it } from 'vitest'

import { idlePrefetchPages, PageDataRequestCoordinator, shouldRevalidatePageData } from './page-data'

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(done => { resolve = done })
  return { promise, resolve }
}

describe('PageDataRequestCoordinator', () => {
  it('deduplicates a page without coupling unrelated page epochs', async () => {
    const requests = new PageDataRequestCoordinator<string>()
    const dashboard = deferred<string>()
    const dashboardFresh = deferred<string>()
    const servers = deferred<string>()

    const firstDashboard = requests.request('dashboard', () => dashboard.promise)
    expect(requests.request('dashboard', () => Promise.resolve('duplicate'))).toBe(firstDashboard)
    const firstServers = requests.request('servers', () => servers.promise)
    const freshDashboard = requests.request('dashboard', () => dashboardFresh.promise, true)

    servers.resolve('servers')
    const serversResponse = await firstServers
    expect(requests.isCurrent('servers', serversResponse)).toBe(true)

    dashboard.resolve('stale dashboard')
    const staleDashboardResponse = await firstDashboard
    expect(requests.isCurrent('dashboard', staleDashboardResponse)).toBe(false)

    dashboardFresh.resolve('fresh dashboard')
    const freshDashboardResponse = await freshDashboard
    expect(requests.isCurrent('dashboard', freshDashboardResponse)).toBe(true)
  })

  it('invalidates only active requests during a cross-page state change', async () => {
    const requests = new PageDataRequestCoordinator<string>()
    const dashboard = deferred<string>()
    const servers = deferred<string>()
    const dashboardRequest = requests.request('dashboard', () => dashboard.promise)
    const serversRequest = requests.request('servers', () => servers.promise)

    requests.invalidateActive()
    dashboard.resolve('dashboard')
    servers.resolve('servers')

    expect(requests.isCurrent('dashboard', await dashboardRequest)).toBe(false)
    expect(requests.isCurrent('servers', await serversRequest)).toBe(false)
  })

  it('keeps responses from a previous session superseded after reset', async () => {
    const requests = new PageDataRequestCoordinator<string>()
    const previousSession = deferred<string>()
    const request = requests.request('dashboard', () => previousSession.promise)

    requests.reset()
    previousSession.resolve('previous session')

    expect(requests.isCurrent('dashboard', await request)).toBe(false)
  })

  it('cancelPrefetches aborts prefetch requests without touching active loads', () => {
    const requests = new PageDataRequestCoordinator<string>()
    const signals: AbortSignal[] = []
    const prefetch = deferred<string>()
    const background = deferred<string>()
    const foreground = deferred<string>()
    const prefetchRequest = requests.request('prefetch', signal => { signals[0] = signal; return prefetch.promise }, { priority: 'prefetch' })
    const backgroundRequest = requests.request('background', signal => { signals[1] = signal; return background.promise }, { priority: 'background' })
    const foregroundRequest = requests.request('foreground', signal => { signals[2] = signal; return foreground.promise }, { priority: 'foreground' })

    requests.cancelPrefetches()

    expect(signals[0].aborted).toBe(true)
    expect(signals[1].aborted).toBe(false)
    expect(signals[2].aborted).toBe(false)
    expect(requests.pending('prefetch')).toBeUndefined()
    expect(requests.pending('background')).toBe(backgroundRequest)
    expect(requests.pending('foreground')).toBe(foregroundRequest)
  })

  it('keeps the target prefetch while cancelling unrelated speculative requests', () => {
    const requests = new PageDataRequestCoordinator<string>()
    const target = deferred<string>()
    const unrelated = deferred<string>()
    const signals: AbortSignal[] = []
    const targetRequest = requests.request('servers', signal => { signals[0] = signal; return target.promise }, { priority: 'prefetch' })
    requests.request('audit', signal => { signals[1] = signal; return unrelated.promise }, { priority: 'prefetch' })

    requests.cancelPrefetches('servers')

    expect(signals[0].aborted).toBe(false)
    expect(signals[1].aborted).toBe(true)
    expect(requests.pending('servers')).toBe(targetRequest)
    expect(requests.pending('audit')).toBeUndefined()
  })
})

describe('page-data cache freshness', () => {
  it('revalidates only dirty, missing or expired cache entries', () => {
    const now = 20_000
    expect(shouldRevalidatePageData(15_000, false, 12_000, now)).toBe(false)
    expect(shouldRevalidatePageData(7_999, false, 12_000, now)).toBe(true)
    expect(shouldRevalidatePageData(15_000, true, 12_000, now)).toBe(true)
    expect(shouldRevalidatePageData(undefined, false, 12_000, now)).toBe(true)
  })
})

describe('idle page-data prefetch', () => {
  it('warms only common next pages instead of downloading every admin page', () => {
    expect(idlePrefetchPages('admin', 'dashboard')).toEqual(['servers', 'proxy-paths', 'users', 'tasks'])
    expect(idlePrefetchPages('admin', 'servers')).toEqual(['proxy-paths', 'users', 'tasks'])
    expect(idlePrefetchPages('admin', 'dashboard')).not.toContain('audit')
    expect(idlePrefetchPages('admin', 'dashboard')).not.toContain('settings')
  })

  it('keeps self-service prefetch scoped for non-operator roles', () => {
    expect(idlePrefetchPages('none', 'dashboard')).toEqual(['nodes', 'account'])
    expect(idlePrefetchPages('viewer', 'nodes')).toEqual(['notifications', 'account'])
  })
})
