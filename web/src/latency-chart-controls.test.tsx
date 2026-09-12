// @vitest-environment jsdom
import React, { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { ServerUnifiedTelemetryChart } from './components/server/ServerUnifiedTelemetryChart'
import { LatencyDashboard } from './components/server/LatencyDashboard'
import type { ConnectivityResponse, ConnectivityWindowKey } from './connectivity-sla'

const originalScrollIntoView = Element.prototype.scrollIntoView
let container: HTMLDivElement
let root: Root
beforeEach(() => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} })
  Element.prototype.scrollIntoView = vi.fn()
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})
afterEach(() => {
  act(() => root.unmount())
  container.remove()
  if (originalScrollIntoView) Element.prototype.scrollIntoView = originalScrollIntoView
  else delete (Element.prototype as Partial<Element>).scrollIntoView
  vi.unstubAllGlobals()
})

it('keeps curves smooth while the optional spike button changes only rendered values', () => {
  const aligned = {
    seriesList: [{ id: 'public_latency', label: '公网', color: 'red', unit: 'ms' as const, yAxis: 'right' as const }],
    buckets: [20, 20, 200, 20, 20].map((value, index) => ({ timestamp: index, timeLabel: `${index}`, values: { public_latency: value } })),
  }
  act(() => root.render(<ServerUnifiedTelemetryChart aligned={aligned} includeResources={false} seriesEnabled={{ public_latency: true }} />))
  const buttons = Array.from(container.querySelectorAll('button'))
  const toggle = buttons.find(button => button.textContent === '削峰')!
  expect(toggle).toBeDefined()
  expect(buttons.some(button => button.textContent === '平滑')).toBe(false)
  expect(toggle.getAttribute('aria-pressed')).toBe('false')
  const originalPath = container.querySelector('.komari-chart-polyline')!.getAttribute('d')
  expect(originalPath).toContain(' C ')
  act(() => toggle.click())
  expect(toggle.getAttribute('aria-pressed')).toBe('true')
  const clippedPath = container.querySelector('.komari-chart-polyline')!.getAttribute('d')
  expect(clippedPath).toContain(' C ')
  expect(clippedPath).not.toBe(originalPath)
  expect(aligned.buckets[2].values.public_latency).toBe(200)
  act(() => toggle.click())
  expect(container.querySelector('.komari-chart-polyline')!.getAttribute('d')).toBe(originalPath)
})

it('offers only standard and fine precision, defaulting to fine and accepting changes', () => {
  const response = { window: { from: '2026-09-11T00:00:00Z', to: '2026-09-12T00:00:00Z' }, probe_target_stats: [] } as unknown as ConnectivityResponse
  act(() => root.render(<LatencyDashboard response={response} windowKey="24h" windowHours={24} windowLabels={{ '24h': '24 小时' } as Record<ConnectivityWindowKey, string>} latencyWindowOptions={['24h']} onWindowChange={() => {}} onWindowKeyDown={() => {}} />))
  const select = container.querySelector('button[role="combobox"][aria-label="显示精度"]') as HTMLButtonElement
  expect(select.textContent).toContain('精细')
  act(() => select.click())
  const options = Array.from(document.querySelectorAll('[role="option"]')) as HTMLButtonElement[]
  expect(options.map(option => option.textContent)).toEqual(['标准', '精细'])
  act(() => options[0].click())
  expect(select.textContent).toContain('标准')
})
