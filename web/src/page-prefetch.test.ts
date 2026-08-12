import { describe, expect, it, vi } from 'vitest'

import { PageDataRequestCoordinator } from './page-data'
import { PagePrefetchScheduler } from './page-prefetch'

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((done, failed) => { resolve = done; reject = failed })
  return { promise, resolve, reject }
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

  it('invalidate aborts the in-flight HTTP request', async () => {
    const requests = new PageDataRequestCoordinator<string>()
    let signalSeen: AbortSignal | undefined
    const pending = deferred<string>()
    const request = requests.request('dashboard', signal => {
      signalSeen = signal
      return pending.promise
    })
    expect(signalSeen).toBeDefined()
    expect(signalSeen!.aborted).toBe(false)

    requests.invalidate('dashboard')
    expect(signalSeen!.aborted).toBe(true)

    pending.resolve('late')
    expect(requests.isCurrent('dashboard', await request)).toBe(false)
  })

  it('reset aborts every pending request', () => {
    const requests = new PageDataRequestCoordinator<string>()
    const signals: AbortSignal[] = []
    requests.request('dashboard', signal => { signals.push(signal); return deferred<string>().promise })
    requests.request('servers', signal => { signals.push(signal); return deferred<string>().promise })

    requests.reset()
    expect(signals.every(signal => signal.aborted)).toBe(true)
  })

  it('ignores stale responses after a forceFresh epoch bump', async () => {
    const requests = new PageDataRequestCoordinator<string>()
    const stale = deferred<string>()
    const first = requests.request('dashboard', () => stale.promise)
    requests.invalidate('dashboard')
    stale.resolve('old')
    expect(requests.isCurrent('dashboard', await first)).toBe(false)
  })

  it('does not surface AbortError from a cancelled request', async () => {
    const requests = new PageDataRequestCoordinator<string>()
    const signals: AbortSignal[] = []
    const request = requests.request('dashboard', signal => {
      signals.push(signal)
      return new Promise<string>((_, reject) => {
        signal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')))
      })
    })
    requests.cancel('dashboard')
    await expect(request).rejects.toMatchObject({ name: 'AbortError' })
    expect(signals[0].aborted).toBe(true)
    expect(requests.pending('dashboard')).toBeUndefined()
  })

  it('promotes a prefetch to foreground without issuing a second request', async () => {
    const requests = new PageDataRequestCoordinator<string>()
    let loads = 0
    const pending = deferred<string>()
    const prefetch = requests.request('dashboard', () => { loads++; return pending.promise }, { priority: 'prefetch' })
    const foreground = requests.request('dashboard', () => { loads++; return Promise.resolve('dup') }, { priority: 'foreground' })

    expect(foreground).toBe(prefetch)
    expect(loads).toBe(1)
    expect(requests.priority('dashboard')).toBe('foreground')

    pending.resolve('data')
    expect((await foreground).data).toBe('data')
  })

  it('cancelPrefetch never aborts a foreground request', async () => {
    const requests = new PageDataRequestCoordinator<string>()
    const signals: AbortSignal[] = []
    const pending = deferred<string>()
    const foreground = requests.request('dashboard', signal => { signals.push(signal); return pending.promise }, { priority: 'foreground' })

    requests.cancelPrefetch('dashboard')
    expect(signals[0].aborted).toBe(false)
    pending.resolve('data')
    expect(requests.isCurrent('dashboard', await foreground)).toBe(true)
  })
})

describe('PagePrefetchScheduler', () => {
  it('keeps at most two background requests active', async () => {
    const scheduler = new PagePrefetchScheduler(() => false)
    const pendings: Array<deferred<void>> = []
    let active = 0
    let peak = 0
    for (let index = 0; index < 8; index++) {
      scheduler.enqueue(`page-${index}`, 'idle', () => {
        active++
        peak = Math.max(peak, active)
        const pending = deferred<void>()
        pendings.push(pending)
        return pending.promise.then(() => { active-- })
      })
    }
    scheduler.pump()
    expect(peak).toBeLessThanOrEqual(2)
    expect(pendings.length).toBe(2)
    pendings.forEach(pending => pending.resolve())
    await Promise.resolve()
    await Promise.resolve()
    expect(pendings.length).toBeGreaterThan(2)
  })

  it('serves HIGH intent before IDLE queue entries', async () => {
    const scheduler = new PagePrefetchScheduler(() => false)
    const order: string[] = []
    for (let index = 0; index < 4; index++) {
      scheduler.enqueue(`idle-${index}`, 'idle', async () => { order.push(`idle-${index}`) })
    }
    scheduler.enqueue('hovered', 'high', async () => { order.push('hovered') })
    scheduler.pump()
    await Promise.resolve()
    expect(order[0]).toBe('hovered')
  })

  it('does not start new normal/idle jobs while a foreground navigation is paused', async () => {
    const scheduler = new PagePrefetchScheduler(() => false)
    const order: string[] = []
    scheduler.enqueue('running', 'idle', async () => { order.push('running') })
    scheduler.pump()
    scheduler.pauseIdle()
    scheduler.enqueue('normal-1', 'normal', async () => { order.push('normal-1') })
    scheduler.enqueue('idle-1', 'idle', async () => { order.push('idle-1') })
    expect(order).toEqual(['running'])
    scheduler.resumeIdle()
    await Promise.resolve()
    await Promise.resolve()
    expect(order).toContain('normal-1')
    expect(order).toContain('idle-1')
  })

  it('promotes a queued page when it is hovered', async () => {
    const scheduler = new PagePrefetchScheduler(() => false)
    const order: string[] = []
    scheduler.enqueue('a', 'idle', async () => { order.push('a') })
    scheduler.enqueue('b', 'idle', async () => { order.push('b') })
    scheduler.promote('b')
    scheduler.pump()
    await Promise.resolve()
    expect(order[0]).toBe('b')
  })

  it('skips guarded pages (active tab, fresh cache, dirty) at dequeue time', async () => {
    const guarded = new Set(['active', 'fresh', 'dirty'])
    const scheduler = new PagePrefetchScheduler(page => guarded.has(page))
    const runs: string[] = []
    scheduler.enqueue('active', 'idle', async () => { runs.push('active') })
    scheduler.enqueue('fresh', 'idle', async () => { runs.push('fresh') })
    scheduler.enqueue('dirty', 'idle', async () => { runs.push('dirty') })
    scheduler.enqueue('ok', 'idle', async () => { runs.push('ok') })
    scheduler.pump()
    await Promise.resolve()
    expect(runs).toEqual(['ok'])
  })

  it('is disabled for saveData and 2g connections', () => {
    const original = (navigator as any).connection
    try {
      Object.defineProperty(navigator, 'connection', { value: { saveData: true, effectiveType: '4g' }, configurable: true })
      expect(new PagePrefetchScheduler(() => false).isEnabled).toBe(false)
      Object.defineProperty(navigator, 'connection', { value: { saveData: false, effectiveType: '2g' }, configurable: true })
      expect(new PagePrefetchScheduler(() => false).isEnabled).toBe(false)
      Object.defineProperty(navigator, 'connection', { value: { saveData: false, effectiveType: 'slow-2g' }, configurable: true })
      expect(new PagePrefetchScheduler(() => false).isEnabled).toBe(false)
    } finally {
      Object.defineProperty(navigator, 'connection', { value: original, configurable: true })
    }
  })

  it('deduplicates enqueues and upgrades priority', () => {
    const scheduler = new PagePrefetchScheduler(() => false)
    const order: string[] = []
    const run = vi.fn(async () => { order.push('page') })
    scheduler.enqueue('page', 'idle', run)
    scheduler.enqueue('page', 'idle', run)
    scheduler.enqueue('page', 'high', run)
    expect(scheduler.size).toBe(1)
    scheduler.enqueue('x', 'idle', async () => { order.push('x') })
    scheduler.pump()
    expect(order[0]).toBe('page')
  })
})
