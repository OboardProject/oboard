// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { useServerMonitorQuery } from './use-server-monitor-query'

let root: Root
let current: ReturnType<typeof useServerMonitorQuery<{ value: string }>>
const pending: Array<{ path: string; signal: AbortSignal; resolve: (value: any) => void; reject: (error: Error) => void }> = []
const client = { request: vi.fn((path: string, init?: RequestInit) => new Promise((resolve, reject) => {
  pending.push({ path, signal: init!.signal as AbortSignal, resolve, reject })
})) }
function Monitor({ tab = 'latency', window = '24h', server = { id: 1 } }: { tab?: string; window?: string; server?: { id: number } }) {
  const latency = useServerMonitorQuery<{ value: string }>(client, `/servers/${server.id}/connectivity?window=${window}`, tab === 'latency')
  const load = useServerMonitorQuery<{ value: string }>(client, `/servers/${server.id}/resource-metrics?hours=1`, tab === 'load')
  current = tab === 'latency' ? latency : load
  return null
}
beforeEach(() => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
  root = createRoot(document.createElement('div'))
  pending.length = 0
  client.request.mockClear()
})
afterEach(() => { act(() => root.unmount()); vi.unstubAllGlobals() })

it('loads only the visible chart and ignores server object updates', async () => {
  await act(async () => root.render(<Monitor server={{ id: 1 }} />))
  expect(pending.map(item => item.path)).toEqual(['/servers/1/connectivity?window=24h'])
  await act(async () => root.render(<Monitor server={{ id: 1 }} />))
  expect(client.request).toHaveBeenCalledTimes(1)
  await act(async () => root.render(<Monitor tab="load" />))
  expect(pending[0].signal.aborted).toBe(true)
  expect(pending[1].path).toBe('/servers/1/resource-metrics?hours=1')
})

it('cancels obsolete windows and ignores their late responses', async () => {
  await act(async () => root.render(<Monitor />))
  await act(async () => root.render(<Monitor window="7d" />))
  expect(pending[0].signal.aborted).toBe(true)
  await act(async () => pending[0].resolve({ value: 'old' }))
  expect(current.response).toBeNull()
  await act(async () => pending[1].resolve({ value: 'new' }))
  expect(current.response).toEqual({ value: 'new' })
  expect(current.loading).toBe(false)
  await act(async () => root.render(<Monitor window="1h" />))
  expect(current.response).toBeNull()
  await act(async () => pending[2].reject(new Error('unavailable')))
  expect(current.response).toBeNull()
  expect(current.loading).toBe(false)
  expect(current.error).toBeInstanceOf(Error)
})

it('refreshes the selected window and retries failed reads', async () => {
  await act(async () => root.render(<Monitor />))
  await act(async () => pending[0].resolve({ value: 'first' }))
  await act(async () => current.refresh())
  expect(pending).toHaveLength(2)
  expect(current.response).toEqual({ value: 'first' })
  await act(async () => pending[1].reject(new Error('temporary')))
  expect(current.loading).toBe(false)
  await act(async () => current.refresh())
  await act(async () => pending[2].resolve({ value: 'latest' }))
  expect(current.response).toEqual({ value: 'latest' })
  expect(current.error).toBeNull()
})

it('cancels reads when hidden or closed and reloads on visibility', async () => {
  await act(async () => root.render(<Monitor />))
  await act(async () => {
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' })
    document.dispatchEvent(new Event('visibilitychange'))
  })
  expect(pending[0].signal.aborted).toBe(true)
  expect(pending).toHaveLength(1)
  await act(async () => {
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
    document.dispatchEvent(new Event('visibilitychange'))
  })
  expect(pending).toHaveLength(2)
  await act(async () => root.render(null))
  expect(pending[1].signal.aborted).toBe(true)
})
