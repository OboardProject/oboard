import { describe, expect, it, vi } from 'vitest'

import { CoalescedReadCoordinator } from './request-coalesce'
import { PageDataRequestCoordinator } from './page-data'

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail })
  return { promise, resolve, reject }
}

describe('CoalescedReadCoordinator', () => {
  it('shares the PageDataRequestCoordinator coalescing model', () => {
    expect(CoalescedReadCoordinator).toBe(PageDataRequestCoordinator)
  })

  it('keeps one in-flight request per key and cancels obsolete reads', async () => {
    const reads = new CoalescedReadCoordinator<string>()
    const first = deferred<string>()
    const signals: AbortSignal[] = []
    const shared = reads.request('audit/risk-overview?window_hours=24', signal => {
      signals.push(signal)
      return first.promise
    })
    expect(reads.request('audit/risk-overview?window_hours=24', () => Promise.resolve('dup'))).toBe(shared)

    reads.cancel('audit/risk-overview?window_hours=24')
    expect(signals[0]?.aborted).toBe(true)
    first.resolve('stale')
    await expect(shared).resolves.toEqual({ data: 'stale', epoch: 0 })
  })

  it('does not retain completed responses as a cache', async () => {
    const reads = new CoalescedReadCoordinator<string>()
    const first = deferred<string>()
    const initial = reads.request('token-sensitive', () => first.promise)
    first.resolve('one-time-token')
    await initial
    expect(reads.pending('token-sensitive')).toBeUndefined()

    const secondLoad = vi.fn(async () => 'fresh')
    const next = await reads.request('token-sensitive', secondLoad)
    expect(secondLoad).toHaveBeenCalledTimes(1)
    expect(next.data).toBe('fresh')
  })
})
