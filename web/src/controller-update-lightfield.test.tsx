// @vitest-environment jsdom
import React, { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { ControllerUpdateLightfield } from './controller-update-lightfield'

it.each([null, '#f8f9fb'])('restores browser chrome metadata after the update closes (%s)', previous => {
  const context = vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(null)
  const existing = previous === null ? null : document.createElement('meta')
  if (existing) {
    existing.name = 'theme-color'
    existing.content = previous!
    document.head.appendChild(existing)
  }
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  try {
    act(() => root.render(<ControllerUpdateLightfield mode="running" reduceMotion />))
    expect(document.querySelectorAll('meta[name="theme-color"]')).toHaveLength(1)
    expect(document.querySelector('meta[name="theme-color"]')?.getAttribute('content')).toBe('#050505')
    act(() => root.unmount())
    expect(document.querySelector('meta[name="theme-color"]')).toBe(existing)
    if (existing) expect(existing.content).toBe(previous)
  } finally {
    container.remove()
    existing?.remove()
    context.mockRestore()
  }
})
