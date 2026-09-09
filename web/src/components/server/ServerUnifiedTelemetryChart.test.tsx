// @vitest-environment jsdom
import React, { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import * as metrics from '../../server-unified-chart'
import { LatencyDashboard } from './LatencyDashboard'
import type { ConnectivityResponse } from '../../connectivity-sla'
import { ServerUnifiedTelemetryChart } from './ServerUnifiedTelemetryChart'

vi.mock('../../server-unified-chart', async importOriginal => {
  const actual = await importOriginal<typeof import('../../server-unified-chart')>()
  return { ...actual, alignUnifiedMetrics: vi.fn(actual.alignUnifiedMetrics), buildLinePath: vi.fn(actual.buildLinePath), buildAreaPath: vi.fn(actual.buildAreaPath) }
})

it('keeps chart height and readable coordinates when the container resizes to mobile', () => {
  ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
  let width = 900
  let resize = () => {}
  const disconnect = vi.fn()
  vi.stubGlobal('ResizeObserver', class {
    constructor(callback: () => void) { resize = callback }
    observe() {}
    disconnect = disconnect
  })
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(() => ({ width } as DOMRect))
  const container = document.createElement('div')
  const root = createRoot(container)
  try {
    act(() => root.render(<ServerUnifiedTelemetryChart includeResources={false} chartHeight={220} windowEndAt="2026-09-09T00:00:00Z" />))
    const svg = container.querySelector('svg')!
    expect(svg.getAttribute('viewBox')).toBe('0 0 900 220')
    expect(svg.style.height).toBe('220px')
    expect(svg.querySelectorAll('text[y="214"]')).toHaveLength(3)
    width = 300
    act(() => resize())
    expect(svg.getAttribute('viewBox')).toBe('0 0 300 220')
    expect(svg.style.height).toBe('220px')
    expect(svg.querySelectorAll('text[y="214"]')).toHaveLength(2)
    width = 0
    act(() => resize())
    expect(svg.getAttribute('viewBox')).toBe('0 0 300 220')
  } finally {
    act(() => root.unmount())
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
    ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = false
  }
  expect(disconnect).toHaveBeenCalledOnce()
})

it('aligns dashboard data once and reuses paths throughout pointer interaction', () => {
  ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} })
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue({ width: 900, left: 0 } as DOMRect)
  vi.spyOn(SVGElement.prototype, 'getBoundingClientRect').mockReturnValue({ width: 900, left: 0 } as DOMRect)
  const response = {
    window: { key: '1h', from: '2026-09-09T00:00:00Z', to: '2026-09-09T01:00:00Z', bucket_seconds: 60 },
    latency_points: [{ at: '2026-09-09T00:10:00Z', avg_ms: 10, count: 1 }, { at: '2026-09-09T00:20:00Z', avg_ms: 20, count: 1 }],
    probe_target_stats: [], regional_latency_points: [], failed_probe_points: [],
  } as unknown as ConnectivityResponse
  const container = document.createElement('div')
  const root = createRoot(container)
  vi.mocked(metrics.alignUnifiedMetrics).mockClear()
  try {
    act(() => root.render(<LatencyDashboard response={response} windowKey="1h" windowHours={1} windowLabels={{ '1h': '1 小时' } as Record<any, string>} latencyWindowOptions={['1h']} onWindowChange={() => {}} onWindowKeyDown={() => {}} />))
    expect(metrics.alignUnifiedMetrics).toHaveBeenCalledTimes(1)
    const lineCalls = vi.mocked(metrics.buildLinePath).mock.calls.length
    const areaCalls = vi.mocked(metrics.buildAreaPath).mock.calls.length
    expect(lineCalls).toBeGreaterThan(0)
    const svg = container.querySelector('.komari-chart-svg')!
    for (let x = 20; x < 850; x += 20) {
      act(() => svg.dispatchEvent(new MouseEvent('pointermove', { bubbles: true, clientX: x })))
    }
    expect(container.querySelector('.komari-crosshair')).not.toBeNull()
    expect(metrics.alignUnifiedMetrics).toHaveBeenCalledTimes(1)
    expect(metrics.buildLinePath).toHaveBeenCalledTimes(lineCalls)
    expect(metrics.buildAreaPath).toHaveBeenCalledTimes(areaCalls)
  } finally {
    act(() => root.unmount())
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
    ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = false
  }
})
