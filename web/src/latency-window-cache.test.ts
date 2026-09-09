import { expect, it, vi } from 'vitest'
import { LatencyWindowCache } from './latency-window-cache'

it('bounds bytes, evicts least recently used entries and rejects oversized entries', () => {
 const cache = new LatencyWindowCache(100, 90)
 cache.put('a', '1234567890', 0)
 cache.put('b', '1234567890', 0)
 cache.put('c', '1234567890', 0)
 expect(cache.get('a', 1)?.response).toBe('1234567890')
 cache.put('d', '1234567890', 0)
 expect(cache.get('b', 1)).toBeNull()
 cache.put('big', 'x'.repeat(100), 0)
 expect(cache.get('big', 1)).toBeNull()
 expect(cache.get('a', 1)?.fresh).toBe(true)
 expect(cache.get('a', 30_000)?.fresh).toBe(false)
 expect(cache.get('a', 150_000)).toBeNull()
})

it('shares a read and only cancels when its final waiter leaves', async () => {
 const cache = new LatencyWindowCache()
 let finish!: (value: unknown) => void
 let signal!: AbortSignal
 const load = vi.fn((input: AbortSignal) => { signal = input; return new Promise(resolve => { finish = resolve }) })
 const a = new AbortController(), b = new AbortController()
 const first = cache.read('day', a.signal, load)
 const second = cache.read('day', b.signal, load)
 await Promise.resolve()
 a.abort()
 await expect(first).rejects.toMatchObject({ name: 'AbortError' })
 expect(signal.aborted).toBe(false)
 finish({ day: 1 })
 await expect(second).resolves.toEqual({ day: 1 })
 expect(load).toHaveBeenCalledTimes(1)
 expect(cache.get('day')?.response).toEqual({ day: 1 })
})

it('does not publish cancelled results after clearing the account', async () => {
 const cache = new LatencyWindowCache()
 let finish!: (value: unknown) => void
 const caller = new AbortController()
 const request = cache.read('day', caller.signal, () => new Promise(resolve => { finish = resolve }))
 await Promise.resolve()
 caller.abort()
 await expect(request).rejects.toMatchObject({ name: 'AbortError' })
 cache.clear()
 finish({ secret: true })
 await Promise.resolve()
 expect(cache.get('day')).toBeNull()
})

it('bounds a stalled read and reports a retryable timeout', async () => {
 vi.useFakeTimers()
 try {
  const cache = new LatencyWindowCache()
  const result = cache.read('stalled', new AbortController().signal, () => new Promise(() => {}))
  const assertion = expect(result).rejects.toThrow('图表读取超时')
  await vi.advanceTimersByTimeAsync(10_000)
  await assertion
  await expect(cache.read('stalled',new AbortController().signal,async () => 'recovered')).resolves.toBe('recovered')
 } finally { vi.useRealTimers() }
})
