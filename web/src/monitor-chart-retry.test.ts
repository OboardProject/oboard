import { expect, it, vi } from 'vitest'
import { readMonitorChart, MONITOR_CHART_RETRY_DELAY_MS } from './monitor-chart-retry'

const unavailable = () => Object.assign(new Error('request canceled'), { status: 503 })

it('recovers one transient server timeout after the failure-cache backoff', async () => {
  vi.useFakeTimers()
  try {
    const read = vi.fn().mockRejectedValueOnce(unavailable()).mockResolvedValueOnce({ points: [1] })
    const onRetry = vi.fn()
    const result = readMonitorChart(new AbortController().signal, read, onRetry)
    await vi.advanceTimersByTimeAsync(MONITOR_CHART_RETRY_DELAY_MS - 1)
    expect(read).toHaveBeenCalledTimes(1)
    expect(onRetry).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    await expect(result).resolves.toEqual({ points: [1] })
    expect(read).toHaveBeenCalledTimes(2)
  } finally { vi.useRealTimers() }
})

it('stops after one retry and does not confuse a server 503 with a browser abort', async () => {
  vi.useFakeTimers()
  try {
    const read = vi.fn().mockRejectedValue(unavailable())
    const result = readMonitorChart(new AbortController().signal, read, () => {})
    const assertion = expect(result).rejects.toThrow('自动重试后仍未完成')
    await vi.advanceTimersByTimeAsync(60_000)
    await assertion
    expect(read).toHaveBeenCalledTimes(2)
  } finally { vi.useRealTimers() }
})

it('cancels delayed retries and never retries authorization or size failures', async () => {
  vi.useFakeTimers()
  try {
    const controller = new AbortController()
    const read = vi.fn().mockRejectedValue(unavailable())
    const result = readMonitorChart(controller.signal, read, () => {})
    const assertion = expect(result).rejects.toMatchObject({ name: 'AbortError' })
    await vi.advanceTimersByTimeAsync(100)
    controller.abort()
    await assertion
    await vi.advanceTimersByTimeAsync(60_000)
    expect(read).toHaveBeenCalledTimes(1)
    for (const status of [401, 403, 413, 500]) {
      const failed = vi.fn().mockRejectedValue(Object.assign(new Error('failure'), { status }))
      await expect(readMonitorChart(new AbortController().signal, failed, () => { throw new Error('must not retry') })).rejects.toThrow('failure')
      expect(failed).toHaveBeenCalledTimes(1)
    }
  } finally { vi.useRealTimers() }
})
