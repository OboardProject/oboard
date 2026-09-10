// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { beforeEach, afterEach, expect, it, vi } from 'vitest'
import { ConnectivityDetails } from './ConnectivityDetails'
import type { ConnectivityEventsResponse, ConnectivitySLAResponse } from '../../connectivity-sla'

let container: HTMLDivElement
let root: Root
const pending: Array<{ path: string; signal: AbortSignal; resolve: (value: unknown) => void; reject: (error: Error) => void }> = []
const client = { request: vi.fn((path: string, init?: RequestInit) => new Promise((resolve, reject) => {
  pending.push({ path, signal: init!.signal as AbortSignal, resolve, reject })
})) }
const window = { key: '1h' as const, from: '2026-09-10T01:00:00Z', to: '2026-09-10T02:00:00Z', bucket_seconds: 300 }
const metadata = { generated_at: window.to, observed_through: window.to, source: 'raw_state_events' as const, retention_clipped: false, statistics_basis: 'test' }
const sla: ConnectivitySLAResponse = {
  server_id: 1, retention_days: 30, window, metadata, buckets: [], outages: [],
  summary: { sla_percent: 80, available_seconds: 2400, unavailable_seconds: 600, unknown_seconds: 600, observed_seconds: 3000, coverage_percent: 83.33, outage_count: 0, longest_outage_seconds: 0 },
}
function page(id: number, next = ''): ConnectivityEventsResponse {
  return { server_id: 1, retention_days: 30, window, metadata, events: [{ id, server_id: 1, kind: 'probe_result', available: false, latency_ms: 0, error: `failure-${id}`, source: 'latency_probe', effective_at: window.from, created_at: window.to, event_key: String(id) }], has_more: !!next, next_cursor: next }
}
async function click(text: string) {
  const button = [...container.querySelectorAll('button')].find(item => item.textContent === text)!
  expect(button).toBeDefined()
  await act(async () => button.click())
}
beforeEach(() => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
  container = document.createElement('div')
  root = createRoot(container)
  pending.length = 0
  client.request.mockClear()
})
afterEach(() => { act(() => root.unmount()); vi.unstubAllGlobals() })

it('reads sections only after expansion and cancels collapsed or replaced windows', async () => {
  await act(async () => root.render(<ConnectivityDetails key="1h" serverID={1} windowKey="1h" client={client} />))
  expect(pending).toHaveLength(0)
  await click('在线率与故障摘要')
  expect(pending[0].path).toContain('view=sla')
  expect(container.querySelector('button')?.getAttribute('aria-expanded')).toBe('true')
  await click('诊断事件')
  expect(pending[1].path).toContain('view=events&limit=100')
  await click('诊断事件')
  expect(pending[1].signal.aborted).toBe(true)
  await act(async () => pending[0].resolve(sla))
  expect(container.textContent).toContain('80.00%')
  expect(container.textContent).toContain('未知')
  await click('刷新在线率')
  expect(pending).toHaveLength(3)
  await act(async () => pending[2].reject(new Error('history_busy')))
  expect(container.textContent).toContain('80.00%')
  expect(container.querySelector('[role="alert"]')?.textContent).toContain('history_busy')
  await click('刷新在线率')
  await act(async () => root.render(<ConnectivityDetails key="24h" serverID={1} windowKey="24h" client={client} />))
  expect(pending[3].signal.aborted).toBe(true)
  await act(async () => pending[3].resolve(sla))
  expect(container.textContent).not.toContain('80.00%')
  expect(pending).toHaveLength(4)
})

it('replaces event pages without accumulating history and refreshes once on visibility restore', async () => {
  await act(async () => root.render(<ConnectivityDetails serverID={1} windowKey="1h" client={client} />))
  await click('诊断事件')
  await act(async () => pending[0].resolve(page(1, 'opaque/+=')))
  await click('下一页')
  expect(new URLSearchParams(pending[1].path.split('?')[1]).get('cursor')).toBe('opaque/+=')
  expect(container.textContent).not.toContain('failure-1')
  await act(async () => pending[1].resolve(page(2)))
  expect(container.textContent).toContain('failure-2')
  expect(container.textContent).not.toContain('failure-1')
  await act(async () => {
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' })
    document.dispatchEvent(new Event('visibilitychange'))
  })
  expect(pending).toHaveLength(2)
  await act(async () => {
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
    document.dispatchEvent(new Event('visibilitychange'))
    document.dispatchEvent(new Event('visibilitychange'))
  })
  expect(pending).toHaveLength(3)
  await act(async () => pending[2].resolve(page(2)))
  await click('返回首页并刷新')
  expect(pending[3].path).not.toContain('cursor=')
  expect(pending.every(item => !item.path.includes('view=sla'))).toBe(true)
})
