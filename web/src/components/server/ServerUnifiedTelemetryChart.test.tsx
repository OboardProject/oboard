// @vitest-environment jsdom
import React, { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { ServerUnifiedTelemetryChart } from './ServerUnifiedTelemetryChart'

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
